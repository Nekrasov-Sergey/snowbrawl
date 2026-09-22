// Package hub — центр сервера: сессии игроков, комнаты, список комнат, реестр матчей,
// дренаж. Все структуры данных под одним мьютексом; матчи живут в своих горутинах и
// общаются с hub через потокобезопасные методы и колбэк onEnd.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/match"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/room"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Hub — состояние сервера.
type Hub struct {
	cfg  config.Config
	prog *sim.Program
	log  zerolog.Logger
	now  func() time.Time

	mu      sync.Mutex
	byToken map[string]*session.Player
	byID    map[string]*session.Player
	// byAccount — живая сессия аккаунта, ключ — id аккаунта. Один аккаунт держит одну сессию:
	// вход со второго устройства перехватывает её вместе с местом в матче, как это уже делает
	// вторая вкладка. Две параллельные сессии одного аккаунта дали бы двух бойцов от одного
	// игрока и сломали бы бронь ника.
	byAccount map[string]*session.Player
	rooms     map[string]*room.Room
	matches   map[string]*match.Match
	draining  bool
	drainAt   time.Time
	rng       *rand.Rand
	// listSubs — кто смотрит список комнат: страницу пересобираем в тике и отправляем,
	// только если она изменилась. Ключ — id игрока.
	listSubs map[string]*listSub
	// codeTries — неудачные попытки войти по коду, по IP: защита от перебора закрытых комнат.
	codeTries map[string]*codeTry
	// matchPush — последняя отправленная в лобби сводка идущего матча, по коду комнаты.
	matchPush map[string]string
	// pingPush — последние отправленные в лобби задержки участников, по коду комнаты.
	pingPush map[string]string
	// lastOnline — последнее разосланное число игроков: рассылаем только при изменении.
	lastOnline int
	// chat — общий чат меню: сообщения в памяти, TTL = cfg.ChatTTL.
	chat    []chatEntry
	chatSeq uint64
	// roomChat — чаты комнат, ключ — код комнаты. Живут не дольше комнаты: буфер удаляет
	// dropRoom вместе с ней, поэтому удалять комнату мимо dropRoom нельзя.
	roomChat map[string][]chatEntry
	// nicks — брони ников до перезапуска сервера, ключ — protocol.NickKey (см. nicks.go).
	nicks map[string]nickHold

	// accs — постоянные аккаунты. У стора свой мьютекс, наружу он не ходит, поэтому звать его
	// под h.mu безопасно; на диск он под нами не пишет (см. internal/accounts). Может быть nil.
	accs *accounts.Store

	// mod — роли и баны по IP. У стора свой мьютекс; hub только читает, запись на диск делает
	// админка вне h.mu (см. internal/hub/moderation.go). Может быть nil.
	mod *moderation.Store

	// authInfo — какие способы входа включены; уходит игроку в welcome. nil — вход выключен.
	authInfo *protocol.AuthInfo

	// series — ряд онлайна для графика в админке. Под h.mu мы только дописываем точку в
	// память; на диск пишет своя горутина серии (см. internal/onlinestat). Может быть nil.
	series *onlinestat.Series

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New создаёт hub. mod может быть nil: тогда все игроки без роли и без бана. series тоже может
// быть nil — тогда истории онлайна нет, а игра работает как раньше. accs задаётся отдельно
// (SetAccounts), потому что аккаунты появляются, только когда включён вход.
func New(cfg config.Config, prog *sim.Program, log zerolog.Logger, mod *moderation.Store, series *onlinestat.Series) *Hub {
	return &Hub{
		cfg: cfg, prog: prog, log: log, now: time.Now, mod: mod, series: series,
		byToken: map[string]*session.Player{}, byID: map[string]*session.Player{},
		byAccount: map[string]*session.Player{},
		rooms:     map[string]*room.Room{},
		matches:   map[string]*match.Match{},
		listSubs:  map[string]*listSub{}, codeTries: map[string]*codeTry{},
		matchPush: map[string]string{},
		pingPush:  map[string]string{},
		nicks:     map[string]nickHold{},
		roomChat:  map[string][]chatEntry{},
		rng:       rand.New(rand.NewPCG(seedOf(cfg), 0xDEADBEEF)),
		stopCh:    make(chan struct{}),
	}
}

// SetAccounts подключает стор аккаунтов. Вызывать до Run: менять его на ходу незачем.
func (h *Hub) SetAccounts(a *accounts.Store) { h.accs = a }

// NickTakenByGuest — занят ли ник живой бронью гостя. Отдаётся входу (internal/auth), чтобы
// новый аккаунт не получил имя, под которым прямо сейчас кто-то играет.
func (h *Hub) NickTakenByGuest(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	hold, ok := h.nicks[key]
	return ok && hold.owner != ""
}

// seedOf — зерно случайности hub'а: заданное в конфигурации или время. См. config.Config.Seed.
func seedOf(cfg config.Config) uint64 {
	if cfg.Seed != 0 {
		return cfg.Seed
	}
	return uint64(time.Now().UnixNano())
}

// OnlineSeries — ряд онлайна для админки. Поле пишется один раз в New, мьютекс не нужен.
func (h *Hub) OnlineSeries() *onlinestat.Series { return h.series }

// Run запускает фоновый цикл таймаутов (TTL сессий и комнат, рассылка списка комнат).
func (h *Hub) Run() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		every := h.cfg.HubTick
		if every <= 0 {
			every = 500 * time.Millisecond
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-t.C:
				h.tick()
			}
		}
	}()
}

