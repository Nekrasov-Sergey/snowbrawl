// Package accounts хранит постоянные записи игроков: ник, прогресс обучения, роль, бан. Запись
// есть и у гостя, и у вошедшего через Яндекс: гость — запись без провайдера, а вход превращает
// её в аккаунт или сливает с уже существующим (Link). Всё остальное про игрока — место в
// комнате и матче, чат, задержка — живёт в памяти (internal/session).
//
// Гостя узнают по подписанной куке, а не по IP (см. internal/auth): у мобильного игрока адрес
// меняется между загрузками страницы, а кука остаётся той же.
//
// Записи лежат в SQLite (internal/store), и база — источник правды: кэша в памяти нет, хаб
// читает запись при подключении и держит нужное в сессии. Из этого два правила для вызывающего.
// Читать (Get, ByNickKey) можно откуда угодно, в том числе под мьютексом хаба: чтение идёт через
// свой пул и писателя не ждёт. Писать (всё остальное) — только вне h.mu: запись ждёт единственное
// соединение писателя, и контрольная точка WAL делает fsync прямо в коммите.
//
// Колбэк taken в Link/EnsureGuest/Rename сообщает про ники, занятые ВНЕ базы (живые брони
// гостей без куки); он обязан быть быстрым и сам брать мьютекс хаба, если нужно.
package accounts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

// Провайдеры входа. Пустой провайдер — гость.
const (
	ProviderGuest  = ""
	ProviderYandex = "yandex"
)

// RenameCooldown — как часто можно менять ник. Без паузы смена ника превращается в способ
// перебирать чужие ники и мельтешить в чате под разными именами.
const RenameCooldown = 10 * time.Minute

// GuestTTL — сколько живёт запись гостя без заходов. Потом она удаляется и ник освобождается;
// роль или бан держат запись. Кука гостя живёт столько же (см. internal/auth).
const GuestTTL = 90 * 24 * time.Hour

// touchEvery — не чаще этого пишем время последнего захода: запись на каждое переподключение
// ничего не даёт уборке гостей, у которой срок — месяцы.
const touchEvery = time.Minute

// Ошибки, которые вызывающий обязан различать: каждой соответствует свой текст игроку.
var (
	ErrNotFound  = errors.New("accounts: запись не найдена")
	ErrNickTaken = errors.New("accounts: ник занят")
	ErrTooSoon   = errors.New("accounts: ник менялся недавно")
	ErrNoStore   = errors.New("accounts: стор не создан")
)

// Account — постоянная часть игрока. Чего здесь намеренно нет: e-mail, токенов провайдера
// (access_token живёт в памяти до запроса профиля и выбрасывается) и IP — адрес относится к
// соединению, а не к игроку.
type Account struct {
	ID       string `json:"id"`       // "a" + hex, виден клиенту
	Provider string `json:"provider"` // ProviderGuest или ProviderYandex
	Subject  string `json:"sub,omitempty"`
	Login    string `json:"login,omitempty"`

	Nick string `json:"nick"`
	// NickAuto — ник выдан сервером. У аккаунта Яндекса это значит «имя из профиля не подошло
	// или было занято», и клиент сразу предлагает выбрать ник.
	NickAuto bool `json:"nickAuto,omitempty"`

	Rank     string   `json:"rank,omitempty"`
	Tutorial []string `json:"tut,omitempty"`

	// Epoch растёт при «выйти на всех устройствах»: куки подписаны вместе с ним, поэтому
	// инкремент разом обесценивает все выданные. Список устройств для этого не нужен.
	Epoch int `json:"epoch"`

	CreatedAt time.Time `json:"createdAt"`
	// NickAt — когда ник менялся в последний раз. Нулевой у записи, которая ещё ни разу не
	// переименовывалась: первая смена бесплатна.
	NickAt time.Time `json:"nickAt,omitempty"`
	SeenAt time.Time `json:"seenAt"`

	BanReason string     `json:"banReason,omitempty"`
	BannedAt  *time.Time `json:"bannedAt,omitempty"`
}

// Banned — заблокирован ли игрок.
func (a Account) Banned() bool { return a.BannedAt != nil }

