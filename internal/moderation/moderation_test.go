package moderation

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func testLog() zerolog.Logger { return zerolog.New(io.Discard) }

func TestStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moderation.json")
	now := time.Now().UTC().Truncate(time.Second)
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRank("10.0.0.1", "admin", "Аня", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Ban("10.0.0.2", "Боря", "флуд", now); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if s2.Rank("10.0.0.1") != "admin" {
		t.Fatalf("роль не сохранилась: %q", s2.Rank("10.0.0.1"))
	}
	if !s2.Banned("10.0.0.2") {
		t.Fatal("бан не сохранился")
	}
	if s2.Broken() {
		t.Fatal("файл не должен считаться битым")
	}
	if got := s2.Ranks(); len(got) != 1 || got[0].Nick != "Аня" || !got[0].Since.Equal(now) {
		t.Fatalf("список ролей: %+v", got)
	}
	if got := s2.Bans(); len(got) != 1 || got[0].Reason != "флуд" {
		t.Fatalf("список банов: %+v", got)
	}

	if err := s2.Unban("10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if err := s2.SetRank("10.0.0.1", "", "", now); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if s3.Banned("10.0.0.2") || s3.Rank("10.0.0.1") != "" {
		t.Fatal("снятие роли и бана не сохранилось")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moderation.json")
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ban("10.0.0.3", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "moderation-") {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("права %v, в файле IP-адреса — ждём 0600", st.Mode().Perm())
	}
}

func TestBrokenFileIsMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moderation.json")
	if err := os.WriteFile(path, []byte("{это не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatalf("сервер должен подниматься даже на битом файле: %v", err)
	}
	if !s.Broken() {
		t.Fatal("битый файл должен помечаться")
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf("битый файл не отложен: %v", err)
	}
	// Первая же запись создаёт нормальный файл заново.
	if err := s.Ban("10.0.0.4", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Broken() {
		t.Fatal("после успешной записи метка должна сниматься")
	}
	s2, err := Open(path, testLog())
	if err != nil || !s2.Banned("10.0.0.4") {
		t.Fatalf("перезаписанный файл не читается: %v", err)
	}
}

func TestEmptyPathIsMemoryOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open("", testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRank("10.0.0.5", "creator", "Вика", time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Rank("10.0.0.5") != "creator" {
		t.Fatal("роль должна работать и без файла")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("без пути на диск писать нельзя: %v", ents)
	}
}

func TestNilStoreIsPlayerAndNotBanned(t *testing.T) {
	var s *Store
	if s.Rank("10.0.0.6") != "" || s.Banned("10.0.0.6") || s.Broken() {
		t.Fatal("nil-стор: все без роли и без бана")
	}
	if s.Ranks() != nil || s.Bans() != nil {
		t.Fatal("nil-стор: списки пустые")
	}
	if err := s.Ban("10.0.0.6", "", "", time.Now()); err == nil {
		t.Fatal("запись в nil-стор должна быть ошибкой")
	}
}