// Shutdown останавливает матчи и закрывает соединения.
func (h *Hub) Shutdown() {
	close(h.stopCh)
	h.wg.Wait()
	h.mu.Lock()
	matches := make([]*match.Match, 0, len(h.matches))
	for _, m := range h.matches {
		matches = append(matches, m)
	}
	conns := make([]*ws.Conn, 0, len(h.byID))
	for _, p := range h.byID {
		if c, ok := p.Conn.(*ws.Conn); ok {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	for _, m := range matches {
		m.Stop(match.ReasonShutdown)
	}
	// Ждём, пока матчи разошлют match.end, и только потом рвём сокеты. Раньше здесь стояла
	// безусловная пауза 200 мс: она платилась даже когда матчей не было вовсе, а когда они были —
	// не гарантировала ничего. Потолок ожидания тот же.
	if len(matches) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		for _, m := range matches {
			if !m.Wait(ctx) {
				h.log.Warn().Str("match", m.ID).Msg("match.end не успел уйти до закрытия сокетов")
			}
		}
		cancel()
	}
	for _, c := range conns {
		c.Close(websocket.StatusGoingAway, "server restart")
	}
}

// ---- ws.Handler ----

// OnMessage обрабатывает сообщение клиента.
func (h *Hub) OnMessage(c *ws.Conn, env protocol.Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, _ := c.Session.(*session.Player)
	if p == nil {
		if env.Type != protocol.CHello {
			h.sendErr(c, protocol.ErrNotAllowed, "send hello first")
			return
		}
		h.handleHello(c, env.Data)
		return
	}
	if p.Conn != c {
		// Старое соединение, которое уже заменено новым.
		c.Close(websocket.StatusPolicyViolation, "replaced")
		return
	}
	switch env.Type {
	case protocol.CHello:
		h.sendErr(c, protocol.ErrNotAllowed, "already said hello")
	case protocol.CPong:
		h.handlePong(p, env.Data)
	case protocol.CRoomCreate:
		h.handleRoomCreate(p, env.Data)
	case protocol.CRoomJoin:
		h.handleRoomJoin(p, env.Data)
	case protocol.CRoomSlot:
		h.handleRoomSlot(p, env.Data)
	case protocol.CRoomRole:
		h.handleRoomRole(p, env.Data)
	case protocol.CRoomConfig:
		h.handleRoomConfig(p, env.Data)
	case protocol.CRoomReady:
		h.handleRoomReady(p, env.Data)
	case protocol.CRoomList:
		h.handleRoomList(p, env.Data)
	case protocol.CRoomUnlist:
		delete(h.listSubs, p.ID)
	case protocol.CNickSet:
		h.handleNickSet(p, env.Data)
	case protocol.CTutorialDone:
		h.handleTutorialDone(p, env.Data)
	case protocol.CTutorialSync:
		h.handleTutorialSync(p, env.Data)
	case protocol.CMatchJoin:
		h.handleMatchJoin(p)
	case protocol.CRoomKick:
		h.handleRoomKick(p, env.Data)
	case protocol.CRoomStart:
		h.handleRoomStart(p)
	case protocol.CRoomLeave:
		h.leaveRoom(p, true)
	case protocol.CMatchLeave:
		h.leaveMatch(p)
	case protocol.CInput:
		h.handleInput(p, env.Data)
	case protocol.CTraining:
		h.handleTraining(p, env.Data)
	case protocol.CChatSend:
		h.handleChatSend(p, env.Data)
	case protocol.CChatDel:
		h.handleChatDel(p, env.Data)
	default:
		h.sendErr(c, protocol.ErrBadMessage, "unknown type "+env.Type)
	}
}

// OnClose — соединение закрылось.
func (h *Hub) OnClose(c *ws.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, _ := c.Session.(*session.Player)
	if p == nil || p.Conn != c {
		return
	}
	p.Conn = nil
	p.DisconnectedAt = h.now()
	resetPing(p) // задержка мёртвого канала не должна висеть в лобби и в админке
	delete(h.listSubs, p.ID)
	switch p.Place {
	case session.InRoom:
		h.broadcastRoom(h.rooms[p.RoomCode])
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			m.Detach(p.ID)
		}
	}
	h.log.Debug().Str("player", p.ID).Str("place", string(p.Place)).Msg("player disconnected")
	h.broadcastOnline(nil)
}

// ---- hello / сессии ----

