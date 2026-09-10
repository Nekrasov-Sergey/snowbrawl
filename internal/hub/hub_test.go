package hub_test

// Интеграционный тест: настоящий HTTP-сервер, настоящие WebSocket-клиенты на Go,
// полный путь «hello → комната → матч с ботами → match.end», реконнект, список комнат,
// готовность с автостартом и вход в идущий матч на место бота.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

type testServer struct {
	hub *hub.Hub
	srv *httptest.Server
	cfg config.Config
	mod *moderation.Store
}

func newServer(t *testing.T, mutate func(*config.Config)) *testServer {
	t.Helper()
	src, err := snowbrawl.Web.ReadFile(snowbrawl.SimPath)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := sim.Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.TickRate = 40 // быстрее, чтобы тесты не ждали
	cfg.ReconnectTTL = 2 * time.Second
	cfg.AFKTimeout = 0 // в тестах не трогаем
	if mutate != nil {
		mutate(&cfg)
	}
	log := zerolog.Nop()
	if cfg.ModerationFile == "" {
		cfg.ModerationFile = filepath.Join(t.TempDir(), "moderation.json")
	}
	mod, err := moderation.Open(cfg.ModerationFile, log)
	if err != nil {
		t.Fatal(err)
	}
	// Серия без файла: история живёт в памяти теста, диск не трогаем.
	series, err := onlinestat.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(cfg, prog, log, mod, series)
	h.Run()
	// TrustProxy прокидываем: без него все тестовые клиенты приходят с 127.0.0.1, и правила,
	// различающие адреса (брони ников, роли, баны), интеграционно не проверить.
	wsServer := ws.NewServer(ws.Options{MaxConns: cfg.MaxConns, MsgRate: 1000, TrustProxy: cfg.TrustProxy, Log: log}, h)
	mux := http.NewServeMux()
	mux.Handle("/ws", wsServer)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { h.Shutdown(); srv.Close() })
	return &testServer{hub: h, srv: srv, cfg: cfg, mod: mod}
}

type client struct {
	t    *testing.T
	c    *websocket.Conn
	ctx  context.Context
	name string
	// welcome
	Token, ID string
	inbox     chan protocol.Envelope
}

func (s *testServer) connect(t *testing.T, nick, token string) *client {
	return s.connectFrom(t, nick, token, "")
}

// connectFrom подключается, представляясь адресом ip через X-Forwarded-For. Работает только
// при cfg.TrustProxy — ровно так же, как в бою за Caddy.
func (s *testServer) connectFrom(t *testing.T, nick, token, ip string) *client {
	t.Helper()
	return s.dial(t, nick, token, ip, false)
}

func (s *testServer) dial(t *testing.T, nick, token, ip string, raw bool) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/ws"
	var opts *websocket.DialOptions
	if ip != "" {
		opts = &websocket.DialOptions{HTTPHeader: http.Header{"X-Forwarded-For": []string{ip}}}
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(1 << 20)
	cl := &client{t: t, c: c, ctx: ctx, name: nick, inbox: make(chan protocol.Envelope, 4096)}
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				close(cl.inbox)
				return
			}
			env, err := protocol.Decode(data)
			if err != nil {
				continue
			}
			cl.inbox <- env
		}
	}()
	cl.send(protocol.CHello, protocol.Hello{Token: token, Nick: nick, BuildVersion: "dev", ProtocolVersion: protocol.Version})
	if raw {
		return cl // welcome может и не прийти: например, ник занят
	}
	var w protocol.Welcome
	cl.expect(protocol.SWelcome, &w)
	cl.Token, cl.ID = w.Token, w.PlayerID
	return cl
}

// connectRaw подключается и говорит hello, но welcome не ждёт: нужен там, где сервер обязан
// ответить ошибкой (занятый ник, мат в нике) и оставить соединение живым.
func (s *testServer) connectRaw(t *testing.T, nick, token, ip string) *client {
	t.Helper()
	return s.dial(t, nick, token, ip, true)
}

func (cl *client) send(typ string, data any) {
	cl.t.Helper()
	if err := cl.c.Write(cl.ctx, websocket.MessageText, protocol.MustEncode(typ, data)); err != nil {
		cl.t.Fatalf("%s: write %s: %v", cl.name, typ, err)
	}
}

// expect ждёт сообщение типа typ (пропуская остальные) и разбирает его в dst.
func (cl *client) expect(typ string, dst any) protocol.Envelope {
	cl.t.Helper()
	return cl.expectWithin(20*time.Second, typ, dst)
}

// expectWithin — то же с явным сроком: бою до KO двадцати секунд мало, а быстрым проверкам
// столько ждать незачем.
func (cl *client) expectWithin(wait time.Duration, typ string, dst any) protocol.Envelope {
	cl.t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case env, ok := <-cl.inbox:
			if !ok {
				cl.t.Fatalf("%s: connection closed while waiting for %s", cl.name, typ)
			}
			if env.Type == protocol.SError && typ != protocol.SError {
				var e protocol.Error
				_ = json.Unmarshal(env.Data, &e)
				cl.t.Fatalf("%s: got error %s (%s) while waiting for %s", cl.name, e.Code, e.Message, typ)
			}
			if env.Type == typ {
				if dst != nil && len(env.Data) > 0 {
					if err := json.Unmarshal(env.Data, dst); err != nil {
						cl.t.Fatalf("%s: unmarshal %s: %v", cl.name, typ, err)
					}
				}
				return env
			}
		case <-deadline:
			cl.t.Fatalf("%s: timeout waiting for %s", cl.name, typ)
		}
	}
}

