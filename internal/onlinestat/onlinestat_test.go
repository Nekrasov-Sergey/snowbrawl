package onlinestat

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
)

func testLog() zerolog.Logger { return zerolog.New(io.Discard) }

func openDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var base = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// TestMinuteBucketsKeepPeak — точка минуты это её пик: так график не зависит от того, в какой
// момент минуты сработал семпл.
func TestMinuteBucketsKeepPeak(t *testing.T) {
	t.Parallel()
	s, err := Open(nil, testLog())
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(base, 3)
	s.Observe(base.Add(20*time.Second), 7)
	s.Observe(base.Add(40*time.Second), 5)
	s.Observe(base.Add(time.Minute), 2)

	step, pts := s.Points(base.Add(-time.Hour), base.Add(time.Hour), 100)
	if step != Step {
		t.Fatalf("шаг %v, ожидался %v", step, Step)
	}
	if len(pts) != 2 {
		t.Fatalf("точек %d, ожидалось 2: %+v", len(pts), pts)
	}
	if pts[0].N != 7 {
		t.Fatalf("первая минута %d, ожидался пик 7", pts[0].N)
	}
	if !pts[0].At.Equal(base) {
		t.Fatalf("метка первой точки %v, ожидалась %v", pts[0].At, base)
	}
	// Незакрытая минута тоже отдаётся — с ней график живой.
	if pts[1].N != 2 {
		t.Fatalf("текущая минута %d, ожидалось 2", pts[1].N)
	}
}

func TestRetentionTrims(t *testing.T) {
	t.Parallel()
	s, err := Open(nil, testLog())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < RetentionPoints+3*trimSlack; i++ {
		s.Observe(base.Add(time.Duration(i)*time.Minute), i%9)
	}
	// Подрезка идёт пачками по trimSlack, поэтому кольцо может перерасти ретенцию на пачку —
	// но не больше: иначе память растёт без границы.
	if len(s.ring) > RetentionPoints+trimSlack {
		t.Fatalf("в кольце %d точек при ретенции %d", len(s.ring), RetentionPoints)
	}
}

// TestCoarserStepOnWideWindow — на широком окне сервер сам укрупняет шаг и сообщает его: иначе
// график врал бы о разрешении данных.
func TestCoarserStepOnWideWindow(t *testing.T) {
	t.Parallel()
	s, err := Open(nil, testLog())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		s.Observe(base.Add(time.Duration(i)*time.Minute), i%5)
	}
	step, pts := s.Points(base, base.Add(600*time.Minute), 60)
	if step <= Step {
		t.Fatalf("шаг %v, ожидался крупнее минуты", step)
	}
	if len(pts) > 61 {
		t.Fatalf("точек %d, просили не больше 60", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if !pts[i].At.After(pts[i-1].At) {
			t.Fatalf("метки не по возрастанию: %v затем %v", pts[i-1].At, pts[i].At)
		}
	}
}

// TestSurvivesRestartAndFillsGap — история переживает перезапуск, а минуты простоя сервера
// отдаются нулями: играть тогда было нельзя, и разрыва в графике после выкладки быть не должно.
func TestSurvivesRestartAndFillsGap(t *testing.T) {
	t.Parallel()
	db := openDB(t, filepath.Join(t.TempDir(), "snowbrawl.db"))
	s, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(base, 4)
	s.Observe(base.Add(time.Minute), 5)
	s.Observe(base.Add(2*time.Minute), 6)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Второй запуск: сервер стоял час.
	s2, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	s2.Observe(base.Add(62*time.Minute), 9)
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	s3, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s3.Close() }()
	_, pts := s3.Points(base.Add(-time.Hour), base.Add(3*time.Hour), 1000)
	// Три минуты до простоя, 59 нулей простоя и минута после: подряд, без разрывов.
	if len(pts) != 63 {
		t.Fatalf("точек %d, ожидалось 63", len(pts))
	}
	for i, p := range pts {
		if !p.At.Equal(base.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("точка %d в %v: минуты должны идти подряд", i, p.At)
		}
	}
	if pts[0].N != 4 || pts[2].N != 6 || pts[62].N != 9 {
		t.Fatalf("значения не сохранились: %+v %+v %+v", pts[0], pts[2], pts[62])
	}
	for _, p := range pts[3:62] {
		if p.N != 0 {
			t.Fatalf("минута простоя %v показана как %d, ожидался ноль", p.At, p.N)
		}
	}
	// До первой записанной минуты истории нет — нулями её не выдумываем.
	if pts[0].At.Before(base) {
		t.Fatal("ряд начинается раньше первой точки")
	}
}