func (h *Hub) handleHello(c *ws.Conn, data json.RawMessage) {
	var hello protocol.Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		h.sendErr(c, protocol.ErrBadMessage, "bad hello")
		c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	if hello.ProtocolVersion != protocol.Version {
		h.sendErr(c, protocol.ErrBadVersion, fmt.Sprintf("protocol %d required", protocol.Version))
		c.Send(protocol.MustEncode(protocol.SReload, nil))
		c.Close(websocket.StatusPolicyViolation, "bad protocol version")
		return
	}
	if h.cfg.BuildVersion != "dev" && hello.BuildVersion != "" && hello.BuildVersion != h.cfg.BuildVersion {
		c.Send(protocol.MustEncode(protocol.SReload, nil))
		c.Close(websocket.StatusPolicyViolation, "stale client")
		return
	}
	// Бан проверяем после proto и build: забаненный со старой сборкой сначала должен получить
	// reload, иначе увидит ошибку, которую его клиент не умеет показать. HTTP не трогаем —
	// страница и /api/* открываются как всем.
	if h.banned(c.IP()) {
		h.sendErr(c, protocol.ErrBanned, "доступ с этого адреса заблокирован")
		c.Close(websocket.StatusPolicyViolation, "banned")
		return
	}
	now := h.now()
	// Аккаунт узнан по куке ещё на апгрейде соединения (см. internal/auth). Забаненный аккаунт
	// не пускаем так же, как забаненный адрес: иначе достаточно было бы разлогиниться.
	acc, hasAcc := h.accs.Get(c.AccountID())
	if hasAcc && acc.Banned() {
		h.sendErr(c, protocol.ErrBanned, "доступ для этого аккаунта заблокирован")
		c.Close(websocket.StatusPolicyViolation, "banned")
		return
	}
	var p *session.Player
	// Сессию аккаунта ищем раньше токена: игрок, зашедший с телефона, должен попасть в свою
	// игру, а не завести вторую сессию под тем же именем.
	if hasAcc {
		p = h.byAccount[acc.ID]
	}
	if p == nil && hello.Token != "" {
		p = h.byToken[hello.Token]
		// Чужой аккаунт при том же токене — не наша сессия: так гостевой токен, попавший в
		// другой браузер (или оставшийся от прежнего входа), не утащил бы аккаунт.
		if p != nil && p.AccountID != c.AccountID() {
			p = nil
		}
	}
	if p == nil {
		nick := acc.Nick // у аккаунта ник свой, введённый в hello игнорируется
		switch {
		case hasAcc:
			// ник уже взят из аккаунта
		case hello.Nick == "":
			// «Играть гостем»: игрок не вводил имени, значит его выдаёт сервер. Пустой ник —
			// это просьба, а не ошибка: экран входа спрашивает «как играть», а не «как вас звать».
			nick = h.pickGuestNick(c.IP())
		default:
			var err error
			nick, err = protocol.NormalizeNick(hello.Nick)
			if err != nil {
				h.sendErr(c, nickErrCode(err), err.Error())
				return
			}
			// Ник занят — отказ без закрытия соединения: клиент возвращает игрока на экран ника.
			if !h.nickFree(nick, c.IP(), "", "") {
				h.sendErr(c, protocol.ErrNickTaken, "nick is taken")
				return
			}
		}
		p = session.New(nick, c.IP(), now)
		p.AccountID = c.AccountID()
		h.byToken[p.Token] = p
		h.byID[p.ID] = p
		if hasAcc {
			h.byAccount[acc.ID] = p
			h.accs.Touch(acc.ID, now)
		}
		h.holdNick(nick, p.IP, p.ID, now)
		h.log.Info().Str("player", p.ID).Str("nick", nick).Str("ip", p.IP).
			Str("account", p.AccountID).Msg("new player")
	} else {
		if hasAcc {
			// Ник аккаунта — источник истины: он мог смениться на другом устройстве.
			if p.Nick != acc.Nick {
				h.releaseNick(protocol.NickKey(p.Nick), p.ID)
				p.Nick = acc.Nick
				h.holdNick(acc.Nick, c.IP(), p.ID, now)
			}
			h.accs.Touch(acc.ID, now)
		}
		if !hasAcc && hello.Nick != "" && p.Place == session.InMenu {
			// Ошибку не глотаем: с цензурой ников молчаливый отказ выглядел бы как «ник не
			// сохранился». Соединение при этом живо, игрок остаётся под прежним ником.
			// Проверяем по адресу этого соединения, а не по p.IP: тот перезаписывается ниже.
			nick, err := protocol.NormalizeNick(hello.Nick)
			switch {
			case err != nil:
				h.sendErr(c, nickErrCode(err), err.Error())
			case !h.nickFree(nick, c.IP(), p.ID, p.AccountID):
				h.sendErr(c, protocol.ErrNickTaken, "nick is taken")
			default:
				// Переименование отпускает прежний ник: это единственный способ его освободить.
				if key := protocol.NickKey(p.Nick); key != protocol.NickKey(nick) {
					h.releaseNick(key, p.ID)
				}
				p.Nick = nick
				h.holdNick(nick, c.IP(), p.ID, now)
			}
		}
		if old, ok := p.Conn.(*ws.Conn); ok && old != c {
			old.Session = nil
			// Текст причины — часть контракта с клиентом: по нему web/client/net.js понимает,
			// что сессию забрала другая вкладка, и НЕ переподключается. Без этого две вкладки
			// вышибали друг друга по кругу раз в секунду, и игра ломалась в обеих.
			old.Close(websocket.StatusPolicyViolation, "replaced by new connection")
		}
		p.IP = c.IP()
	}
	// Новое соединение — новая половина пути: прежняя задержка относится к мёртвому каналу.
	resetPing(p)
	p.Conn = c
	c.Session = p

	welcome := protocol.Welcome{
		Token: p.Token, PlayerID: p.ID, Nick: p.Nick, Build: h.cfg.BuildVersion, SimVersion: h.prog.Version(),
		Proto: protocol.Version, Draining: h.draining, Resume: string(p.Place), Online: h.onlineLocked(),
		Rank: h.rankOf(p),
	}
	if h.authInfo != nil {
		welcome.Auth = h.authInfo
	}
	if hasAcc {
		// Аккаунт мог измениться на другом устройстве, поэтому читаем его заново, а не берём
		// копию, снятую в начале обработки.
		if fresh, ok := h.accs.Get(acc.ID); ok {
			welcome.Account, welcome.Tutorial = accountInfo(fresh), fresh.Tutorial
		}
	}
	c.Send(protocol.MustEncode(protocol.SWelcome, welcome))
	if h.draining {
		c.Send(h.drainMessage())
	}
	h.sendChatHistory(p)
	// Восстановление места.
	switch p.Place {
	case session.InRoom:
		if r := h.rooms[p.RoomCode]; r != nil {
			h.broadcastRoom(r)
			h.sendRoomChatHistory(p, r)
		} else {
			p.ToMenu()
		}
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			c.Send(m.StartMessage(p.ID))
			m.Attach(p.ID, c)
			if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
				h.sendRoomChatHistory(p, r)
			}
		} else {
			p.ToMenu()
			if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
				p.Place, p.RoomCode = session.InRoom, r.Code
				h.broadcastRoom(r)
				h.sendRoomChatHistory(p, r)
			}
		}
	}
	h.broadcastOnline(p)
}

// ---- Комнаты ----