func (cl *client) close() { _ = cl.c.Close(websocket.StatusNormalClosure, "") }

// waitRoom ждёт состояние комнаты, удовлетворяющее cond: в очереди клиента могут лежать
// состояния прошлых изменений. Каждое сообщение разбирается в свежую структуру — иначе
// json.Unmarshal слил бы поля с omitempty из предыдущего состояния.
func (cl *client) waitRoom(what string, cond func(protocol.RoomState) bool) protocol.RoomState {
	cl.t.Helper()
	for i := 0; i < 20; i++ {
		var st protocol.RoomState
		cl.expect(protocol.SRoomState, &st)
		if cond(st) {
			return st
		}
	}
	cl.t.Fatalf("%s: no room state matching %s", cl.name, what)
	return protocol.RoomState{}
}

func TestRoomMatchWithBotsToEnd(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	guest := s.connect(t, "Гость", "")

	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	if rs.HostID != host.ID || rs.Mode != 2 {
		t.Fatalf("room state: %+v", rs)
	}
	guest.send(protocol.CRoomJoin, protocol.RoomJoin{Code: strings.ToLower(rs.Code)})
	guest.expect(protocol.SRoomState, &rs)
	if len(rs.Players) != 2 {
		t.Fatalf("expected 2 players in room, got %+v", rs.Players)
	}
	guest.send(protocol.CRoomRole, protocol.RoomRole{Role: "Танк"})
	guest.expect(protocol.SRoomState, &rs)
	// Не хост не может стартовать.
	guest.send(protocol.CRoomStart, nil)
	var e protocol.Error
	guest.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNotAllowed {
		t.Fatalf("expected not_allowed, got %s", e.Code)
	}

	host.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	host.expect(protocol.SMatchStart, &ms)
	var msGuest protocol.MatchStart
	guest.expect(protocol.SMatchStart, &msGuest)
	if ms.MatchID != msGuest.MatchID || len(ms.Players) != 4 || ms.RoomCode != rs.Code {
		t.Fatalf("match start mismatch: %+v / %+v", ms, msGuest)
	}
	bots := 0
	for _, p := range ms.Players {
		if p.Bot {
			bots++
		}
	}
	if bots != 2 {
		t.Fatalf("expected 2 bots, got %d", bots)
	}

	// Снапшоты идут обоим.
	var snap protocol.Snapshot
	host.expect(protocol.SSnapshot, &snap)
	guest.expect(protocol.SSnapshot, &snap)
	if snap.Tick == 0 || len(snap.State) == 0 {
		t.Fatalf("bad snapshot: %+v", snap)
	}

	// Люди не играют — матч доигрывают боты (люди стоят, боты их выносят) либо таймер.
	// Чтобы не ждать, гость сам выходит, а хост тоже уходит: матч завершается как abandoned.
	// Выход из матча оставляет в комнате: оба возвращаются в лобби, матч гибнет как abandoned.
	guest.send(protocol.CMatchLeave, nil)
	guest.waitRoom("guest in lobby", func(st protocol.RoomState) bool {
		for _, p := range st.Players {
			if p.ID == guest.ID {
				return !p.InMatch
			}
		}
		return false
	})
	host.send(protocol.CMatchLeave, nil)
	host.waitRoom("host in lobby", func(st protocol.RoomState) bool {
		for _, p := range st.Players {
			if p.ID == host.ID {
				return !p.InMatch
			}
		}
		return false
	})

	st := s.hub.Stats()
	deadline := time.Now().Add(5 * time.Second)
	for st.MatchesLive > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		st = s.hub.Stats()
	}
	if st.MatchesLive != 0 {
		t.Fatalf("match must be destroyed after everyone left: %+v", st)
	}
	host.close()
	guest.close()
}

