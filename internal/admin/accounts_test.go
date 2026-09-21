package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// testCookie — кука, по которой тестовый Identify узнаёт аккаунт. Настоящую подписанную куку
// проверяет internal/auth; админке важно лишь то, что игрок опознан.
const testCookie = "acc"

func newAdminWithAccounts(t *testing.T) (*httptest.Server, *accounts.Store) {
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
	accs, err := accounts.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.HubTick = 20 * time.Millisecond
	cfg.AdminStreamEvery = 20 * time.Millisecond
	series, err := onlinestat.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(cfg, prog, log, mod, series)
	h.SetAccounts(accs)
	h.Run()
	r := gin.New()
	stop := Register(r, Deps{
		Hub: h, Moderation: mod, Accounts: accs,
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
	t.Cleanup(func() { stop(); h.Shutdown(); srv.Close() })
	return srv, accs
}

func do(t *testing.T, method, url, body, cookie string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: testCookie, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// Главное следствие аккаунтов для админки: админ заходит с любого устройства по куке, а не
// только с «того самого» адреса.
func TestAdminEntryByAccountCookie(t *testing.T) {
	t.Parallel()
	srv, accs := newAdminWithAccounts(t)
	acc, _, err := accs.Ensure(accounts.ProviderYandex, "y-1", "", "Снежок", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if resp := do(t, http.MethodGet, srv.URL+"/admin/state", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена и куки: %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/admin/state", "", acc.ID); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("обычный игрок пущен в админку: %d", resp.StatusCode)
	}
	if err := accs.SetRank(acc.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/admin/state", "", acc.ID); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("модератор пущен в админку: %d", resp.StatusCode)
	}
	if err := accs.SetRank(acc.ID, protocol.RankAdmin); err != nil {
		t.Fatal(err)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/admin/state", "", acc.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("админ не пущен по куке: %d", resp.StatusCode)
	}
}

func TestAccountRankAndBanHandlers(t *testing.T) {
	t.Parallel()
	srv, accs := newAdminWithAccounts(t)
	acc, _, err := accs.Ensure(accounts.ProviderYandex, "y-1", "", "Снежок", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/admin"
	tok := "?token=" + testToken

	resp := do(t, http.MethodPost, url+"/account/rank"+tok, `{"id":"`+acc.ID+`","rank":"moderator"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("выдача роли: %d", resp.StatusCode)
	}
	if got, _ := accs.Get(acc.ID); got.Rank != protocol.RankModerator {
		t.Fatalf("роль не выдана: %q", got.Rank)
	}

	resp = do(t, http.MethodPost, url+"/account/ban"+tok, `{"id":"`+acc.ID+`","reason":"мат"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("бан: %d", resp.StatusCode)
	}
	if got, _ := accs.Get(acc.ID); !got.Banned() {
		t.Fatal("бан не записался")
	}
	resp = do(t, http.MethodDelete, url+"/account/ban"+tok+"&id="+acc.ID, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("разбан: %d", resp.StatusCode)
	}

	// Админа забанить нельзя — то же правило, что у ролей по адресу.
	if err := accs.SetRank(acc.ID, protocol.RankAdmin); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodPost, url+"/account/ban"+tok, `{"id":"`+acc.ID+`","reason":""}`, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("бан админа: %d", resp.StatusCode)
	}

	// «Выйти везде» двигает epoch, чем обесценивает все выданные куки.
	resp = do(t, http.MethodPost, url+"/account/logout-all"+tok+"&id="+acc.ID, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("выход на всех устройствах: %d", resp.StatusCode)
	}
	if got, _ := accs.Get(acc.ID); got.Epoch != 1 {
		t.Fatalf("epoch: %d", got.Epoch)
	}

	// Несуществующий аккаунт — 404, а не молчаливый успех.
	resp = do(t, http.MethodPost, url+"/account/rank"+tok, `{"id":"нет","rank":"admin"}`, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("несуществующий аккаунт: %d", resp.StatusCode)
	}
}

func TestAccountsListing(t *testing.T) {
	t.Parallel()
	srv, accs := newAdminWithAccounts(t)
	if _, _, err := accs.Ensure(accounts.ProviderYandex, "y-1", "vasya", "Снежок", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodGet, srv.URL+"/admin/accounts?token="+testToken, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список аккаунтов: %d", resp.StatusCode)
	}
	var body struct {
		Accounts []accounts.Account `json:"accounts"`
		Broken   bool               `json:"broken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Accounts) != 1 || body.Accounts[0].Nick != "Снежок" || body.Broken {
		t.Fatalf("в списке: %+v", body)
	}
}