func (h *Hub) handleRoomCreate(p *session.Player, data json.RawMessage) {
	var req protocol.RoomCreate
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.create")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	if req.Mode < 1 || req.Mode > 4 {
		h.sendErrP(p, protocol.ErrBadMode, "mode must be 1..4")
		return
	}
	if req.Arena < 0 || req.Arena >= h.prog.ArenaCount() {
		h.sendErrP(p, protocol.ErrBadArena, "unknown arena")
		return
	}
	gameMode := normGameMode(req.GameMode)
	if !h.prog.HasGameMode(gameMode) {
		h.sendErrP(p, protocol.ErrBadGameMode, "unknown game mode")
		return
	}
	difficulty := clampDifficulty(req.Difficulty)
	live := 0
	for _, r := range h.rooms {
		if r.HostIP == p.IP && !r.IsEmpty() {
			live++
		}
	}
	if live >= h.cfg.RoomsPerIP {
		h.sendErrP(p, protocol.ErrRoomLimit, "too many rooms from your address")
		return
	}
	code := room.GenerateCode()
	for h.rooms[code] != nil {
		code = room.GenerateCode()
	}
	r := room.New(code, p.ID, p.IP, room.Config{Mode: req.Mode, Arena: req.Arena, GameMode: gameMode,
		Campaign: req.Campaign, Difficulty: difficulty, Visibility: req.Visibility}, h.now())
	h.rooms[code] = r
	p.Training = false
	p.Place, p.RoomCode = session.InRoom, code
	h.log.Info().Str("room", code).Str("host", p.ID).Int("mode", req.Mode).Str("gameMode", gameMode).Msg("room created")
	h.broadcastRoom(r)
	h.sendRoomChatHistory(p, r)
}

func (h *Hub) handleRoomJoin(p *session.Player, data json.RawMessage) {
	var req protocol.RoomJoin
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.join")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	if !h.codeTryAllowed(p.IP) {
		h.sendErrP(p, protocol.ErrTooManyTries, "too many attempts, wait a minute")
		return
	}
	code, err := protocol.NormalizeRoomCode(req.Code)
	if err != nil {
		h.codeTryFailed(p.IP)
		h.sendErrP(p, protocol.ErrBadCode, "bad code")
		return
	}
	r := h.rooms[code]
	if r == nil {
		h.codeTryFailed(p.IP)
		h.sendErrP(p, protocol.ErrRoomNotFound, "room not found")
		return
	}
	// Разделы не пересекаются: по коду PVE-комнаты из раздела PVP не пускаем. Попытку
	// считаем неудачной — иначе отличимый ответ «другой раздел» даёт перебору закрытых
	// комнат бесплатный оракул «код существует».
	if req.Section != "" && r.Section() != normSection(req.Section) {
		h.codeTryFailed(p.IP)
		h.sendErrP(p, protocol.ErrWrongSection, "room from another section")
		return
	}
	// Войти можно и в комнату с идущим матчем: слот комнаты — это боец матча, вошедший
	// получает свободное место и сам решает, вступать ли в бой.
	if err := r.Join(p.ID); err != nil {
		h.sendErrP(p, protocol.ErrRoomFull, "room is full")
		return
	}
	p.Training = false
	p.Place, p.RoomCode = session.InRoom, code
	delete(h.listSubs, p.ID)
	h.broadcastRoom(r)
	h.sendRoomChatHistory(p, r) // вошедший видит, о чём говорили в комнате до него
	h.sendRoomMatch(p, r)       // вошёл в комнату с идущим матчем — сразу показываем, что там происходит
}

func (h *Hub) roomOf(p *session.Player) *room.Room {
	if p.Place != session.InRoom {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a room")
		return nil
	}
	r := h.rooms[p.RoomCode]
	if r == nil {
		p.ToMenu()
		h.sendErrP(p, protocol.ErrRoomNotFound, "room is gone")
		return nil
	}
	return r
}

func (h *Hub) handleRoomSlot(p *session.Player, data json.RawMessage) {
	var req protocol.RoomSlot
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.slot")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	// В матче слот меняет только тот, кто сидит в лобби: у играющего боец уже привязан к слоту.
	if err := r.SetSlot(p.ID, req.Team, req.Index); err != nil {
		h.sendErrP(p, protocol.ErrBadSlot, err.Error())
		return
	}
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomRole(p *session.Player, data json.RawMessage) {
	var req protocol.RoomRole
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.role")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if !h.prog.HasRole(req.Role) {
		h.sendErrP(p, protocol.ErrBadRole, "unknown role")
		return
	}
	_ = r.SetRole(p.ID, req.Role)
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomConfig(p *session.Player, data json.RawMessage) {
	var req protocol.RoomConfig
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.config")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	gameMode := normGameMode(req.GameMode)
	if !h.prog.HasGameMode(gameMode) {
		h.sendErrP(p, protocol.ErrBadGameMode, "unknown game mode")
		return
	}
	cfg := room.Config{Mode: req.Mode, Arena: req.Arena, GameMode: gameMode,
		Campaign: req.Campaign, Difficulty: clampDifficulty(req.Difficulty), Visibility: req.Visibility}
	if err := r.SetConfig(p.ID, cfg, h.prog.ArenaCount()); err != nil {
		h.sendErrP(p, protocol.ErrNotAllowed, err.Error())
		return
	}
	h.broadcastRoom(r)
}

// handleRoomReady отмечает готовность. Когда готовы все люди в комнате, включая хоста,
// матч стартует сам — отдельного отсчёта в лобби нет, он есть в начале матча.
func (h *Hub) handleRoomReady(p *session.Player, data json.RawMessage) {
	var req protocol.RoomReady
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.ready")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if r.InMatch {
		h.sendErrP(p, protocol.ErrBusy, "match already running")
		return
	}
	if err := r.SetReady(p.ID, req.Ready); err != nil {
		h.sendErrP(p, protocol.ErrNotAllowed, err.Error())
		return
	}
	if h.tryAutoStart(r) {
		return
	}
	h.broadcastRoom(r)
}