// snapshotPlayers достаёт бойцов из очередного снапшота. Снапшоты идут 40 раз в секунду,
// поэтому проверять состояние симуляции удобнее по ним, а не по таймингам.
func (cl *client) snapshotPlayers(wait time.Duration) []struct {
	ID       string  `json:"id"`
	Team     string  `json:"team"`
	Role     string  `json:"role"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Charging bool    `json:"charging"`
	RL       float64 `json:"rl"`
	Koed     bool    `json:"koed"`
} {
	cl.t.Helper()
	var snap struct {
		S struct {
			Players []struct {
				ID       string  `json:"id"`
				Team     string  `json:"team"`
				Role     string  `json:"role"`
				X        float64 `json:"x"`
				Y        float64 `json:"y"`
				HP       int     `json:"hp"`
				Charging bool    `json:"charging"`
				RL       float64 `json:"rl"`
				Koed     bool    `json:"koed"`
			} `json:"players"`
			Balls []struct {
				Team string `json:"team"`
			} `json:"balls"`
		} `json:"s"`
	}
	cl.expectWithin(wait, protocol.SSnapshot, &snap)
	return snap.S.Players
}

// TestHumanInputReachesSim — детерминированная часть: ввод игрока доходит до симуляции.
// Ни боя, ни KO: только движение, замах и появившийся снежок. Отсчёт выключен, иначе ввод
// первые три секунды отбрасывается сервером.
func TestHumanInputReachesSim(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.Countdown = 0 })
	p := s.connect(t, "Игрок", "")
	p.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var rs protocol.RoomState
	p.expect(protocol.SRoomState, &rs)
	p.send(protocol.CRoomRole, protocol.RoomRole{Role: "Раннер"})
	p.waitRoom("роль выбрана", func(st protocol.RoomState) bool {
		return len(st.Players) > 0 && st.Players[0].Role == "Раннер"
	})
	p.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	p.expect(protocol.SMatchStart, &ms)

	me := func(list []struct {
		ID       string  `json:"id"`
		Team     string  `json:"team"`
		Role     string  `json:"role"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		HP       int     `json:"hp"`
		Charging bool    `json:"charging"`
		RL       float64 `json:"rl"`
		Koed     bool    `json:"koed"`
	}) *struct {
		ID       string  `json:"id"`
		Team     string  `json:"team"`
		Role     string  `json:"role"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		HP       int     `json:"hp"`
		Charging bool    `json:"charging"`
		RL       float64 `json:"rl"`
		Koed     bool    `json:"koed"`
	} {
		for i := range list {
			if list[i].ID == ms.YourID {
				return &list[i]
			}
		}
		return nil
	}

	start := me(p.snapshotPlayers(5 * time.Second))
	if start == nil {
		t.Fatal("своего бойца нет в снапшоте")
	}
	// Цель движения — вниз по свободному коридору: точка (450,100) занята укрытием арены,
	// в него боец упирается и «не двигается».
	p.send(protocol.CInput, protocol.Input{Kind: "move", X: start.X, Y: 480})
	moved := false
	for i := 0; i < 200 && !moved; i++ {
		if cur := me(p.snapshotPlayers(5 * time.Second)); cur != nil && cur.Y > start.Y+30 {
			moved = true
		}
	}
	if !moved {
		t.Fatal("боец не поехал к цели: ввод move не дошёл до симуляции")
	}

	// Замах виден в снапшоте, бросок создаёт снежок команды A.
	p.send(protocol.CInput, protocol.Input{Kind: "chargeStart", X: 740, Y: 280})
	charging := false
	for i := 0; i < 100 && !charging; i++ {
		if cur := me(p.snapshotPlayers(5 * time.Second)); cur != nil && cur.Charging {
			charging = true
		}
	}
	if !charging {
		t.Fatal("замах не дошёл до симуляции")
	}
	pw := 0.6
	p.send(protocol.CInput, protocol.Input{Kind: "throw", X: 740, Y: 280, Power: &pw})
	if !p.waitBall(5*time.Second, "A") {
		t.Fatal("снежок не появился: бросок не дошёл до симуляции")
	}
	p.close()
}

// waitBall ждёт в снапшотах снежок нужной команды.
func (cl *client) waitBall(wait time.Duration, team string) bool {
	cl.t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		var snap struct {
			S struct {
				Balls []struct {
					Team string `json:"team"`
				} `json:"balls"`
			} `json:"s"`
		}
		cl.expectWithin(time.Until(deadline), protocol.SSnapshot, &snap)
		for _, b := range snap.S.Balls {
			if b.Team == team {
				return true
			}
		}
	}
	return false
}

// TestMatchEndsWithKO — бой до KO и сообщение match.end. Чтобы это не было лотереей:
// отсчёт выключен, бот самого слабого уровня (уклоняется в 10 % случаев вместо 62 %), игрок
// играет Раннером (перезарядка 500 мс) и целится по снапшотам с силой, посчитанной по
// дистанции. Всё в одной горутине: второй читатель inbox мог бы проглотить сам match.end.
func TestMatchEndsWithKO(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.Countdown = 0 })
	p := s.connect(t, "Игрок", "")
	easy := 0
	p.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0, Difficulty: &easy})
	var rs protocol.RoomState
	p.expect(protocol.SRoomState, &rs)
	p.send(protocol.CRoomRole, protocol.RoomRole{Role: "Раннер"})
	p.waitRoom("роль выбрана", func(st protocol.RoomState) bool {
		return len(st.Players) > 0 && st.Players[0].Role == "Раннер"
	})
	p.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	p.expect(protocol.SMatchStart, &ms)

	type snapPlayer struct {
		ID   string  `json:"id"`
		Team string  `json:"team"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		RL   float64 `json:"rl"`
		Stun float64 `json:"stun"`
		Koed bool    `json:"koed"`
	}
	var end protocol.MatchEnd
	deadline := time.After(60 * time.Second)
	var throwAt time.Time // когда отпускать замах; ноль — замаха нет
	var aimX, aimY float64
	got := false
	for !got {
		select {
		case <-deadline:
			t.Fatal("матч не закончился за 60 секунд")
		case env, ok := <-p.inbox:
			if !ok {
				t.Fatal("соединение закрылось до конца матча")
			}
			switch env.Type {
			case protocol.SMatchEnd:
				if err := json.Unmarshal(env.Data, &end); err != nil {
					t.Fatalf("match.end: %v", err)
				}
				got = true
			case protocol.SError:
				var e protocol.Error
				_ = json.Unmarshal(env.Data, &e)
				t.Fatalf("ошибка сервера в бою: %s (%s)", e.Code, e.Message)
			case protocol.SSnapshot:
				var snap struct {
					S struct {
						Players []snapPlayer `json:"players"`
					} `json:"s"`
				}
				if json.Unmarshal(env.Data, &snap) != nil {
					continue
				}
				var me, enemy *snapPlayer
				for i := range snap.S.Players {
					q := &snap.S.Players[i]
					if q.ID == ms.YourID {
						me = q
					} else if !q.Koed {
						enemy = q
					}
				}
				if me == nil || enemy == nil {
					continue
				}
				if !throwAt.IsZero() { // замах идёт — ждём нужной силы и бросаем
					if time.Now().After(throwAt) {
						pw := math.Min(1, math.Max(0, (math.Hypot(aimX-me.X, aimY-me.Y)+20-140)/380))
						p.send(protocol.CInput, protocol.Input{Kind: "throw", X: aimX, Y: aimY, Power: &pw})
						throwAt = time.Time{}
					}
					continue
				}
				if me.RL > 0 || me.Stun > 0 || me.Koed {
					continue // перезарядка, оглушение или уже выбит
				}
				dist := math.Hypot(enemy.X-me.X, enemy.Y-me.Y)
				if dist > 340 { // дальше максимальной дальности броска (140 + 380) — сближаемся
					p.send(protocol.CInput, protocol.Input{Kind: "move", X: enemy.X, Y: enemy.Y})
					continue
				}
				aimX, aimY = enemy.X, enemy.Y
				// Сила по дистанции — та же формула, что у ИИ (updateAI в sim.js).
				power := math.Min(1, math.Max(0, (math.Hypot(aimX-me.X, aimY-me.Y)+20-140)/380))
				p.send(protocol.CInput, protocol.Input{Kind: "chargeStart", X: aimX, Y: aimY})
				throwAt = time.Now().Add(time.Duration(power*1200+60) * time.Millisecond)
			}
		}
	}

	// Кто именно победил — не проверяем: даже слабый бот иногда попадает первым. Важно, что
	// матч закончился по KO и сообщение доехало.
	if end.Reason != "ko" || end.Winner == "" || end.YourTeam != "A" {
		t.Fatalf("unexpected match end: %+v", end)
	}
	// После матча из комнаты приходит room.state с результатом.
	st := p.waitRoom("матч закончен", func(st protocol.RoomState) bool { return !st.InMatch })
	if st.LastWinner == "" {
		t.Fatalf("room after match: %+v", st)
	}
	p.close()
}

