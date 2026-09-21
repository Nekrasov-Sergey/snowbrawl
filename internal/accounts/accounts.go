// Package accounts хранит постоянные аккаунты игроков: то немногое, что должно пережить
// перезапуск сервера и переезд игрока на другое устройство, — ник, прогресс обучения, роль
// модерации. Всё остальное про игрока по-прежнему живёт в памяти (internal/session).
//
// Базы в проекте нет, поэтому аккаунты лежат в одном JSON-файле рядом с ролями и банами
// (internal/moderation): тот же приём с temp+rename, та же версия формата, та же осторожность
// с битым файлом.
//
// Отличие от moderation — в том, КТО ходит на диск. Роли правит только админка, редко; аккаунты
// же меняются в игре: отметка урока, переименование, обновление времени входа. Синхронный fsync
// на каждое такое событие встал бы поперёк всех матчей, потому что зовут их из-под мьютекса хаба.
// Поэтому здесь приём из internal/onlinestat: любая правка меняет только память и поднимает
// dirty, а на диск пишут фоновая горутина (Run), Close и явный Flush из HTTP-обработчиков,
// которым важно не потерять событие (вход через OAuth, действия админки). Цена — потеря до
// периода флаша при kill -9; для отметки урока это приемлемо, для входа — нет, отсюда Flush.
//
// Наружу стор ничего не вызывает (кроме колбэка taken в Ensure/Rename, который обязан быть
// быстрым и не брать мьютекс хаба), поэтому дедлок с h.mu невозможен по построению.
package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// Провайдеры входа. Пока один, но поле в файле есть с самого начала: добавить второй провайдер
// потом будет нельзя без миграции, если различать их только по формату Subject.
const ProviderYandex = "yandex"

// fileVersion — версия формата файла. Растёт при несовместимом изменении, как в moderation.
const fileVersion = 1

// RenameCooldown — как часто можно менять ник. Без паузы смена ника превращается в способ
// перебирать чужие брони и мельтешить в чате под разными именами.
const RenameCooldown = 10 * time.Minute

// FlushEvery — период фоновой записи на диск (см. Run).
const FlushEvery = 5 * time.Second

// Ошибки, которые вызывающий обязан различать: каждой соответствует свой текст игроку.
var (
	ErrNotFound  = errors.New("accounts: аккаунт не найден")
	ErrNickTaken = errors.New("accounts: ник занят")
	ErrTooSoon   = errors.New("accounts: ник менялся недавно")
	ErrNoStore   = errors.New("accounts: стор не создан")
)

// Account — постоянная часть игрока. Чего здесь намеренно нет: e-mail, токенов провайдера
// (access_token живёт в памяти до запроса профиля и выбрасывается) и IP — адрес относится
// к соединению, а не к аккаунту, и его хранит moderation.
type Account struct {
	ID       string `json:"id"`       // "a" + hex, виден клиенту
	Provider string `json:"provider"` // ProviderYandex
	Subject  string `json:"sub"`      // идентификатор пользователя у провайдера
	Login    string `json:"login,omitempty"`

	Nick string `json:"nick"`
	// NickAuto — ник выдан сервером, потому что имя из профиля не подошло или было занято.
	// По нему клиент сразу предлагает выбрать ник: иначе игрок узнаёт об «Игрок 4821» в бою.
	NickAuto bool `json:"nickAuto,omitempty"`

	Rank     string   `json:"rank,omitempty"`
	Tutorial []string `json:"tut,omitempty"`

	// Epoch растёт при «выйти на всех устройствах»: куки подписаны вместе с ним, поэтому
	// инкремент разом обесценивает все выданные. Список устройств для этого не нужен.
	Epoch int `json:"epoch"`

	CreatedAt time.Time `json:"createdAt"`
	// NickAt — когда ник менялся в последний раз. Нулевой у аккаунта, который ещё ни разу не
	// переименовывался: первая смена бесплатна (см. Ensure).
	NickAt time.Time `json:"nickAt,omitempty"`
	SeenAt time.Time `json:"seenAt"`

	BanReason string     `json:"banReason,omitempty"`
	BannedAt  *time.Time `json:"bannedAt,omitempty"`
}

