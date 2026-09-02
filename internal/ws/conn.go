// Package ws — тонкая обёртка над WebSocket-соединением: цикл чтения с лимитом
// частоты сообщений, неблокирующая очередь отправки, учёт числа соединений.
package ws

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// Handler получает события соединения. Вызовы для одного соединения последовательны.
type Handler interface {
	OnMessage(c *Conn, env protocol.Envelope)
	OnClose(c *Conn)
}

// Options — параметры сервера соединений.
type Options struct {
	MaxConns   int
	MsgRate    int  // сообщений в секунду на соединение
	TrustProxy bool // брать IP из X-Forwarded-For
	Log        zerolog.Logger
}

// Server принимает WebSocket-соединения и раздаёт их Handler-у.
type Server struct {
	opts    Options
	handler Handler
	conns   atomic.Int64
	nextID  atomic.Int64
	wg      sync.WaitGroup
}

// NewServer создаёт сервер соединений.
func NewServer(opts Options, h Handler) *Server {
	if opts.MsgRate <= 0 {
		opts.MsgRate = 30
	}
	return &Server{opts: opts, handler: h}
}

// Count возвращает число открытых соединений.
func (s *Server) Count() int { return int(s.conns.Load()) }

// Wait блокируется до завершения всех циклов чтения.
func (s *Server) Wait() { s.wg.Wait() }

// ServeHTTP апгрейдит запрос до WebSocket и запускает цикл чтения.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.MaxConns > 0 && int(s.conns.Load()) >= s.opts.MaxConns {
		http.Error(w, "server full", http.StatusServiceUnavailable)
		return
	}
	wsc, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Клиент отдаётся этим же сервером, но за прокси Origin может отличаться портом —
		// игра не хранит ничего ценного, проверку Origin не делаем.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.opts.Log.Debug().Err(err).Msg("ws accept")
		return
	}
	wsc.SetReadLimit(protocol.MaxMessageSize)
	c := &Conn{
		ID:     s.nextID.Add(1),
		ip:     clientIP(r, s.opts.TrustProxy),
		ws:     wsc,
		out:    make(chan []byte, 256),
		closed: make(chan struct{}),
		rate:   newBucket(s.opts.MsgRate, s.opts.MsgRate*2),
	}
	s.conns.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.conns.Add(-1)
		c.run(s.handler, s.opts.Log)
	}()
}

// Conn — одно клиентское соединение.
type Conn struct {
	ID   int64
	ip   string
	ws   *websocket.Conn
	out  chan []byte
	rate *bucket

	closeOnce sync.Once
	closed    chan struct{}
	closeCode websocket.StatusCode
	closeText string

	// Session — произвольные данные владельца (hub хранит здесь ссылку на игрока).
	Session any
}

// IP возвращает адрес клиента.
func (c *Conn) IP() string { return c.ip }

// Send ставит сообщение в очередь отправки. Если очередь переполнена (клиент не читает),
// соединение закрывается: медленный клиент не должен тормозить матч.
func (c *Conn) Send(msg []byte) {
	select {
	case <-c.closed:
	case c.out <- msg:
	default:
		c.Close(websocket.StatusPolicyViolation, "slow consumer")
	}
}

// Close инициирует закрытие соединения. Повторные вызовы игнорируются.
func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode, c.closeText = code, reason
		close(c.closed)
	})
}

// Closed сообщает, закрыто ли соединение.
func (c *Conn) Closed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *Conn) run(h Handler, log zerolog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Писатель. При закрытии сначала дописывает то, что уже стоит в очереди
	// (например, error + reload перед разрывом), и только потом выходит.
	writeDone := make(chan struct{})
	write := func(msg []byte) bool {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.ws.Write(wctx, websocket.MessageText, msg)
		wcancel()
		if err != nil {
			c.Close(websocket.StatusAbnormalClosure, "write failed")
			return false
		}
		return true
	}
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-c.closed:
				for {
					select {
					case msg := <-c.out:
						if !write(msg) {
							return
						}
					default:
						return
					}
				}
			case msg := <-c.out:
				if !write(msg) {
					return
				}
			}
		}
	}()

	// Читатель (в текущей горутине).
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := c.ws.Read(ctx)
			if err != nil {
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			if !c.rate.take() {
				c.Close(websocket.StatusPolicyViolation, "rate limit")
				return
			}
			env, err := protocol.Decode(data)
			if err != nil {
				c.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: protocol.ErrBadMessage, Message: err.Error()}))
				continue
			}
			h.OnMessage(c, env)
		}
	}()

	<-c.closed
	<-writeDone // дать очереди отправки опустеть до закрытия сокета
	code, text := c.closeCode, c.closeText
	if code == 0 {
		code = websocket.StatusNormalClosure
	}
	_ = c.ws.Close(code, text)
	cancel()
	<-readDone
	h.OnClose(c)
	log.Debug().Int64("conn", c.ID).Str("ip", c.ip).Str("reason", text).Msg("ws closed")
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i > 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// bucket — простой token bucket для лимита частоты сообщений.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	rate   float64
	last   time.Time
}

func newBucket(rate, burst int) *bucket {
	return &bucket{tokens: float64(burst), max: float64(burst), rate: float64(rate), last: time.Now()}
}

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.max {
		b.tokens = b.max
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ErrClosed возвращается при попытке работать с закрытым соединением.
var ErrClosed = errors.New("connection closed")
