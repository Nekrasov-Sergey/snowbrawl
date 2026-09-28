package store

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func open(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(path, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenMigratesAndReopens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "snowbrawl.db")
	d := open(t, path)
	var v int
	if err := d.R.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations()) {
		t.Fatalf("версия схемы %d (%v), ждали %d", v, err, len(migrations()))
	}
	if err := d.SetMeta("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMeta("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// Каталог и файл закрыты от чужих: в базе ники и связи с Яндексом.
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("права файла: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("права каталога: %v %v", fi, err)
	}

	d2 := open(t, path)
	got, ok, err := d2.Meta("k")
	if err != nil || !ok || got != "v2" {
		t.Fatalf("meta после перезапуска: %q %v %v", got, ok, err)
	}
	if d2.Broken() {
		t.Error("целая база отмечена битой")
	}
	if _, ok, _ := d2.Meta("нет"); ok {
		t.Error("несуществующий ключ нашёлся")
	}
}

// Битый файл не роняет сервер: он откладывается, база создаётся заново, и это видно.
func TestBrokenFileMovedAside(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "snowbrawl.db")
	if err := os.WriteFile(path, []byte("это не база SQLite, а просто текст длиннее заголовка страницы"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := open(t, path)
	if !d.Broken() {
		t.Error("битый файл не отмечен")
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Errorf("битый файл не отложен: %v", err)
	}
	if err := d.SetMeta("k", "v"); err != nil {
		t.Fatalf("новая база не пишется: %v", err)
	}
}

// Схема новее сборки — это откат бинарника на старую версию. Работать молча с непонятной схемой
// нельзя: старый код мог бы испортить данные.
func TestNewerSchemaRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "snowbrawl.db")
	d := open(t, path)
	if _, err := d.W.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if _, err := Open(path, zerolog.New(io.Discard)); err == nil {
		t.Fatal("база с более новой схемой открылась")
	}
}

func TestReadPoolIsReadOnly(t *testing.T) {
	t.Parallel()
	d := open(t, "")
	if _, err := d.R.Exec("INSERT INTO meta(key, value) VALUES('a', 'b')"); err == nil {
		t.Fatal("пул чтения записал в базу")
	}
}

func TestTempDBRemovedOnClose(t *testing.T) {
	t.Parallel()
	d, err := Open("", zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(d.Path())
	if _, err := os.Stat(d.Path()); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("временный каталог остался: %v", err)
	}
}

func TestIsUnique(t *testing.T) {
	t.Parallel()
	d := open(t, "")
	if err := d.SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
	_, err := d.W.Exec("INSERT INTO meta(key, value) VALUES('k', 'v')")
	uniq, what := IsUnique(err)
	if !uniq || what != "meta.key" {
		t.Fatalf("IsUnique: %v %q (%v)", uniq, what, err)
	}
	if uniq, _ := IsUnique(nil); uniq {
		t.Error("nil — не нарушение уникальности")
	}
}

func TestBackupDailyAndPrune(t *testing.T) {
	t.Parallel()
	d := open(t, "")
	if err := d.SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	made, err := d.Backup(dir, 3, day)
	if err != nil || made == "" {
		t.Fatalf("снимок: %q %v", made, err)
	}
	// Второй раз за те же сутки снимок не делается: сервер перезапускают чаще раза в день.
	if again, err := d.Backup(dir, 3, day.Add(time.Hour)); err != nil || again != "" {
		t.Fatalf("повторный снимок за сутки: %q %v", again, err)
	}
	if fi, err := os.Stat(made); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("права снимка: %v %v", fi, err)
	}
	// Снимок — настоящая база с данными.
	snap, err := Open(made, zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := snap.Meta("k"); !ok || v != "v" {
		t.Errorf("в снимке нет данных: %q %v", v, ok)
	}
	_ = snap.Close()

	for i := 1; i <= 4; i++ {
		if _, err := d.Backup(dir, 3, day.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".db" {
			names = append(names, e.Name())
		}
	}
	if len(names) != 3 || names[0] != "snowbrawl-20260903.db" || names[2] != "snowbrawl-20260905.db" {
		t.Fatalf("после ротации остались: %v", names)
	}
}

func TestRunDailyRunsAtStartAndStops(t *testing.T) {
	t.Parallel()
	d := open(t, "")
	ran := make(chan struct{}, 10)
	d.RunDaily(time.Hour, func(time.Time) { ran <- struct{}{} })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("задача не запустилась при старте")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNilDBIsSafe(t *testing.T) {
	t.Parallel()
	var d *DB
	if d.Broken() {
		t.Error("Broken на nil")
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
}
