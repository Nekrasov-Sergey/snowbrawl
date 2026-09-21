package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
)

// fakeYandex — провайдер, который ведёт себя как Яндекс ID: меняет код на токен и отдаёт профиль.
// Ради него адреса провайдера и вынесены в Config: в тестах никакой сети.
type fakeYandex struct {
	srv       *httptest.Server
	code      string // какой код считает верным
	token     string
	profile   map[string]any
	tokenCode int // статус ответа /token, ноль — 200
	infoCode  int
}

func newFakeYandex(t *testing.T) *fakeYandex {
	t.Helper()
	f := &fakeYandex{code: "good-code", token: "tok", profile: map[string]any{
		"id": "y-42", "login": "vasya", "display_name": "Вася",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenCode != 0 {
			w.WriteHeader(f.tokenCode)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("code") != f.code {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": f.token})
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		if f.infoCode != 0 {
			w.WriteHeader(f.infoCode)
		}
		if r.Header.Get("Authorization") != "OAuth "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(f.profile)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type harness struct {
	svc  *Service
	accs *accounts.Store
	ya   *fakeYandex
	srv  *httptest.Server
	now  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ya := newFakeYandex(t)
	accs, err := accounts.Open("", zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{ya: ya, accs: accs, now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	svc, err := New(Config{
		ClientID: "cid", ClientSecret: "sec", PublicURL: "https://snowbrawl.test",
		Secret: "test-secret", AuthURL: ya.srv.URL + "/authorize",
		TokenURL: ya.srv.URL + "/token", InfoURL: ya.srv.URL + "/info",
	}, accs, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return h.now }
	h.svc = svc

	r := gin.New()
	r.Use(svc.Refresh())
	svc.Register(r)
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

// client без следования редиректам: нам важны сами Location и Set-Cookie.
func (h *harness) client() *http.Client {
	jar := &cookieJar{m: map[string]string{}}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// cookieJar — простейшая банка: домен один, срок не проверяем.
type cookieJar struct{ m map[string]string }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	for _, c := range cs {
		if c.MaxAge < 0 {
			delete(j.m, c.Name)
			continue
		}
		j.m[c.Name] = c.Value
	}
}

func (j *cookieJar) Cookies(*url.URL) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(j.m))
	for k, v := range j.m {
		out = append(out, &http.Cookie{Name: k, Value: v})
	}
	return out
}

// login проходит весь путь: /auth/yandex → «Яндекс» → /auth/yandex/callback.
func (h *harness) login(t *testing.T, c *http.Client, code string) *http.Response {
	t.Helper()
	resp, err := c.Get(h.srv.URL + "/auth/yandex")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("старт входа: %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("в редиректе нет state")
	}
	if got := loc.Query().Get("redirect_uri"); got != "https://snowbrawl.test/auth/yandex/callback" {
		t.Fatalf("redirect_uri построен не из публичного адреса: %s", got)
	}
	back, err := c.Get(h.srv.URL + "/auth/yandex/callback?code=" + code + "&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	_ = back.Body.Close()
	return back
}

func TestLoginCreatesAccountAndSetsCookies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	resp := h.login(t, c, h.ya.code)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("после входа: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	acc, ok := h.accs.BySubject(accounts.ProviderYandex, "y-42")
	if !ok {
		t.Fatal("аккаунт не создан")
	}
	if acc.Nick != "Вася" {
		t.Errorf("ник из профиля: %q", acc.Nick)
	}

	var session, hint *http.Cookie
	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case CookieSession:
			session = ck
		case CookieHint:
			hint = ck
		}
	}
	if session == nil || hint == nil {
		t.Fatal("куки входа не выставлены")
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Errorf("кука входа без защиты: %+v", session)
	}
	if hint.HttpOnly || hint.Value != acc.ID {
		t.Errorf("подсказка должна быть читаемой клиентом и содержать id: %+v", hint)
	}
	// Identify узнаёт игрока по той же куке — так его узнает и апгрейд /ws.
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/ws", nil)
	req.AddCookie(session)
	if got := h.svc.Identify(req); got != acc.ID {
		t.Errorf("Identify вернул %q вместо %q", got, acc.ID)
	}
}

// Повторный вход тем же Яндекс-аккаунтом не должен плодить аккаунты — в этом весь смысл затеи.
func TestLoginTwiceKeepsAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.login(t, h.client(), h.ya.code)
	first, _ := h.accs.BySubject(accounts.ProviderYandex, "y-42")
	if err := h.accs.Rename(first.ID, "Снежок", nil, h.now.Add(accounts.RenameCooldown)); err != nil {
		t.Fatal(err)
	}
	// Второе устройство: своя банка кук, тот же профиль.
	h.login(t, h.client(), h.ya.code)
	if h.accs.Len() != 1 {
		t.Fatalf("аккаунтов стало %d", h.accs.Len())
	}
	again, _ := h.accs.BySubject(accounts.ProviderYandex, "y-42")
	if again.ID != first.ID || again.Nick != "Снежок" {
		t.Fatalf("второй вход поменял аккаунт: %+v", again)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	// Без начатого входа куки state нет вовсе.
	resp, err := c.Get(h.srv.URL + "/auth/yandex/callback?code=good-code&state=подделка")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("колбэк без state: %d", resp.StatusCode)
	}
	// Вход начат, но state в query чужой.
	start, err := c.Get(h.srv.URL + "/auth/yandex")
	if err != nil {
		t.Fatal(err)
	}
	_ = start.Body.Close()
	resp2, err := c.Get(h.srv.URL + "/auth/yandex/callback?code=good-code&state=чужой")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("колбэк с чужим state: %d", resp2.StatusCode)
	}
	if h.accs.Len() != 0 {
		t.Error("аккаунт создан по неподтверждённому входу")
	}
}

// Просроченная кука state — тот же отказ: игрок ушёл на Яндекс и вернулся через час.
func TestCallbackRejectsExpiredState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	start, err := c.Get(h.srv.URL + "/auth/yandex")
	if err != nil {
		t.Fatal(err)
	}
	_ = start.Body.Close()
	loc, _ := url.Parse(start.Header.Get("Location"))
	h.now = h.now.Add(stateTTL + time.Minute)
	resp, err := c.Get(h.srv.URL + "/auth/yandex/callback?code=good-code&state=" + loc.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("просроченный state: %d", resp.StatusCode)
	}
}

// Игрок нажал «Отказать»: это не ошибка, его молча возвращают в игру.
func TestCallbackUserDenied(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	start, err := c.Get(h.srv.URL + "/auth/yandex")
	if err != nil {
		t.Fatal(err)
	}
	_ = start.Body.Close()
	loc, _ := url.Parse(start.Header.Get("Location"))
	resp, err := c.Get(h.srv.URL + "/auth/yandex/callback?error=access_denied&state=" + loc.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("отказ игрока: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if h.accs.Len() != 0 {
		t.Error("при отказе аккаунт создаваться не должен")
	}
}

func TestCallbackProviderFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(*harness)
		code  string
		want  int
	}{
		{"неверный код", func(*harness) {}, "плохой-код", http.StatusBadGateway},
		{"профиль недоступен", func(h *harness) { h.ya.infoCode = http.StatusInternalServerError }, "good-code", http.StatusBadGateway},
		{"профиль без id", func(h *harness) { h.ya.profile = map[string]any{"login": "vasya"} }, "good-code", http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c.setup(h)
			resp := h.login(t, h.client(), c.code)
			if resp.StatusCode != c.want {
				t.Fatalf("статус %d, ждали %d", resp.StatusCode, c.want)
			}
			if h.accs.Len() != 0 {
				t.Error("аккаунт создан при неудачном входе")
			}
		})
	}
}

