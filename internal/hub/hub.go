// Package hub — центр сервера: сессии игроков, комнаты, список комнат, реестр матчей,
// дренаж. Все структуры данных под одним мьютексом; матчи живут в своих горутинах и
// общаются с hub через потокобезопасные методы и колбэк onEnd.
package hub

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/match"
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

	mu       sync.Mutex
	byToken  map[string]*session.Player
	byID     map[string]*session.Player
	rooms    map[string]*room.Room
	matches  map[string]*match.Match
	draining bool
	drainAt  time.Time
	rng      *rand.Rand
	// listSubs — кто смотрит список комнат: страницу пересобираем в тике и отправляем,
	// только если она изменилась. Ключ — id игрока.
	listSubs map[string]*listSub
	// codeTries — неудачные попытки войти по коду, по IP: защита от перебора закрытых комнат.
	codeTries map[string]*codeTry
	// matchPush — последняя отправленная в лобби сводка идущего матча, по коду комнаты.
	matchPush map[string]string
	// lastOnline — последнее разосланное число игроков: рассылаем только при изменении.
	lastOnline int

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New создаёт hub.
func New(cfg config.Config, prog *sim.Program, log zerolog.Logger) *Hub {
	return &Hub{
		cfg: cfg, prog: prog, log: log, now: time.Now,
		byToken: map[string]*session.Player{}, byID: map[string]*session.Player{},
		rooms:    map[string]*room.Room{},
		matches:  map[string]*match.Match{},
		listSubs: map[string]*listSub{}, codeTries: map[string]*codeTry{},
		matchPush: map[string]string{},
		rng:       rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xDEADBEEF)),
		stopCh:    make(chan struct{}),
	}
}

