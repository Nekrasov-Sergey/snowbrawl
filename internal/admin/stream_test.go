package admin

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

const testToken = "t0ken"

func newAdmin(t *testing.T) (*httptest.Server, *hub.Hub, func()) {
	srv, h, _, stop := newAdminWithStore(t)
	return srv, h, stop
}

func newAdminWithStore(t *testing.T) (*httptest.Server, *hub.Hub, *moderation.Store, func()) {
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
	mod, err := moderation.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	series, err := onlinestat.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(cfg, prog, log, mod, series)
	h.Run()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// trustProxy=false: адрес берётся только из RemoteAddr, заголовкам не верим.
	stop := Register(r, h, mod, Info{Build: "test", SimVersion: prog.Version(), Proto: 3}, testToken, time.Now(), false)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { stop(); h.Shutdown(); srv.Close() })
	return srv, h, mod, stop
}

// Вход в админку по роли «Создатель»: токен ему не выдаётся, пускаем по адресу запроса.
func TestCreatorEntersWithoutToken(t *testing.T) {
	srv, _, mod, _ := newAdminWithStore(t)

	res, err := http.Get(srv.URL + "/admin/state")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена и без роли ожидался 401, получен %d", res.StatusCode)
	}

	// httptest слушает loopback, поэтому адрес запроса — 127.0.0.1.
	if err := mod.SetRank("127.0.0.1", protocol.RankCreator, "Хозяин", time.Now()); err != nil {
		t.Fatal(err)
	}
	res2, err := http.Get(srv.URL + "/admin/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("создателя должно пускать без токена, получен %d", res2.StatusCode)
	}

	// «Админ» — не «Создатель»: доступа к странице у него нет.
	if err := mod.SetRank("127.0.0.1", protocol.RankAdmin, "Аня", time.Now()); err != nil {
		t.Fatal(err)
	}
	res3, err := http.Get(srv.URL + "/admin/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("админу вход в панель не положен, получен %d", res3.StatusCode)
	}

	// Подделка адреса заголовком не работает: trustProxy выключен.
	if err := mod.SetRank("10.1.2.3", protocol.RankCreator, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/state", nil)
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	res4, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res4.Body.Close()
	if res4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("X-Forwarded-For не должен давать доступ, получен %d", res4.StatusCode)
	}
}

func TestStreamNeedsToken(t *testing.T) {
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
	// build в сводке — из конфига hub, а не из admin.Info: проверяем поле протокола.
	if !strings.Contains(first, `"proto":3`) {
		t.Fatalf("первый кадр: %q", first)
	}
	// Состояние не менялось — второго кадра быть не должно.
	if next := readFrame(t, br, 2500*time.Millisecond); next != "" {
		t.Fatalf("лишний кадр при неизменном состоянии: %q", next)
	}
}

func TestStreamSendsOnChange(t *testing.T) {
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
