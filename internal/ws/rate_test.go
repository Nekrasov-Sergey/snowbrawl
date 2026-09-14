package ws_test

// Лимит частоты сообщений. Главное правило: болтливый, но честный клиент теряет лишние
// сообщения и остаётся на связи, а кратное устойчивое превышение — закрытие 1008.
// До этой правки любое превышение закрывало соединение, и активный бой (движение + прицел
// + залпы) отстреливался сам по себе.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// input собирает конверт игрового ввода нужного вида.
func input(kind string) []byte {
	return protocol.MustEncode(protocol.CInput, protocol.Input{Kind: kind, X: 100, Y: 100})
}

// flood шлёт n сообщений с интервалом gap, возвращает ошибку записи (если соединение закрыли).
// Вид ввода важен: сверх лимита сервер отбрасывает только move и aim (protocol.Droppable).
func flood(ctx context.Context, c *websocket.Conn, msg []byte, n int, gap time.Duration) error {
	for i := 0; i < n; i++ {
		if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
			return err
		}
		time.Sleep(gap)
	}
	return nil
}

func TestRateLimitDropsButKeepsConnection(t *testing.T) {
	t.Parallel()
	const rate = 20
	httpSrv, srv, rec := newWSServer(t, ws.Options{MsgRate: rate})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	// Поток 2.5×rate две секунды: первый бакет (rate 20/с, всплеск 40) за это время пропустит
	// 80 сообщений из 100, остальные обязаны отброситься. Второй бакет считает весь поток, но
	// его скорость 3×rate = 60/с выше 50/с — запас не тратится, соединение остаётся живым.
	const sent = 5 * rate
	if err := flood(ctx, c, input("move"), sent, time.Second/(5*rate/2)); err != nil {
		t.Fatalf("соединение закрыто при потоке 2.5×лимита: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	msgs, closed := rec.counts()
	if closed > 0 || srv.Count() != 1 {
		t.Fatalf("соединение закрыто: закрытий %d, открыто %d", closed, srv.Count())
	}
	if msgs >= sent {
		t.Fatalf("обработано %d из %d — лишнее должно было отброситься", msgs, sent)
	}
	if msgs == 0 {
		t.Fatal("не обработано ни одного сообщения — лимит съел даже разрешённые")
	}
	// Связь живая: сообщение после паузы доходит.
	before := msgs
	time.Sleep(300 * time.Millisecond)
	if err := c.Write(ctx, websocket.MessageText, input("move")); err != nil {
		t.Fatalf("живое соединение не принимает сообщения: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if m, _ := rec.counts(); m > before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("сообщение после паузы не дошло")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRateLimitClosesGrossAbuse(t *testing.T) {
	t.Parallel()
	const rate = 20
	httpSrv, srv, rec := newWSServer(t, ws.Options{MsgRate: rate})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.CloseNow() }()
	// Читаем в фоне: иначе закрытие со стороны сервера повиснет в ожидании ответного
	// close-кадра, и тест проверял бы не лимит, а таймаут рукопожатия.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	// Флуд без паузы: второй бакет — 3×rate при всплеске 10×rate, то есть 200 сообщений запаса
	// при этом rate. Шлём заведомо больше и пишем до первой ошибки: после закрытия запись
	// перестанет проходить.
	writeErr := flood(ctx, c, input("move"), 30*rate, 0)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, closed := rec.counts()
		if closed > 0 && srv.Count() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("грубый флуд не закрыт: закрытий %d, открыто %d, ошибка записи %v", closed, srv.Count(), writeErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Код и причина закрытия важны клиенту: по ним в консоли видно, что это лимит, а не сеть.
func TestRateLimitCloseCode(t *testing.T) {
	t.Parallel()
	const rate = 20
	httpSrv, _, _ := newWSServer(t, ws.Options{MsgRate: rate})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.CloseNow() }()

	done := make(chan error, 1)
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		for i := 0; i < 40*rate; i++ {
			if err := c.Write(ctx, websocket.MessageText, input("move")); err != nil {
				return
			}
		}
	}()
	select {
	case err := <-done:
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("код закрытия %v, ожидался 1008: %v", websocket.CloseStatus(err), err)
		}
		if !strings.Contains(err.Error(), "rate limit") {
			t.Fatalf("в причине закрытия нет «rate limit»: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("флуд не закрыт за 5 с")
	}
}

// TestRateLimitNeverDropsShots — сверх лимита отбрасываются только move и aim. Бросок обязан
// дойти даже при исчерпанном бакете: потерянный throw оставляет бойца в вечном замахе
// (chargeStart уже выставил charging, а нового замаха симуляция не примет).
func TestRateLimitNeverDropsShots(t *testing.T) {
	t.Parallel()
	const rate = 20
	httpSrv, _, rec := newWSServer(t, ws.Options{MsgRate: rate})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	// Выбираем бакет движением (всплеск 2×rate), затем сразу бросаем.
	if err := flood(ctx, c, input("move"), 3*rate, 0); err != nil {
		t.Fatalf("соединение закрыто на движении: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, input("throw")); err != nil {
		t.Fatalf("бросок не отправился: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for rec.kindCount("throw") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("бросок не дошёл до обработчика: move=%d throw=%d", rec.kindCount("move"), rec.kindCount("throw"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Движение при этом обязано было отброситься — иначе тест не про лимит.
	if got := rec.kindCount("move"); got >= 3*rate {
		t.Fatalf("обработано %d движений из %d — лимит не сработал", got, 3*rate)
	}
}
