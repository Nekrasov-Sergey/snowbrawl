package sim_test

import (
	"encoding/json"
	"testing"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// Пассивки, которые видит клиент: заряд щитового пузыря в снапшоте, область укороченного
// кулдауна обучения и снос ящика взрывами. Всё это условия шагов обучения, поэтому регресс
// здесь ломает уроки, а не только рендер.

type fighter struct {
	ID     string  `json:"id"`
	Role   string  `json:"role"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	HP     int     `json:"hp"`
	Koed   bool    `json:"koed"`
	Bubble bool    `json:"bubble"`
	BB     float64 `json:"bb"`
	CD     float64 `json:"cd"`
	RL     float64 `json:"rl"`
	Slow   bool    `json:"slow"`
}

func fighters(t *testing.T, m *sim.Match) []fighter {
	t.Helper()
	raw, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Players []fighter `json:"players"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Players
}

func fighterByID(t *testing.T, m *sim.Match, id string) fighter {
	t.Helper()
	for _, f := range fighters(t, m) {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("бойца %q нет в снапшоте", id)
	return fighter{}
}

func steps(t *testing.T, m *sim.Match, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
}

// shooterAt готовит матч обучения: боец роли role стоит на чистой линии y=150 (у спавна колонна,
// из-за неё броски упирались бы в неё), стрельба идёт в точку (560,150).
func shooterAt(t *testing.T, p *sim.Program, role string, seed uint32) *sim.Match {
	t.Helper()
	cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: true,
		Players: []sim.PlayerConfig{{ID: "me", Team: "A", Role: role}}}
	m, err := p.NewMatch(cfg, seed)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"move","x":300,"y":150}`)); !ok {
		t.Fatal("move отклонён")
	}
	steps(t, m, 80)
	return m
}

// fireAt бросает в точку, дождавшись конца перезарядки: иначе chargeStart отклоняется и
// «промах» теста означает лишь то, что боец ещё перезаряжался.
func fireAt(t *testing.T, m *sim.Match, x, y, power float64) {
	t.Helper()
	for i := 0; i < 60 && fighterByID(t, m, "me").RL > 0; i++ {
		steps(t, m, 1)
	}
	body, _ := json.Marshal(map[string]any{"kind": "chargeStart", "x": x, "y": y})
	if ok, _ := m.ApplyInput("me", body); !ok {
		t.Fatal("замах отклонён — перезарядка не кончилась")
	}
	steps(t, m, 9)
	body, _ = json.Marshal(map[string]any{"kind": "throw", "x": x, "y": y, "power": power})
	if ok, _ := m.ApplyInput("me", body); !ok {
		t.Fatal("бросок отклонён")
	}
	steps(t, m, 16)
}

// TestBubbleRechargeSnapshot — «Закалка» Щита в снапшоте: пузырь гасит попадание целиком, поле
// bb показывает остаток восстановления, и реген требует ОБОИХ условий сразу — 12 с с момента
// хлопка и 12 с без реального урона. На этом стоит дуга вокруг бойца и подсказка шага.
func TestBubbleRechargeSnapshot(t *testing.T) {
	p := loadProgram(t)
	m := shooterAt(t, p, "Снайпер", 7)
	eid, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Щит", X: 560, Y: 150, Bot: false})
	if err != nil {
		t.Fatal(err)
	}
	if e := fighterByID(t, m, eid); !e.Bubble || e.BB != 0 {
		t.Fatalf("свежий Щит: bubble=%v bb=%.2f, ожидалось true и 0", e.Bubble, e.BB)
	}

	fireAt(t, m, 560, 150, 0.34)
	e := fighterByID(t, m, eid)
	if e.Bubble {
		t.Fatal("первое попадание должно было лопнуть пузырь")
	}
	if e.HP != 3 {
		t.Fatalf("пузырь обязан гасить попадание целиком, HP=%d", e.HP)
	}
	if e.BB < 0.85 {
		t.Fatalf("сразу после хлопка bb=%.2f, ожидалось около 1", e.BB)
	}

	steps(t, m, 6*20) // 6 секунд без урона — полпути
	if e = fighterByID(t, m, eid); e.BB < 0.4 || e.BB > 0.6 {
		t.Fatalf("через 6 с bb=%.2f, ожидалось около 0.5", e.BB)
	}

	// Реальный урон отодвигает реген заново: второе условие считается от lastDamagedAt.
	fireAt(t, m, 560, 150, 0.34)
	if e = fighterByID(t, m, eid); e.HP != 2 {
		t.Fatalf("после хлопка попадание обязано снимать HP, HP=%d", e.HP)
	} else if e.BB < 0.85 {
		t.Fatalf("урон должен был отодвинуть реген, bb=%.2f", e.BB)
	}

	steps(t, m, 13*20) // 13 секунд покоя — пузырь возвращается
	if e = fighterByID(t, m, eid); !e.Bubble || e.BB != 0 {
		t.Fatalf("после 13 с покоя bubble=%v bb=%.2f, ожидалось true и 0", e.Bubble, e.BB)
	}
}

// TestBubbleFieldAlwaysPresent — поле bb есть у каждого бойца, а не только у Щита: клиент
// переиспользует объекты бойцов при интерполяции, и пропущенный ключ сохранил бы прошлое
// значение — дуга висела бы на чужой роли.
func TestBubbleFieldAlwaysPresent(t *testing.T) {
	p := loadProgram(t)
	m, err := p.NewMatch(botsConfig(3, p.Roles()), 4)
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
	if len(s.Players) != 6 {
		t.Fatalf("бойцов в снапшоте %d, ожидалось 6", len(s.Players))
	}
	for i, pl := range s.Players {
		if _, ok := pl["bb"]; !ok {
			t.Fatalf("у бойца %d нет поля bb", i)
		}
	}
	for _, f := range fighters(t, m) {
		if f.Role != "Щит" && f.BB != 0 {
			t.Fatalf("роль %q: bb=%.2f, ожидался 0", f.Role, f.BB)
		}
	}
}

// TestTutorialCooldownScope — укороченный кулдаун обучения достаётся только ученику. С коротким
// кулдауном у соперника Танк уровня 2 таранит каждые 2 секунды, и шаги «дайте сопернику попасть»
// превращаются в непрерывное оглушение.
func TestTutorialCooldownScope(t *testing.T) {
	p := loadProgram(t)
	cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: true,
		Players: []sim.PlayerConfig{{ID: "me", Team: "A", Role: "Щит"}}}
	m, err := p.NewMatch(cfg, 9)
	if err != nil {
		t.Fatal(err)
	}
	eid, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Щит", X: 600, Y: 280, Bot: false})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"special","x":300,"y":280}`)); !ok {
		t.Fatal("способность ученика не применилась")
	}
	body, _ := json.Marshal(map[string]any{"kind": "special", "x": 700, "y": 280})
	if ok, _ := m.ApplyInput(eid, body); !ok {
		t.Fatal("способность соперника не применилась")
	}
	steps(t, m, 1)
	if got := fighterByID(t, m, "me").CD; got > 2 {
		t.Fatalf("у ученика кулдаун %.1f с, ожидалось не больше 2", got)
	}
	if got := fighterByID(t, m, eid).CD; got < 10 {
		t.Fatalf("у соперника кулдаун %.1f с, ожидался полный (около 13.5)", got)
	}
}