func TestLogoutClearsCookies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	h.login(t, c, h.ya.code)
	resp, err := c.Post(h.srv.URL+"/auth/logout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("выход: %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if (ck.Name == CookieSession || ck.Name == CookieHint) && ck.MaxAge >= 0 {
			t.Errorf("кука %s не погашена: %+v", ck.Name, ck)
		}
	}
}

// Подделать куку без ключа нельзя, и отзыв всех устройств обесценивает выданные.
func TestIdentifyRejectsForgedAndRevoked(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.login(t, h.client(), h.ya.code)
	acc, _ := h.accs.BySubject(accounts.ProviderYandex, "y-42")
	good := sessionValue(h.svc.secret, acc.ID, acc.Epoch, h.now.Add(SessionTTL))

	check := func(value string) string {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/ws", nil)
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: value})
		return h.svc.Identify(req)
	}
	if check(good) != acc.ID {
		t.Fatal("правильная кука не принята")
	}
	if check(sessionValue([]byte("чужой ключ"), acc.ID, acc.Epoch, h.now.Add(SessionTTL))) != "" {
		t.Error("принята кука, подписанная чужим ключом")
	}
	if check(strings.Replace(good, acc.ID, "a00000000000", 1)) != "" {
		t.Error("принята кука с подменённым аккаунтом")
	}
	if check(sessionValue(h.svc.secret, acc.ID, acc.Epoch, h.now.Add(-time.Hour))) != "" {
		t.Error("принята просроченная кука")
	}
	if check("мусор") != "" {
		t.Error("принят мусор вместо куки")
	}
	if err := h.accs.BumpEpoch(acc.ID); err != nil {
		t.Fatal(err)
	}
	if check(good) != "" {
		t.Error("после отзыва всех устройств старая кука должна перестать работать")
	}
}

