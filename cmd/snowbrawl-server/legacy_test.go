package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

// Первый старт новой сборки на боевом томе: всё из старых файлов оказывается в базе, файлы
// переименовываются, второй старт ничего не повторяет.
func TestImportLegacy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DBFile = filepath.Join(dir, "snowbrawl.db")
	cfg.AccountsFile = filepath.Join(dir, "accounts.json")
	cfg.OnlineFile = filepath.Join(dir, "online.log")
	cfg.ModerationFile = filepath.Join(dir, "moderation.json")
	key := strings.Repeat("ab", 32)
	files := map[string]string{
		cfg.AccountsFile: `{"version":1,"accounts":[{"id":"a111111111111","provider":"yandex","sub":"42",
			"nick":"Вася","rank":"admin","tut":["basics"],"epoch":3,
			"createdAt":"2026-09-01T10:00:00Z","seenAt":"2026-09-02T10:00:00Z"}]}`,
		cfg.OnlineFile:                 "#snowbrawl-online 1\n1757505600 4\n1757505660 5\n",
		cfg.ModerationFile:             `{"version":2,"ranks":[{"ip":"1.2.3.4","rank":"admin"}],"bans":[]}`,
		filepath.Join(dir, "auth.key"): key,
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := zerolog.New(io.Discard)
	db, err := store.Open(cfg.DBFile, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := importLegacy(db, cfg, log); err != nil {
		t.Fatal(err)
	}

	accs := accounts.Open(db, log)
	a, ok := accs.Get("a111111111111")
	if !ok || a.Nick != "Вася" || a.Rank != "admin" || a.Epoch != 3 || len(a.Tutorial) != 1 {
		t.Fatalf("аккаунт не перенёсся: %+v %v", a, ok)
	}
	// Эпоха и ключ перенесены — значит, выданные куки продолжат работать после выкладки.
	if got, ok, _ := db.Meta("auth_secret"); !ok || got != key {
		t.Fatalf("ключ подписи: %q %v", got, ok)
	}
	series, err := onlinestat.Open(db, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := series.Close(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.R.QueryRow("SELECT count(*) FROM online_points").Scan(&n); err != nil || n != 2 {
		t.Fatalf("точек онлайна %d (%v)", n, err)
	}
	for p := range files {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s не переименован", filepath.Base(p))
		}
		if _, err := os.Stat(p + ".imported"); err != nil {
			t.Errorf("%s.imported: %v", filepath.Base(p), err)
		}
	}

	// Повторный старт: файлы вернули на место (откат и снова выкладка) — импорт не повторяется.
	if err := os.Rename(cfg.AccountsFile+".imported", cfg.AccountsFile); err != nil {
		t.Fatal(err)
	}
	if err := importLegacy(db, cfg, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.AccountsFile); err != nil {
		t.Error("повторный импорт тронул файл")
	}
}

// Без старых файлов (чистая установка) импорт просто отмечается.
func TestImportLegacyNothing(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.AccountsFile = filepath.Join(t.TempDir(), "accounts.json")
	log := zerolog.New(io.Discard)
	db, err := store.Open("", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := importLegacy(db, cfg, log); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.Meta(metaImported); !ok {
		t.Error("импорт не отмечен")
	}
}