func TestPveRoomMatch(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")

	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)

	normal := 1
	host.send(protocol.CRoomConfig, protocol.RoomConfig{
		Mode: 2, Arena: 0, GameMode: "survival", Campaign: true, Difficulty: &normal,
	})
	host.expect(protocol.SRoomState, &rs)
	if rs.GameMode != "survival" || !rs.Campaign {
		t.Fatalf("room config not applied: %+v", rs)
	}

	host.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	host.expect(protocol.SMatchStart, &ms)
	if ms.GameMode != "survival" {
		t.Fatalf("match.start gameMode = %q", ms.GameMode)
	}
	if len(ms.Players) != 2 { // пати из 2, без команды B
		t.Fatalf("pve roster should be party of 2, got %+v", ms.Players)
	}
	for _, p := range ms.Players {
		if p.Team != "A" {
			t.Fatalf("pve match player on team %q, want A: %+v", p.Team, p)
		}
	}

	type pveSnap struct {
		Players []struct {
			Team string `json:"team"`
			ET   string `json:"et"`
		} `json:"players"`
		Pve *struct {
			Phase string `json:"phase"`
			Wave  int    `json:"wave"`
		} `json:"pve"`
	}
	sawPveBlock, sawEnemy := false, false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (!sawPveBlock || !sawEnemy) {
		var snap protocol.Snapshot
		host.expect(protocol.SSnapshot, &snap)
		var ss pveSnap
		if err := json.Unmarshal(snap.State, &ss); err != nil {
			t.Fatalf("snapshot state: %v", err)
		}
		if ss.Pve != nil {
			sawPveBlock = true
		}
		for _, p := range ss.Players {
			if p.Team == "B" && p.ET != "" {
				sawEnemy = true
			}
		}
	}
	if !sawPveBlock || !sawEnemy {
		t.Fatalf("pve match: sawPveBlock=%v sawEnemy=%v", sawPveBlock, sawEnemy)
	}

	host.send(protocol.CMatchLeave, nil)
	host.waitRoom("host in lobby", func(st protocol.RoomState) bool {
		for _, p := range st.Players {
			if p.ID == host.ID {
				return !p.InMatch
			}
		}
		return false
	})
	host.close()
}

