package sim_test

import (
	"encoding/json"
	"testing"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// Счёт выбитых соперников (поле k, с 1.10.0) — на нём стоит таблица плашки итогов. Важны три
// вещи: фраг достаётся владельцу снаряда, поле есть у каждого бойца в каждом кадре (клиент
// переиспользует объекты при интерполяции) и чужой урон счёт не трогает.

type scorer struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Team  string `json:"team"`
	HP    int    `json:"hp"`
	Koed  bool   `json:"koed"`
	Kills int    `json:"k"`
}

func scorers(t *testing.T, m *sim.Match) []scorer {
	t.Helper()
	raw, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Players []scorer `json:"players"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Players
}

func scorerByID(t *testing.T, m *sim.Match, id string) scorer {
	t.Helper()
	for _, s := range scorers(t, m) {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("бойца %q нет в снапшоте", id)
	return scorer{}
}

// TestKillCreditsShooter — три попадания подряд выбивают соперника, и фраг уходит стрелку.
// Соперник стоит (Bot: false), стрелок бьёт с чистой линии y=150 — см. shooterAt.
func TestKillCreditsShooter(t *testing.T) {
	p := loadProgram(t)
	m := shooterAt(t, p, "Снайпер", 11)
	eid, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Раннер", X: 560, Y: 150, Bot: false})
	if err != nil {
		t.Fatal(err)
	}
	if k := scorerByID(t, m, "me").Kills; k != 0 {
		t.Fatalf("до боя k=%d, ожидался 0", k)
	}

	for i := 0; i < 3; i++ {
		fireAt(t, m, 560, 150, 0.34)
	}
	e := scorerByID(t, m, eid)
	if !e.Koed {
		t.Fatalf("соперник должен быть выбит тремя попаданиями, hp=%d koed=%v", e.HP, e.Koed)
	}
	if k := scorerByID(t, m, "me").Kills; k != 1 {
		t.Fatalf("стрелку засчитано k=%d, ожидался 1", k)
	}
	if e.Kills != 0 {
		t.Fatalf("выбитому засчитано k=%d, ожидался 0", e.Kills)
	}

	// Добивать некого — счёт больше не растёт.
	fireAt(t, m, 560, 150, 0.34)
	if k := scorerByID(t, m, "me").Kills; k != 1 {
		t.Fatalf("после выстрела по выбитому k=%d, ожидался 1", k)
	}
}

// TestKillFieldAlwaysPresent — поле k есть у каждого бойца; пропущенный ключ в снапшоте клиент
// не удаляет при интерполяции, и чужое число осталось бы висеть на бойце.
func TestKillFieldAlwaysPresent(t *testing.T) {
	p := loadProgram(t)
	m, err := p.NewMatch(botsConfig(3, p.Roles()), 9)
	if err != nil {
		t.Fatal(err)
	}
	steps(t, m, 5)
	raw, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Players []map[string]json.RawMessage `json:"players"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	for i, pl := range s.Players {
		if _, ok := pl["k"]; !ok {
			t.Fatalf("у бойца %d нет поля k", i)
		}
	}
	for _, sc := range scorers(t, m) {
		if sc.Kills != 0 {
			t.Fatalf("в начале матча у %q k=%d, ожидался 0", sc.ID, sc.Kills)
		}
	}
}

// TestKillSumMatchesLosses — в бою ботов сумма фрагов совпадает с числом выбитых. Проверка ловит
// двойной зачёт (например, если взрыв засчитает и прямое попадание, и осколки).
func TestKillSumMatchesLosses(t *testing.T) {
	p := loadProgram(t)
	m, err := p.NewMatch(botsConfig(3, p.Roles()), 21)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20*90 && !m.IsOver(); i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
	kills, koed := 0, 0
	for _, sc := range scorers(t, m) {
		kills += sc.Kills
		if sc.Koed {
			koed++
		}
	}
	if koed == 0 {
		t.Skip("за матч никого не выбили — сравнивать нечего")
	}
	if kills != koed {
		t.Fatalf("сумма фрагов %d, выбито бойцов %d", kills, koed)
	}
}