// Run запускает фоновый цикл таймаутов (TTL сессий и комнат, рассылка списка комнат).
func (h *Hub) Run() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		t := time.NewTicker(500 * time.Millisecond)
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
	time.Sleep(200 * time.Millisecond) // дать match.end уйти в сокеты
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
	case protocol.CPing:
		c.Send(protocol.MustEncode(protocol.SPong, nil))
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
	now := h.now()
	var p *session.Player
	if hello.Token != "" {
		p = h.byToken[hello.Token]
	}
	if p == nil {
		nick, err := protocol.NormalizeNick(hello.Nick)
		if err != nil {
			h.sendErr(c, protocol.ErrBadNick, err.Error())
			return
		}
		p = session.New(nick, c.IP(), now)
		h.byToken[p.Token] = p
		h.byID[p.ID] = p
		h.log.Info().Str("player", p.ID).Str("nick", nick).Str("ip", p.IP).Msg("new player")
	} else {
		if hello.Nick != "" && p.Place == session.InMenu {
			if nick, err := protocol.NormalizeNick(hello.Nick); err == nil {
				p.Nick = nick
			}
		}
		if old, ok := p.Conn.(*ws.Conn); ok && old != c {
			old.Session = nil
			old.Close(websocket.StatusPolicyViolation, "replaced by new connection")
		}
		p.IP = c.IP()
	}
	p.Conn = c
	c.Session = p

	c.Send(protocol.MustEncode(protocol.SWelcome, protocol.Welcome{
		Token: p.Token, PlayerID: p.ID, Nick: p.Nick, Build: h.cfg.BuildVersion, SimVersion: h.prog.Version(),
		Proto: protocol.Version, Draining: h.draining, Resume: string(p.Place), Online: h.onlineLocked(),
	}))
	if h.draining {
		c.Send(h.drainMessage())
	}
	// Восстановление места.
	switch p.Place {
	case session.InRoom:
		if r := h.rooms[p.RoomCode]; r != nil {
			h.broadcastRoom(r)
		} else {
			p.ToMenu()
		}
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			c.Send(m.StartMessage(p.ID))
			m.Attach(p.ID, c)
		} else {
			p.ToMenu()
			if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
				p.Place, p.RoomCode = session.InRoom, r.Code
				h.broadcastRoom(r)
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
	h.sendRoomMatch(p, r) // вошёл в комнату с идущим матчем — сразу показываем, что там происходит
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
		delete(h.rooms, r.Code)
		return
	}
	h.broadcastRoom(r)
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
		players = append(players, protocol.MatchPlayer{ID: m.ID, Nick: mp.Nick, Team: m.Team, Index: m.Index, Role: m.Role})
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
		}
		st.Players = append(st.Players, rp)
	}
	return st
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
	m, err := match.New(h.prog, roomCode, mode, arena, players, match.Options{
		TickRate: h.cfg.TickRate, AFKTimeout: h.cfg.AFKTimeout, Countdown: h.cfg.Countdown, Log: h.log, Now: h.now,
		GameMode: gameMode, Campaign: campaign, Difficulty: difficulty,
	}, h.onMatchEnd)
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
	for _, team := range teams {
		for i := 0; i < mode; i++ {
			if taken[team+strconv.Itoa(i)] {
				continue
			}
			botN++
			level := difficulty
			bp := protocol.MatchPlayer{
				ID: fmt.Sprintf("bot%d", botN), Nick: fmt.Sprintf("Бот %d", botN), Team: team,
				Index: i, Role: roles[h.rng.IntN(len(roles))], Bot: true, BotLevel: &level,
			}
			if pve {
				bp.Nick = fmt.Sprintf("Союзник %d", botN)
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
			delete(h.rooms, r.Code)
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

	for ip, t := range h.codeTries {
		if now.After(t.resetAt) {
			delete(h.codeTries, ip)
		}
	}

	for code, r := range h.rooms {
		if r.IsEmpty() && !r.InMatch && now.Sub(r.EmptySince) > h.cfg.RoomTTL {
			delete(h.rooms, code)
		}
	}

	for _, p := range h.byID {
		if p.Connected() {
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
}

// PlayerStat — сессия игрока в сводке: кто это, где находится и на связи ли.
type PlayerStat struct {
	ID         string `json:"id"`
	Nick       string `json:"nick"`
	IP         string `json:"ip"`
	Place      string `json:"place"`           // menu | room | match
	Where      string `json:"where,omitempty"` // код комнаты, режим очереди или id матча
	Online     bool   `json:"online"`
	AgeMs      int64  `json:"ageMs"`                  // сколько существует сессия
	OfflineFor int64  `json:"offlineForMs,omitempty"` // сколько нет связи

	// Тренировка с ботами: сервер её не считает, данные со слов клиента.
	Training      bool   `json:"training,omitempty"`
	TrainingMode  int    `json:"trainingMode,omitempty"`
	TrainingArena int    `json:"trainingArena,omitempty"`
	TrainingRole  string `json:"trainingRole,omitempty"`
	TrainingMs    int64  `json:"trainingMs,omitempty"`
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
			AgeMs: now.Sub(p.CreatedAt).Milliseconds(),
		}
		switch p.Place {
		case session.InRoom:
			ps.Where = p.RoomCode
		case session.InMatch:
			ps.Where = p.MatchID
		}
		if !online && !p.DisconnectedAt.IsZero() {
			ps.OfflineFor = now.Sub(p.DisconnectedAt).Milliseconds()
		}
		if p.Training {
			ps.Training, ps.TrainingMode, ps.TrainingArena, ps.TrainingRole = true, p.TrainingMode, p.TrainingArena, p.TrainingRole
			ps.TrainingMs = now.Sub(p.TrainingSince).Milliseconds()
			st.Training++
		}
		st.Sessions = append(st.Sessions, ps)
	}
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].Nick < st.Sessions[j].Nick })
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
	for _, m := range h.matches {
		st.Matches = append(st.Matches, m.Info())
	}
	st.MatchesLive = len(h.matches)
	return st
}

// ---- утилиты ----

func (h *Hub) sendErr(c *ws.Conn, code, msg string) {
	c.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}

func (h *Hub) sendErrP(p *session.Player, code, msg string) {
	p.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}
