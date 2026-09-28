package accounts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

func openDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return Open(openDB(t, ""), zerolog.New(io.Discard))
}

func TestEnsureCreatesAndFinds(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()

	acc, created, err := s.Ensure(ProviderYandex, "42", "vasya", "Вася", nil, now)
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	if acc.Nick != "Вася" || acc.NickAuto {
		t.Fatalf("ник из профиля не взят: %+v", acc)
	}

	again, created, err := s.Ensure(ProviderYandex, "42", "vasya", "Другой", nil, now.Add(time.Hour))
	if err != nil || created {
		t.Fatalf("повторный вход завёл новый аккаунт: created=%v err=%v", created, err)
	}
	if again.ID != acc.ID || again.Nick != "Вася" {
		t.Fatalf("повторный вход поменял аккаунт: %+v", again)
	}
	if !again.SeenAt.After(acc.SeenAt) {
		t.Error("SeenAt не обновился")
	}
	if got, ok := s.ByNickKey(protocol.NickKey("ВАСЯ")); !ok || got.ID != acc.ID {
		t.Error("аккаунт не ищется по ключу ника")
	}
}

// Ник из профиля Яндекса может быть занят, матерным или просто непригодным — вход из-за этого
// падать не должен ни в одном из случаев.
func TestEnsurePicksOwnNickWhenSuggestionUnusable(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cases := []struct {
		name    string
		suggest string
		taken   func(string) bool
	}{
		{"пустой", "", nil},
		{"короткий", "я", nil},
		{"запрещённые символы", "ва$я", nil},
		{"занят другим аккаунтом", "Вася", nil},
		{"занят живым гостем", "Петя", func(key string) bool { return key == protocol.NickKey("Петя") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			if _, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, now); err != nil {
				t.Fatal(err)
			}
			acc, _, err := s.Ensure(ProviderYandex, "2", "", c.suggest, c.taken, now)
			if err != nil {
				t.Fatalf("вход не должен падать из-за ника: %v", err)
			}
			// Конкретное имя не проверяем: генератор общий с гостями (protocol.RandomNick),
			// важно лишь то, что сервер выдал своё имя и пометил его автоматическим.
			if !acc.NickAuto || acc.Nick == "" || acc.Nick == c.suggest {
				t.Fatalf("ожидался автоматический ник, получено %+v", acc)
			}
			if _, err := protocol.NormalizeNick(acc.Nick); err != nil {
				t.Errorf("выданный ник не проходит проверку ников: %v", err)
			}
		})
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	acc, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.Ensure(ProviderYandex, "2", "", "Петя", nil, now)
	if err != nil {
		t.Fatal(err)
	}

	later := now.Add(RenameCooldown + time.Second)
	if err := s.Rename(acc.ID, "Петя", nil, later); !errors.Is(err, ErrNickTaken) {
		t.Errorf("чужой ник: ждали ErrNickTaken, получили %v", err)
	}
	if err := s.Rename(acc.ID, "Коля", func(string) bool { return true }, later); !errors.Is(err, ErrNickTaken) {
		t.Errorf("ник занят гостем: ждали ErrNickTaken, получили %v", err)
	}
	if err := s.Rename(acc.ID, "Коля", nil, later); err != nil {
		t.Fatalf("переименование: %v", err)
	}
	if got, _ := s.Get(acc.ID); got.Nick != "Коля" || got.NickAuto {
		t.Fatalf("после переименования: %+v", got)
	}
	// Прежний ник освободился и достаётся соседу, новый — занят.
	if _, ok := s.ByNickKey(protocol.NickKey("Вася")); ok {
		t.Error("прежний ник остался за аккаунтом")
	}
	if err := s.Rename(acc.ID, "Вася", nil, later.Add(time.Minute)); !errors.Is(err, ErrTooSoon) {
		t.Errorf("кулдаун: ждали ErrTooSoon, получили %v", err)
	}
	// Освободившийся ник достаётся соседу.
	if err := s.Rename(other.ID, "Вася", nil, later); err != nil {
		t.Errorf("сосед не смог занять освободившийся ник: %v", err)
	}
	if err := s.Rename("нет такого", "Коля", nil, later); !errors.Is(err, ErrNotFound) {
		t.Errorf("ждали ErrNotFound, получили %v", err)
	}
}

