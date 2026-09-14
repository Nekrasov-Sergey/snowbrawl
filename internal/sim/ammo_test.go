package sim_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// Боезапас: три выстрела в запасе, между ними короткая пауза, отделения заполняются по одному.
// Тест держит правило целиком — и запрет четвёртого выстрела, и последовательный возврат
// зарядов, и то, что запас не перерастает AMMO_MAX.

// shootOnce бросает без ожидания заполнения: возвращает false, если замах отклонён (пусто или
// не прошла пауза). Пауза между выстрелами — 250 мс, то есть 5 шагов по 1/20 с.
func shootOnce(t *testing.T, m *sim.Match, id string) bool {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"kind": "chargeStart", "x": 700, "y": 150})
	if ok, _ := m.ApplyInput(id, body); !ok {
		return false
	}
	steps(t, m, 2)
	body, _ = json.Marshal(map[string]any{"kind": "throw", "x": 700, "y": 150, "power": 0.3})
	if ok, _ := m.ApplyInput(id, body); !ok {
		t.Fatal("бросок отклонён при идущем замахе")
	}
	return true
}

func TestAmmoThreeShots(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	// Снайпер: самое долгое заполнение (1600×1.4 = 2240 мс), возврат заряда не успеет
	// случайно произойти между выстрелами и смазать проверку.
	m := shooterAt(t, p, "Снайпер", 17)

	if am := fighterByID(t, m, "me").Am; am != 3 {
		t.Fatalf("на старте запас %.2f, ожидалось 3 — боец обязан начинать с полным", am)
	}
	for i := 1; i <= 3; i++ {
		if !shootOnce(t, m, "me") {
			t.Fatalf("выстрел %d из запаса отклонён", i)
		}
		if am := fighterByID(t, m, "me").Am; math.Floor(am) != float64(3-i) {
			t.Fatalf("после выстрела %d целых отделений %.0f, ожидалось %d", i, math.Floor(am), 3-i)
		}
		steps(t, m, 6) // пауза между выстрелами (250 мс) + запас на округление
	}
	if shootOnce(t, m, "me") {
		t.Fatal("четвёртый выстрел прошёл с пустым запасом")
	}

	// Первое отделение возвращается примерно через 2240 мс; ждём с запасом и проверяем,
	// что вернулось ровно одно, а не весь запас сразу.
	steps(t, m, 48)
	am := fighterByID(t, m, "me").Am
	if am < 1 || am >= 2 {
		t.Fatalf("через ~2.4 с запас %.2f, ожидалось от 1 до 2 — отделения заполняются по одному", am)
	}
	if !shootOnce(t, m, "me") {
		t.Fatal("с одним заполненным отделением выстрел обязан пройти")
	}

	// Долгое ожидание не должно копить заряды сверх запаса (три отделения Снайпера — ~135 шагов).
	steps(t, m, 160)
	if am := fighterByID(t, m, "me").Am; am != 3 {
		t.Fatalf("после долгого простоя запас %.2f, ожидалось ровно 3", am)
	}
}

// TestAmmoShotGap — пауза между выстрелами есть: сразу после броска замах отклоняется, хотя
// в запасе ещё остались заряды. Без неё три снежка уходили одним залпом в упор.
func TestAmmoShotGap(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	m := shooterAt(t, p, "Раннер", 19)

	if !shootOnce(t, m, "me") {
		t.Fatal("первый выстрел отклонён")
	}
	body, _ := json.Marshal(map[string]any{"kind": "chargeStart", "x": 700, "y": 150})
	if ok, _ := m.ApplyInput("me", body); ok {
		t.Fatal("замах принят сразу после броска — паузы между выстрелами нет")
	}
	if am := fighterByID(t, m, "me").Am; math.Floor(am) < 1 {
		t.Fatalf("запас %.2f: заряды остались, отклонить замах могла только пауза", am)
	}
	steps(t, m, 6)
	if ok, _ := m.ApplyInput("me", body); !ok {
		t.Fatal("после паузы замах обязан приниматься")
	}
}

// TestAmmoPromiseHoldsForClient — если снапшот говорит «заряд есть и паузы нет», симуляция
// обязана принять замах. Клиент решает по этим двум полям, и любое расхождение означает молча
// потерянный выстрел: замах начнётся локально, сервер его отклонит, бросок пропадёт.
func TestAmmoPromiseHoldsForClient(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	m := shooterAt(t, p, "Снайпер", 31) // самое долгое отделение: больше всего шансов поймать границу

	// Опустошаем запас.
	for i := 0; i < 3; i++ {
		if !shootOnce(t, m, "me") {
			t.Fatalf("выстрел %d отклонён", i+1)
		}
		steps(t, m, 6)
	}
	// Дальше идём к границе заполнения мелким шагом: расхождение живёт в последних миллисекундах
	// перед появлением заряда, и шаг в 50 мс через него просто перепрыгивает.
	body, _ := json.Marshal(map[string]any{"kind": "chargeStart", "x": 700, "y": 150})
	const fine = 1.0 / 200 // 5 мс
	for i := 0; i < 600; i++ {
		f := fighterByID(t, m, "me")
		ok, _ := m.ApplyInput("me", body)
		if ok {
			if f.Am < 1 {
				t.Fatalf("шаг %d: замах принят при am=%.2f — снапшот занижает запас", i, f.Am)
			}
			return // обещание выполнено: как только am дорос до 1, замах приняли
		}
		if f.Am >= 1 && f.RL == 0 {
			t.Fatalf("шаг %d: снапшот обещает запас (am=%.2f, rl=%.2f), а замах отклонён", i, f.Am, f.RL)
		}
		if _, err := m.Step(fine); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("за 3 с отделение так и не заполнилось")
}

// TestPveMobsKeepRoleCadence — пауза между выстрелами (250 мс) введена для игроков с запасом
// зарядов. У мобов PvE запаса нет, и подмена их прежнего кулдауна этой паузой втрое ускорила бы
// стрельбу волн — незаявленный буст сложности.
func TestPveMobsKeepRoleCadence(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	m, err := p.NewMatch(pveConfig("survival", 3, p.Roles()), 41)
	if err != nil {
		t.Fatal(err)
	}
	// Ждём броска любого моба и смотрим, как долго держится его пауза.
	for i := 0; i < 20*60; i++ {
		raw, err := m.Step(1.0 / 20)
		if err != nil {
			t.Fatal(err)
		}
		var events []struct {
			Type     string `json:"type"`
			PlayerID string `json:"playerId"`
		}
		_ = json.Unmarshal(raw, &events)
		var thrower string
		for _, e := range events {
			if e.Type == "throw" {
				thrower = e.PlayerID
			}
		}
		if thrower == "" {
			continue
		}
		f := fighterByID(t, m, thrower)
		if f.ET == "" {
			continue // это боец пати, у него запас и короткая пауза — так и задумано
		}
		// Шесть шагов — это 300 мс, больше SHOT_GAP_MS. Прежний кулдаун роли (минимум 500 мс)
		// обязан ещё держаться.
		steps(t, m, 6)
		if again := fighterByID(t, m, thrower); again.RL == 0 && !again.Koed {
			t.Fatalf("моб %q (%s) готов стрелять через 300 мс — кулдаун роли подменён паузой запаса",
				thrower, again.Role)
		}
		return
	}
	t.Fatal("за 60 с ни один моб не бросил снежок")
}
