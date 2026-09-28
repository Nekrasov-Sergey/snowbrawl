package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

func newAdminWithAccounts(t *testing.T) (*httptest.Server, *accounts.Store) {
	t.Helper()
	srv, _, accs, _ := newAdminFull(t)
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

	// Несуществующий аккаунт — 404, а не молчаливый успех.
	resp = do(t, http.MethodPost, url+"/account/rank"+tok, `{"id":"нет","rank":"admin"}`, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("несуществующий аккаунт: %d", resp.StatusCode)
	}
}

// Список игроков через ручку: записи из базы, поиск, фильтр, пустой ответ — пустой список.
// Слияние с живыми сессиями проверяет TestPlayersPage.
func TestPlayersListing(t *testing.T) {
	t.Parallel()
	srv, accs := newAdminWithAccounts(t)
	now := time.Now()
	acc, _, err := accs.Ensure(accounts.ProviderYandex, "y-1", "vasya", "Снежок", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := accs.SetRank(acc.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	if _, err := accs.EnsureGuest(accounts.NewID(), "Пурга", true, nil, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	list := func(query string) (out struct {
		Rows    []playerRow `json:"rows"`
		Total   int         `json:"total"`
		PerPage int         `json:"perPage"`
		Broken  bool        `json:"broken"`
	}) {
		t.Helper()
		resp := do(t, http.MethodGet, srv.URL+"/admin/players?token="+testToken+query, "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("список игроков: %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Модератор выше, хотя гость заходил позже.
	all := list("")
	if all.Total != 2 || len(all.Rows) != 2 || all.Broken || all.PerPage != accounts.SearchPage ||
		all.Rows[0].Nick != "Снежок" || all.Rows[0].Kind != kindYandex || all.Rows[0].Login != "vasya" {
		t.Fatalf("все: %+v", all)
	}
	if g := list("&filter=guests"); g.Total != 1 || g.Rows[0].Nick != "Пурга" || g.Rows[0].Kind != kindGuest {
		t.Fatalf("гости: %+v", g)
	}
	if q := list("&q=%D1%81%D0%BD%D0%B5%D0%B6"); q.Total != 1 || q.Rows[0].Nick != "Снежок" {
		t.Fatalf("поиск: %+v", q)
	}
	if empty := list("&q=nobody"); empty.Rows == nil || len(empty.Rows) != 0 {
		t.Fatalf("пустой результат должен быть пустым списком, а не null: %+v", empty)
	}
	// «Выйти везде» из админки убрано.
	if resp := do(t, http.MethodPost, srv.URL+"/admin/account/logout-all?token="+testToken+"&id="+acc.ID, "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("ручка logout-all всё ещё есть: %d", resp.StatusCode)
	}
}
