package hub

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Измерение задержки проверяем внутри пакета: снаружи не подменить часы, а без подмены тест
// пришлось бы усыплять на секунды.

type sentConn struct{ msgs [][]byte }

func (c *sentConn) Send(msg []byte) { c.msgs = append(c.msgs, msg) }
func (c *sentConn) Closed() bool    { return false }

// lastPing возвращает последний отправленный зонд и сколько их всего было.
func lastPing(t *testing.T, c *sentConn) (last protocol.Ping, count int) {
	t.Helper()
	for _, raw := range c.msgs {
		env, err := protocol.Decode(raw)
		if err != nil || env.Type != protocol.SPing {
			continue
		}
		var ping protocol.Ping
		if err := json.Unmarshal(env.Data, &ping); err != nil {
			t.Fatal(err)
		}
		last = ping
		count++
	}
	return last, count
}

func pingHub(now *time.Time) *Hub {
	return &Hub{
		log:  zerolog.New(io.Discard),
		byID: map[string]*session.Player{},
		now:  func() time.Time { return *now },
	}
}

func TestPingProbeAndMeasure(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	// Первый зонд уходит сразу: от него живёт индикатор связи у игрока, и держать его пустым
	// лишние секунды незачем.
	h.probePing(p, now)
	first, n := lastPing(t, conn)
	if n != 1 || first.Seq != 1 {
		t.Fatalf("зондов %d, seq %d; ожидались 1 и 1", n, first.Seq)
	}
	if first.Ms != 0 {
		t.Fatalf("в первом зонде ms=%d, задержка ещё не измерена", first.Ms)
	}
	// Пока зонд в полёте, второй не уходит — иначе номер перестал бы что-то значить.
	now = now.Add(pingProbeEvery)
	h.probePing(p, now)
	if _, n := lastPing(t, conn); n != 1 {
		t.Fatalf("зондов %d, ожидался 1 пока предыдущий в полёте", n)
	}

	// Чужой номер игнорируется.
	now = base.Add(40 * time.Millisecond)
	h.handlePong(p, json.RawMessage(`{"seq":99}`))
	if p.PingMs != 0 {
		t.Fatalf("ответ с чужим номером принят: ping=%d", p.PingMs)
	}
	h.handlePong(p, json.RawMessage(`{"seq":1}`))
	if p.PingMs != 40 {
		t.Fatalf("ping=%d, ожидалось 40", p.PingMs)
	}

	// Измеренное значение уезжает клиенту в следующем зонде: отдельного сообщения нет.
	now = now.Add(pingProbeEvery)
	h.probePing(p, now)
	next, _ := lastPing(t, conn)
	if next.Ms != 40 {
		t.Fatalf("в зонде ms=%d, ожидалось 40", next.Ms)
	}
}

func TestPingPublishesExactMs(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	probe := func(rtt time.Duration) {
		now = now.Add(pingProbeEvery)
		h.probePing(p, now)
		last, _ := lastPing(t, conn)
		now = now.Add(rtt)
		body, _ := json.Marshal(protocol.Ping{Seq: last.Seq})
		h.handlePong(p, body)
	}

	// Публикуется ровно то, что измерено, с точностью до миллисекунды: раньше здесь были шаг
	// 10 мс и зона нечувствительности 12 мс, и число врало — переход на одну ступень не
	// публиковался никогда, потому что зона была шире шага.
	probe(43 * time.Millisecond)
	if p.PingMs != 43 {
		t.Fatalf("ping=%d, ожидалось 43", p.PingMs)
	}
	// Небольшой сдвиг доходит, а не гасится: сглаживание пополам, 43 и 51 дают 47.
	probe(51 * time.Millisecond)
	if p.PingMs != 47 {
		t.Fatalf("ping=%d, ожидалось 47", p.PingMs)
	}
	// Loopback: сырой RTT почти ноль, но ноль означает «неизвестно», поэтому показываем 1 мс.
	p.RTT = 0
	probe(0)
	if p.PingMs != 1 {
		t.Fatalf("ping=%d, ожидалось 1", p.PingMs)
	}
}

func TestPingLostAndReset(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	h.probePing(p, now)
	last, _ := lastPing(t, conn)
	now = now.Add(50 * time.Millisecond)
	body, _ := json.Marshal(protocol.Ping{Seq: last.Seq})
	h.handlePong(p, body)
	if p.PingMs == 0 {
		t.Fatal("задержка не измерена")
	}

	// Зонд без ответа: задержка снова неизвестна, и зонды продолжают уходить.
	now = now.Add(pingProbeEvery)
	h.probePing(p, now)
	now = now.Add(pingLost + time.Second)
	h.probePing(p, now)
	if p.PingMs != 0 || !p.PingSentAt.IsZero() {
		t.Fatalf("потерянный зонд не сбросил состояние: ping=%d sent=%v", p.PingMs, p.PingSentAt)
	}
	h.probePing(p, now)
	if _, n := lastPing(t, conn); n < 3 {
		t.Fatalf("зондов %d, после потери должен уйти новый", n)
	}

	// Реконнект и обрыв забывают задержку целиком.
	p.PingMs, p.RTT = 70, 70*time.Millisecond
	resetPing(p)
	if p.PingMs != 0 || p.RTT != 0 || p.PingSeq != 0 {
		t.Fatalf("resetPing не очистил состояние: %+v", p)
	}
}
