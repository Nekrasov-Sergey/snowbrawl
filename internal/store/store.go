// Package store — единственная база сервера: файл SQLite, в котором лежит всё, что переживает
// перезапуск, — записи игроков (internal/accounts), ряд онлайна (internal/onlinestat) и ключ
// подписи кук (internal/auth). Пакет знает про файл, соединения, схему, бэкапы и импорт старых
// JSON-файлов; про смысл таблиц знают пакеты-владельцы.
//
// Соединений два пула, и это главное, что нужно помнить при правке. Писатель у SQLite один,
// поэтому пул записи держит ровно одно соединение (W). Читатели в режиме WAL писателя не ждут,
// поэтому у чтения свой пул (R). Отсюда правило для хаба: читать под h.mu можно — чтение не
// встанет за чужой записью и её fsync; писать под h.mu нельзя — запись ждёт единственное
// соединение, а контрольная точка WAL делает fsync прямо в коммите.
//
// Драйвер — modernc.org/sqlite на чистом Go: образ собирается с CGO_ENABLED=0 на distroless,
// и драйвер на cgo там не соберётся.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// readConns — сколько соединений у пула чтения. Читают hello, админка и вход; больше четырёх
// одновременно им не нужно, а каждое соединение — это свой кэш страниц.
const readConns = 4

// DB — открытая база. Методы безопасны на nil-приёмнике там, где это имеет смысл (Broken,
// Close): сервер без базы не стартует, но тестам удобно не таскать её туда, где она не нужна.
type DB struct {
	// W — пул записи, ровно одно соединение. Транзакции на нём BEGIN IMMEDIATE: блокировка на
	// запись берётся сразу, и две транзакции не упираются друг в друга посреди работы.
	W *sql.DB
	// R — пул чтения, только запросы (query_only). Писать через него нельзя, и драйвер
	// это проверит.
	R *sql.DB

	path   string
	tmpDir string // непустой — база во временном каталоге, удаляется в Close
	broken bool
	log    zerolog.Logger

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// Open открывает (или создаёт) базу по пути. Пустой путь — база во временном каталоге, который
// удалится при закрытии: так живут тесты и запуск без тома, и ведёт себя такая база ровно как
// боевая, с WAL и двумя пулами (у SQLite в памяти нет WAL, и поведение пулов было бы другим).
//
// Битый файл не роняет сервер: он откладывается рядом с суффиксом .bad, и база создаётся
// заново. Это видно — ERROR в логе и Broken() для админки. Уронить игру из-за испорченного
// файла хуже, чем начать с пустыми записями.
func Open(path string, log zerolog.Logger) (*DB, error) {
	d := &DB{path: path, log: log, stopCh: make(chan struct{})}
	if path == "" {
		dir, err := os.MkdirTemp("", "snowbrawl-db-*")
		if err != nil {
			return nil, fmt.Errorf("store: временный каталог: %w", err)
		}
		d.tmpDir, d.path = dir, filepath.Join(dir, "snowbrawl.db")
		log.Info().Msg("store: путь к базе не задан, база временная и пропадёт при перезапуске")
	}
	if err := d.open(); err != nil {
		if !isCorrupt(err) {
			d.cleanupTmp()
			return nil, err
		}
		d.setAside(err)
		if err := d.open(); err != nil {
			d.cleanupTmp()
			return nil, err
		}
	}
	log.Info().Str("path", d.path).Int("schema", len(migrations())).Msg("store: база открыта")
	return d, nil
}

// open открывает пулы, проверяет файл и накатывает миграции.
func (d *DB) open() error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil { // в базе ники и связи с Яндексом
		return fmt.Errorf("store: каталог базы: %w", err)
	}
	// Файл создаём сами и с правами 0600: SQLite создал бы его по umask, а -wal и -shm
	// наследуют права основного файла.
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // путь из конфига
	if err != nil {
		return fmt.Errorf("store: файл базы: %w", err)
	}
	_ = f.Close()

	base := "file:" + d.path + "?_busy_timeout=5000&_foreign_keys=1&_journal_mode=WAL&_synchronous=NORMAL"
	w, err := sql.Open("sqlite", base+"&_txlock=immediate")
	if err != nil {
		return fmt.Errorf("store: открытие базы: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxIdleTime(0)
	if err := check(w); err != nil {
		_ = w.Close()
		return err
	}
	if err := migrate(w); err != nil {
		_ = w.Close()
		return err
	}
	r, err := sql.Open("sqlite", base+"&_query_only=1")
	if err != nil {
		_ = w.Close()
		return fmt.Errorf("store: пул чтения: %w", err)
	}
	r.SetMaxOpenConns(readConns)
	r.SetMaxIdleConns(readConns)
	d.W, d.R = w, r
	return nil
}

// check — быстрая проверка целостности. На битом файле SQLite открывается молча и падает
// только на первом запросе, поэтому спрашиваем сразу.
func check(db *sql.DB) error {
	var res string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("store: проверка файла: %w", err)
	}
	if res != "ok" {
		return corruptError{res}
	}
	return nil
}

type corruptError struct{ detail string }

func (e corruptError) Error() string { return "store: файл повреждён: " + e.detail }

// isCorrupt — ошибка говорит о битом файле, а не о правах или диске.
func isCorrupt(err error) bool {
	var ce corruptError
	if errors.As(err, &ce) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return true
		}
	}
	return false
}