// tryAutoStart запускает матч, если все на связи готовы. Возвращает true, если матч пошёл.
func (h *Hub) tryAutoStart(r *room.Room) bool {
	if r == nil || r.InMatch || h.draining || !r.AllReady(h.connectedByID) {
		return false
	}
	return h.startRoomMatch(r) != nil
}

// connectedByID — есть ли у игрока живое соединение (для расчёта готовности).
func (h *Hub) connectedByID(id string) bool {
	p := h.byID[id]
	return p != nil && p.Connected()
}

func (h *Hub) handleRoomKick(p *session.Player, data json.RawMessage) {
	var req protocol.RoomKick
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.kick")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if err := r.Kick(p.ID, req.PlayerID, h.now()); err != nil {
		h.sendErrP(p, protocol.ErrNotAllowed, err.Error())
		return
	}
	if target := h.byID[req.PlayerID]; target != nil && target.RoomCode == r.Code {
		target.ToMenu()
		target.Send(protocol.MustEncode(protocol.SRoomLeft, protocol.Error{Code: "kicked", Message: "вас выгнали из комнаты"}))
	}
	h.broadcastRoom(r)
}

// leaveRoom выводит игрока из комнаты; notify — послать ему room.left.
func (h *Hub) leaveRoom(p *session.Player, notify bool) {
	r := h.rooms[p.RoomCode]
	if r == nil {
		p.ToMenu()
		return
	}
	empty := r.Leave(p.ID, h.now())
	p.ToMenu()
	if notify {
		p.Send(protocol.MustEncode(protocol.SRoomLeft, nil))
	}
	if empty {
		if r.InMatch {
			return // удалим, когда матч закончится
		}
		h.dropRoom(r.Code)
		return
	}
	h.broadcastRoom(r)
}

// dropRoom удаляет комнату вместе с её чатом: буфер сообщений живёт ровно столько же,
// сколько комната. Вызывать под h.mu.
func (h *Hub) dropRoom(code string) {
	delete(h.rooms, code)
	delete(h.roomChat, code)
}

func (h *Hub) handleRoomStart(p *session.Player) {
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if r.HostID != p.ID {
		h.sendErrP(p, protocol.ErrNotAllowed, "only host can start")
		return
	}
	if r.InMatch {
		h.sendErrP(p, protocol.ErrBusy, "match already running")
		return
	}
	if h.draining {
		h.sendErrP(p, protocol.ErrDraining, "server is restarting soon")
		return
	}
	h.startRoomMatch(r)
}

// startRoomMatch собирает состав комнаты и запускает матч. Вызывать под h.mu.
func (h *Hub) startRoomMatch(r *room.Room) *match.Match {
	r.PlaceAll()
	var players []protocol.MatchPlayer
	for _, m := range r.Members {
		mp := h.byID[m.ID]
		if mp == nil {
			continue
		}
		players = append(players, protocol.MatchPlayer{ID: m.ID, Nick: mp.Nick, Team: m.Team, Index: m.Index,
			Role: m.Role, Rank: h.rankOf(mp)})
	}
	m := h.launchMatch(r.Code, r.Mode, r.Arena, r.GameMode, r.Campaign, r.Difficulty, players)
	if m == nil {
		return nil
	}
	r.InMatch, r.MatchID = true, m.ID
	r.ResetReady() // следующий матч комнаты начинается с чистой готовности
	h.broadcastRoom(r)
	return m
}

// normGameMode приводит пустой режим к "pvp".
func normGameMode(gm string) string {
	if gm == "" {
		return "pvp"
	}
	return gm
}

// clampDifficulty ограничивает ручку сложности диапазоном 0..2. Не присланная сложность —
// «Обычный» (1), а не «Лёгкий»: иначе забытое поле незаметно ослабляло бы ботов.
func clampDifficulty(d *int) int {
	if d == nil || *d < 0 || *d > 2 {
		return 1
	}
	return *d
}

func (h *Hub) roomState(r *room.Room) protocol.RoomState {
	ready, _ := r.ReadyCount(h.connectedByID)
	st := protocol.RoomState{Code: r.Code, HostID: r.HostID, Mode: r.Mode, Arena: r.Arena,
		GameMode: r.GameMode, Campaign: r.Campaign, Difficulty: r.Difficulty, Visibility: r.Visibility,
		InMatch: r.InMatch, LastWinner: r.LastWinner, ReadyCount: ready}
	for _, m := range r.Members {
		p := h.byID[m.ID]
		rp := protocol.RoomPlayer{ID: m.ID, Team: m.Team, Index: m.Index, Role: m.Role,
			Host: m.ID == r.HostID, Ready: m.Ready}
		if p != nil {
			rp.Nick, rp.Connected = p.Nick, p.Connected()
			rp.InMatch = p.Place == session.InMatch && p.MatchID == r.MatchID
			rp.Rank = h.rankOf(p)
			rp.Ping = p.PingMs
		}
		st.Players = append(st.Players, rp)
	}
	return st
}

// sendToRoom отправляет сообщение всем участникам комнаты — и тем, кто в лобби, и тем, кто
// сейчас в матче: слот в комнате за ними сохраняется, а значит и чат комнаты тоже.
func (h *Hub) sendToRoom(r *room.Room, msg []byte) {
	if r == nil {
		return
	}
	for _, m := range r.Members {
		if p := h.byID[m.ID]; p != nil {
			p.Send(msg)
		}
	}
}

func (h *Hub) broadcastRoom(r *room.Room) {
	if r == nil {
		return
	}
	msg := protocol.MustEncode(protocol.SRoomState, h.roomState(r))
	for _, m := range r.Members {
		if p := h.byID[m.ID]; p != nil && p.Place == session.InRoom {
			p.Send(msg)
		}
	}
}

// ---- Матчи ----

