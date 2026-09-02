package hub_test

// Интеграционный тест: настоящий HTTP-сервер, настоящие WebSocket-клиенты на Go,
// полный путь «hello → комната → матч с ботами → match.end», реконнект и Quick Match.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

type testServer struct {
	hub *hub.Hub
	srv *httptest.Server
	cfg config.Config
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
	cfg.QueueWait = 300 * time.Millisecond
	cfg.ReconnectTTL = 2 * time.Second
	cfg.AFKTimeout = 0 // в тестах не трогаем
	if mutate != nil {
		mutate(&cfg)
	}
	log := zerolog.Nop()
	h := hub.New(cfg, prog, log)
	h.Run()
	wsServer := ws.NewServer(ws.Options{MaxConns: cfg.MaxConns, MsgRate: 1000, Log: log}, h)
	mux := http.NewServeMux()
	mux.Handle("/ws", wsServer)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { h.Shutdown(); srv.Close() })
	return &testServer{hub: h, srv: srv, cfg: cfg}
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
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, url, nil)
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
	var w protocol.Welcome
	cl.expect(protocol.SWelcome, &w)
	cl.Token, cl.ID = w.Token, w.PlayerID
	return cl
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
	deadline := time.After(20 * time.Second)
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
	guest.send(protocol.CMatchLeave, nil)
	guest.expect(protocol.SRoomLeft, nil)
	host.send(protocol.CMatchLeave, nil)
	host.expect(protocol.SRoomLeft, nil)

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

func TestHumanInputAndMatchEnd(t *testing.T) {
	s := newServer(t, nil)
	p := s.connect(t, "Игрок", "")
	p.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var rs protocol.RoomState
	p.expect(protocol.SRoomState, &rs)
	p.send(protocol.CRoomStart, nil)
	var ms protocol.MatchStart
	p.expect(protocol.SMatchStart, &ms)

	// Игрок двигается и бросает, бот отвечает: матч 1×1 должен закончиться KO кого-то из них.
	go func() {
		for i := 0; i < 400; i++ {
			time.Sleep(60 * time.Millisecond)
			_ = p.c.Write(p.ctx, websocket.MessageText, protocol.MustEncode(protocol.CInput, protocol.Input{Kind: "move", X: 450, Y: 100}))
			_ = p.c.Write(p.ctx, websocket.MessageText, protocol.MustEncode(protocol.CInput, protocol.Input{Kind: "chargeStart", X: 740, Y: 280}))
			time.Sleep(600 * time.Millisecond)
			pw := 0.5
			_ = p.c.Write(p.ctx, websocket.MessageText, protocol.MustEncode(protocol.CInput, protocol.Input{Kind: "throw", X: 740, Y: 280, Power: &pw}))
		}
	}()
	var end protocol.MatchEnd
	p.expect(protocol.SMatchEnd, &end)
	if end.Reason != "ko" || end.Winner == "" || end.YourTeam != "A" {
		t.Fatalf("unexpected match end: %+v", end)
	}
	// После матча из комнаты приходит room.state с результатом.
	p.expect(protocol.SRoomState, &rs)
	if rs.InMatch || rs.LastWinner == "" {
		t.Fatalf("room after match: %+v", rs)
	}
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

func TestQuickMatchFillsWithBots(t *testing.T) {
	s := newServer(t, nil)
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")
	a.send(protocol.CQueueJoin, protocol.QueueJoin{Mode: 2, Role: "Раннер"})
	var qs protocol.QueueStatus
	a.expect(protocol.SQueueStatus, &qs)
	if !qs.InQueue || qs.Players != 1 || qs.Needed != 4 {
		t.Fatalf("queue status: %+v", qs)
	}
	b.send(protocol.CQueueJoin, protocol.QueueJoin{Mode: 2, Role: "Танк"})
	// Через QueueWait добор ботами — оба получают match.start одного матча.
	var msA, msB protocol.MatchStart
	a.expect(protocol.SMatchStart, &msA)
	b.expect(protocol.SMatchStart, &msB)
	if msA.MatchID != msB.MatchID || msA.RoomCode != "" || len(msA.Players) != 4 {
		t.Fatalf("quick match: %+v / %+v", msA, msB)
	}
	humans := 0
	for _, p := range msA.Players {
		if !p.Bot {
			humans++
		}
	}
	if humans != 2 {
		t.Fatalf("expected 2 humans, got %d", humans)
	}
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
	p.send(protocol.CQueueJoin, protocol.QueueJoin{Mode: 1, Role: "Танк"})
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