// Перезапуск посреди минуты: минута есть и в истории, и в текущей — отдаётся одна, с пиком.
func TestRestartMidMinuteNoDuplicate(t *testing.T) {
	t.Parallel()
	db := openDB(t, "")
	s, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(base, 3)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	s2.Observe(base.Add(30*time.Second), 5)
	_, pts := s2.Points(base.Add(-time.Hour), base.Add(time.Hour), 100)
	if len(pts) != 1 || pts[0].N != 5 {
		t.Fatalf("минута перезапуска: %+v", pts)
	}
}

// TestRetentionInDB — база не растёт вечно: точки старше ретенции стираются при записи, а после
// перезапуска в памяти ровно месяц.
func TestRetentionInDB(t *testing.T) {
	t.Parallel()
	db := openDB(t, "")
	s, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	// Точка раз в полчаса на 35 дней, а не поминутно: ретенция режется по времени, и для проверки
	// хватает полутора тысяч строк. Поминутный месяц — 43 тысячи записей в SQLite под -race, это
	// десять секунд процессора, которые отнимали время у соседних тестов с таймингами (релиз
	// v0.25.1 упал на TestRoomPingPushedOnlyOnChange именно так).
	const every = 30 * time.Minute
	total := int((Retention + 5*24*time.Hour) / every)
	for i := 0; i < total; i++ {
		s.Observe(base.Add(time.Duration(i)*every), i%7)
		if i%500 == 0 {
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	last := base.Add(time.Duration(total-1) * every)
	var n int
	var oldest int64
	if err := db.R.QueryRow("SELECT count(*), min(at) FROM online_points").Scan(&n, &oldest); err != nil {
		t.Fatal(err)
	}
	if time.Unix(oldest, 0).Before(last.Add(-Retention)) {
		t.Fatalf("в базе точка %v старше ретенции (последняя %v)", time.Unix(oldest, 0).UTC(), last)
	}
	if want := int(Retention/every) + 1; n != want {
		t.Fatalf("в базе %d точек, ожидалось %d — ровно месяц", n, want)
	}
	s2, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if len(s2.ring) != n {
		t.Fatalf("после перезапуска точек %d, в базе %d", len(s2.ring), n)
	}
}

// TestImportLog — перенос старого online.log: битые строки пропускаются, чужой заголовок — ошибка.
func TestImportLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "online.log")
	content := legacyHeader + "\n" + "1757505600 4\nмусор\n1757505660 5\n1757505720 3"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, "")
	n, err := ImportLog(db.W, path)
	if err != nil || n != 3 {
		t.Fatalf("импорт: %d %v", n, err)
	}
	s, err := Open(db, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ring) != 3 || s.ring[1].N != 5 {
		t.Fatalf("после импорта: %+v", s.ring)
	}
	bad := filepath.Join(dir, "bad.log")
	if err := os.WriteFile(bad, []byte("чужой формат\n1 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLog(db.W, bad); err == nil {
		t.Error("чужой формат принят")
	}
	if n, err := ImportLog(db.W, filepath.Join(dir, "нет.log")); err != nil || n != 0 {
		t.Errorf("отсутствующий файл: %d %v", n, err)
	}
}

func TestNilSeriesIsSafe(t *testing.T) {
	t.Parallel()
	var s *Series
	s.Observe(base, 3)
	if step, pts := s.Points(base, base.Add(time.Hour), 10); step != Step || pts != nil {
		t.Fatalf("nil-серия вернула %v %+v", step, pts)
	}
	if s.Broken() {
		t.Fatal("nil-серия не битая")
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