func TestReconnectIntoMatch(t *testing.T) {
	s := newServer(t, nil)
	p := s.connect(t, "Игрок", "")
	p.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 1})
	p.expect(protocol.SRoomState, nil)
	p.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	p.expect(protocol.SMatchStart, &ms)
	p.expect(protocol.SSnapshot, nil)
	p.close()
	time.Sleep(100 * time.Millisecond)

	// Возвращаемся по токену: сервер снова присылает match.start того же матча и снапшоты.
	p2 := s.connect(t, "Игрок", p.Token)
	if p2.ID != p.ID {
		t.Fatalf("player id must survive reconnect: %s != %s", p2.ID, p.ID)
	}
	var ms2 protocol.MatchStart
	p2.expect(protocol.SMatchStart, &ms2)
	if ms2.MatchID != ms.MatchID {
		t.Fatalf("reconnected into a different match: %s != %s", ms2.MatchID, ms.MatchID)
	}
	p2.expect(protocol.SSnapshot, nil)
	p2.close()

	// Без реконнекта дольше TTL сессия истекает, матч без людей уничтожается.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		st := s.hub.Stats()
		if st.MatchesLive == 0 && st.Players == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("session/match must expire: %+v", s.hub.Stats())
}

func TestRoomListSectionsOrderAndPaging(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.RoomsPerIP = 100 })
	// Три PvP-комнаты по очереди и одна PvE: список должен отдать только свой раздел
	// и в порядке создания, старые первыми.
	var codes []string
	for i := 0; i < 3; i++ {
		host := s.connect(t, fmt.Sprintf("Хост%d", i), "")
		host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
		var rs protocol.RoomState
		host.expect(protocol.SRoomState, &rs)
		codes = append(codes, rs.Code)
		time.Sleep(10 * time.Millisecond) // чтобы CreatedAt отличался
	}
	pveHost := s.connect(t, "ПвеХост", "")
	pveHost.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0, GameMode: "survival", Campaign: true})
	pveHost.expect(protocol.SRoomState, nil)

	viewer := s.connect(t, "Зритель", "")
	viewer.send(protocol.CRoomList, protocol.RoomList{Section: "pvp", Page: 0})
	var page protocol.RoomListPage
	viewer.expect(protocol.SRoomList, &page)
	if page.Total != 3 || len(page.Rooms) != 3 || page.Pages != 1 {
		t.Fatalf("pvp list: %+v", page)
	}
	for i, r := range page.Rooms {
		if r.Code != codes[i] {
			t.Fatalf("room %d: got %s, want %s (order by creation)", i, r.Code, codes[i])
		}
		if r.Section != "pvp" || r.Capacity != 4 || r.Humans != 1 || r.Bots != 3 || !r.Joinable {
			t.Fatalf("brief %+v", r)
		}
	}
	viewer.send(protocol.CRoomList, protocol.RoomList{Section: "pve", Page: 0})
	viewer.expect(protocol.SRoomList, &page)
	if page.Total != 1 || page.Rooms[0].Section != "pve" || page.Rooms[0].GameMode != "survival" {
		t.Fatalf("pve list: %+v", page)
	}
	// Подписка живая: новая комната прилетает без запроса.
	extra := s.connect(t, "Ещё", "")
	extra.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0, GameMode: "defense"})
	extra.expect(protocol.SRoomState, nil)
	viewer.expect(protocol.SRoomList, &page)
	if page.Total != 2 {
		t.Fatalf("push must bring the new pve room: %+v", page)
	}
}

func TestClosedRoomHidesCodeAndLimitsTries(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0, Visibility: "closed"})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	if rs.Visibility != "closed" {
		t.Fatalf("visibility: %+v", rs)
	}
	viewer := s.connect(t, "Зритель", "")
	viewer.send(protocol.CRoomList, protocol.RoomList{Section: "pvp"})
	var page protocol.RoomListPage
	viewer.expect(protocol.SRoomList, &page)
	if len(page.Rooms) != 1 || page.Rooms[0].Code != "" || !page.Rooms[0].NeedCode {
		t.Fatalf("closed room must be visible without code: %+v", page.Rooms)
	}
	// Пять неудачных попыток кода, шестая — отказ.
	var e protocol.Error
	for i := 0; i < 5; i++ {
		viewer.send(protocol.CRoomJoin, protocol.RoomJoin{Code: "0000"})
		viewer.expect(protocol.SError, &e)
		if e.Code != protocol.ErrRoomNotFound && e.Code != protocol.ErrBadCode {
			t.Fatalf("attempt %d: %+v", i, e)
		}
	}
	viewer.send(protocol.CRoomJoin, protocol.RoomJoin{Code: "0000"})
	viewer.expect(protocol.SError, &e)
	if e.Code != protocol.ErrTooManyTries {
		t.Fatalf("expected too_many_tries, got %s", e.Code)
	}
	// Правильный код после лимита тоже не пускает — лимит по адресу, а не по коду.
	viewer.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	viewer.expect(protocol.SError, &e)
	if e.Code != protocol.ErrTooManyTries {
		t.Fatalf("expected too_many_tries, got %s", e.Code)
	}
}