// launchMatch дополняет состав ботами, создаёт матч и переводит игроков в него.
func (h *Hub) launchMatch(roomCode string, mode, arena int, gameMode string, campaign bool, difficulty int, humans []protocol.MatchPlayer) *match.Match {
	players := h.fillTeams(mode, gameMode, difficulty, humans)
	opts := match.Options{
		TickRate: h.cfg.TickRate, AFKTimeout: h.cfg.AFKTimeout, Countdown: h.cfg.Countdown, Log: h.log, Now: h.now,
		TimeScale: h.cfg.TimeScale,
		GameMode:  gameMode, Campaign: campaign, Difficulty: difficulty,
	}
	if h.cfg.Seed != 0 {
		// Зерно матча — производная от h.rng, а не от cfg.Seed напрямую: иначе все матчи одного
		// сервера играли бы по одному и тому же сценарию.
		seed := h.rng.Uint32()
		opts.Seed = &seed
	}
	m, err := match.New(h.prog, roomCode, mode, arena, players, opts, h.onMatchEnd)
	if err != nil {
		h.log.Error().Err(err).Msg("create match")
		for _, hp := range humans {
			if p := h.byID[hp.ID]; p != nil {
				h.sendErrP(p, protocol.ErrInternal, "failed to create match")
				p.ToMenu()
			}
		}
		return nil
	}
	h.matches[m.ID] = m
	for _, hp := range humans {
		p := h.byID[hp.ID]
		if p == nil {
			continue
		}
		p.Place, p.MatchID, p.RoomCode = session.InMatch, m.ID, roomCode
		if p.Connected() {
			p.Send(m.StartMessage(p.ID))
			m.Attach(p.ID, p.Conn)
		}
	}
	m.Start()
	h.log.Info().Str("match", m.ID).Str("room", roomCode).Int("mode", mode).Int("humans", len(humans)).Msg("match started")
	return m
}

// fillTeams назначает роли не выбравшим, дополняет состав ботами и убирает дубли ников.
// PvP: обе команды добиваются до mode. PvE: добивается только пати (команда A) —
// врагов создаёт волновой планировщик в sim.js.
func (h *Hub) fillTeams(mode int, gameMode string, difficulty int, humans []protocol.MatchPlayer) []protocol.MatchPlayer {
	roles := h.prog.Roles()
	pve := gameMode != "" && gameMode != "pvp"
	players := make([]protocol.MatchPlayer, 0, 2*mode)
	count := map[string]int{"A": 0, "B": 0}
	nicks := map[string]int{}
	for _, hp := range humans {
		if hp.Role == "" || !h.prog.HasRole(hp.Role) {
			hp.Role = roles[h.rng.IntN(len(roles))]
		}
		if n := nicks[hp.Nick]; n > 0 {
			hp.Nick = fmt.Sprintf("%s (%d)", hp.Nick, n+1)
		}
		nicks[hp.Nick]++
		count[hp.Team]++
		players = append(players, hp)
	}
	teams := []string{"A", "B"}
	if pve {
		teams = []string{"A"}
	}
	// Боты занимают свободные слоты команд: индекс слота нужен, чтобы вошедший позже игрок
	// сел именно за бойца своего места в комнате.
	taken := map[string]bool{}
	for _, hp := range players {
		taken[hp.Team+strconv.Itoa(hp.Index)] = true
	}
	botN := 0
	botNicks := map[string]bool{} // имена ботов в матче не повторяются и не совпадают с никами людей
	for _, hp := range players {
		botNicks[hp.Nick] = true
	}
	for _, team := range teams {
		for i := 0; i < mode; i++ {
			if taken[team+strconv.Itoa(i)] {
				continue
			}
			botN++
			level := difficulty
			nick := match.PickBotNick(gameMode, botNicks, h.rng.IntN)
			botNicks[nick] = true
			bp := protocol.MatchPlayer{
				ID: fmt.Sprintf("bot%d", botN), Nick: nick, Team: team,
				Index: i, Role: roles[h.rng.IntN(len(roles))], Bot: true, BotLevel: &level,
			}
			players = append(players, bp)
			count[team]++
		}
	}
	return players
}

func (h *Hub) onMatchEnd(m *match.Match, res match.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.matches, m.ID)
	r := h.rooms[m.RoomCode]
	// Именно HumanIDs: у подсевшего на место бота id бойца в матче не равен id сессии.
	for _, id := range m.HumanIDs() {
		p := h.byID[id]
		if p == nil || p.MatchID != m.ID {
			continue
		}
		p.ToMenu()
		if r != nil && r.Member(p.ID) != nil {
			p.Place, p.RoomCode = session.InRoom, r.Code
		}
	}
	if r != nil {
		r.InMatch, r.MatchID = false, ""
		switch {
		case r.IsPvE():
			r.LastWinner = res.Reason // cleared | wiped | objective | expired
		case res.Winner != "":
			r.LastWinner = res.Winner
		default:
			r.LastWinner = "draw"
		}
		if r.IsEmpty() {
			h.dropRoom(r.Code)
		} else {
			h.broadcastRoom(r)
		}
	}
}

// leaveMatch выводит игрока из боя, но оставляет в комнате: место остаётся за ним, бойца ведёт
// бот, и кнопкой «Присоединиться к матчу» игрок возвращается за того же бойца.
func (h *Hub) leaveMatch(p *session.Player) {
	if p.Place != session.InMatch {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a match")
		return
	}
	if m := h.matches[p.MatchID]; m != nil {
		m.Leave(p.ID)
	}
	r := h.rooms[p.RoomCode]
	if r == nil || r.Member(p.ID) == nil {
		p.ToMenu()
		p.Send(protocol.MustEncode(protocol.SRoomLeft, nil))
		return
	}
	p.Place, p.MatchID = session.InRoom, ""
	h.broadcastRoom(r)
	h.sendRoomMatch(p, r)
}

func (h *Hub) handleInput(p *session.Player, data json.RawMessage) {
	if p.Place != session.InMatch {
		return
	}
	var in protocol.Input
	if err := json.Unmarshal(data, &in); err != nil || in.Kind == "" {
		return
	}
	if m := h.matches[p.MatchID]; m != nil {
		m.Input(p.ID, data)
	}
}