// Guest — запись гостя, без входа через провайдера.
func (a Account) Guest() bool { return a.Provider == ProviderGuest }

// Store — записи игроков поверх базы. Все методы безопасны на nil-приёмнике: хаб и админка
// работают и без стора (часть тестов), как раньше работали без аккаунтов.
type Store struct {
	db  *store.DB
	log zerolog.Logger

	touchMu sync.Mutex
	touched map[string]time.Time // id → когда в последний раз писали seen_at
}

// Open создаёт стор поверх открытой базы.
func Open(db *store.DB, log zerolog.Logger) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db, log: log, touched: map[string]time.Time{}}
}

// NewID — новый идентификатор записи. Гостевой куке id выдаётся раньше, чем появляется запись
// (см. internal/auth), поэтому генератор открыт наружу.
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "a" + hex.EncodeToString(b)
}

const cols = `id, provider, subject, login, nick, nick_auto, rank, epoch, created_at, nick_at, seen_at, ban_reason, banned_at`

// querier — общее у *sql.DB и *sql.Tx для чтения.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

type scanner interface{ Scan(dest ...any) error }

func scanAccount(r scanner) (Account, error) {
	var (
		a                Account
		auto             int
		created, seen    int64
		nickAt, bannedAt sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.Provider, &a.Subject, &a.Login, &a.Nick, &auto, &a.Rank, &a.Epoch,
		&created, &nickAt, &seen, &a.BanReason, &bannedAt); err != nil {
		return Account{}, err
	}
	a.NickAuto = auto != 0
	a.CreatedAt, a.SeenAt = fromMs(created), fromMs(seen)
	if nickAt.Valid {
		a.NickAt = fromMs(nickAt.Int64)
	}
	if bannedAt.Valid {
		t := fromMs(bannedAt.Int64)
		a.BannedAt = &t
	}
	return a, nil
}

func fromMs(ms int64) time.Time { return time.UnixMilli(ms) }

func ms(t time.Time) int64 { return t.UnixMilli() }

