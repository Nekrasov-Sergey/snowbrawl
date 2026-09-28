package onlinestat

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// legacyHeader — первая строка старого online.log: по ней отличаем свой формат от мусора.
const legacyHeader = "#snowbrawl-online 1"

// readLegacy читает старый файл «<минута unix> <онлайн>» построчно. Файла нет — пусто и без
// ошибки; чужой заголовок — ошибка; битые строки (обрыв последней после жёсткой остановки)
// пропускаются.
func readLegacy(path string) ([]Point, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // путь задаёт администратор через конфиг
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != legacyHeader {
		return nil, fmt.Errorf("online.log: неизвестный формат файла")
	}
	var out []Point
	for sc.Scan() {
		if p, ok := parseLine(strings.TrimSpace(sc.Text())); ok {
			out = append(out, p)
		}
	}
	return out, sc.Err()
}

func parseLine(line string) (Point, bool) {
	minute, n, found := strings.Cut(line, " ")
	if !found {
		return Point{}, false
	}
	sec, err := strconv.ParseInt(minute, 10, 64)
	if err != nil {
		return Point{}, false
	}
	cnt, err := strconv.Atoi(n)
	if err != nil || cnt < 0 {
		return Point{}, false
	}
	return Point{At: time.Unix(sec, 0).UTC(), N: cnt}, true
}
