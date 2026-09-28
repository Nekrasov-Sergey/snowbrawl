package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

// legacyFile — формат accounts.json, в котором аккаунты жили до базы. Нужен только импорту.
type legacyFile struct {
	Version  int       `json:"version"`
	Accounts []Account `json:"accounts"`
}

// ImportJSON переносит аккаунты из старого accounts.json в базу внутри транзакции вызывающего
// (см. cmd/snowbrawl-server). Файла нет — ноль и без ошибки. Записи без id или subject
// пропускаются, как их пропускала и старая загрузка. Ник, совпавший с уже перенесённым, заменяется
// выданным сервером: в старом файле такого быть не могло, но терять из-за этого аккаунт нельзя.
func ImportJSON(tx store.Execer, path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // путь задаёт администратор через конфиг
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var f legacyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("accounts.json не разобран: %w", err)
	}
	n := 0
	for _, a := range f.Accounts {
		if a.ID == "" || a.Subject == "" {
			continue
		}
		if a.Provider == ProviderGuest {
			a.Provider = ProviderYandex // в старом файле гостей не было
		}
		if err := insertLegacy(tx, a); err != nil {
			return n, fmt.Errorf("аккаунт %s: %w", a.ID, err)
		}
		n++
	}
	return n, nil
}

func insertLegacy(tx store.Execer, a Account) error {
	for try := 0; ; try++ {
		_, err := tx.Exec("INSERT INTO players("+cols+", nick_key) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			a.ID, a.Provider, a.Subject, a.Login, a.Nick, boolInt(a.NickAuto), a.Rank, a.Epoch,
			ms(a.CreatedAt), nullMs(a.NickAt), ms(a.SeenAt), a.BanReason, bannedMs(a), protocol.NickKey(a.Nick))
		uniq, what := store.IsUnique(err)
		if !uniq || what != "players.nick_key" || try >= 30 {
			if err != nil {
				return err
			}
			break
		}
		a.Nick, a.NickAuto = protocol.FallbackNick(), true
	}
	for _, l := range a.Tutorial {
		if l == "" {
			continue
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO player_lessons(player_id, lesson) VALUES(?, ?)", a.ID, l); err != nil {
			return err
		}
	}
	return nil
}

func bannedMs(a Account) any {
	if a.BannedAt == nil {
		return nil
	}
	return ms(*a.BannedAt)
}