// setAside откладывает битый файл вместе с -wal и -shm: чужой WAL к новой базе прикладывать нельзя.
func (d *DB) setAside(cause error) {
	d.broken = true
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := d.path + suffix
		if err := os.Rename(p, p+".bad"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			d.log.Error().Err(err).Str("path", p).Msg("store: битый файл не переименовывается")
		}
	}
	d.log.Error().Err(cause).Str("moved", d.path+".bad").Msg("store: база повреждена, начинаем с пустой")
}

// migration — шаг схемы: номер из имени файла и текст.
type migration struct {
	version int
	sql     string
}

// migrations читает встроенные миграции по порядку. Номер шага — префикс имени файла
// («001_init.sql»), текущая версия схемы лежит в PRAGMA user_version.
func migrations() []migration {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		panic(err) // встроено при сборке: без каталога бинарник не собрался бы
	}
	var out []migration
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			panic("store: миграция без номера: " + e.Name())
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			panic(err)
		}
		out = append(out, migration{version: v, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			panic(fmt.Sprintf("store: миграции должны идти подряд с 1, а %d-я имеет номер %d", i+1, m.version))
		}
	}
	return out
}

// migrate накатывает недостающие шаги, каждый в своей транзакции вместе с новой версией:
// оборванный шаг не оставит схему наполовину.
func migrate(db *sql.DB) error {
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return fmt.Errorf("store: версия схемы: %w", err)
	}
	all := migrations()
	if have > len(all) {
		// База от более новой сборки: откат бинарника не должен молча работать со схемой,
		// которую он не понимает.
		return fmt.Errorf("store: схема базы %d новее этой сборки (%d)", have, len(all))
	}
	for _, m := range all[have:] {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: миграция %d: %w", m.version, err)
		}
		if _, err := tx.Exec("PRAGMA user_version = " + strconv.Itoa(m.version)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Path — где лежит файл базы.
func (d *DB) Path() string { return d.path }

// Broken — при старте файл оказался битым и отложен в .bad.
func (d *DB) Broken() bool { return d != nil && d.broken }

// Meta читает значение из служебной таблицы. Второе значение — нашлось ли.
func (d *DB) Meta(key string) (string, bool, error) {
	var v string
	err := d.R.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta пишет значение в служебную таблицу.
func (d *DB) SetMeta(key, value string) error {
	return SetMetaTx(d.W, key, value)
}

// Execer — то общее, что есть у *sql.DB и *sql.Tx: импорт пишет в чужую транзакцию.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// SetMetaTx — SetMeta внутри транзакции вызывающего.
func SetMetaTx(e Execer, key, value string) error {
	_, err := e.Exec("INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// IsUnique — нарушено ограничение уникальности (UNIQUE или PRIMARY KEY). Второе значение —
// что именно: "players.id", "players.nick_key" и т.п. из текста ошибки SQLite.
func IsUnique(err error) (bool, string) {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false, ""
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
	default:
		return false, ""
	}
	// Текст вида «constraint failed: UNIQUE constraint failed: players.nick_key (2067)».
	msg := se.Error()
	if i := strings.LastIndex(msg, "failed: "); i >= 0 {
		what := msg[i+len("failed: "):]
		if j := strings.IndexByte(what, ' '); j >= 0 {
			what = what[:j]
		}
		return true, what
	}
	return true, ""
}

// RunDaily запускает задачи обслуживания: сразу при старте и потом раз в период. Задачи идут
// в своей горутине и никогда не под мьютексом хаба. Останавливается в Close.
func (d *DB) RunDaily(every time.Duration, tasks ...func(now time.Time)) {
	if every <= 0 {
		every = 24 * time.Hour
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		run := func() {
			now := time.Now()
			for _, task := range tasks {
				task(now)
			}
		}
		run()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-d.stopCh:
				return
			case <-t.C:
				run()
			}
		}
	}()
}

// Close останавливает обслуживание и закрывает пулы. Временная база удаляется вместе с каталогом.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
	d.wg.Wait()
	var err error
	if d.R != nil {
		err = d.R.Close()
	}
	if d.W != nil {
		// Контрольная точка на прощание: база на диске становится одним файлом без -wal, и её
		// можно копировать как есть.
		_, _ = d.W.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)")
		if cerr := d.W.Close(); err == nil {
			err = cerr
		}
	}
	d.cleanupTmp()
	return err
}

func (d *DB) cleanupTmp() {
	if d.tmpDir != "" {
		_ = os.RemoveAll(d.tmpDir)
	}
}