func nullMs(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// getBy читает одну запись по условию и её уроки.
func getBy(q querier, where string, args ...any) (Account, bool, error) {
	a, err := scanAccount(q.QueryRow("SELECT "+cols+" FROM players WHERE "+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	lessons, err := lessonsOf(q, a.ID)
	if err != nil {
		return Account{}, false, err
	}
	a.Tutorial = lessons
	return a, true, nil
}

func lessonsOf(q querier, id string) ([]string, error) {
	rows, err := q.Query("SELECT lesson FROM player_lessons WHERE player_id = ? ORDER BY lesson", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// read — чтение с логом ошибки: вызывающему (хабу под мьютексом) ошибка базы не нужна, ему
// нужно «нашлось или нет», а сбой должен быть виден в логе.
func (s *Store) read(where string, args ...any) (Account, bool) {
	a, ok, err := getBy(s.db.R, where, args...)
	if err != nil {
		s.log.Error().Err(err).Msg("accounts: чтение записи")
		return Account{}, false
	}
	return a, ok
}

// Get возвращает запись по id.
func (s *Store) Get(id string) (Account, bool) {
	if s == nil || id == "" {
		return Account{}, false
	}
	return s.read("id = ?", id)
}

// BySubject ищет аккаунт по идентификатору у провайдера.
func (s *Store) BySubject(provider, sub string) (Account, bool) {
	if s == nil || provider == ProviderGuest || sub == "" {
		return Account{}, false
	}
	return s.read("provider = ? AND subject = ?", provider, sub)
}

// ByNickKey ищет запись по нормализованному нику (protocol.NickKey). Так хаб узнаёт, что имя
// закреплено за игроком и выдавать его другому нельзя.
func (s *Store) ByNickKey(key string) (Account, bool) {
	if s == nil || key == "" {
		return Account{}, false
	}
	return s.read("nick_key = ?", key)
}

// nickFree — свободен ли ник для записи self (пустой self — для новой).
func nickFree(q querier, key, self string, taken func(string) bool) (bool, error) {
	var id string
	err := q.QueryRow("SELECT id FROM players WHERE nick_key = ?", key).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, err
	case id != self:
		return false, nil
	}
	if taken != nil && taken(key) {
		return false, nil
	}
	return true, nil
}

// pickNick подбирает ник новой записи: предложенный, если он годится и свободен, иначе свой —
// тем же генератором, что и гостям («Ловкий Пингвин» выглядит именем, а не заглушкой), и только
// если не повезло — «Игрок NNNN», у него пространство больше.
func pickNick(q querier, suggest string, taken func(string) bool) (string, bool, error) {
	if nick, err := protocol.NormalizeNick(suggest); err == nil {
		free, err := nickFree(q, protocol.NickKey(nick), "", taken)
		if err != nil {
			return "", false, err
		}
		if free {
			return nick, false, nil
		}
	}
	for _, gen := range []func() string{protocol.RandomNick, protocol.FallbackNick} {
		for i := 0; i < 30; i++ {
			nick := gen()
			free, err := nickFree(q, protocol.NickKey(nick), "", taken)
			if err != nil {
				return "", false, err
			}
			if free {
				return nick, true, nil
			}
		}
	}
	return "", false, errors.New("accounts: не удалось подобрать свободный ник")
}

func insert(e store.Execer, a Account) error {
	_, err := e.Exec("INSERT INTO players("+cols+", nick_key) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		a.ID, a.Provider, a.Subject, a.Login, a.Nick, boolInt(a.NickAuto), a.Rank, a.Epoch,
		ms(a.CreatedAt), nullMs(a.NickAt), ms(a.SeenAt), a.BanReason, nil, protocol.NickKey(a.Nick))
	return err
}

// EnsureGuest заводит запись гостя с этим id или возвращает уже существующую (второй вкладке,
// опередившей первую). Ник уже проверен вызывающим (protocol.NormalizeNick); занятый ник —
// ErrNickTaken. auto — ник выдал сервер, а не игрок.
func (s *Store) EnsureGuest(id, nick string, auto bool, taken func(key string) bool, now time.Time) (Account, error) {
	if s == nil {
		return Account{}, ErrNoStore
	}
	if a, ok := s.Get(id); ok {
		return a, nil
	}
	key := protocol.NickKey(nick)
	if taken != nil && taken(key) {
		return Account{}, ErrNickTaken
	}
	a := Account{ID: id, Nick: nick, NickAuto: auto, CreatedAt: now, SeenAt: now}
	err := insert(s.db.W, a)
	if uniq, what := store.IsUnique(err); uniq {
		if what == "players.id" {
			// Вторая вкладка того же браузера успела первой: берём её запись.
			if a, ok := s.Get(id); ok {
				return a, nil
			}
		}
		return Account{}, ErrNickTaken
	}
	if err != nil {
		return Account{}, err
	}
	s.markTouched(id, now)
	return a, nil
}

// Ensure находит аккаунт провайдера или заводит новый. Второе значение — «создан впервые».
// Это Link без гостя: так входят из браузера без гостевой куки и через /auth/dev/login.
func (s *Store) Ensure(provider, sub, login, suggestNick string, taken func(key string) bool, now time.Time) (Account, bool, error) {
	return s.Link("", provider, sub, login, suggestNick, taken, now)
}

// Link — вход через провайдера из браузера, где уже играл гость guestID (может быть пустым).
//
//   - Аккаунт провайдера уже есть: прогресс обучения гостя объединяется с ним, ник остаётся от
//     аккаунта, запись гостя удаляется и освобождает свой ник. Бан гостя переезжает в аккаунт
//     (иначе от бана спасал бы вход), роль — если у аккаунта её нет.
//   - Аккаунта нет, а гость есть: гость становится аккаунтом с тем же id, ником, обучением и
//     ролью. Куки, выданные гостю, продолжают работать.
//   - Нет ни того, ни другого: новый аккаунт с ником из профиля (suggestNick).
//
// Всё в одной транзакции: обрыв посередине не оставит ни двух записей, ни потерянного прогресса.
func (s *Store) Link(guestID, provider, sub, login, suggestNick string, taken func(key string) bool, now time.Time) (Account, bool, error) {
	if s == nil {
		return Account{}, false, ErrNoStore
	}
	if provider == ProviderGuest || sub == "" {
		return Account{}, false, errors.New("accounts: пустой провайдер или subject")
	}
	tx, err := s.db.W.Begin()
	if err != nil {
		return Account{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var guest *Account
	if guestID != "" {
		g, ok, err := getBy(tx, "id = ?", guestID)
		if err != nil {
			return Account{}, false, err
		}
		if ok && g.Guest() {
			guest = &g
		}
	}
	existing, found, err := getBy(tx, "provider = ? AND subject = ?", provider, sub)
	if err != nil {
		return Account{}, false, err
	}
	created := false
	var id string
	switch {
	case found:
		id = existing.ID
		if guest != nil {
			if err := mergeGuest(tx, *guest, existing); err != nil {
				return Account{}, false, err
			}
		}
		if _, err := tx.Exec("UPDATE players SET login = ?, seen_at = ? WHERE id = ?", login, ms(now), id); err != nil {
			return Account{}, false, err
		}
	case guest != nil:
		// Ник гостя игрок выбрал сам или уже привык к выданному, поэтому он остаётся. Отметку
		// «ник выдал сервер» снимаем: у аккаунта она значит «имя из Яндекса не подошло», и
		// клиент сразу потащил бы игрока на экран ника.
		id, created = guest.ID, true
		if _, err := tx.Exec(`UPDATE players SET provider = ?, subject = ?, login = ?, nick_auto = 0, seen_at = ?
			WHERE id = ?`, provider, sub, login, ms(now), id); err != nil {
			return Account{}, false, err
		}
	default:
		nick, auto, err := pickNick(tx, suggestNick, taken)
		if err != nil {
			return Account{}, false, err
		}
		// NickAt намеренно нулевой: первая смена ника после входа бесплатна. Иначе игрок,
		// которому сервер только что выдал «Игрок 4821», десять минут не мог бы назваться
		// по-человечески.
		id, created = NewID(), true
		a := Account{ID: id, Provider: provider, Subject: sub, Login: login, Nick: nick, NickAuto: auto,
			CreatedAt: now, SeenAt: now}
		if err := insert(tx, a); err != nil {
			if uniq, _ := store.IsUnique(err); uniq {
				return Account{}, false, ErrNickTaken
			}
			return Account{}, false, err
		}
	}
	a, _, err := getBy(tx, "id = ?", id)
	if err != nil {
		return Account{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, false, err
	}
	s.markTouched(id, now)
	return a, created, nil
}

// mergeGuest переносит гостя в существующий аккаунт и удаляет его запись.
func mergeGuest(tx *sql.Tx, guest, acc Account) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO player_lessons(player_id, lesson)
		SELECT ?, lesson FROM player_lessons WHERE player_id = ?`, acc.ID, guest.ID); err != nil {
		return err
	}
	if guest.Banned() && !acc.Banned() {
		if _, err := tx.Exec("UPDATE players SET ban_reason = ?, banned_at = ? WHERE id = ?",
			guest.BanReason, ms(*guest.BannedAt), acc.ID); err != nil {
			return err
		}
	}
	if guest.Rank != "" && acc.Rank == "" {
		if _, err := tx.Exec("UPDATE players SET rank = ? WHERE id = ?", guest.Rank, acc.ID); err != nil {
			return err
		}
	}
	_, err := tx.Exec("DELETE FROM players WHERE id = ?", guest.ID)
	return err
}

// Rename меняет ник записи. Ник уже должен быть проверен protocol.NormalizeNick вызывающим —
// здесь проверяются только занятость и кулдаун.
func (s *Store) Rename(id, nick string, taken func(key string) bool, now time.Time) error {
	if s == nil {
		return ErrNoStore
	}
	tx, err := s.db.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, ok, err := getBy(tx, "id = ?", id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	key := protocol.NickKey(nick)
	if key == protocol.NickKey(a.Nick) {
		// Тот же ник в другом написании: кулдаун не тратим.
		if _, err := tx.Exec("UPDATE players SET nick = ?, nick_auto = 0 WHERE id = ?", nick, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !a.NickAt.IsZero() && now.Sub(a.NickAt) < RenameCooldown {
		return ErrTooSoon
	}
	free, err := nickFree(tx, key, id, taken)
	if err != nil {
		return err
	}
	if !free {
		return ErrNickTaken
	}
	_, err = tx.Exec("UPDATE players SET nick = ?, nick_key = ?, nick_auto = 0, nick_at = ? WHERE id = ?",
		nick, key, ms(now), id)
	if uniq, _ := store.IsUnique(err); uniq {
		return ErrNickTaken
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// AddTutorial отмечает пройденные уроки. Возвращает true, если список действительно вырос:
// объединение множеств конфликтов не даёт, поэтому слияние с localStorage безопасно в любую
// сторону.
func (s *Store) AddTutorial(id string, lessons ...string) bool {
	if s == nil || id == "" || len(lessons) == 0 {
		return false
	}
	tx, err := s.db.W.Begin()
	if err != nil {
		s.log.Error().Err(err).Msg("accounts: отметка урока")
		return false
	}
	defer func() { _ = tx.Rollback() }()
	changed := false
	for _, l := range lessons {
		if l == "" {
			continue
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO player_lessons(player_id, lesson)
			SELECT id, ? FROM players WHERE id = ?`, l, id)
		if err != nil {
			s.log.Error().Err(err).Msg("accounts: отметка урока")
			return false
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
	}
	if err := tx.Commit(); err != nil {
		s.log.Error().Err(err).Msg("accounts: отметка урока")
		return false
	}
	return changed
}

// SetRank выдаёт роль; пустая роль снимает её.
func (s *Store) SetRank(id, rank string) error {
	return s.update(id, "UPDATE players SET rank = ? WHERE id = ?", rank, id)
}

// Ban блокирует игрока, Unban снимает блокировку.
func (s *Store) Ban(id, reason string, now time.Time) error {
	return s.update(id, "UPDATE players SET ban_reason = ?, banned_at = ? WHERE id = ?", reason, ms(now), id)
}

func (s *Store) Unban(id string) error {
	return s.update(id, "UPDATE players SET ban_reason = '', banned_at = NULL WHERE id = ?", id)
}

// BumpEpoch обесценивает все выданные куки записи («выйти на всех устройствах»).
func (s *Store) BumpEpoch(id string) error {
	return s.update(id, "UPDATE players SET epoch = epoch + 1 WHERE id = ?", id)
}

func (s *Store) update(id, query string, args ...any) error {
	if s == nil {
		return ErrNoStore
	}
	if id == "" {
		return ErrNotFound
	}
	res, err := s.db.W.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Touch отмечает время захода — по нему уборка решает, что гость пропал. Пишет не чаще раза в
// touchEvery на запись.
func (s *Store) Touch(id string, now time.Time) {
	if s == nil || id == "" {
		return
	}
	s.touchMu.Lock()
	last, ok := s.touched[id]
	if ok && now.Sub(last) < touchEvery {
		s.touchMu.Unlock()
		return
	}
	s.touched[id] = now
	if len(s.touched) > 10000 {
		// Отметки нужны только на минуту вперёд: старые — просто мусор.
		for k, t := range s.touched {
			if now.Sub(t) >= touchEvery {
				delete(s.touched, k)
			}
		}
	}
	s.touchMu.Unlock()
	if _, err := s.db.W.Exec("UPDATE players SET seen_at = ? WHERE id = ?", ms(now), id); err != nil {
		s.log.Error().Err(err).Msg("accounts: время захода")
	}
}

func (s *Store) markTouched(id string, now time.Time) {
	s.touchMu.Lock()
	s.touched[id] = now
	s.touchMu.Unlock()
}

// PurgeGuests удаляет гостей, не заходивших с before, — без роли и без бана: такие записи
// держат, пока их не снимут руками. Возвращает, сколько удалено.
func (s *Store) PurgeGuests(before time.Time) (int, error) {
	if s == nil {
		return 0, nil
	}
	res, err := s.db.W.Exec(`DELETE FROM players
		WHERE provider = '' AND rank = '' AND banned_at IS NULL AND seen_at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PurgeTask — задача для store.RunDaily: уборка гостей старше GuestTTL.
func (s *Store) PurgeTask() func(time.Time) {
	return func(now time.Time) {
		n, err := s.PurgeGuests(now.Add(-GuestTTL))
		switch {
		case err != nil:
			s.log.Error().Err(err).Msg("accounts: уборка гостей")
		case n > 0:
			s.log.Info().Int("removed", n).Msg("accounts: удалены давно не заходившие гости")
		}
	}
}

// Фильтры списка игроков в админке.
const (
	FilterAll    = ""
	FilterGuests = "guests"
	FilterYandex = "yandex"
	FilterRanked = "ranked"
	FilterBanned = "banned"
)

// SearchPage — сколько записей на странице списка в админке.
const SearchPage = 50

// SearchOrder — порядок списка игроков в админке: админы, модераторы, остальные; внутри — кто
// заходил позже, тот выше; дальше по нику. Онлайн-игроков админка ставит выше сама: о сессиях
// база не знает.
const SearchOrder = `CASE rank WHEN 'admin' THEN 0 WHEN 'moderator' THEN 1 ELSE 2 END, seen_at DESC, nick_key, id`

// Search — кусок списка игроков для админки: поиск по нику, логину или id (без учёта регистра),
// фильтр, исключённые id (они уже показаны выше как онлайн) и окно offset/limit. Порядок —
// SearchOrder. Возвращает кусок и общее число подходящих записей без исключённых.
func (s *Store) Search(query, filter string, exclude []string, offset, limit int) ([]Account, int, error) {
	if s == nil {
		return nil, 0, ErrNoStore
	}
	var where []string
	var args []any
	if q := strings.TrimSpace(query); q != "" {
		// Ищем по ключу ника: он уже приведён к нижнему регистру и ё→е, так что «ёлка» найдёт
		// «Ёлку». LIKE-символы в запросе экранируем, иначе «_» совпадал бы с чем угодно.
		pat := "%" + likeEscape(protocol.NickKey(q)) + "%"
		where = append(where, `(nick_key LIKE ? ESCAPE '\' OR lower(login) LIKE ? ESCAPE '\' OR id = ?)`)
		args = append(args, pat, "%"+likeEscape(strings.ToLower(q))+"%", q)
	}
	switch filter {
	case FilterGuests:
		where = append(where, "provider = ''")
	case FilterYandex:
		where = append(where, "provider = 'yandex'")
	case FilterRanked:
		where = append(where, "rank <> ''")
	case FilterBanned:
		where = append(where, "banned_at IS NOT NULL")
	}
	if len(exclude) > 0 {
		where = append(where, "id NOT IN (?"+strings.Repeat(", ?", len(exclude)-1)+")")
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.R.QueryRow("SELECT count(*) FROM players"+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		return nil, total, nil
	}
	rows, err := s.db.R.Query("SELECT "+cols+" FROM players"+cond+" ORDER BY "+SearchOrder+" LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		out = append(out, a)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i := range out {
		if out[i].Tutorial, err = lessonsOf(s.db.R, out[i].ID); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// Matches — подходит ли запись под поиск и фильтр ровно так же, как в Search. Нужно админке
// для онлайн-игроков: их она отбирает сама, не спрашивая базу.
func Matches(a Account, query, filter string) bool {
	switch filter {
	case FilterGuests:
		if !a.Guest() {
			return false
		}
	case FilterYandex:
		if a.Provider != ProviderYandex {
			return false
		}
	case FilterRanked:
		if a.Rank == "" {
			return false
		}
	case FilterBanned:
		if !a.Banned() {
			return false
		}
	}
	return MatchesQuery(a.ID, a.Nick, a.Login, query)
}

// MatchesQuery — поиск Search по нику, логину и id без фильтра. Годится и для гостя без записи.
func MatchesQuery(id, nick, login, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return true
	}
	return strings.Contains(protocol.NickKey(nick), protocol.NickKey(q)) ||
		(login != "" && strings.Contains(strings.ToLower(login), strings.ToLower(q))) ||
		(id != "" && id == q)
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Len — сколько записей в базе (админке и логам).
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	var n int
	if err := s.db.R.QueryRow("SELECT count(*) FROM players").Scan(&n); err != nil {
		s.log.Error().Err(err).Msg("accounts: подсчёт записей")
	}
	return n
}

// Broken — файл базы при старте оказался испорченным (записи сброшены).
func (s *Store) Broken() bool { return s != nil && s.db.Broken() }