func TestReadyAutoStartsMatch(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	guest := s.connect(t, "Гость", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	guest.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	guest.expect(protocol.SRoomState, &rs)

	// Готов только хост — матч не стартует.
	host.send(protocol.CRoomReady, protocol.RoomReady{Ready: true})
	host.waitRoom("ready=1", func(st protocol.RoomState) bool { return st.ReadyCount == 1 && !st.InMatch })
	time.Sleep(300 * time.Millisecond)
	if live := s.hub.Stats().MatchesLive; live != 0 {
		t.Fatalf("match must not start with one ready, live=%d", live)
	}
	// Готов второй — матч стартует сам, без кнопки хоста.
	guest.send(protocol.CRoomReady, protocol.RoomReady{Ready: true})
	var msHost, msGuest protocol.MatchStart
	host.expect(protocol.SMatchStart, &msHost)
	guest.expect(protocol.SMatchStart, &msGuest)
	if msHost.MatchID != msGuest.MatchID || msHost.RoomCode != rs.Code {
		t.Fatalf("auto start: %+v / %+v", msHost, msGuest)
	}
}

func TestConfigChangeResetsReady(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	guest := s.connect(t, "Гость", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	guest.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	guest.expect(protocol.SRoomState, &rs)
	guest.send(protocol.CRoomReady, protocol.RoomReady{Ready: true})
	guest.waitRoom("ready=1", func(st protocol.RoomState) bool { return st.ReadyCount == 1 })
	host.send(protocol.CRoomConfig, protocol.RoomConfig{Mode: 3, Arena: 1})
	st := host.waitRoom("mode=3", func(st protocol.RoomState) bool { return st.Mode == 3 })
	if st.ReadyCount != 0 {
		t.Fatalf("config change must reset ready: %+v", st)
	}
	for _, pl := range st.Players {
		if pl.Ready {
			t.Fatalf("player must not stay ready: %+v", pl)
		}
	}
}

func TestJoinRunningMatchTakesOwnSlot(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	host.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	host.expect(protocol.SMatchStart, &ms)
	if len(ms.Players) != 4 {
		t.Fatalf("match players: %+v", ms.Players)
	}
	// Каждый боец привязан к слоту комнаты: у ботов индексы свободных мест.
	seen := map[string]bool{}
	for _, p := range ms.Players {
		key := p.Team + string(rune('0'+p.Index))
		if seen[key] {
			t.Fatalf("slot %s used twice: %+v", key, ms.Players)
		}
		seen[key] = true
	}

	// Комната в матче видна в списке и открыта для входа: людей меньше, чем мест.
	guest := s.connect(t, "Гость", "")
	guest.send(protocol.CRoomList, protocol.RoomList{Section: "pvp"})
	var page protocol.RoomListPage
	guest.expect(protocol.SRoomList, &page)
	if len(page.Rooms) != 1 || !page.Rooms[0].InMatch || !page.Rooms[0].Joinable {
		t.Fatalf("running room in list: %+v", page.Rooms)
	}
	// Вход в комнату во время матча даёт свободный слот и сводку матча в лобби.
	guest.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	st := guest.waitRoom("me placed", func(st protocol.RoomState) bool { return len(st.Players) == 2 })
	var mySlot protocol.RoomPlayer
	for _, p := range st.Players {
		if p.ID == guest.ID {
			mySlot = p
		}
	}
	if mySlot.Team == "" || mySlot.InMatch {
		t.Fatalf("guest must get a free slot and stay in lobby: %+v", st.Players)
	}
	var rm protocol.RoomMatch
	guest.expect(protocol.SRoomMatch, &rm)
	if len(rm.Slots) != 4 || rm.TimeLeftMs <= 0 {
		t.Fatalf("lobby must see the match: %+v", rm)
	}
	// Присоединяемся к матчу — садимся именно за бойца своего слота.
	guest.send(protocol.CMatchJoin, nil)
	var msGuest protocol.MatchStart
	guest.expect(protocol.SMatchStart, &msGuest)
	var mine protocol.MatchPlayer
	for _, p := range msGuest.Players {
		if p.ID == msGuest.YourID {
			mine = p
		}
	}
	if mine.Team != mySlot.Team || mine.Index != mySlot.Index || mine.Nick != "Гость" || mine.Bot {
		t.Fatalf("joined the wrong fighter: slot=%+v fighter=%+v", mySlot, mine)
	}
	var roster protocol.MatchRoster
	host.expect(protocol.SMatchRoster, &roster)

	// Выход из матча оставляет в комнате и держит место: возвращаемся за того же бойца.
	guest.send(protocol.CMatchLeave, nil)
	back := guest.waitRoom("back in lobby", func(st protocol.RoomState) bool {
		for _, p := range st.Players {
			if p.ID == guest.ID {
				return !p.InMatch
			}
		}
		return false
	})
	for _, p := range back.Players {
		if p.ID == guest.ID && (p.Team != mySlot.Team || p.Index != mySlot.Index) {
			t.Fatalf("slot must stay with the player: %+v", p)
		}
	}
	guest.send(protocol.CMatchJoin, nil)
	guest.expect(protocol.SMatchStart, &msGuest)
	if msGuest.YourID != mine.ID {
		t.Fatalf("player must return to the same fighter: %s != %s", msGuest.YourID, mine.ID)
	}
}

func TestRoomListSortsOpenAndFreeFirst(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.RoomsPerIP = 100 })
	// Четыре комнаты 1×1: открытая свободная, открытая заполненная, закрытая свободная,
	// закрытая заполненная. Создаём в обратном порядке — сортировка должна их развернуть.
	mk := func(nick, visibility string, fill bool) string {
		host := s.connect(t, nick, "")
		host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0, Visibility: visibility})
		var rs protocol.RoomState
		host.expect(protocol.SRoomState, &rs)
		if fill {
			mate := s.connect(t, nick+"-2", "")
			mate.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
			mate.expect(protocol.SRoomState, nil)
		}
		time.Sleep(10 * time.Millisecond)
		return rs.Code
	}
	closedFull := mk("ЗакрПолн", "closed", true)
	closedFree := mk("ЗакрСвоб", "closed", false)
	openFull := mk("ОткрПолн", "open", true)
	openFree := mk("ОткрСвоб", "open", false)

	viewer := s.connect(t, "Зритель", "")
	viewer.send(protocol.CRoomList, protocol.RoomList{Section: "pvp"})
	var page protocol.RoomListPage
	viewer.expect(protocol.SRoomList, &page)
	if len(page.Rooms) != 4 {
		t.Fatalf("expected 4 rooms, got %+v", page.Rooms)
	}
	// У закрытых комнат кода нет, поэтому сверяем по признакам.
	kind := func(b protocol.RoomBrief) string {
		k := b.Visibility
		if b.Humans >= b.Capacity {
			return k + "-full"
		}
		return k + "-free"
	}
	want := []string{"open-free", "open-full", "closed-free", "closed-full"}
	for i, b := range page.Rooms {
		if kind(b) != want[i] {
			t.Fatalf("room %d is %s, want %s (%+v)", i, kind(b), want[i], page.Rooms)
		}
	}
	if page.Rooms[0].Code != openFree || page.Rooms[1].Code != openFull {
		t.Fatalf("open rooms out of order: %+v", page.Rooms)
	}
	_, _ = closedFull, closedFree
}