// Кука продлевается сама, когда до конца остаётся меньше месяца, — иначе постоянный игрок
// однажды всё равно окажется разлогинен.
func TestRefreshExtendsCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	h.login(t, c, h.ya.code)

	h.now = h.now.Add(SessionTTL - refreshBefore + time.Hour)
	resp, err := c.Get(h.srv.URL + "/auth/logout-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var got *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == CookieSession {
			got = ck
		}
	}
	if got == nil {
		t.Fatal("кука не продлена")
	}
	_, _, err = parseSession(h.svc.secret, got.Value, h.now.Add(SessionTTL-time.Hour))
	if err != nil {
		t.Errorf("продлённая кука живёт не полный срок: %v", err)
	}
}

func TestStartLimitsPerIP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.client()
	var last int
	for i := 0; i < 12; i++ {
		resp, err := c.Get(h.srv.URL + "/auth/yandex")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("лимит не сработал, последний ответ %d", last)
	}
}

// Возврат после входа — только внутрь игры: иначе ручка становится открытым редиректом.
func TestReturnPath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                       "/",
		"/#/rooms/pvp":           "/#/rooms/pvp",
		"//evil.example":         "/",
		"https://evil.example":   "/",
		"javascript:alert(1)":    "/",
		"/settings?x=1":          "/settings?x=1",
		"\\\\evil.example/share": "/",
	}
	for in, want := range cases {
		if got := returnPath(in); got != want {
			t.Errorf("returnPath(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestDevLoginOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	resp, err := h.client().Get(h.srv.URL + "/auth/dev/login?sub=1&nick=Тест")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("офлайновый вход не должен существовать без флага: %d", resp.StatusCode)
	}
}

func TestEnabled(t *testing.T) {
	t.Parallel()
	if Enabled(Config{ClientID: "a"}) || Enabled(Config{ClientSecret: "b"}) || Enabled(Config{}) {
		t.Error("вход считается включённым без пары id+секрет")
	}
	if !Enabled(Config{ClientID: "a", ClientSecret: "b"}) {
		t.Error("вход не включился с id и секретом")
	}
}
