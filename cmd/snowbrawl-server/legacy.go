package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

// Импорт файлов, в которых данные жили до базы: accounts.json, online.log, auth.key и
// moderation.json. Срабатывает один раз — при первом старте с новой базой, — одной транзакцией,
// и после неё файлы переименовываются в *.imported: откатить выкладку можно, вернув им имена.
//
// Из moderation.json не переносится ничего: роли и баны там привязаны к IP, а у записей игроков
// адресов нет, так что сопоставить их не с чем. Админку по-прежнему открывает токен, роли
// выдаются заново уже игрокам.

// metaImported — отметка в базе, что импорт был (даже если переносить было нечего).
const metaImported = "legacy_imported_at"

// legacyKeyName — где лежал ключ подписи: рядом с файлом аккаунтов.
const legacyKeyName = "auth.key"

func importLegacy(db *store.DB, cfg config.Config, log zerolog.Logger) error {
	if _, done, err := db.Meta(metaImported); err != nil || done {
		return err
	}
	keyFile := ""
	if cfg.AccountsFile != "" {
		keyFile = filepath.Join(filepath.Dir(cfg.AccountsFile), legacyKeyName)
	}

	tx, err := db.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	nAcc, err := accounts.ImportJSON(tx, cfg.AccountsFile)
	if err != nil {
		return fmt.Errorf("импорт аккаунтов: %w", err)
	}
	nPts, err := onlinestat.ImportLog(tx, cfg.OnlineFile)
	if err != nil {
		// Графиком ради старта не жертвуем: битый ряд просто не переносится.
		log.Error().Err(err).Msg("legacy: ряд онлайна не перенесён")
		nPts = 0
	}
	key := false
	if keyFile != "" {
		data, err := os.ReadFile(keyFile) //nolint:gosec // путь из конфига
		switch {
		case err == nil && len(strings.TrimSpace(string(data))) >= 32:
			if err := store.SetMetaTx(tx, "auth_secret", strings.TrimSpace(string(data))); err != nil {
				return err
			}
			key = true
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("импорт ключа подписи: %w", err)
		}
	}
	if err := store.SetMetaTx(tx, metaImported, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	moved := 0
	for _, p := range []string{cfg.AccountsFile, cfg.OnlineFile, keyFile, cfg.ModerationFile} {
		if p == "" {
			continue
		}
		if err := os.Rename(p, p+".imported"); err == nil {
			moved++
		} else if !errors.Is(err, fs.ErrNotExist) {
			log.Warn().Err(err).Str("path", p).Msg("legacy: файл перенесён, но не переименован")
		}
	}
	if moved > 0 {
		log.Info().Int("accounts", nAcc).Int("onlinePoints", nPts).Bool("authKey", key).
			Msg("legacy: старые файлы перенесены в базу и переименованы в *.imported")
	}
	return nil
}
