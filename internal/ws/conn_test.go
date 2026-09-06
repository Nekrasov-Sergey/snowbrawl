package ws_test

// Проверка heartbeat: живое соединение переживает несколько периодов пинга,
// а молчащий клиент (не отвечает на ping) закрывается по таймауту.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

type recorder struct {
	mu       sync.Mutex
	messages int
	closed   int
}

func (r *recorder) OnMessage(_ *ws.Conn, _ protocol.Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages++
}

func (r *recorder) OnClose(_ *ws.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
}

func (r *recorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.messages, r.closed
}

func newWSServer(t *testing.T, opts ws.Options) (*httptest.Server, *ws.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	opts.Log = zerolog.Nop()
	srv := ws.NewServer(opts, rec)
	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv, srv, rec
}

// Клиент, который читает сокет (то есть отвечает на ping), живёт сколько угодно.
func TestHeartbeatKeepsLiveConnection(t *testing.T) {
	httpSrv, srv, rec := newWSServer(t, ws.Options{PingPeriod: 100 * time.Millisecond, PongTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()

	// Читаем в фоне: библиотека отвечает на ping только во время чтения.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	time.Sleep(600 * time.Millisecond) // шесть периодов пинга
	if got := srv.Count(); got != 1 {
		t.Fatalf("после шести пингов соединений %d, ожидалось 1", got)
	}
	if err := c.Write(ctx, websocket.MessageText, protocol.MustEncode("ping", nil)); err != nil {
		t.Fatalf("живое соединение не принимает сообщения: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if msgs, _ := rec.counts(); msgs > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("сервер не получил сообщение по живому соединению")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Клиент, который перестал читать сокет, не отвечает на ping — сервер закрывает соединение.
func TestHeartbeatDropsSilentConnection(t *testing.T) {
	httpSrv, srv, rec := newWSServer(t, ws.Options{PingPeriod: 100 * time.Millisecond, PongTimeout: 200 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.CloseNow() }()
	// Намеренно не читаем: pong не уйдёт, соединение для сервера мёртвое.

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, closed := rec.counts()
		if closed > 0 && srv.Count() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("молчащее соединение не закрыто: закрытий %d, открыто %d", closed, srv.Count())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