func TestDrainBlocksNewMatches(t *testing.T) {
	s := newServer(t, nil)
	p := s.connect(t, "Игрок", "")
	s.hub.SetDrain(true)
	var d protocol.Drain
	p.expect(protocol.SDrain, &d)
	if !d.Active {
		t.Fatal("drain must be active")
	}
	p.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	p.expect(protocol.SRoomState, nil)
	p.send(protocol.CRoomStart, nil)
	var e protocol.Error
	p.expect(protocol.SError, &e)
	if e.Code != protocol.ErrDraining {
		t.Fatalf("expected draining, got %s", e.Code)
	}
	s.hub.SetDrain(false)
	p.expect(protocol.SDrain, &d)
	if d.Active {
		t.Fatal("drain must be off")
	}
}

func TestBadProtocolVersionGetsReload(t *testing.T) {
	s := newServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(ctx, websocket.MessageText, protocol.MustEncode(protocol.CHello, protocol.Hello{Nick: "x1", ProtocolVersion: 99}))
	sawReload := false
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		env, _ := protocol.Decode(data)
		if env.Type == protocol.SReload {
			sawReload = true
		}
	}
	if !sawReload {
		t.Fatal("client with wrong protocol must receive reload and be closed")
	}
}

// Счётчик онлайна привязан к игровому сокету: первый же клиент видит в welcome единицу
// (раньше число бралось отдельным HTTP-запросом, который успевал ответить нулём до hello),
// а подключение и отключение соседа приходят push-сообщением online.
func TestOnlineCountFollowsConnections(t *testing.T) {
	s := newServer(t, nil)

	a := s.connect(t, "Аня", "")
	if got := s.hub.Online(); got != 1 {
		t.Fatalf("после первого подключения online = %d, ожидалась 1", got)
	}

	b := s.connect(t, "Боря", "")
	var on protocol.Online
	a.expect(protocol.SOnline, &on)
	if on.N != 2 {
		t.Fatalf("push после второго подключения: n = %d, ожидалось 2", on.N)
	}

	b.close()
	a.expect(protocol.SOnline, &on)
	if on.N != 1 {
		t.Fatalf("push после отключения: n = %d, ожидалась 1", on.N)
	}
}

// Welcome несёт актуальное число игроков, включая самого подключившегося.
func TestWelcomeCarriesOnline(t *testing.T) {
	s := newServer(t, nil)
	s.connect(t, "Первый", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()
	if err := c.Write(ctx, websocket.MessageText, protocol.MustEncode(protocol.CHello,
		protocol.Hello{Nick: "Второй", BuildVersion: "dev", ProtocolVersion: protocol.Version})); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		env, err := protocol.Decode(data)
		if err != nil || env.Type != protocol.SWelcome {
			continue
		}
		var w protocol.Welcome
		if err := json.Unmarshal(env.Data, &w); err != nil {
			t.Fatal(err)
		}
		if w.Online != 2 {
			t.Fatalf("welcome.online = %d, ожидалось 2 (сам плюс уже подключённый)", w.Online)
		}
		return
	}
}

