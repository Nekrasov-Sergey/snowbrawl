// Package onlinestat — ряд онлайна для графика в админке: одна точка в минуту, семь дней в
// памяти, дозапись в файл на томе.
//
// Инвариант, зеркальный internal/moderation. Там hub только читает стор, а пишет админка вне
// h.mu; здесь наоборот: hub под своим мьютексом только дописывает точку в память (Observe), а в
// файловую систему ходят собственная горутина серии (Run) и Close. Поэтому в Observe не должно
// появиться ни одного обращения к диску и ни одного вызова назад в hub — иначе fsync встанет
// поперёк всех матчей.
//
// Формат файла — дозапись строкой на минуту («<минута unix> <онлайн>»), а не полный дамп: писать
// 10080 точек каждую минуту ради одной новой цифры незачем. Полная перезапись (temp+Sync+Rename)
// бывает только при компактизации, когда строк накопилось вдвое больше ретенции.
package onlinestat

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	// Step — шаг ряда. Точка — максимум онлайна за минуту: пик читается осмысленнее среднего
	// и не зависит от того, в какой момент минуты сработал семпл.
	Step = time.Minute
	// Retention — сколько истории держим.
	Retention = 7 * 24 * time.Hour
	// RetentionPoints — тот же срок в точках.
	RetentionPoints = int(Retention / Step)
	// FlushEvery — как часто сбрасываем накопленное на диск. Цена жёсткой остановки —
	// потеря последних минут графика, и это дешевле fsync каждую минуту на дешёвой VPS.
	FlushEvery = 5 * time.Minute
	// compactAt — при каком числе строк в файле переписываем его целиком.
	compactAt = 2 * RetentionPoints
	// header — первая строка файла: по ней отличаем свой формат от чужого мусора.
	header = "#snowbrawl-online 1"
)

// Point — одна точка ряда.
type Point struct {
	At time.Time `json:"-"`
	N  int       `json:"-"`
}

// Series — ряд онлайна. Все методы безопасны на nil-приёмнике: сервер должен работать и без
// файла, и без самой серии.
type Series struct {
	mu   sync.Mutex
	ring []Point // не длиннее RetentionPoints, старые точки с головы
	cur  Point   // незакрытая минута
	pend []Point // закрытые минуты, ещё не записанные на диск

	fileMu sync.Mutex // всё, что касается файла: fsync никогда не под mu
	f      *os.File
	lines  int
	path   string
	broken bool

	log    zerolog.Logger
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// Open читает ряд с диска. Файла нет — пустой ряд. Файл битый — откладывается рядом с суффиксом
// .bad, сервер поднимается с пустым рядом: уронить игру из-за испорченного графика нельзя, но
// это должно быть видно (ERROR в лог, см. Broken).
func Open(path string, log zerolog.Logger) (*Series, error) {
	s := &Series{path: path, log: log, stopCh: make(chan struct{})}
	if path == "" {
		log.Info().Msg("onlinestat: файл не задан, история онлайна живёт только в памяти")
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.openFile(); err != nil {
		return nil, err
	}
	log.Info().Int("points", len(s.ring)).Str("path", path).Msg("onlinestat: история загружена")
	return s, nil
}

// load читает файл в кольцо. Битые строки пропускает, битый заголовок — повод отложить файл.
func (s *Series) load() error {
	f, err := os.Open(s.path) //nolint:gosec // путь задаёт администратор через конфиг
	if errors.Is(err, fs.ErrNotExist) {
		s.log.Info().Str("path", s.path).Msg("onlinestat: файла нет, начинаем с пустого ряда")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != header {
		_ = f.Close()
		return s.setAside(errors.New("неизвестный формат файла"))
	}
	var skipped int
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.lines++
		minute, n, ok := parseLine(line)
		if !ok {
			skipped++
			continue
		}
		s.appendPoint(Point{At: minute, N: n})
	}
	if err := sc.Err(); err != nil {
		_ = f.Close()
		return s.setAside(err)
	}
	if skipped > 0 {
		// Файл дозаписывается, поэтому обрыв на последней строке — обычное дело после жёсткой
		// остановки. Не повод откладывать файл, но повод его перезаписать при следующем флаше.
		s.lines = compactAt
		s.log.Warn().Int("skipped", skipped).Msg("onlinestat: битые строки пропущены, файл будет перезаписан")
	}
	// Ретенция могла измениться между версиями: подрезаем то, что уже не нужно.
	s.trim()
	return nil
}

func parseLine(line string) (time.Time, int, bool) {
	minute, n, found := strings.Cut(line, " ")
	if !found {
		return time.Time{}, 0, false
	}
	sec, err := strconv.ParseInt(minute, 10, 64)
	if err != nil {
		return time.Time{}, 0, false
	}
	cnt, err := strconv.Atoi(n)
	if err != nil || cnt < 0 {
		return time.Time{}, 0, false
	}
	return time.Unix(sec, 0).UTC(), cnt, true
}

// setAside откладывает битый файл и оставляет ряд пустым.
func (s *Series) setAside(cause error) error {
	s.broken = true
	s.ring = nil
	s.lines = 0
	bad := s.path + ".bad"
	if err := os.Rename(s.path, bad); err != nil {
		s.log.Error().Err(err).Str("path", s.path).Msg("onlinestat: файл битый и не переименовывается")
	}
	s.log.Error().Err(cause).Str("moved", bad).Msg("onlinestat: файл битый, история онлайна сброшена")
	return nil
}

func (s *Series) openFile() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // путь из конфига
	if err != nil {
		return err
	}
	s.f = f
	if s.lines == 0 {
		if _, err := fmt.Fprintln(f, header); err != nil {
			return err
		}
	}
	return nil
}

// Observe закрывает минуту и запоминает пик. Вызывается из тика hub под h.mu — поэтому здесь
// нет ни файловых операций, ни блокирующих ожиданий.
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
		s.appendPoint(s.cur)
		s.pend = append(s.pend, s.cur)
		s.trim()
		s.cur = Point{At: minute, N: n}
	}
}

