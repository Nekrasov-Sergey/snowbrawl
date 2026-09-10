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

// lastPing возвращает номер последнего отправленного зонда и сколько их всего было.
func lastPing(t *testing.T, c *sentConn) (seq uint32, count int) {
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
		seq = ping.Seq
		count++
	}
	return seq, count
}

func pingHub(now *time.Time) *Hub {
	return &Hub{
		log:  zerolog.New(io.Discard),
		byID: map[string]*session.Player{},
		now:  func() time.Time { return *now },
	}
}

func TestPingProbeAndMeasure(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	// Первый зонд не раньше pingProbeEvery от начала сессии: иначе задержка появлялась бы в
	// первые же миллисекунды и ломала стабильность сводки админки.
	now = base.Add(pingProbeEvery - time.Millisecond)
	h.probePing(p, now)
	if _, n := lastPing(t, conn); n != 0 {
		t.Fatalf("зондов %d, ожидалось 0 до истечения %v", n, pingProbeEvery)
	}

	now = base.Add(pingProbeEvery)
	h.probePing(p, now)
	seq, n := lastPing(t, conn)
	if n != 1 || seq != 1 {
		t.Fatalf("зондов %d, seq %d; ожидались 1 и 1", n, seq)
	}
	// Пока зонд в полёте, второй не уходит — иначе номер перестал бы что-то значить.
	now = now.Add(pingProbeEvery)
	h.probePing(p, now)
	if _, n := lastPing(t, conn); n != 1 {
		t.Fatalf("зондов %d, ожидался 1 пока предыдущий в полёте", n)
	}

	// Чужой номер игнорируется.
	now = base.Add(pingProbeEvery + 40*time.Millisecond)
	h.handlePong(p, json.RawMessage(`{"seq":99}`))
	if p.PingMs != 0 {
		t.Fatalf("ответ с чужим номером принят: ping=%d", p.PingMs)
	}
	h.handlePong(p, json.RawMessage(`{"seq":1}`))
	if p.PingMs != 40 {
		t.Fatalf("ping=%d, ожидалось 40", p.PingMs)
	}
}

func TestPingDeadbandAndQuantization(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	probe := func(rtt time.Duration) {
		now = now.Add(pingProbeEvery)
		h.probePing(p, now)
		seq, _ := lastPing(t, conn)
		now = now.Add(rtt)
		body, _ := json.Marshal(protocol.Ping{Seq: seq})
		h.handlePong(p, body)
	}

	probe(80 * time.Millisecond)
	if p.PingMs != 80 {
		t.Fatalf("ping=%d, ожидалось 80", p.PingMs)
	}
	// Дрожание в пределах зоны нечувствительности не публикуется: иначе админка получала бы
	// новый SSE-кадр каждую секунду и мигала целыми таблицами.
	before := p.PingMs
	probe(84 * time.Millisecond)
	if p.PingMs != before {
		t.Fatalf("дрожание опубликовано: было %d, стало %d", before, p.PingMs)
	}
	// Заметный сдвиг публикуется и округляется до шага.
	probe(300 * time.Millisecond)
	if p.PingMs%pingStep != 0 || p.PingMs < 150 {
		t.Fatalf("ping=%d, ожидалось кратное %d и заметно больше прежнего", p.PingMs, pingStep)
	}
	// Loopback: сырой RTT почти ноль, но ноль означает «неизвестно», поэтому показываем шаг.
	p.RTT = 0
	probe(0)
	if p.PingMs != pingStep {
		t.Fatalf("ping=%d, ожидалось %d", p.PingMs, pingStep)
	}
}

func TestPingLostAndReset(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	now := base
	h := pingHub(&now)
	conn := &sentConn{}
	p := &session.Player{ID: "p1", CreatedAt: base, Conn: conn}

	now = base.Add(pingProbeEvery)
	h.probePing(p, now)
	seq, _ := lastPing(t, conn)
	now = now.Add(50 * time.Millisecond)
	body, _ := json.Marshal(protocol.Ping{Seq: seq})
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
