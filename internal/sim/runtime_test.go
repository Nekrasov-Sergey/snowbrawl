package sim_test

import (
	"encoding/json"
	"testing"
	"time"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

func loadProgram(t testing.TB) *sim.Program {
	t.Helper()
	src, err := snowbrawl.Web.ReadFile(snowbrawl.SimPath)
	if err != nil {
		t.Fatalf("read sim.js: %v", err)
	}
	p, err := sim.Compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func botsConfig(mode int, roles []string) sim.MatchConfig {
	cfg := sim.MatchConfig{Mode: mode, ArenaIndex: 0}
	for i := 0; i < mode; i++ {
		cfg.Players = append(cfg.Players, sim.PlayerConfig{ID: "a" + string(rune('0'+i)), Team: "A", Role: roles[i%len(roles)], Bot: true})
	}
	for i := 0; i < mode; i++ {
		cfg.Players = append(cfg.Players, sim.PlayerConfig{ID: "b" + string(rune('0'+i)), Team: "B", Role: roles[(i+1)%len(roles)], Bot: true})
	}
	return cfg
}

type snap struct {
	Tick    int  `json:"tick"`
	Over    bool `json:"over"`
	Winner  any  `json:"winner"`
	Players []struct {
		ID       string  `json:"id"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		HP       int     `json:"hp"`
		Charging bool    `json:"charging"`
	} `json:"players"`
	Balls []json.RawMessage `json:"balls"`
}

// TestCancelCharge — отмена замаха: chargeStart → cancelCharge не бросает снежок,
// повторный chargeStart принимается, cancelCharge без замаха отклоняется.
func TestCancelCharge(t *testing.T) {
	p := loadProgram(t)
	cfg := botsConfig(1, p.Roles())
	cfg.Players[0].Bot = false
	m, err := p.NewMatch(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"cancelCharge"}`)); ok {
		t.Fatal("cancelCharge without charge must be rejected")
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"chargeStart","x":700,"y":280}`)); !ok {
		t.Fatal("chargeStart rejected")
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"cancelCharge"}`)); !ok {
		t.Fatal("cancelCharge rejected during charge")
	}
	// События applyInput между шагами sim не накапливает (step обнуляет state.events),
	// поэтому проверяем результат по снапшоту: замах снят, снежка нет.
	if _, err := m.Step(1.0 / 20); err != nil {
		t.Fatal(err)
	}
	raw, _ := m.Snapshot()
	var s snap
	_ = json.Unmarshal(raw, &s)
	if s.Players[0].Charging {
		t.Fatal("charging must be reset after cancelCharge")
	}
	if len(s.Balls) != 0 {
		t.Fatal("cancelCharge must not throw a snowball")
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"throw","x":700,"y":280}`)); ok {
		t.Fatal("throw after cancel must be rejected")
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"chargeStart","x":700,"y":280}`)); !ok {
		t.Fatal("chargeStart after cancel rejected")
	}
}

func TestCompileExports(t *testing.T) {
	p := loadProgram(t)
	if p.Version() == "" || p.ArenaCount() < 1 || len(p.Roles()) < 6 {
		t.Fatalf("bad program meta: %q %d %v", p.Version(), p.ArenaCount(), p.Roles())
	}
	// Клиентский контракт: чистые функции для луча прицела. Сервер их не зовёт, но их
	// пропажа сломала бы прицел, а это заметно только в браузере.
	vm := aimVM(t)
	for _, fn := range []string{"canHitTarget", "aimPath"} {
		if v, err := vm.RunString("typeof SnowBrawlSim." + fn); err != nil || v.String() != "function" {
			t.Fatalf("sim.js: экспорт %q не функция (%v)", fn, err)
		}
	}
	// Радиусы и сроки: по ним клиент рисует ауру, взрыв и дугу пузыря, а обучение строит условия
	// шагов. Пропажа экспорта не сломает сервер, но развалит рендер и уроки.
	for _, c := range []string{"FREEZER_AURA_R", "FREEZER_AURA_SLOW", "FREEZER_AURA_RELOAD",
		"FROST_R", "FROST_SLOW", "EXPLOSION_RADIUS", "BUBBLE_REGEN_MS", "WALL_LIFETIME_MS"} {
		v, err := vm.RunString("SnowBrawlSim." + c)
		if err != nil {
			t.Fatalf("sim.js: экспорт %q не читается: %v", c, err)
		}
		if n := v.ToFloat(); !(n > 0) {
			t.Fatalf("sim.js: экспорт %q = %v, ожидалось положительное число", c, v)
		}
	}
}