// Смена написания своего же ника не должна стоить кулдауна и терять бронь.
func TestRenameSameKeyKeepsHold(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	acc, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(acc.ID, "ВАСЯ", nil, now.Add(time.Second)); err != nil {
		t.Fatalf("смена написания: %v", err)
	}
	got, _ := s.Get(acc.ID)
	if got.Nick != "ВАСЯ" {
		t.Fatalf("ник не изменился: %+v", got)
	}
	if byKey, ok := s.ByNickKey(protocol.NickKey("Вася")); !ok || byKey.ID != acc.ID {
		t.Error("ник потерялся")
	}
}

func TestAddTutorialMerges(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	acc, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !s.AddTutorial(acc.ID, "basics", "sniper") {
		t.Fatal("первые уроки не записались")
	}
	if s.AddTutorial(acc.ID, "basics") {
		t.Error("повторный урок не должен считаться изменением")
	}
	if !s.AddTutorial(acc.ID, "bomber") {
		t.Error("новый урок не записался")
	}
	got, _ := s.Get(acc.ID)
	if strings.Join(got.Tutorial, ",") != "basics,bomber,sniper" {
		t.Fatalf("список уроков: %v", got.Tutorial)
	}
	if s.AddTutorial("нет такого", "basics") {
		t.Error("несуществующий аккаунт не должен ничего менять")
	}
}