// TestSapperBreaksCrate — пассив «Сапёр»: ящик «Классики» держит 4 HP, взрыв снимает 3, значит
// на снос нужно ровно два попадания. На этом стоит шаг обучения Бомбера.
func TestSapperBreaksCrate(t *testing.T) {
	p := loadProgram(t)
	// Боец стоит на чистой линии y=150: у спавна колонна, и подойти к ящику «в лоб» нельзя —
	// столкновение выдавливает бойца влево, а бомба взрывается о колонну.
	m := shooterAt(t, p, "Бомбер", 13)
	crate := func() (hp, maxHP int) {
		raw, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Destr []struct {
				X     float64 `json:"x"`
				HP    int     `json:"hp"`
				MaxHP int     `json:"maxHp"`
			} `json:"destr"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		for _, d := range s.Destr {
			if d.X > 430 && d.X < 446 {
				return d.HP, d.MaxHP
			}
		}
		t.Fatal("ящика нет в снапшоте")
		return 0, 0
	}
	if hp, maxHP := crate(); hp != 4 || maxHP != 4 {
		t.Fatalf("ящик «Классики» %d/%d, ожидалось 4/4", hp, maxHP)
	}

	shot := 0
	bomb := func() {
		shot++
		// В обучении кулдаун 2 с, а бросок с перезарядкой укладывается быстрее — ждём явно.
		// cd в снапшоте округлён до десятых, поэтому после нуля добавляем запас на остаток.
		for i := 0; i < 80 && fighterByID(t, m, "me").CD > 0; i++ {
			steps(t, m, 1)
		}
		steps(t, m, 3)
		if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"special","x":438,"y":260}`)); !ok {
			t.Fatalf("взрывной снежок не заряжен (бросок %d, cd=%.1f)", shot, fighterByID(t, m, "me").CD)
		}
		fireAt(t, m, 438, 260, 0.1)
	}
	bomb()
	hp, _ := crate()
	if hp != 1 {
		t.Fatalf("после одного взрыва ящик %d/4, ожидалось 1/4", hp)
	}
	bomb()
	if hp, _ = crate(); hp != 0 {
		t.Fatalf("после двух взрывов ящик %d/4, ожидалось 0/4", hp)
	}
}