// Banned — заблокирован ли аккаунт.
func (a Account) Banned() bool { return a.BannedAt != nil }

type file struct {
	Version  int       `json:"version"`
	Accounts []Account `json:"accounts"`
}

// Store — аккаунты в памяти с фоновой записью на диск. Все методы безопасны на nil-приёмнике:
// сервер обязан работать и без аккаунтов (разработка, отключённый вход), как он работает без
// стора модерации.
type Store struct {
	mu      sync.RWMutex
	path    string // "" — только память
	byID    map[string]*Account
	bySub   map[string]string // "provider:subject" → id
	byNick  map[string]string // protocol.NickKey(nick) → id
	dirty   bool
	broken  bool
	log     zerolog.Logger
	stopCh  chan struct{}
	stopOne sync.Once
	wg      sync.WaitGroup
}

// Open читает файл аккаунтов. Файла нет — пустой стор. Файл битый — он откладывается рядом с
// суффиксом .bad, а сервер поднимается с пустым списком: уронить игру из-за испорченного файла
// хуже, чем потерять аккаунты, но это должно быть видно (ERROR в лог, Broken для админки).
func Open(path string, log zerolog.Logger) (*Store, error) {
	s := &Store{
		path: path, byID: map[string]*Account{}, bySub: map[string]string{},
		byNick: map[string]string{}, log: log, stopCh: make(chan struct{}),
	}
	if path == "" {
		log.Info().Msg("accounts: файл не задан, аккаунты живут только в памяти")
		return s, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // путь задаёт администратор через конфиг
	if errors.Is(err, fs.ErrNotExist) {
		log.Info().Str("path", path).Msg("accounts: файла нет, начинаем с пустого списка")
		return s, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "accounts: чтение файла")
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		s.broken = true
		bad := path + ".bad"
		if rerr := os.Rename(path, bad); rerr != nil {
			log.Error().Err(rerr).Str("path", path).Msg("accounts: файл битый и не переименовывается")
		}
		log.Error().Err(err).Str("moved", bad).Msg("accounts: файл битый, аккаунты сброшены")
		return s, nil
	}
	for i := range f.Accounts {
		a := f.Accounts[i]
		if a.ID == "" || a.Subject == "" {
			continue
		}
		s.put(&a)
	}
	log.Info().Int("accounts", len(s.byID)).Str("path", path).Msg("accounts: список загружен")
	return s, nil
}

// put кладёт аккаунт в карты и индексы. Вызывать под s.mu (или до выдачи стора наружу).
func (s *Store) put(a *Account) {
	s.byID[a.ID] = a
	s.bySub[subKey(a.Provider, a.Subject)] = a.ID
	if a.Nick != "" {
		s.byNick[protocol.NickKey(a.Nick)] = a.ID
	}
}

func subKey(provider, sub string) string { return provider + ":" + sub }

