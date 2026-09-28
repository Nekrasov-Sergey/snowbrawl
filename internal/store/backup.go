package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Бэкапы. SQLite в режиме WAL нельзя копировать обычным cp на ходу: копия основного файла без
// свежего -wal бывает неконсистентной. VACUUM INTO пишет целостный снимок в отдельный файл, не
// останавливая чтение; писатели на это время ждут, но база маленькая, и это миллисекунды.
//
// Снимок — один в сутки, имя по дате, хранится KeepBackups последних. Снять копию с сервера
// после этого можно чем угодно: файл снимка никто не пишет.

// KeepBackups — сколько суточных снимков держим.
const KeepBackups = 7

const backupPrefix = "snowbrawl-"

// Backup делает снимок за сутки now, если его ещё нет, и удаляет лишние старые. Возвращает путь
// снимка или пустую строку, если снимок за эти сутки уже есть.
func (d *DB) Backup(dir string, keep int, now time.Time) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, backupPrefix+now.UTC().Format("20060102")+".db")
	made := ""
	if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
		tmp := name + ".tmp"
		_ = os.Remove(tmp) // хвост оборванного снимка
		if _, err := d.W.Exec("VACUUM INTO ?", tmp); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
		if err := os.Chmod(tmp, 0o600); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, name); err != nil {
			return "", err
		}
		made = name
	} else if err != nil {
		return "", err
	}
	return made, prune(dir, keep)
}

// prune оставляет keep самых свежих снимков. Имена по дате сортируются так же, как даты.
func prune(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var snaps []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, ".db") {
			snaps = append(snaps, n)
		}
	}
	sort.Strings(snaps)
	for len(snaps) > keep {
		if err := os.Remove(filepath.Join(dir, snaps[0])); err != nil {
			return err
		}
		snaps = snaps[1:]
	}
	return nil
}

// BackupTask — задача для RunDaily: снимок в dir, ошибки — в лог.
func (d *DB) BackupTask(dir string) func(time.Time) {
	return func(now time.Time) {
		if dir == "" {
			return
		}
		made, err := d.Backup(dir, KeepBackups, now)
		switch {
		case err != nil:
			d.log.Error().Err(err).Str("dir", dir).Msg("store: снимок базы не сделан")
		case made != "":
			d.log.Info().Str("file", made).Msg("store: снимок базы сделан")
		}
	}
}