// TestFreezerAuraSlowsTimers — пассив «Стужа» замедляет не только шаг: в ауре вражеского Фризера
// у бойца медленнее течёт перезарядка броска и кулдаун способности. Вне радиуса — обычная
// скорость: это растяжение времени, а не штраф в момент броска.
func TestFreezerAuraSlowsTimers(t *testing.T) {
	p := loadProgram(t)
	// Ученик — Щит: у него самый длинный кулдаун, разницу видно без долгих прогонов.
	m := shooterAt(t, p, "Щит", 21)

	// Замеряем, как убывает кулдаун без ауры и в ней. Соперника ставим вплотную, чтобы тест не
	// зависел от конкретного радиуса — границу проверяет TestFreezerAuraBoundary.
	measure := func(withAura bool) (cd, rl float64) {
		if withAura {
			me := fighterByID(t, m, "me")
			if _, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Фризер", X: me.X, Y: me.Y + 20, Bot: false}); err != nil {
				t.Fatal(err)
			}
			steps(t, m, 1)
		}
		if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"special","x":420,"y":150}`)); !ok {
			t.Fatal("способность не применилась")
		}
		fireAt(t, m, 700, 150, 0.5)
		me := fighterByID(t, m, "me")
		return me.CD, me.RL
	}

	freeCD, freeRL := measure(false)
	if freeCD <= 0 || freeRL <= 0 {
		t.Fatalf("без ауры нечего сравнивать: cd=%.2f rl=%.2f", freeCD, freeRL)
	}

	// Ждём полного восстановления и повторяем то же самое рядом с Фризером.
	for i := 0; i < 600 && fighterByID(t, m, "me").CD > 0; i++ {
		steps(t, m, 1)
	}
	steps(t, m, 5)
	auraCD, auraRL := measure(true)

	if auraCD <= freeCD {
		t.Fatalf("в ауре кулдаун %.2f, без неё %.2f — замедления нет", auraCD, freeCD)
	}
	if auraRL <= freeRL {
		t.Fatalf("в ауре перезарядка %.2f, без неё %.2f — замедления нет", auraRL, freeRL)
	}
	if auraRL > 1 {
		t.Fatalf("rl=%.2f: поле обязано быть зажато в 1, иначе клиент рисует дугу больше круга", auraRL)
	}
}

// TestFreezerAuraOnlyEnemies — своя аура союзников не тормозит.
func TestFreezerAuraOnlyEnemies(t *testing.T) {
	p := loadProgram(t)
	cfg := sim.MatchConfig{Mode: 2, ArenaIndex: 0, Players: []sim.PlayerConfig{
		{ID: "a0", Team: "A", Role: "Щит"},
		{ID: "a1", Team: "A", Role: "Фризер", Bot: true},
		{ID: "b0", Team: "B", Role: "Танк", Bot: true},
		{ID: "b1", Team: "B", Role: "Танк", Bot: true},
	}}
	m, err := p.NewMatch(cfg, 23)
	if err != nil {
		t.Fatal(err)
	}
	// Ставим союзников рядом и бросаем: перезарядка обязана идти обычным темпом.
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"chargeStart","x":700,"y":280}`)); !ok {
		t.Fatal("замах отклонён")
	}
	steps(t, m, 9)
	body, _ := json.Marshal(map[string]any{"kind": "throw", "x": 700, "y": 280, "power": 0.5})
	if ok, _ := m.ApplyInput("a0", body); !ok {
		t.Fatal("бросок отклонён")
	}
	start := fighterByID(t, m, "a0").RL
	steps(t, m, 4)
	after := fighterByID(t, m, "a0").RL
	if !(after < start) {
		t.Fatalf("перезарядка не убывает рядом со своим Фризером: %.2f → %.2f", start, after)
	}
}

// TestFreezerAuraBoundary — замедление включается внутри радиуса ауры и не работает снаружи.
// Единственный тест, который поймает изменение FREEZER_AURA_R: остальные ставят Фризера вплотную
// и переживут любой радиус, поэтому опечатка в константе прошла бы незамеченной.
func TestFreezerAuraBoundary(t *testing.T) {
	p := loadProgram(t)
	radius := auraRadius(t)

	// Ученик стоит на чистой линии y=150, Фризер — на заданной дистанции по той же линии.
	slowedAt := func(gap float64) bool {
		m := shooterAt(t, p, "Танк", 31)
		me := fighterByID(t, m, "me")
		if _, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Фризер", X: me.X + gap, Y: me.Y, Bot: false}); err != nil {
			t.Fatal(err)
		}
		steps(t, m, 2)
		return fighterByID(t, m, "me").Slow
	}

	if !slowedAt(radius - 10) {
		t.Fatalf("на %.0f px (внутри радиуса %.0f) замедления нет", radius-10, radius)
	}
	if slowedAt(radius + 20) {
		t.Fatalf("на %.0f px (снаружи радиуса %.0f) замедление всё равно есть", radius+20, radius)
	}
}

// auraRadius читает радиус из экспорта sim.js: захардкоженное число в тесте разъехалось бы
// с правилами при первой же правке баланса.
func auraRadius(t *testing.T) float64 {
	t.Helper()
	vm := aimVM(t)
	v, err := vm.RunString("SnowBrawlSim.FREEZER_AURA_R")
	if err != nil {
		t.Fatal(err)
	}
	r := v.ToFloat()
	if !(r > 0) {
		t.Fatalf("FREEZER_AURA_R = %v", r)
	}
	return r
}