func TestStatsCarriesNicks(t *testing.T) {
	s := newServer(t, nil)
	host := s.connect(t, "Хозяин", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)

	st := s.hub.Stats()
	var found *hub.PlayerStat
	for i := range st.Sessions {
		if st.Sessions[i].Nick == "Хозяин" {
			found = &st.Sessions[i]
		}
	}
	if found == nil {
		t.Fatalf("сессия с ником не найдена: %+v", st.Sessions)
	}
	if found.Place != "room" || found.Where != rs.Code {
		t.Fatalf("место игрока = %q/%q, ожидалось room/%s", found.Place, found.Where, rs.Code)
	}
	if !found.Online {
		t.Fatal("игрок помечен как отключённый, хотя соединение живо")
	}
	if len(st.Rooms) != 1 || len(st.Rooms[0].Players) != 1 {
		t.Fatalf("комнаты в сводке: %+v", st.Rooms)
	}
	m := st.Rooms[0].Players[0]
	if m.Nick != "Хозяин" || !m.Host {
		t.Fatalf("участник комнаты = %+v, ожидался хост с ником", m)
	}

	// В матче состав тоже с никами: человек и добранный бот.
	host.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	host.expect(protocol.SMatchStart, &ms)
	st = s.hub.Stats()
	if len(st.Matches) != 1 {
		t.Fatalf("матчей в сводке: %d", len(st.Matches))
	}
	nicks := map[string]bool{}
	for _, p := range st.Matches[0].Players {
		nicks[p.Nick] = p.Bot
	}
	if bot, ok := nicks["Хозяин"]; !ok || bot {
		t.Fatalf("состав матча = %+v, человек не найден", st.Matches[0].Players)
	}
	if len(st.Matches[0].Players) != 2 {
		t.Fatalf("в матче 1×1 ожидались двое, получено %+v", st.Matches[0].Players)
	}
	host.close()
}

func TestTrainingShownInStats(t *testing.T) {
	s := newServer(t, nil)
	p := s.connect(t, "Тренирующийся", "")
	p.send(protocol.CTraining, protocol.Training{On: true, Mode: 3, Arena: 2, Role: "Снайпер"})

	// Ответа на training нет, поэтому ждём, пока hub обработает сообщение.
	var ps *hub.PlayerStat
	for i := 0; i < 100; i++ {
		st := s.hub.Stats()
		for j := range st.Sessions {
			if st.Sessions[j].Training {
				ps = &st.Sessions[j]
			}
		}
		if ps != nil {
			if st.Training != 1 {
				t.Fatalf("счётчик тренировок = %d, ожидалась 1", st.Training)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ps == nil {
		t.Fatal("тренировка не попала в сводку")
	}
	if ps.Nick != "Тренирующийся" || ps.TrainingMode != 3 || ps.TrainingArena != 2 || ps.TrainingRole != "Снайпер" {
		t.Fatalf("сводка тренировки = %+v", *ps)
	}
	if ps.Place != string(session.InMenu) {
		t.Fatalf("место игрока = %q, тренировка не должна его менять", ps.Place)
	}

	// Выход из тренировки снимает пометку.
	p.send(protocol.CTraining, protocol.Training{On: false})
	for i := 0; i < 100; i++ {
		if s.hub.Stats().Training == 0 {
			p.close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("пометка тренировки не снялась")
}

// TestChat — сообщение доходит до всех, история приходит новому клиенту, флуд отклоняется.
// Пауза между сообщениями задрана до часа: любое второе сообщение в тесте — флуд,
// проверка не зависит от таймингов гонки/CI.
func TestChat(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = time.Hour })
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")

	// Пустое сообщение отклоняется до проверки антифлуда.
	a.send(protocol.CChatSend, protocol.ChatSend{Text: "   "})
	var e protocol.Error
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBadMessage {
		t.Fatalf("ожидался bad_message на пустое, got %s", e.Code)
	}

	a.send(protocol.CChatSend, protocol.ChatSend{Text: "  привет   всем\n\n"})
	var m protocol.ChatMessage
	a.expect(protocol.SChatMsg, &m)
	if m.Nick != "Аня" || m.Text != "привет всем" || m.ID == 0 || m.TS == 0 {
		t.Fatalf("chat.msg = %+v", m)
	}
	b.expect(protocol.SChatMsg, &m)
	if m.Nick != "Аня" || m.Text != "привет всем" {
		t.Fatalf("второй клиент получил %+v", m)
	}

	// Второе сообщение — флуд (пауза час).
	a.send(protocol.CChatSend, protocol.ChatSend{Text: "ещё"})
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrChatFlood {
		t.Fatalf("ожидался chat_flood, got %s", e.Code)
	}

	// Новый клиент получает историю.
	c := s.connect(t, "Вика", "")
	var hist protocol.ChatHistory
	c.expect(protocol.SChatHistory, &hist)
	if len(hist.Messages) != 1 || hist.Messages[0].Text != "привет всем" {
		t.Fatalf("история = %+v", hist.Messages)
	}
	a.close()
	b.close()
	c.close()
}