// Get возвращает копию аккаунта: наружу указатели не отдаём, иначе его правили бы без мьютекса.
func (s *Store) Get(id string) (Account, bool) {
	if s == nil || id == "" {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byID[id]
	if !ok {
		return Account{}, false
	}
	return *a, true
}

// BySubject ищет аккаунт по идентификатору у провайдера.
func (s *Store) BySubject(provider, sub string) (Account, bool) {
	if s == nil {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySub[subKey(provider, sub)]
	if !ok {
		return Account{}, false
	}
	return *s.byID[id], true
}

// ByNickKey ищет аккаунт по нормализованному нику (protocol.NickKey). Так хаб узнаёт, что имя
// закреплено за аккаунтом и гостю его выдавать нельзя.
func (s *Store) ByNickKey(key string) (Account, bool) {
	if s == nil || key == "" {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byNick[key]
	if !ok {
		return Account{}, false
	}
	return *s.byID[id], true
}

// Ensure находит аккаунт по провайдеру или заводит новый. Второе значение — «создан впервые».
//
// taken сообщает, занят ли ник чем-то ВНЕ стора (живой бронью гостя); может быть nil. Ник из
// профиля берётся, только если он проходит обычную проверку ника и свободен, иначе выдаётся
// «Игрок NNNN» с отметкой NickAuto: вход не должен падать из-за чужого или матерного имени.
func (s *Store) Ensure(provider, sub, login, suggestNick string, taken func(key string) bool, now time.Time) (Account, bool, error) {
	if s == nil {
		return Account{}, false, ErrNoStore
	}
	if provider == "" || sub == "" {
		return Account{}, false, errors.New("accounts: пустой провайдер или subject")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.bySub[subKey(provider, sub)]; ok {
		a := s.byID[id]
		if login != "" && a.Login != login {
			a.Login = login
			s.dirty = true
		}
		a.SeenAt = now
		s.dirty = true
		return *a, false, nil
	}
	nick, auto := s.pickNick(suggestNick, taken)
	if nick == "" {
		return Account{}, false, errors.New("accounts: не удалось подобрать свободный ник")
	}
	// NickAt намеренно нулевой: первая смена ника после входа бесплатна. Иначе игрок, которому
	// сервер только что выдал «Игрок 4821», десять минут не мог бы назваться по-человечески.
	a := &Account{
		ID: newID(), Provider: provider, Subject: sub, Login: login,
		Nick: nick, NickAuto: auto, CreatedAt: now, SeenAt: now,
	}
	s.put(a)
	s.dirty = true
	return *a, true, nil
}

// pickNick подбирает ник новому аккаунту. Вызывать под s.mu.
func (s *Store) pickNick(suggest string, taken func(string) bool) (string, bool) {
	if nick, err := protocol.NormalizeNick(suggest); err == nil && s.nickFree(protocol.NickKey(nick), "", taken) {
		return nick, false
	}
	// Имя из профиля не подошло — выдаём своё, тем же генератором, что и гостям: «Ловкий
	// Пингвин» выглядит именем, а не заглушкой. Если не повезло — «Игрок NNNN», у него
	// пространство больше.
	for i := 0; i < 30; i++ {
		nick := protocol.RandomNick()
		if s.nickFree(protocol.NickKey(nick), "", taken) {
			return nick, true
		}
	}
	for i := 0; i < 30; i++ {
		nick := protocol.FallbackNick()
		if s.nickFree(protocol.NickKey(nick), "", taken) {
			return nick, true
		}
	}
	return "", false
}

// nickFree — свободен ли ключ ника для аккаунта self (пустой self — для нового). Вызывать под s.mu.
func (s *Store) nickFree(key, self string, taken func(string) bool) bool {
	if id, ok := s.byNick[key]; ok && id != self {
		return false
	}
	if taken != nil && taken(key) {
		return false
	}
	return true
}

// Rename меняет ник аккаунта. Ник уже должен быть проверен protocol.NormalizeNick вызывающим —
// здесь проверяется только занятость и кулдаун.
func (s *Store) Rename(id, nick string, taken func(key string) bool, now time.Time) error {
	if s == nil {
		return ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	key := protocol.NickKey(nick)
	if key == protocol.NickKey(a.Nick) {
		// Тот же ник в другом написании: кулдаун не тратим, бронь не трогаем.
		a.Nick, a.NickAuto = nick, false
		s.dirty = true
		return nil
	}
	// Нулевой NickAt бывает у файла, написанного руками: кулдаун к нему не применяем.
	if !a.NickAt.IsZero() && now.Sub(a.NickAt) < RenameCooldown {
		return ErrTooSoon
	}
	if !s.nickFree(key, id, taken) {
		return ErrNickTaken
	}
	delete(s.byNick, protocol.NickKey(a.Nick))
	a.Nick, a.NickAuto, a.NickAt = nick, false, now
	s.byNick[key] = id
	s.dirty = true
	return nil
}

// AddTutorial отмечает пройденные уроки. Возвращает true, если список действительно вырос:
// объединение множеств конфликтов не даёт, поэтому слияние с localStorage безопасно в любую
// сторону. Зовётся из-под h.mu — на диск не ходит.
func (s *Store) AddTutorial(id string, lessons ...string) bool {
	if s == nil || len(lessons) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return false
	}
	have := make(map[string]bool, len(a.Tutorial))
	for _, l := range a.Tutorial {
		have[l] = true
	}
	changed := false
	for _, l := range lessons {
		if l == "" || have[l] {
			continue
		}
		have[l] = true
		a.Tutorial = append(a.Tutorial, l)
		changed = true
	}
	if changed {
		sort.Strings(a.Tutorial) // порядок не важен игре, но важен диффу файла и тестам
		s.dirty = true
	}
	return changed
}

// SetRank выдаёт роль аккаунту; пустая роль снимает её.
func (s *Store) SetRank(id, rank string) error { return s.edit(id, func(a *Account) { a.Rank = rank }) }

// Ban блокирует аккаунт, Unban снимает блокировку.
func (s *Store) Ban(id, reason string, now time.Time) error {
	return s.edit(id, func(a *Account) { a.BanReason, a.BannedAt = reason, &now })
}

func (s *Store) Unban(id string) error {
	return s.edit(id, func(a *Account) { a.BanReason, a.BannedAt = "", nil })
}

// BumpEpoch обесценивает все выданные куки аккаунта («выйти на всех устройствах»).
func (s *Store) BumpEpoch(id string) error { return s.edit(id, func(a *Account) { a.Epoch++ }) }

// Touch отмечает время последнего входа. Зовётся из-под h.mu на каждом hello, поэтому только
// память: писать файл раз в подключение незачем.
func (s *Store) Touch(id string, now time.Time) {
	_ = s.edit(id, func(a *Account) { a.SeenAt = now })
}

func (s *Store) edit(id string, fn func(*Account)) error {
	if s == nil {
		return ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	fn(a)
	s.dirty = true
	return nil
}

// List возвращает копию списка для админки, от новых к старым. Порядок детерминированный:
// сводка админки идёт в SSE-поток и обязана быть побайтово той же при неизменном состоянии.
func (s *Store) List() []Account {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.byID))
	for _, a := range s.byID {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Len — сколько аккаунтов заведено (админке и логам).
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// Broken — файл при старте оказался испорченным (список сброшен).
func (s *Store) Broken() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.broken
}

// Run запускает фоновую запись на диск. Вызывать один раз при старте.
func (s *Store) Run(every time.Duration) {
	if s == nil || s.path == "" {
		return
	}
	if every <= 0 {
		every = FlushEvery
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				if err := s.Flush(); err != nil {
					s.log.Error().Err(err).Msg("accounts: не удалось записать файл")
				}
			}
		}
	}()
}

// Close останавливает фоновую горутину и дописывает последние изменения.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.stopOne.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	return s.Flush()
}

// Flush пишет файл, если с прошлого раза что-то менялось. Зовётся фоновой горутиной, Close и
// теми обработчиками вне h.mu, которым нельзя терять событие (вход, действия админки).
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty || s.path == "" {
		return nil
	}
	if err := s.save(); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// save пишет файл целиком: временный файл в том же каталоге плюс rename — чтобы обрыв записи
// не оставил половину списка. Вызывать под s.mu.
func (s *Store) save() error {
	f := file{Version: fileVersion}
	for _, a := range s.byID {
		f.Accounts = append(f.Accounts, *a)
	}
	sort.Slice(f.Accounts, func(i, j int) bool { return f.Accounts[i].ID < f.Accounts[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil { // в каталоге лежат имена и связи с Яндексом
		return err
	}
	tmp, err := os.CreateTemp(dir, "accounts-*.json") // тот же каталог: rename через границу ФС не работает
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
	if err := os.Chmod(name, 0o600); err != nil {
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

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "a" + hex.EncodeToString(b)
}