// appendPoint кладёт точку в кольцо. Вызывать под mu (или до старта горутин, из load).
func (s *Series) appendPoint(p Point) {
	s.ring = append(s.ring, p)
}

// trim выкидывает точки старше ретенции. Вызывать под mu.
func (s *Series) trim() {
	if len(s.ring) <= RetentionPoints {
		return
	}
	drop := len(s.ring) - RetentionPoints
	s.ring = append(s.ring[:0], s.ring[drop:]...)
}

// Points отдаёт точки окна [from, to] не более maxPoints штук. Если точек больше, шаг
// укрупняется (значение в корзине — максимум), и фактический шаг возвращается вызывающему:
// иначе график врал бы о разрешении данных. Незакрытая минута тоже отдаётся — с ней график живой.
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

// Broken — файл был битым и отложен в .bad.
func (s *Series) Broken() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// Flush дозаписывает закрытые минуты. Забирает накопленное под mu и отпускает его до записи:
// иначе запрос админки ждал бы fsync.
func (s *Series) Flush() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	pend := s.pend
	s.pend = nil
	s.mu.Unlock()
	if len(pend) == 0 {
		return nil
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.f == nil {
		return nil
	}
	var b strings.Builder
	for _, p := range pend {
		fmt.Fprintf(&b, "%d %d\n", p.At.Unix(), p.N)
	}
	if _, err := s.f.WriteString(b.String()); err != nil {
		return err
	}
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.lines += len(pend)
	if s.lines > compactAt {
		return s.compactLocked()
	}
	return nil
}

// compactLocked переписывает файл целиком, оставляя только точки в ретенции. Вызывать под
// fileMu. Только здесь нужна атомарная замена: обрыв на дозаписи стоит одной строки, а обрыв на
// перезаписи оставил бы обрубок вместо истории.
func (s *Series) compactLocked() error {
	s.mu.Lock()
	points := make([]Point, len(s.ring))
	copy(points, s.ring)
	s.mu.Unlock()

	var b strings.Builder
	b.WriteString(header + "\n")
	for _, p := range points {
		fmt.Fprintf(&b, "%d %d\n", p.At.Unix(), p.N)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "online-*.log") // тот же каталог: rename через границу ФС не работает
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if _, err := tmp.WriteString(b.String()); err != nil {
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
	if s.f != nil {
		_ = s.f.Close()
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // путь из конфига
	if err != nil {
		return err
	}
	s.f = f
	s.lines = len(points)
	s.log.Info().Int("points", len(points)).Msg("onlinestat: файл перезаписан")
	return nil
}

// Run сбрасывает накопленное на диск по таймеру. Останавливается в Close.
func (s *Series) Run(period time.Duration) {
	if s == nil || s.path == "" {
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

// Close закрывает текущую минуту, дописывает всё и закрывает файл. Вызывать после остановки
// hub: иначе тик успеет дописать точку в уже закрытую серию.
func (s *Series) Close() error {
	if s == nil {
		return nil
	}
	close(s.stopCh)
	s.wg.Wait()
	s.mu.Lock()
	if !s.cur.At.IsZero() {
		s.appendPoint(s.cur)
		s.pend = append(s.pend, s.cur)
		s.cur = Point{}
	}
	s.mu.Unlock()
	err := s.Flush()
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.f != nil {
		if cerr := s.f.Close(); err == nil {
			err = cerr
		}
		s.f = nil
	}
	return err
}
