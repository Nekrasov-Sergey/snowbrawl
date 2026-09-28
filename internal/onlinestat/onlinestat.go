// Package onlinestat — ряд онлайна для графика в админке: одна точка в минуту, тридцать дней в
// памяти, дозапись в базу (internal/store, таблица online_points).
//
// Инвариант: hub под своим мьютексом только дописывает точку в память (Observe), а в базу
// ходят собственная горутина серии (Run) и Close. Поэтому в Observe не должно появиться ни
// одного обращения к базе и ни одного вызова назад в hub — иначе запись с её fsync встанет
// поперёк всех матчей.
package onlinestat

import (
	"database/sql"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

const (
	// Step — шаг ряда. Точка — максимум онлайна за минуту: пик читается осмысленнее среднего
	// и не зависит от того, в какой момент минуты сработал семпл.
	Step = time.Minute
	// Retention — сколько истории держим. Месяц: график в админке умеет показывать 30 дней.
	Retention = 30 * 24 * time.Hour
	// RetentionPoints — тот же срок в точках.
	RetentionPoints = int(Retention / Step)
	// FlushEvery — как часто сбрасываем накопленное в базу. Цена жёсткой остановки — потеря
	// накопленного с последнего флаша (такие минуты график покажет нулями), поэтому пишем раз
	// в минуту: одна строка в минуту дешевле пятиминутного провала после каждого деплоя.
	FlushEvery = time.Minute
	// trimSlack — на сколько точек кольцу разрешено перерасти ретенцию, прежде чем его
	// подрежут. Режем пачками, потому что подрезка копирует кольцо целиком: на каждой точке
	// это копия всех 43 тысяч (месяц поминутно) — впустую и в сервере, и в тестах.
	trimSlack = 1024
)

// Point — одна точка ряда.
type Point struct {
	At time.Time `json:"-"`
	N  int       `json:"-"`
}

// Series — ряд онлайна. Все методы безопасны на nil-приёмнике: сервер должен работать и без
// базы, и без самой серии.
type Series struct {
	mu   sync.Mutex
	ring []Point // не длиннее RetentionPoints (+ trimSlack), старые точки с головы
	cur  Point   // незакрытая минута
	pend []Point // закрытые минуты, ещё не записанные в базу

	flushMu sync.Mutex // запись в базу никогда не под mu
	db      *store.DB  // nil — только память

	log      zerolog.Logger
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Open читает из базы последние RetentionPoints точек. db может быть nil — тогда ряд только в
// памяти. Старше ретенции в базе ничего не лежит (это подрезает Flush), так что окно по числу
// точек совпадает с окном по времени, а от текущих часов загрузка не зависит.
func Open(db *store.DB, log zerolog.Logger) (*Series, error) {
	s := &Series{db: db, log: log, stopCh: make(chan struct{})}
	if db == nil {
		log.Info().Msg("onlinestat: базы нет, история онлайна живёт только в памяти")
		return s, nil
	}
	rows, err := db.R.Query(`SELECT at, n FROM (SELECT at, n FROM online_points ORDER BY at DESC LIMIT ?)
		ORDER BY at`, RetentionPoints)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var at int64
		var n int
		if err := rows.Scan(&at, &n); err != nil {
			return nil, err
		}
		s.ring = append(s.ring, Point{At: time.Unix(at, 0).UTC(), N: n})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	log.Info().Int("points", len(s.ring)).Msg("onlinestat: история загружена")
	return s, nil
}

// Observe закрывает минуту и запоминает пик. Вызывается из тика hub под h.mu — поэтому здесь
// нет ни обращений к базе, ни блокирующих ожиданий.
func (s *Series) Observe(now time.Time, n int) {
	if s == nil {
		return
	}
	minute := now.UTC().Truncate(Step)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.cur.At.IsZero():
		s.cur = Point{At: minute, N: n}
	case minute.Equal(s.cur.At):
		if n > s.cur.N {
			s.cur.N = n // пик минуты
		}
	default:
		s.ring = append(s.ring, s.cur)
		s.pend = append(s.pend, s.cur)
		s.trim()
		s.cur = Point{At: minute, N: n}
	}
}

// trim выкидывает точки старше ретенции, пачками по trimSlack. Вызывать под mu. Кольцо может
// на эту пачку перерастать ретенцию — лишние минуты никому не мешают, окно запроса всё равно
// режет ряд по from/to.
func (s *Series) trim() {
	drop := len(s.ring) - RetentionPoints
	if drop < trimSlack {
		return
	}
	s.ring = append(s.ring[:0], s.ring[drop:]...)
}

// Points отдаёт точки окна [from, to] не более maxPoints штук. Если точек больше, шаг
// укрупняется (значение в корзине — максимум), и фактический шаг возвращается вызывающему:
// иначе график врал бы о разрешении данных. Незакрытая минута тоже отдаётся — с ней график живой.
//
// Минуты, когда сервер не работал (перезапуск при выкладке, простой), отдаются нулями: играть в
// это время было нельзя, и разрыв в линии после каждой выкладки только сбивал с толку. Заполняются
// только промежутки между точками: до первой записанной минуты истории просто нет.
func (s *Series) Points(from, to time.Time, maxPoints int) (time.Duration, []Point) {
	if s == nil {
		return Step, nil
	}
	if maxPoints < 1 {
		maxPoints = 1
	}
	s.mu.Lock()
	src := make([]Point, 0, len(s.ring)+1)
	for _, p := range s.ring {
		if !p.At.Before(from) && !p.At.After(to) {
			src = append(src, p)
		}
	}
	if !s.cur.At.IsZero() && !s.cur.At.Before(from) && !s.cur.At.After(to) {
		src = append(src, s.cur)
	}
	s.mu.Unlock()
	src = fillGaps(src)

	step := Step
	if len(src) > maxPoints {
		// Шаг берём из ряда «минута, 5 минут, 15 минут, час» — по нему клиент подписывает ось.
		for _, cand := range []time.Duration{5 * Step, 15 * Step, 60 * Step, 6 * 60 * Step} {
			step = cand
			if (len(src)*int(Step))/int(cand) <= maxPoints {
				break
			}
		}
	}
	if step == Step {
		return step, src
	}
	out := make([]Point, 0, maxPoints+1)
	for _, p := range src {
		bucket := p.At.Truncate(step)
		if n := len(out); n > 0 && out[n-1].At.Equal(bucket) {
			if p.N > out[n-1].N {
				out[n-1].N = p.N
			}
			continue
		}
		out = append(out, Point{At: bucket, N: p.N})
	}
	return step, out
}

// fillGaps вставляет нули на пропущенные минуты между точками и сливает повторы одной минуты
// (после перезапуска посреди минуты она есть и в истории, и в текущей) — в повторе берётся пик.
func fillGaps(src []Point) []Point {
	if len(src) < 2 {
		return src
	}
	out := make([]Point, 0, len(src))
	for _, p := range src {
		if n := len(out); n > 0 {
			last := out[n-1]
			if !p.At.After(last.At) {
				if p.N > last.N {
					out[n-1].N = p.N
				}
				continue
			}
			for t := last.At.Add(Step); t.Before(p.At); t = t.Add(Step) {
				out = append(out, Point{At: t})
			}
		}
		out = append(out, p)
	}
	return out
}

// Broken — база при старте была битой, и история сброшена.
func (s *Series) Broken() bool { return s != nil && s.db.Broken() }

// Flush пишет закрытые минуты в базу и стирает то, что вышло за ретенцию. Забирает накопленное
// под mu и отпускает его до записи: иначе запрос админки ждал бы базу.
func (s *Series) Flush() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	pend := s.pend
	s.pend = nil
	s.mu.Unlock()
	if len(pend) == 0 {
		return nil
	}
	if err := writePoints(s.db.W, pend); err != nil {
		// Не потерять точки: вернуть их в очередь, следующий флаш попробует ещё раз.
		s.mu.Lock()
		s.pend = append(pend, s.pend...)
		s.mu.Unlock()
		return err
	}
	return nil
}