func TestBotsOnlyMatchFinishes(t *testing.T) {
	p := loadProgram(t)
	m, err := p.NewMatch(botsConfig(3, p.Roles()), 42)
	if err != nil {
		t.Fatal(err)
	}
	const dt = 1.0 / 20
	for i := 0; i < 20*300; i++ { // максимум 5 минут игрового времени
		if _, err := m.Step(dt); err != nil {
			t.Fatal(err)
		}
		if m.IsOver() {
			break
		}
	}
	if !m.IsOver() {
		t.Fatal("match with bots only did not finish in 5 minutes")
	}
	raw, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var s snap
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if !s.Over || len(s.Players) != 6 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	t.Logf("finished at tick %d, winner %v", s.Tick, m.Winner())
}

func TestDeterministic(t *testing.T) {
	p := loadProgram(t)
	run := func() string {
		m, err := p.NewMatch(botsConfig(2, p.Roles()), 7)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20*30; i++ {
			if _, err := m.Step(1.0 / 20); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := m.Snapshot()
		return string(raw)
	}
	first, second := run(), run()
	if first != second {
		t.Fatal("same seed produced different snapshots")
	}
}

func TestHumanInputAndBotToggle(t *testing.T) {
	p := loadProgram(t)
	cfg := botsConfig(1, p.Roles())
	cfg.Players[0].Bot = false
	m, err := p.NewMatch(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := m.ApplyInput("a0", json.RawMessage(`{"kind":"move","x":120,"y":100}`))
	if err != nil || !ok {
		t.Fatalf("move rejected: %v %v", ok, err)
	}
	for i := 0; i < 20; i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := m.Snapshot()
	var s snap
	_ = json.Unmarshal(raw, &s)
	if s.Players[0].Y >= 280 {
		t.Fatalf("human did not move: y=%v", s.Players[0].Y)
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"throw","x":700,"y":280}`)); ok {
		t.Fatal("throw without charge must be rejected")
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"chargeStart","x":700,"y":280}`)); !ok {
		t.Fatal("chargeStart rejected")
	}
	for i := 0; i < 10; i++ {
		_, _ = m.Step(1.0 / 20)
	}
	if ok, _ := m.ApplyInput("a0", json.RawMessage(`{"kind":"throw","x":700,"y":280,"power":0.4}`)); !ok {
		t.Fatal("throw rejected")
	}
	ev, err := m.Step(1.0 / 20)
	if err != nil {
		t.Fatal(err)
	}
	_ = ev
	if err := m.SetBot("a0", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.ApplyInput("zz", json.RawMessage(`{"kind":"move","x":1,"y":1}`)); ok {
		t.Fatal("unknown player accepted")
	}
}

// pveConfig — PvE-матч: пати из ботов на команде A, урезанная кампания.
func pveConfig(gameMode string, party int, roles []string) sim.MatchConfig {
	cfg := sim.MatchConfig{GameMode: gameMode, Mode: party, Difficulty: 1,
		Pve: &sim.PveConfig{Levels: 1, Waves: 2}}
	for i := 0; i < party; i++ {
		cfg.Players = append(cfg.Players, sim.PlayerConfig{
			ID: "a" + string(rune('0'+i)), Team: "A", Role: roles[i%len(roles)], Bot: true})
	}
	return cfg
}

type pveSnap struct {
	Over    bool   `json:"over"`
	Reason  string `json:"reason"`
	Players []struct {
		ID    string `json:"id"`
		Team  string `json:"team"`
		ET    string `json:"et"`
		Lives *int   `json:"lives"`
	} `json:"players"`
	Pve *struct {
		Objective string `json:"objective"`
		Level     int    `json:"level"`
		Wave      int    `json:"wave"`
		Phase     string `json:"phase"`
		Enemies   int    `json:"enemiesLeft"`
	} `json:"pve"`
}

// TestPveWaveMode — PvE-матч создаётся, идут волны, враги появляются на команде B,
// снапшот несёт блок pve, матч завершается с PvE-причиной в пределах потолка.
// Эндлесс + одинокий боец на «Сложном»: волны рано или поздно его выносят (wiped),
// и это не зависит от того, добьют ли боты босса.
func TestPveWaveMode(t *testing.T) {
	p := loadProgram(t)
	endless := false
	cfg := sim.MatchConfig{GameMode: "survival", Mode: 1, Difficulty: 2, Campaign: &endless}
	cfg.Players = append(cfg.Players, sim.PlayerConfig{ID: "a0", Team: "A", Role: p.Roles()[0], Bot: true})
	m, err := p.NewMatch(cfg, 99)
	if err != nil {
		t.Fatal(err)
	}
	sawEnemy, sawFighting, sawPve := false, false, false
	const dt = 1.0 / 20
	for i := 0; i < 20*600 && !m.IsOver(); i++ {
		if _, err := m.Step(dt); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if i%20 != 0 {
			continue
		}
		raw, _ := m.Snapshot()
		var s pveSnap
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if s.Pve == nil {
			t.Fatal("snapshot has no pve block")
		}
		sawPve = true
		if s.Pve.Phase == "fighting" {
			sawFighting = true
		}
		for _, pl := range s.Players {
			if pl.Team == "B" {
				sawEnemy = true
			}
			if pl.Team == "A" && pl.Lives == nil {
				t.Fatalf("party member %s has no lives field", pl.ID)
			}
		}
	}
	if !sawPve || !sawFighting || !sawEnemy {
		t.Fatalf("pve=%v fighting=%v enemy=%v", sawPve, sawFighting, sawEnemy)
	}
	if !m.IsOver() {
		t.Fatal("pve match did not finish within 10 minutes")
	}
	switch m.Reason() {
	case "cleared", "wiped", "objective", "expired":
	default:
		t.Fatalf("unexpected pve end reason %q", m.Reason())
	}
}

// TestPveDeterministic — один сид + урезанная кампания дают байт-идентичный снапшот.
func TestPveDeterministic(t *testing.T) {
	p := loadProgram(t)
	run := func() string {
		m, err := p.NewMatch(pveConfig("defense", 3, p.Roles()), 5)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20*45; i++ {
			if _, err := m.Step(1.0 / 20); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := m.Snapshot()
		return string(raw)
	}
	first, second := run(), run()
	if first != second {
		t.Fatal("same seed produced different pve snapshots")
	}
}

// BenchmarkFullLoad — целевая нагрузка: 17 матчей 4×4 (136 бойцов) при 20 тиках/с.
// Один «раунд» бенчмарка = одна игровая секунда всех матчей (17 × 20 шагов + снапшоты).
func BenchmarkFullLoad(b *testing.B) {
	p := loadProgram(b)
	const matches = 17
	ms := make([]*sim.Match, matches)
	for i := range ms {
		m, err := p.NewMatch(botsConfig(4, p.Roles()), uint32(i+1))
		if err != nil {
			b.Fatal(err)
		}
		ms[i] = m
	}
	b.ResetTimer()
	start := time.Now()
	for n := 0; n < b.N; n++ {
		for tick := 0; tick < 20; tick++ {
			for _, m := range ms {
				if _, err := m.Step(1.0 / 20); err != nil {
					b.Fatal(err)
				}
				if _, err := m.Snapshot(); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	el := time.Since(start)
	// Доля одного ядра, нужная для симуляции 17 матчей в реальном времени.
	b.ReportMetric(float64(el)/float64(time.Second)/float64(b.N)*100, "%cpu-of-one-core")
}

// TestTutorialMode — правила режима обучения: состав из одного бойца, матч не заканчивается
// ни по таймеру, ни по KO, боец-человек не опускается ниже 1 HP, соперник ставится и убирается.
func TestTutorialMode(t *testing.T) {
	p := loadProgram(t)
	cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: true,
		Players: []sim.PlayerConfig{{ID: "me", Team: "A", Role: p.Roles()[0], Bot: false}}}
	m, err := p.NewMatch(cfg, 7)
	if err != nil {
		t.Fatalf("tutorial match with a single fighter must be allowed: %v", err)
	}
	// Пять минут — штатный таймер PvP; в обучении он не срабатывает.
	for i := 0; i < 20*60*6; i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
	if m.IsOver() {
		t.Fatal("tutorial match must not end by timer")
	}

	id, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Снайпер", X: 320, Y: 200, BotLevel: 2, Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	for i := 0; i < 20*150; i++ {
		raw, err := m.Step(1.0 / 20)
		if err != nil {
			t.Fatal(err)
		}
		var events []struct {
			Type     string `json:"type"`
			TargetID string `json:"targetId"`
		}
		_ = json.Unmarshal(raw, &events)
		for _, e := range events {
			if e.Type == "hit" && e.TargetID == "me" {
				hits++
			}
			if e.Type == "ko" && e.TargetID == "me" {
				t.Fatal("human must not be knocked out in tutorial")
			}
		}
	}
	if hits == 0 {
		t.Fatal("enemy must be able to hit the player")
	}
	raw, _ := m.Snapshot()
	var s snap
	_ = json.Unmarshal(raw, &s)
	me := s.Players[0]
	if me.ID != "me" || me.HP != 1 {
		t.Fatalf("human hp must floor at 1, got %+v", me)
	}
	if m.IsOver() {
		t.Fatal("tutorial match must not end while the human is alive")
	}
	if err := m.TutorialRemove(id); err != nil {
		t.Fatal(err)
	}
	raw, _ = m.Snapshot()
	s = snap{}
	_ = json.Unmarshal(raw, &s)
	if len(s.Players) != 1 {
		t.Fatalf("enemy must be removed, got %d fighters", len(s.Players))
	}
}

// TestTutorialLockEnemy — на последнем шаге соперника нельзя добить обычным попаданием,
// пока держится замок; после tutorialLock(false) следующее попадание его выносит.
func TestTutorialLockEnemy(t *testing.T) {
	p := loadProgram(t)
	cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: true,
		Players: []sim.PlayerConfig{{ID: "me", Team: "A", Role: "Снайпер", Bot: false}}}
	m, err := p.NewMatch(cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Уводим бойца от колонны у спавна на чистую линию (y=150).
	if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"move","x":300,"y":150}`)); !ok {
		t.Fatal("move rejected")
	}
	for i := 0; i < 80; i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
	eid, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Танк", X: 560, Y: 150, Bot: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.TutorialLock(true); err != nil {
		t.Fatal(err)
	}

	enemyHP := func() (int, bool) {
		raw, _ := m.Snapshot()
		var s struct {
			Players []struct {
				ID   string `json:"id"`
				HP   int    `json:"hp"`
				Koed bool   `json:"koed"`
			} `json:"players"`
		}
		_ = json.Unmarshal(raw, &s)
		for _, pl := range s.Players {
			if pl.ID == eid {
				return pl.HP, pl.Koed
			}
		}
		return 0, true
	}
	fire := func(pw float64) {
		m.ApplyInput("me", json.RawMessage(`{"kind":"chargeStart","x":560,"y":150}`))
		for i := 0; i < 9; i++ {
			m.Step(1.0 / 20)
		}
		body, _ := json.Marshal(map[string]any{"kind": "throw", "x": 560, "y": 150, "power": pw})
		m.ApplyInput("me", body)
		for i := 0; i < 16; i++ {
			m.Step(1.0 / 20)
		}
	}

	sawFloor := false
	for i := 0; i < 14; i++ {
		fire(0.34)
		hp, koed := enemyHP()
		if koed || hp < 1 {
			t.Fatalf("locked enemy went down after %d shots (hp=%d koed=%v)", i+1, hp, koed)
		}
		if hp == 1 {
			sawFloor = true
		}
	}
	if !sawFloor {
		t.Fatal("enemy never took damage — throw geometry is off, test is not exercising the rule")
	}

	if err := m.TutorialLock(false); err != nil {
		t.Fatal(err)
	}
	down := false
	for i := 0; i < 6 && !down; i++ {
		fire(0.34)
		hp, koed := enemyHP()
		down = koed || hp <= 0
	}
	if !down {
		t.Fatal("after tutorialLock(false) the enemy must be finishable")
	}
}

// TestTutorialShortCooldown — в обучении способность возвращается за 2 секунды, а в обычном
// матче держит свой полный кулдаун. Иначе шаг про способность превращается в ожидание.
func TestTutorialShortCooldown(t *testing.T) {
	p := loadProgram(t)
	cd := func(tutorial bool) float64 {
		cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: tutorial}
		cfg.Players = []sim.PlayerConfig{{ID: "me", Team: "A", Role: "Щит"}}
		if !tutorial { // обычный матч требует полного состава
			cfg.Players = append(cfg.Players, sim.PlayerConfig{ID: "b0", Team: "B", Role: "Танк", Bot: true})
		}
		m, err := p.NewMatch(cfg, 11)
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := m.ApplyInput("me", json.RawMessage(`{"kind":"special","x":400,"y":280}`)); !ok {
			t.Fatal("способность не применилась")
		}
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
		var snap struct {
			Players []struct {
				ID string  `json:"id"`
				CD float64 `json:"cd"`
			} `json:"players"`
		}
		raw, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		for _, q := range snap.Players {
			if q.ID == "me" {
				return q.CD
			}
		}
		t.Fatal("бойца нет в снапшоте")
		return 0
	}
	if got := cd(true); got > 2 {
		t.Fatalf("в обучении кулдаун %.1f с, ожидалось не больше 2", got)
	}
	if got := cd(false); got < 10 {
		t.Fatalf("в обычном матче кулдаун стены %.1f с, ожидалось около 13.5", got)
	}
}

// TestBotShootsWhileRetreating — бот отвечает броском, даже когда игрок подошёл ближе его
// рабочей дистанции. Раньше ветка отхода выходила из ИИ до стрельбы, и Снайпер (minRange 260)
// молча пятился от подошедшего игрока — в обучении и в PvP одинаково.
func TestBotShootsWhileRetreating(t *testing.T) {
	p := loadProgram(t)
	cfg := sim.MatchConfig{Mode: 1, ArenaIndex: 0, Tutorial: true}
	cfg.Players = []sim.PlayerConfig{{ID: "me", Team: "A", Role: "Танк"}}
	m, err := p.NewMatch(cfg, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Соперник в 100 px — вдвое ближе minRange Снайпера. Игрока держим на чистой линии y=150:
	// у спавна стоит колонна, из-за неё бот считал бы линию перекрытой.
	id, err := m.TutorialSpawn(sim.TutorialSpawnOpts{Role: "Снайпер", X: 260, Y: 150, BotLevel: 2, Bot: true})
	if err != nil || id == "" {
		t.Fatalf("соперник не поставлен: %v", err)
	}
	throws := 0
	for i := 0; i < 20*30; i++ { // 30 секунд игрового времени
		if _, err := m.ApplyInput("me", json.RawMessage(`{"kind":"move","x":160,"y":150}`)); err != nil {
			t.Fatal(err)
		}
		evs, err := m.Step(1.0 / 20)
		if err != nil {
			t.Fatal(err)
		}
		var list []struct {
			Type     string `json:"type"`
			PlayerID string `json:"playerId"`
		}
		if len(evs) > 0 {
			if err := json.Unmarshal(evs, &list); err != nil {
				t.Fatal(err)
			}
		}
		for _, e := range list {
			if e.Type == "throw" && e.PlayerID == id {
				throws++
			}
		}
	}
	if throws == 0 {
		t.Fatal("бот не бросил ни разу: отход снова съедает стрельбу")
	}
}
