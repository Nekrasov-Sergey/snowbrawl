package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestOnlineSeriesNeedsToken — ряд онлайна закрыт так же, как остальная админка.
func TestOnlineSeriesNeedsToken(t *testing.T) {
	srv, _, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/online-series")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("статус %d, ожидался 401", res.StatusCode)
	}
}

func TestOnlineSeriesReturnsWindow(t *testing.T) {
	srv, _, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/online-series?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("статус %d, ожидался 200", res.StatusCode)
	}
	var body struct {
		Step   int       `json:"step"`
		From   int64     `json:"from"`
		To     int64     `json:"to"`
		Points [][]int64 `json:"points"`
		Broken bool      `json:"broken"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Step <= 0 || body.From >= body.To {
		t.Fatalf("окно не заполнено: %+v", body)
	}
	if body.Broken {
		t.Fatal("серия в памяти не может быть битой")
	}
	if body.Points == nil {
		t.Fatal("points должен быть массивом, даже пустым: клиент его перебирает")
	}
}

// TestStatsHasNoOnlineSeries — ряд не попадает в сводку. Иначе он менялся бы каждую минуту, и
// диффинг SSE потерял бы смысл: кадр уходил бы всем подписчикам без причины.
func TestStatsHasNoOnlineSeries(t *testing.T) {
	srv, _, _ := newAdmin(t)
	res, err := http.Get(srv.URL + "/admin/state?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"series"`, `"points"`, `"history"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("в /admin/state есть ключ %s — ряд обязан жить отдельной ручкой", key)
		}
	}
}

// TestAdminPageHasChart — у встроенной страницы нет ни линтера, ни тестов, поэтому хотя бы
// проверяем, что узел графика и его обработчики не выпали при правке HTML.
func TestAdminPageHasChart(t *testing.T) {
	page := string(adminPage)
	for _, needle := range []string{`id="onlineChart"`, "online-series", "chartSetRange", "пинг"} {
		if !strings.Contains(page, needle) {
			t.Fatalf("страница админки потеряла %q", needle)
		}
	}
}