func TestPersistence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "snowbrawl.db")
	db, err := store.Open(path, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	s := Open(db, zerolog.New(io.Discard))
	now := time.Now().Round(time.Millisecond)
	acc, _, err := s.Ensure(ProviderYandex, "42", "vasya", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	s.AddTutorial(acc.ID, "basics")
	if err := s.SetRank(acc.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := Open(openDB(t, path), zerolog.New(io.Discard))
	got, ok := s2.BySubject(ProviderYandex, "42")
	if !ok {
		t.Fatal("аккаунт не прочитался")
	}
	if got.ID != acc.ID || got.Nick != "Вася" || got.Rank != protocol.RankModerator || len(got.Tutorial) != 1 {
		t.Fatalf("аккаунт прочитался не целиком: %+v", got)
	}
	if !got.CreatedAt.Equal(now) || !got.NickAt.IsZero() || got.Banned() {
		t.Fatalf("время прочиталось не так: %+v", got)
	}
	if _, ok := s2.ByNickKey(protocol.NickKey("Вася")); !ok {
		t.Error("аккаунт не ищется по нику после перезапуска")
	}
}

// Гость — запись без провайдера с id из его куки. Вторая вкладка, которая пришла одновременно,
// получает ту же запись, а не ошибку.
func TestEnsureGuest(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	g, err := s.EnsureGuest("a000000000001", "Снежок", true, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Guest() || g.Nick != "Снежок" || !g.NickAuto || g.ID != "a000000000001" {
		t.Fatalf("гость: %+v", g)
	}
	again, err := s.EnsureGuest("a000000000001", "Другой", false, nil, now)
	if err != nil || again.Nick != "Снежок" {
		t.Fatalf("повтор с тем же id: %+v %v", again, err)
	}
	// Ник записи занят навсегда: ни другой гость, ни аккаунт его не получат.
	if _, err := s.EnsureGuest("a000000000002", "СНЕЖОК", false, nil, now); !errors.Is(err, ErrNickTaken) {
		t.Errorf("чужой ник гостю: %v", err)
	}
	if _, err := s.EnsureGuest("a000000000003", "Пурга", false, func(string) bool { return true }, now); !errors.Is(err, ErrNickTaken) {
		t.Errorf("ник, занятый живой бронью: %v", err)
	}
	acc, _, err := s.Ensure(ProviderYandex, "1", "", "Снежок", nil, now)
	if err != nil || acc.Nick == "Снежок" {
		t.Fatalf("аккаунт получил ник гостя: %+v %v", acc, err)
	}
}

// Первый вход через Яндекс из браузера гостя: гость становится аккаунтом со всем, что у него было.
func TestLinkTurnsGuestIntoAccount(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	g, err := s.EnsureGuest(NewID(), "Снежок", true, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	s.AddTutorial(g.ID, "basics")
	if err := s.SetRank(g.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	acc, created, err := s.Link(g.ID, ProviderYandex, "y-1", "vasya", "Вася", nil, now)
	if err != nil || !created {
		t.Fatalf("Link: %v %v", created, err)
	}
	if acc.ID != g.ID || acc.Guest() || acc.Nick != "Снежок" || acc.NickAuto || acc.Rank != protocol.RankModerator ||
		strings.Join(acc.Tutorial, ",") != "basics" || acc.Login != "vasya" {
		t.Fatalf("гость не стал аккаунтом целиком: %+v", acc)
	}
	if s.Len() != 1 {
		t.Errorf("записей: %d", s.Len())
	}
	// Второй вход с тем же Яндексом находит тот же аккаунт.
	again, created, err := s.Ensure(ProviderYandex, "y-1", "vasya", "", nil, now)
	if err != nil || created || again.ID != g.ID {
		t.Fatalf("повторный вход: %+v %v %v", again, created, err)
	}
}

// Вход в уже существующий аккаунт из браузера гостя: уроки объединяются, ник остаётся от
// аккаунта, гость исчезает и освобождает ник. Бан гостя переезжает — иначе от бана спасал бы вход.
func TestLinkMergesGuestIntoExisting(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	acc, _, err := s.Ensure(ProviderYandex, "y-1", "", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	s.AddTutorial(acc.ID, "dash")
	g, err := s.EnsureGuest(NewID(), "Снежок", false, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	s.AddTutorial(g.ID, "basics", "dash")
	if err := s.Ban(g.ID, "мат", now); err != nil {
		t.Fatal(err)
	}
	got, created, err := s.Link(g.ID, ProviderYandex, "y-1", "", "", nil, now)
	if err != nil || created {
		t.Fatalf("Link: %v %v", created, err)
	}
	if got.ID != acc.ID || got.Nick != "Вася" || strings.Join(got.Tutorial, ",") != "basics,dash" {
		t.Fatalf("слияние: %+v", got)
	}
	if !got.Banned() || got.BanReason != "мат" {
		t.Error("бан гостя не переехал в аккаунт")
	}
	if _, ok := s.Get(g.ID); ok {
		t.Error("запись гостя осталась")
	}
	if _, ok := s.ByNickKey(protocol.NickKey("Снежок")); ok {
		t.Error("ник гостя не освободился")
	}
}

// Вход из браузера, где уже вошли другим аккаунтом: чужой аккаунт не трогаем.
func TestLinkIgnoresNonGuest(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	a, _, err := s.Ensure(ProviderYandex, "y-1", "", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	b, created, err := s.Link(a.ID, ProviderYandex, "y-2", "", "Петя", nil, now)
	if err != nil || !created || b.ID == a.ID || b.Nick != "Петя" {
		t.Fatalf("второй аккаунт: %+v %v %v", b, created, err)
	}
	if got, ok := s.Get(a.ID); !ok || got.Subject != "y-1" {
		t.Fatal("первый аккаунт испорчен")
	}
}

func TestPurgeGuests(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	old := time.Now().Add(-GuestTTL - time.Hour)
	fresh := time.Now()
	mk := func(nick string, at time.Time) Account {
		g, err := s.EnsureGuest(NewID(), nick, false, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	gone := mk("Старый", old)
	ranked := mk("Модератор", old)
	banned := mk("Хулиган", old)
	alive := mk("Свежий", fresh)
	if err := s.SetRank(ranked.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	if err := s.Ban(banned.ID, "", old); err != nil {
		t.Fatal(err)
	}
	acc, _, err := s.Ensure(ProviderYandex, "y", "", "Аккаунт", nil, old)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeGuests(time.Now().Add(-GuestTTL))
	if err != nil || n != 1 {
		t.Fatalf("удалено %d (%v), ждали 1", n, err)
	}
	if _, ok := s.Get(gone.ID); ok {
		t.Error("пропавший гость остался")
	}
	for _, id := range []string{ranked.ID, banned.ID, alive.ID, acc.ID} {
		if _, ok := s.Get(id); !ok {
			t.Errorf("удалена запись, которую надо было оставить: %s", id)
		}
	}
	// Ник удалённого гостя свободен.
	if _, err := s.EnsureGuest(NewID(), "Старый", false, nil, fresh); err != nil {
		t.Errorf("ник удалённого гостя не освободился: %v", err)
	}
}

// Touch пишет время захода не чаще раза в минуту: запись на каждое переподключение уборке гостей
// ничего не даёт.
func TestTouchThrottled(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	t0 := time.Now().Round(time.Millisecond)
	g, err := s.EnsureGuest(NewID(), "Снежок", false, nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	s.Touch(g.ID, t0.Add(10*time.Second))
	if got, _ := s.Get(g.ID); !got.SeenAt.Equal(t0) {
		t.Errorf("заход записан раньше минуты: %v", got.SeenAt)
	}
	s.Touch(g.ID, t0.Add(2*time.Minute))
	if got, _ := s.Get(g.ID); !got.SeenAt.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("заход не записан: %v", got.SeenAt)
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	// Ники по порядку, а не protocol.FallbackNick: у «Игрок NNNN» девять тысяч вариантов, и среди
	// 55 случайных два совпадают примерно в каждом шестом прогоне — так упал релиз v0.25.0.
	for i := 0; i < SearchPage+5; i++ {
		if _, err := s.EnsureGuest(NewID(), fmt.Sprintf("Гость %03d", i), true, nil, now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	acc, _, err := s.Ensure(ProviderYandex, "y", "vasya_login", "Ёлка", nil, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ban(acc.ID, "", now); err != nil {
		t.Fatal(err)
	}
	acc, _ = s.Get(acc.ID)

	page, total, err := s.Search("", FilterAll, nil, 0, SearchPage)
	if err != nil || total != SearchPage+6 || len(page) != SearchPage || page[0].ID != acc.ID {
		t.Fatalf("первая страница: %d из %d (%v)", len(page), total, err)
	}
	page, _, _ = s.Search("", FilterAll, nil, SearchPage, SearchPage)
	if len(page) != 6 {
		t.Fatalf("вторая страница: %d", len(page))
	}
	for q, want := range map[string]int{"елка": 1, "ЁЛ": 1, "vasya": 1, acc.ID: 1, "нет_такого": 0, "%": 0} {
		if _, total, err := s.Search(q, FilterAll, nil, 0, SearchPage); err != nil || total != want {
			t.Errorf("поиск %q: %d (%v), ждали %d", q, total, err, want)
		}
		if got := Matches(acc, q, FilterAll); got != (want == 1) {
			t.Errorf("Matches %q: %v, а Search нашёл %d", q, got, want)
		}
	}
	for f, want := range map[string]int{FilterGuests: SearchPage + 5, FilterYandex: 1, FilterBanned: 1, FilterRanked: 0} {
		if _, total, err := s.Search("", f, nil, 0, SearchPage); err != nil || total != want {
			t.Errorf("фильтр %q: %d (%v), ждали %d", f, total, err, want)
		}
		if got := Matches(acc, "", f); got != (f == FilterYandex || f == FilterBanned) {
			t.Errorf("Matches с фильтром %q: %v", f, got)
		}
	}
	// Исключённые (они уже показаны как онлайн) не попадают ни в страницу, ни в итог.
	page, total, err = s.Search("", FilterAll, []string{acc.ID}, 0, SearchPage)
	if err != nil || total != SearchPage+5 || page[0].ID == acc.ID {
		t.Fatalf("исключение: %d записей, первая %s (%v)", total, page[0].ID, err)
	}
	if _, total, err := s.Search("", FilterAll, nil, 0, 0); err != nil || total != SearchPage+6 {
		t.Errorf("пустое окно всё равно должно считать итог: %d (%v)", total, err)
	}
}

// Порядок списка: админы, модераторы, остальные; внутри — кто заходил позже, тот выше; при
// равенстве — по нику.
func TestSearchOrder(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	t0 := time.Now().Round(time.Millisecond)
	mk := func(nick string, seen time.Time, rank string) {
		g, err := s.EnsureGuest(NewID(), nick, false, nil, seen)
		if err != nil {
			t.Fatal(err)
		}
		if rank != "" {
			if err := s.SetRank(g.ID, rank); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("Старый", t0, "")
	mk("Свежий", t0.Add(time.Hour), "")
	mk("Бета", t0.Add(time.Minute), "")
	mk("Альфа", t0.Add(time.Minute), "")
	mk("Модер", t0.Add(-time.Hour), protocol.RankModerator)
	mk("Админ", t0.Add(-2*time.Hour), protocol.RankAdmin)
	page, _, err := s.Search("", FilterAll, nil, 0, SearchPage)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range page {
		got = append(got, a.Nick)
	}
	if want := "Админ,Модер,Свежий,Альфа,Бета,Старый"; strings.Join(got, ",") != want {
		t.Fatalf("порядок %v, ждали %s", got, want)
	}
}

// Импорт старого accounts.json: всё, что было в файле, оказывается в базе.
func TestImportJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "accounts.json")
	data := `{"version":1,"accounts":[
	 {"id":"a111111111111","provider":"yandex","sub":"42","login":"vasya","nick":"Вася","rank":"admin",
	  "tut":["basics","dash"],"epoch":2,"createdAt":"2026-09-01T10:00:00Z","nickAt":"2026-09-02T10:00:00Z",
	  "seenAt":"2026-09-03T10:00:00Z","banReason":"мат","bannedAt":"2026-09-04T10:00:00Z"},
	 {"id":"a222222222222","provider":"yandex","sub":"43","nick":"вася","createdAt":"2026-09-01T10:00:00Z","seenAt":"2026-09-01T10:00:00Z"},
	 {"id":"","sub":"44","nick":"Пусто"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, "")
	tx, err := db.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	n, err := ImportJSON(tx, path)
	if err != nil || n != 2 {
		t.Fatalf("импорт: %d %v", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s := Open(db, zerolog.New(io.Discard))
	a, ok := s.Get("a111111111111")
	if !ok || a.Nick != "Вася" || a.Rank != "admin" || a.Epoch != 2 || a.Login != "vasya" ||
		strings.Join(a.Tutorial, ",") != "basics,dash" || !a.Banned() || a.BanReason != "мат" || a.NickAt.IsZero() {
		t.Fatalf("аккаунт перенёсся не целиком: %+v", a)
	}
	// Второй с тем же ником в другом регистре получил свой ник, а не потерялся.
	b, ok := s.Get("a222222222222")
	if !ok || protocol.NickKey(b.Nick) == protocol.NickKey("Вася") || !b.NickAuto {
		t.Fatalf("дубль ника: %+v", b)
	}
	if n, err := ImportJSON(tx, filepath.Join(t.TempDir(), "нет.json")); err != nil || n != 0 {
		t.Errorf("отсутствующий файл: %d %v", n, err)
	}
}

func TestBanAndEpoch(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	acc, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Banned() {
		t.Error("новый аккаунт не должен быть забанен")
	}
	if err := s.Ban(acc.ID, "мат", now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(acc.ID)
	if !got.Banned() || got.BanReason != "мат" {
		t.Fatalf("бан не записался: %+v", got)
	}
	if err := s.Unban(acc.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(acc.ID); got.Banned() {
		t.Error("бан не снялся")
	}
	if err := s.BumpEpoch(acc.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(acc.ID); got.Epoch != 1 {
		t.Errorf("epoch: %d", got.Epoch)
	}
}

// Стор дёргают из горутин соединений и из HTTP-обработчиков одновременно: гонок и ошибок
// «база занята» быть не должно.
func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := string(rune('a' + i))
			acc, _, err := s.Ensure(ProviderYandex, sub, "", "", nil, now)
			if err != nil {
				t.Errorf("Ensure: %v", err)
				return
			}
			if _, err := s.EnsureGuest(NewID(), protocol.FallbackNick(), true, nil, now); err != nil && !errors.Is(err, ErrNickTaken) {
				t.Errorf("EnsureGuest: %v", err)
			}
			s.AddTutorial(acc.ID, "basics")
			s.Touch(acc.ID, now)
			_, _ = s.Get(acc.ID)
			_, _, _ = s.Search("", FilterAll, nil, 0, SearchPage)
		}(i)
	}
	wg.Wait()
	if _, total, _ := s.Search("", FilterYandex, nil, 0, SearchPage); total != 20 {
		t.Fatalf("заведено аккаунтов: %d", total)
	}
}

// Ни один метод не должен падать на nil-сторе: сервер умеет работать без аккаунтов вообще.
func TestNilStoreIsSafe(t *testing.T) {
	t.Parallel()
	var s *Store
	if _, ok := s.Get("a1"); ok {
		t.Error("Get на nil")
	}
	if _, ok := s.BySubject(ProviderYandex, "1"); ok {
		t.Error("BySubject на nil")
	}
	if _, ok := s.ByNickKey("вася"); ok {
		t.Error("ByNickKey на nil")
	}
	if _, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, time.Now()); !errors.Is(err, ErrNoStore) {
		t.Errorf("Ensure на nil: %v", err)
	}
	if err := s.Rename("a1", "Вася", nil, time.Now()); !errors.Is(err, ErrNoStore) {
		t.Errorf("Rename на nil: %v", err)
	}
	if _, err := s.EnsureGuest("a1", "Вася", false, nil, time.Now()); !errors.Is(err, ErrNoStore) {
		t.Errorf("EnsureGuest на nil: %v", err)
	}
	if s.AddTutorial("a1", "basics") || s.Len() != 0 || s.Broken() {
		t.Error("остальные методы на nil")
	}
	if _, _, err := s.Search("", "", nil, 0, SearchPage); !errors.Is(err, ErrNoStore) {
		t.Errorf("Search на nil: %v", err)
	}
	if n, err := s.PurgeGuests(time.Now()); n != 0 || err != nil {
		t.Errorf("PurgeGuests на nil: %d %v", n, err)
	}
	s.Touch("a1", time.Now())
}
