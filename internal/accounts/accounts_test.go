package accounts

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

func testStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEnsureCreatesAndFinds(t *testing.T) {
	t.Parallel()
	s := testStore(t, "")
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
			s := testStore(t, "")
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
	s := testStore(t, "")
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
	s := testStore(t, "")
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
	if id, ok := s.byNick[protocol.NickKey("Вася")]; !ok || id != acc.ID {
		t.Error("бронь ника потерялась")
	}
}

func TestAddTutorialMerges(t *testing.T) {
	t.Parallel()
	s := testStore(t, "")
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
	path := filepath.Join(t.TempDir(), "accounts.json")
	s := testStore(t, path)
	now := time.Now().Round(time.Millisecond)
	acc, _, err := s.Ensure(ProviderYandex, "42", "vasya", "Вася", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	s.AddTutorial(acc.ID, "basics")
	if err := s.SetRank(acc.ID, protocol.RankModerator); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("права файла: %v %v", fi, err)
	}

	s2 := testStore(t, path)
	got, ok := s2.BySubject(ProviderYandex, "42")
	if !ok {
		t.Fatal("аккаунт не прочитался")
	}
	if got.ID != acc.ID || got.Nick != "Вася" || got.Rank != protocol.RankModerator || len(got.Tutorial) != 1 {
		t.Fatalf("аккаунт прочитался не целиком: %+v", got)
	}
	if _, ok := s2.ByNickKey(protocol.NickKey("Вася")); !ok {
		t.Error("индекс ников не построился при чтении")
	}
}

// Flush пишет файл только когда есть что писать: фоновая горутина тикает постоянно, и
// переписывать файл каждые пять секунд без изменений незачем.
func TestFlushOnlyWhenDirty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "accounts.json")
	s := testStore(t, path)
	if _, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Error("файл переписан без изменений")
	}
}

func TestBrokenFileMovedAside(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := testStore(t, path)
	if !s.Broken() {
		t.Error("битый файл не отмечен как битый")
	}
	if s.Len() != 0 {
		t.Error("после битого файла стор должен быть пуст")
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Errorf("битый файл не отложен: %v", err)
	}
	// Сервер обязан продолжать работать: новый аккаунт заводится и пишется.
	if _, _, err := s.Ensure(ProviderYandex, "1", "", "Вася", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.Broken() {
		t.Error("после успешной записи флаг битого файла должен сняться")
	}
}

func TestBanAndEpoch(t *testing.T) {
	t.Parallel()
	s := testStore(t, "")
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

// Стор дёргают из горутины хаба и из HTTP-обработчиков одновременно: гонок быть не должно.
func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "accounts.json")
	s := testStore(t, path)
	s.Run(time.Millisecond)
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
			s.AddTutorial(acc.ID, "basics")
			s.Touch(acc.ID, now)
			_, _ = s.Get(acc.ID)
			_ = s.List()
		}(i)
	}
	wg.Wait()
	if s.Len() != 20 {
		t.Fatalf("заведено аккаунтов: %d", s.Len())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("файл не читается: %v", err)
	}
	if len(f.Accounts) != 20 || f.Version != fileVersion {
		t.Fatalf("в файле %d аккаунтов, версия %d", len(f.Accounts), f.Version)
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
	if s.AddTutorial("a1", "basics") || s.Len() != 0 || s.Broken() || s.List() != nil {
		t.Error("остальные методы на nil")
	}
	s.Run(time.Millisecond)
	s.Touch("a1", time.Now())
	if err := s.Close(); err != nil {
		t.Errorf("Close на nil: %v", err)
	}
}