// ---- Фоновые таймауты ----

// handleTraining запоминает, что игрок ушёл в тренировку с ботами. Место игрока не меняется:
// для сервера он по-прежнему в меню и может встать в очередь или создать комнату.
// Вызывается из OnMessage, то есть уже под h.mu — свой Lock здесь был бы дедлоком.
func (h *Hub) handleTraining(p *session.Player, data json.RawMessage) {
	var req protocol.Training
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad training")
		return
	}
	if !req.On {
		p.Training = false
		return
	}
	if !p.Training {
		p.TrainingSince = h.now()
	}
	p.Training, p.TrainingMode, p.TrainingArena, p.TrainingRole = true, req.Mode, req.Arena, req.Role
}

func (h *Hub) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()

	h.pushRoomLists()
	h.pushRoomMatches()
	h.pushRoomPings()
	// Точка графика: под h.mu только память, файл пишет своя горутина серии.
	h.series.Observe(now, h.onlineLocked())

	for ip, t := range h.codeTries {
		if now.After(t.resetAt) {
			delete(h.codeTries, ip)
		}
	}

	h.pruneChat(now)

	for code, r := range h.rooms {
		if r.IsEmpty() && !r.InMatch && now.Sub(r.EmptySince) > h.cfg.RoomTTL {
			h.dropRoom(code)
		}
	}

	for _, p := range h.byID {
		if p.Connected() {
			h.probePing(p, now)
			h.pushSelfPing(p)
			continue
		}
		if now.Sub(p.DisconnectedAt) < h.cfg.ReconnectTTL {
			continue
		}
		h.expirePlayer(p)
	}
}

func (h *Hub) expirePlayer(p *session.Player) {
	delete(h.listSubs, p.ID)
	switch p.Place {
	case session.InRoom:
		h.leaveRoom(p, false)
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			m.Leave(p.ID)
		}
		if r := h.rooms[p.RoomCode]; r != nil {
			if !r.Leave(p.ID, h.now()) {
				h.broadcastRoom(r)
			}
		}
	}
	delete(h.byID, p.ID)
	delete(h.byToken, p.Token)
	if p.AccountID != "" && h.byAccount[p.AccountID] == p {
		delete(h.byAccount, p.AccountID)
	}
	h.log.Debug().Str("player", p.ID).Msg("session expired")
}

// ---- Дренаж и админка ----

// SetDrain включает/выключает режим дренажа: новые матчи не стартуют, всем показан баннер.
func (h *Hub) SetDrain(active bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = active
	if active {
		h.drainAt = h.now()
	}
	msg := h.drainMessage()
	for _, p := range h.byID {
		p.Send(msg)
	}
}

func (h *Hub) drainMessage() []byte {
	d := protocol.Drain{Active: h.draining}
	if h.draining {
		left := h.cfg.DrainTimeout - h.now().Sub(h.drainAt)
		if left < 0 {
			left = 0
		}
		d.InSeconds = int(left / time.Second)
	}
	return protocol.MustEncode(protocol.SDrain, d)
}

// Stats — сводка для админки.
//
// ВАЖНО: сводка не должна меняться сама по себе между вызовами при неизменном состоянии —
// на этом стоит SSE-поток админки (отправляем только изменения). Поэтому здесь абсолютные
// метки времени, а не «мс назад», и детерминированный порядок списков; длительности считает
// страница от поля Now. См. TestStatsIsStableBetweenCalls.
type Stats struct {
	Build       string       `json:"build"`
	SimVersion  string       `json:"sim"`
	Proto       int          `json:"proto"`
	Now         time.Time    `json:"now"`
	Draining    bool         `json:"draining"`
	DrainSince  *time.Time   `json:"drainSince,omitempty"`
	Players     int          `json:"players"`
	Online      int          `json:"online"`
	Training    int          `json:"training"` // сколько игроков в тренировке с ботами
	Sessions    []PlayerStat `json:"sessions"`
	Rooms       []RoomStat   `json:"rooms"`
	Matches     []match.Info `json:"matches"`
	MatchesLive int          `json:"matchesLive"`

	// Модерация: выданные роли, баны и размер чата — админка получает их тем же потоком.
	Ranks     []moderation.Entry `json:"ranks"`
	Bans      []moderation.Ban   `json:"bans"`
	ChatSize  int                `json:"chatSize"`
	RoomChat  int                `json:"roomChat"`            // сообщений во всех чатах комнат
	ModBroken bool               `json:"modBroken,omitempty"` // файл ролей и банов был битым
}

// PlayerStat — сессия игрока в сводке: кто это, где находится и на связи ли.
type PlayerStat struct {
	ID           string     `json:"id"`
	Nick         string     `json:"nick"`
	IP           string     `json:"ip"`
	Place        string     `json:"place"`           // menu | room | match
	Where        string     `json:"where,omitempty"` // код комнаты, режим очереди или id матча
	Online       bool       `json:"online"`
	Rank         string     `json:"rank,omitempty"`         // роль модерации по IP
	Ping         int        `json:"ping,omitempty"`         // задержка до сервера, мс; её же игрок видит у себя
	Banned       bool       `json:"banned,omitempty"`       // адрес в бане (сессия ещё не выкинута)
	Since        time.Time  `json:"since"`                  // когда игрок зашёл в игру
	OfflineSince *time.Time `json:"offlineSince,omitempty"` // с какого момента нет связи

	// Тренировка с ботами: сервер её не считает, данные со слов клиента.
	Training      bool       `json:"training,omitempty"`
	TrainingMode  int        `json:"trainingMode,omitempty"`
	TrainingArena int        `json:"trainingArena,omitempty"`
	TrainingRole  string     `json:"trainingRole,omitempty"`
	TrainingSince *time.Time `json:"trainingSince,omitempty"`
}

