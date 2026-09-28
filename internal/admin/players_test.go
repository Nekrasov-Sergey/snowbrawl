package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// fakeDB — записи игроков для слияния без базы: Search ведёт себя как настоящий (фильтр,
// исключения, окно), но порядок задаёт сам тест.
type fakeDB struct{ recs []accounts.Account }

func (f fakeDB) get(id string) (accounts.Account, bool) {
	for _, a := range f.recs {
		if a.ID == id {
			return a, true
		}
	}
	return accounts.Account{}, false
}

func (f fakeDB) search(q, filter string, exclude []string, offset, limit int) ([]accounts.Account, int, error) {
	skip := map[string]bool{}
	for _, id := range exclude {
		skip[id] = true
	}
	var all []accounts.Account
	for _, a := range f.recs {
		if !skip[a.ID] && accounts.Matches(a, q, filter) {
			all = append(all, a)
		}
	}
	if offset > len(all) {
		offset = len(all)
	}
	end := min(len(all), offset+max(0, limit))
	return all[offset:end], len(all), nil
}

func nicks(rows []playerRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Nick)
	}
	return strings.Join(out, ",")
}

func TestPlayersPage(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	banned := t0
	db := fakeDB{recs: []accounts.Account{
		{ID: "a1", Provider: accounts.ProviderYandex, Nick: "Онлайн-модер", Rank: protocol.RankModerator, Login: "moder", SeenAt: t0},
		{ID: "a2", Nick: "Онлайн-гость", SeenAt: t0},
		{ID: "a3", Provider: accounts.ProviderYandex, Nick: "Офлайн-админ", Rank: protocol.RankAdmin, SeenAt: t0},
		{ID: "a4", Nick: "Офлайн-гость", SeenAt: t0, BannedAt: &banned},
	}}
	sessions := []hub.PlayerStat{
		{ID: "p1", Nick: "Онлайн-гость", Account: "a2", Guest: true, Online: true, Since: t0.Add(2 * time.Minute)},
		{ID: "p2", Nick: "Без кук", Online: true, Since: t0.Add(3 * time.Minute)},
		{ID: "p3", Nick: "Онлайн-модер", Account: "a1", Rank: protocol.RankModerator, Online: true, Since: t0.Add(time.Minute)},
		// Запись уже слита с аккаунтом, а сессия ещё доживает: показываем как гостя без записи.
		{ID: "p4", Nick: "Слитый", Account: "a-gone", Online: false, Since: t0},
	}
	page := func(q, filter string, n, per int) ([]playerRow, int) {
		t.Helper()
		rows, total, err := playersPage(sessions, db.get, db.search, q, filter, n, per)
		if err != nil {
			t.Fatal(err)
		}
		return rows, total
	}

	// Сначала в игре: роль, потом кто зашёл позже; затем база без уже показанных.
	rows, total := page("", "", 0, 50)
	if want := "Онлайн-модер,Без кук,Онлайн-гость,Слитый,Офлайн-админ,Офлайн-гость"; nicks(rows) != want || total != 6 {
		t.Fatalf("порядок: %s (%d)", nicks(rows), total)
	}
	if r := rows[0]; r.ID != "a1" || r.Session != "p3" || r.Kind != kindYandex || r.Login != "moder" {
		t.Errorf("онлайн-строка с записью: %+v", r)
	}
	if r := rows[1]; r.ID != "" || r.Kind != kindNoCookie {
		t.Errorf("гость без кук: %+v", r)
	}
	if r := rows[5]; r.Session != "" || !r.Banned || r.Kind != kindGuest {
		t.Errorf("офлайн-строка: %+v", r)
	}

	// Страницы сквозные: онлайн занимает начало, база продолжает с нужного места.
	p0, _ := page("", "", 0, 3)
	p1, total := page("", "", 1, 3)
	if nicks(p0) != "Онлайн-модер,Без кук,Онлайн-гость" || nicks(p1) != "Слитый,Офлайн-админ,Офлайн-гость" || total != 6 {
		t.Fatalf("страницы: %s | %s (%d)", nicks(p0), nicks(p1), total)
	}
	if p2, _ := page("", "", 2, 3); len(p2) != 0 {
		t.Fatalf("за концом списка: %s", nicks(p2))
	}

	// Фильтры: гость без кук — только в «все» и «гости».
	for filter, want := range map[string]string{
		accounts.FilterGuests: "Без кук,Онлайн-гость,Слитый,Офлайн-гость",
		accounts.FilterYandex: "Онлайн-модер,Офлайн-админ",
		accounts.FilterRanked: "Онлайн-модер,Офлайн-админ",
		accounts.FilterBanned: "Офлайн-гость",
	} {
		if rows, _ := page("", filter, 0, 50); nicks(rows) != want {
			t.Errorf("фильтр %q: %s, ждали %s", filter, nicks(rows), want)
		}
	}
	if rows, _ := page("без", "", 0, 50); nicks(rows) != "Без кук" {
		t.Errorf("поиск по нику гостя без кук: %s", nicks(rows))
	}
	if rows, _ := page("moder", "", 0, 50); nicks(rows) != "Онлайн-модер" {
		t.Errorf("поиск по логину: %s", nicks(rows))
	}
}
