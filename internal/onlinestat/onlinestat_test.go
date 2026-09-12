package onlinestat

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func testLog() zerolog.Logger { return zerolog.New(io.Discard) }

var base = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// TestMinuteBucketsKeepPeak — точка минуты это её пик: так график не зависит от того, в какой
// момент минуты сработал семпл.
func TestMinuteBucketsKeepPeak(t *testing.T) {
	s, err := Open("", testLog())
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
	s, err := Open("", testLog())
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
	s, err := Open("", testLog())
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

// TestSurvivesRestartAndKeepsGap — история переживает перезапуск, а простой сервера остаётся
// дыркой в данных: нулями его заполнять нельзя, иначе «выключен» не отличить от «никого нет».
func TestSurvivesRestartAndKeepsGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online.log")
	s, err := Open(path, testLog())
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
	s2, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	s2.Observe(base.Add(62*time.Minute), 9)
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	s3, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s3.Close() }()
	_, pts := s3.Points(base.Add(-time.Hour), base.Add(3*time.Hour), 1000)
	if len(pts) != 4 {
		t.Fatalf("точек %d, ожидалось 4: %+v", len(pts), pts)
	}
	if pts[2].N != 6 || pts[3].N != 9 {
		t.Fatalf("значения не сохранились: %+v", pts)
	}
	if got := pts[3].At.Sub(pts[2].At); got != 60*time.Minute {
		t.Fatalf("простой сервера %v, ожидался час — дырка обязана сохраниться", got)
	}
}

func TestBrokenFileMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "online.log")
	if err := os.WriteFile(path, []byte("это не наш формат\n1 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatalf("битый файл не должен ронять сервер: %v", err)
	}
	defer func() { _ = s.Close() }()
	if !s.Broken() {
		t.Fatal("Broken() обязан сообщить о битом файле")
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf("битый файл не отложен: %v", err)
	}
	if _, pts := s.Points(base.Add(-time.Hour), base.Add(time.Hour), 100); len(pts) != 0 {
		t.Fatalf("ряд должен быть пустым, получено %+v", pts)
	}
}

// TestBrokenLineSkipped — обрыв на дозаписи стоит одной строки, а не всей истории.
func TestBrokenLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online.log")
	content := header + "\n" + "1757505600 4\nмусор\n1757505660 5\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.Broken() {
		t.Fatal("одна битая строка — не повод откладывать файл")
	}
	if len(s.ring) != 2 {
		t.Fatalf("загружено %d точек, ожидалось 2", len(s.ring))
	}
}

func TestNilSeriesIsSafe(t *testing.T) {
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

// TestCompactionRewritesFile — append-only файл не растёт вечно: при переполнении он
// переписывается целиком, и история остаётся читаемой.
func TestCompactionRewritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online.log")
	s, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	// Пишем чуть больше порога компактизации, флашим по частям. Порог — два месяца поминутно,
	// поэтому флашим редко: каждый вызов делает fsync, и под -race сотня их заметна в CI.
	for i := 0; i < compactAt+10; i++ {
		s.Observe(base.Add(time.Duration(i)*time.Minute), i%7)
		if i%5000 == 0 {
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.lines > compactAt {
		t.Fatalf("строк в файле %d, компактизация не сработала", s.lines)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, testLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if s2.Broken() {
		t.Fatal("после компактизации файл читается как битый")
	}
	if len(s2.ring) < RetentionPoints || len(s2.ring) > RetentionPoints+trimSlack {
		t.Fatalf("после перезапуска точек %d, ожидалось около %d", len(s2.ring), RetentionPoints)
	}
}
