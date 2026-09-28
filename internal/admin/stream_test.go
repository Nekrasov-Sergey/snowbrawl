package admin

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

const testToken = "t0ken"

func newAdmin(t *testing.T) (*httptest.Server, *hub.Hub, func()) {
	srv, h, _, stop := newAdminFull(t)
	return srv, h, stop
}

// gin.SetMode — глобальная настройка; при параллельных тестах её нельзя дёргать из каждого
// хелпера, поэтому она здесь, один раз на пакет.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// testCookie — кука, по которой тестовый Identify узнаёт запись игрока. Настоящую подписанную
// куку проверяет internal/auth; админке важно лишь то, что игрок опознан.
const testCookie = "acc"

func newAdminFull(t *testing.T) (*httptest.Server, *hub.Hub, *accounts.Store, func()) {
	t.Helper()
	src, err := snowbrawl.Web.ReadFile(snowbrawl.SimPath)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := sim.Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	log := zerolog.New(io.Discard)
	db, err := store.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	accs := accounts.Open(db, log)
	cfg := config.Defaults()
	// Ноль у этих полей означает боевое значение; тесты их сжимают, чтобы не пережидать
	// секундный период потока и полусекундный фоновый цикл (см. config.Config).
	cfg.HubTick = 20 * time.Millisecond
	cfg.AdminStreamEvery = 20 * time.Millisecond
	series, err := onlinestat.Open(nil, log)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(cfg, prog, log, series)
	h.SetAccounts(accs)
	h.Run()
	r := gin.New()
	stop := Register(r, Deps{
		Hub: h, Accounts: accs,
		Info:  Info{Build: "test", SimVersion: prog.Version(), Proto: 3},
		Token: testToken, Started: time.Now(), StreamEvery: cfg.AdminStreamEvery,
		Identify: func(r *http.Request) string {
			if c, err := r.Cookie(testCookie); err == nil {
				return c.Value
			}
			return ""
		},
	})
	srv := httptest.NewServer(r)
	t.Cleanup(func() { stop(); h.Shutdown(); srv.Close(); _ = db.Close() })
	return srv, h, accs, stop
}

// Адрес запроса доступа не даёт вовсе: ролей по IP больше нет, а X-Forwarded-For подделывается.
func TestNoEntryByAddress(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newAdminFull(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/state", nil)
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена и роли ожидался 401, получен %d", res.StatusCode)
	}
}

func TestStreamNeedsToken(t *testing.T) {
	t.Parallel()
	srv, _, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("статус %d, ожидался 401", res.StatusCode)
	}
}

// readFrame читает один кадр SSE (строку data:) с таймаутом.
func readFrame(t *testing.T, br *bufio.Reader, wait time.Duration) string {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- res{err: err}
				return
			}
			if strings.HasPrefix(line, "data: ") {
				ch <- res{line: strings.TrimSpace(strings.TrimPrefix(line, "data: "))}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return ""
		}
		return r.line
	case <-time.After(wait):
		return ""
	}
}

func TestStreamSendsFirstStateImmediately(t *testing.T) {
	t.Parallel()
	srv, _, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/stream?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type %q", ct)
	}
	br := bufio.NewReader(res.Body)
	first := readFrame(t, br, 3*time.Second)
	// build в сводке — из конфига hub, а не из admin.Info: проверяем поле протокола. Номер
	// берём из константы: бамп версии не должен ронять тест про диффинг потока.
	if !strings.Contains(first, fmt.Sprintf(`"proto":%d`, protocol.Version)) {
		t.Fatalf("первый кадр: %q", first)
	}
	// Состояние не менялось — второго кадра быть не должно. Окно короткое, но с запасом
	// к периоду потока (20 мс): будь диффинг сломан, кадр пришёл бы сразу.
	if next := readFrame(t, br, 300*time.Millisecond); next != "" {
		t.Fatalf("лишний кадр при неизменном состоянии: %q", next)
	}
}

func TestStreamSendsOnChange(t *testing.T) {
	t.Parallel()
	srv, h, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/stream?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if first := readFrame(t, br, 3*time.Second); first == "" {
		t.Fatal("нет первого кадра")
	}
	h.SetDrain(true) // любое изменение состояния
	frame := readFrame(t, br, 3*time.Second)
	if !strings.Contains(frame, `"draining":true`) {
		t.Fatalf("изменение не приехало: %q", frame)
	}
}

func TestStreamStopsOnShutdown(t *testing.T) {
	t.Parallel()
	srv, _, stop := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/stream?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if first := readFrame(t, br, 3*time.Second); first == "" {
		t.Fatal("нет первого кадра")
	}
	stop()
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(res.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("поток не закрылся после остановки")
	}
}