// RoomStat — комната в сводке.
type RoomStat struct {
	Code       string       `json:"code"`
	Mode       int          `json:"mode"`
	Arena      int          `json:"arena"`
	GameMode   string       `json:"gameMode,omitempty"`
	Visibility string       `json:"visibility,omitempty"`
	Members    int          `json:"members"`
	InMatch    bool         `json:"inMatch"`
	Players    []MemberStat `json:"players"`
}

// MemberStat — участник комнаты в сводке.
type MemberStat struct {
	ID     string `json:"id"`
	Nick   string `json:"nick"`
	Team   string `json:"team,omitempty"`
	Role   string `json:"role,omitempty"`
	Host   bool   `json:"host,omitempty"`
	Online bool   `json:"online"`
	Ready  bool   `json:"ready,omitempty"`
}

// Online возвращает число подключённых игроков (для публичного /api/online).
func (h *Hub) Online() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.onlineLocked()
}

// onlineLocked считает подключённых игроков. Вызывать под h.mu.
func (h *Hub) onlineLocked() int {
	n := 0
	for _, p := range h.byID {
		if p.Connected() {
			n++
		}
	}
	return n
}

// broadcastOnline рассылает новое число игроков, если оно изменилось. Вызывать под h.mu.
// Счётчик идёт по игровому сокету, поэтому клиент видит ровно то состояние, в котором сам
// находится: при обрыве связи он не получит цифру и покажет прочерк вместо ложного нуля.
// except пропускается: тот, кто только что подключился, уже получил число в welcome.
func (h *Hub) broadcastOnline(except *session.Player) {
	n := h.onlineLocked()
	if n == h.lastOnline {
		return
	}
	h.lastOnline = n
	msg := protocol.MustEncode(protocol.SOnline, protocol.Online{N: n})
	for _, p := range h.byID {
		if p != except {
			p.Send(msg)
		}
	}
}

// Stats возвращает сводку.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	st := Stats{Build: h.cfg.BuildVersion, SimVersion: h.prog.Version(), Proto: protocol.Version, Now: now, Draining: h.draining}
	if h.draining {
		t := h.drainAt
		st.DrainSince = &t
	}
	st.Players = len(h.byID)
	for _, p := range h.byID {
		online := p.Connected()
		if online {
			st.Online++
		}
		ps := PlayerStat{
			ID: p.ID, Nick: p.Nick, IP: p.IP, Place: string(p.Place), Online: online,
			Since: p.CreatedAt, Rank: h.rankOf(p), Banned: h.banned(p.IP) || h.accountBanned(p), Ping: p.PingMs,
		}
		switch p.Place {
		case session.InRoom:
			ps.Where = p.RoomCode
		case session.InMatch:
			ps.Where = p.MatchID
		}
		if !online && !p.DisconnectedAt.IsZero() {
			t := p.DisconnectedAt
			ps.OfflineSince = &t
		}
		if p.Training {
			ps.Training, ps.TrainingMode, ps.TrainingArena, ps.TrainingRole = true, p.TrainingMode, p.TrainingArena, p.TrainingRole
			t := p.TrainingSince
			ps.TrainingSince = &t
			st.Training++
		}
		st.Sessions = append(st.Sessions, ps)
	}
	// Игроки — от самых «старых» к новым; тай-брейк по id, чтобы порядок не плавал.
	sort.Slice(st.Sessions, func(i, j int) bool {
		if !st.Sessions[i].Since.Equal(st.Sessions[j].Since) {
			return st.Sessions[i].Since.Before(st.Sessions[j].Since)
		}
		return st.Sessions[i].ID < st.Sessions[j].ID
	})
	for _, r := range h.rooms {
		rs := RoomStat{Code: r.Code, Mode: r.Mode, Arena: r.Arena, GameMode: r.GameMode,
			Visibility: r.Visibility, Members: len(r.Members), InMatch: r.InMatch}
		for _, m := range r.Members {
			ms := MemberStat{ID: m.ID, Team: m.Team, Role: m.Role, Host: m.ID == r.HostID, Ready: m.Ready}
			if p := h.byID[m.ID]; p != nil {
				ms.Nick, ms.Online = p.Nick, p.Connected()
			}
			rs.Players = append(rs.Players, ms)
		}
		st.Rooms = append(st.Rooms, rs)
	}
	// Обход map даёт случайный порядок: без сортировки диффинг потока админки не сработал бы
	// никогда, да и таблицы прыгали бы на глазах.
	sort.Slice(st.Rooms, func(i, j int) bool { return st.Rooms[i].Code < st.Rooms[j].Code })
	for _, m := range h.matches {
		st.Matches = append(st.Matches, m.Info())
	}
	sort.Slice(st.Matches, func(i, j int) bool {
		if !st.Matches[i].Created.Equal(st.Matches[j].Created) {
			return st.Matches[i].Created.Before(st.Matches[j].Created)
		}
		return st.Matches[i].ID < st.Matches[j].ID
	})
	st.MatchesLive = len(h.matches)
	st.Ranks, st.Bans = h.mod.Ranks(), h.mod.Bans()
	st.ChatSize = len(h.chat)
	for _, buf := range h.roomChat {
		st.RoomChat += len(buf)
	}
	st.ModBroken = h.mod.Broken()
	return st
}

// ---- утилиты ----

// nickErrCode — какой код ошибки отправить игроку: мат в нике объясняется отдельно от
// требований к длине и символам.
func nickErrCode(err error) string {
	if errors.Is(err, protocol.ErrProfaneNick) {
		return protocol.ErrNickProfanity
	}
	return protocol.ErrBadNick
}

func (h *Hub) sendErr(c *ws.Conn, code, msg string) {
	c.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}

func (h *Hub) sendErrP(p *session.Player, code, msg string) {
	p.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}