// upsertPoint — запись точки; минута, уже лежащая в базе (перезапуск посреди минуты), хранит пик.
const upsertPoint = "INSERT INTO online_points(at, n) VALUES(?, ?) ON CONFLICT(at) DO UPDATE SET n = max(n, excluded.n)"

func writePoints(db *sql.DB, pts []Point) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(upsertPoint)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range pts {
		if _, err := stmt.Exec(p.At.Unix(), p.N); err != nil {
			return err
		}
	}
	cut := pts[len(pts)-1].At.Add(-Retention).Unix()
	if _, err := tx.Exec("DELETE FROM online_points WHERE at < ?", cut); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportLog переносит ряд из старого online.log в базу внутри транзакции вызывающего (см.
// cmd/snowbrawl-server). Файла нет — ноль и без ошибки; битые строки пропускаются.
func ImportLog(tx store.Execer, path string) (int, error) {
	pts, err := readLegacy(path)
	if err != nil || len(pts) == 0 {
		return 0, err
	}
	for _, p := range pts {
		if _, err := tx.Exec(upsertPoint, p.At.Unix(), p.N); err != nil {
			return 0, err
		}
	}
	return len(pts), nil
}

// Run сбрасывает накопленное в базу по таймеру. Останавливается в Close.
func (s *Series) Run(period time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				if err := s.Flush(); err != nil {
					s.log.Error().Err(err).Msg("onlinestat: не удалось записать историю")
				}
			}
		}
	}()
}

// Close закрывает текущую минуту и дописывает всё. Вызывать после остановки hub (иначе тик
// успеет дописать точку в уже закрытую серию) и до закрытия базы.
func (s *Series) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	s.mu.Lock()
	if !s.cur.At.IsZero() {
		s.ring = append(s.ring, s.cur)
		s.pend = append(s.pend, s.cur)
		s.cur = Point{}
	}
	s.mu.Unlock()
	return s.Flush()
}
