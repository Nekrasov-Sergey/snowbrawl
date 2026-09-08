// Package moderation хранит роли и баны по IP-адресам. Базы в проекте нет, поэтому список
// лежит в одном JSON-файле: он должен переживать перезапуск и выкладку новой версии.
//
// У стора свой мьютекс, и наружу он ничего не вызывает — дедлок с мьютексом хаба невозможен
// по построению. Важный инвариант: hub только ЧИТАЕТ стор (Rank/Banned), а запись на диск
// делает админка вне h.mu. Иначе fsync окажется под общим мьютексом и остановит все матчи.
//
// Роли — по адресу, а не по игроку: за одним IP может сидеть несколько человек (NAT), это
// осознанное упрощение до появления аккаунтов.
package moderation

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Entry — выданная роль. Обычный игрок записи не имеет.
type Entry struct {
	IP    string    `json:"ip"`
	Rank  string    `json:"rank"`
	Nick  string    `json:"nick,omitempty"` // ник на момент выдачи, чтобы список читался глазами
	Since time.Time `json:"since"`
}

// Ban — заблокированный адрес. Бессрочно: снимается только разбаном.
type Ban struct {
	IP     string    `json:"ip"`
	Nick   string    `json:"nick,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type file struct {
	Version int     `json:"version"`
	Ranks   []Entry `json:"ranks"`
	Bans    []Ban   `json:"bans"`
}

const fileVersion = 1

// Store — роли и баны. Все методы безопасны на nil-приёмнике: так hub и тесты обходятся без
// проверок, а сервер может работать вообще без файла.
type Store struct {
	mu     sync.RWMutex
	path   string // "" — только в памяти (разработка)
	ranks  map[string]Entry
	bans   map[string]Ban
	broken bool
	log    zerolog.Logger
}

// Open читает файл ролей и банов. Файла нет — пустой стор. Файл битый — он откладывается
// рядом с суффиксом .bad, и сервер поднимается с пустыми списками: уронить игру из-за
// испорченного файла хуже, чем потерять баны, но это должно быть видно (ERROR в лог,
// красная строка в админке — см. Broken).
func Open(path string, log zerolog.Logger) (*Store, error) {
	s := &Store{path: path, ranks: map[string]Entry{}, bans: map[string]Ban{}, log: log}
	if path == "" {
		log.Info().Msg("moderation: файл не задан, роли и баны живут только в памяти")
		return s, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // путь задаёт администратор через конфиг
	if errors.Is(err, fs.ErrNotExist) {
		log.Info().Str("path", path).Msg("moderation: файла нет, начинаем с пустого списка")
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		s.broken = true
		bad := path + ".bad"
		if rerr := os.Rename(path, bad); rerr != nil {
			log.Error().Err(rerr).Str("path", path).Msg("moderation: файл битый и не переименовывается")
		}
		log.Error().Err(err).Str("moved", bad).Msg("moderation: файл битый, роли и баны сброшены")
		return s, nil
	}
	for _, e := range f.Ranks {
		if e.IP != "" && e.Rank != "" {
			s.ranks[e.IP] = e
		}
	}
	for _, b := range f.Bans {
		if b.IP != "" {
			s.bans[b.IP] = b
		}
	}
	log.Info().Int("ranks", len(s.ranks)).Int("bans", len(s.bans)).Str("path", path).Msg("moderation: список загружен")
	return s, nil
}

// Rank возвращает роль адреса; пустая строка — обычный игрок.
func (s *Store) Rank(ip string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ranks[ip].Rank
}

// Banned сообщает, заблокирован ли адрес.
func (s *Store) Banned(ip string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bans[ip]
	return ok
}

// Broken — файл при старте оказался испорченным (списки сброшены).
func (s *Store) Broken() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.broken
}

// SetRank выдаёт роль адресу; пустая роль снимает запись.
func (s *Store) SetRank(ip, rank, nick string, now time.Time) error {
	if s == nil {
		return errors.New("moderation: стор не создан")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rank == "" {
		delete(s.ranks, ip)
	} else {
		s.ranks[ip] = Entry{IP: ip, Rank: rank, Nick: nick, Since: now}
	}
	return s.save()
}

// Ban блокирует адрес.
func (s *Store) Ban(ip, nick, reason string, now time.Time) error {
	if s == nil {
		return errors.New("moderation: стор не создан")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bans[ip] = Ban{IP: ip, Nick: nick, Reason: reason, At: now}
	return s.save()
}

// Unban снимает блокировку.
func (s *Store) Unban(ip string) error {
	if s == nil {
		return errors.New("moderation: стор не создан")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bans, ip)
	return s.save()
}

// Ranks возвращает копию списка ролей, отсортированную по адресу.
func (s *Store) Ranks() []Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.ranks))
	for _, e := range s.ranks {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// Bans возвращает копию списка банов, от свежих к старым.
func (s *Store) Bans() []Ban {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Ban, 0, len(s.bans))
	for _, b := range s.bans {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// save пишет файл целиком: временный файл в том же каталоге плюс rename — чтобы обрыв
// записи не оставил половину списка. Вызывать под s.mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	f := file{Version: fileVersion}
	for _, e := range s.ranks {
		f.Ranks = append(f.Ranks, e)
	}
	for _, b := range s.bans {
		f.Bans = append(f.Bans, b)
	}
	sort.Slice(f.Ranks, func(i, j int) bool { return f.Ranks[i].IP < f.Ranks[j].IP })
	sort.Slice(f.Bans, func(i, j int) bool { return f.Bans[i].IP < f.Bans[j].IP })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil { // в каталоге лежат IP-адреса
		return err
	}
	tmp, err := os.CreateTemp(dir, "moderation-*.json") // тот же каталог: rename через границу ФС не работает
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil { // в файле IP-адреса
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	s.broken = false
	return nil
}
