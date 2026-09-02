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
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
		HP int     `json:"hp"`
	} `json:"players"`
}

func TestCompileExports(t *testing.T) {
	p := loadProgram(t)
	if p.Version() == "" || p.ArenaCount() < 1 || len(p.Roles()) < 6 {
		t.Fatalf("bad program meta: %q %d %v", p.Version(), p.ArenaCount(), p.Roles())
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
