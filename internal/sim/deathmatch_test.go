package sim_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/dop251/goja"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// «Бой насмерть» (deathmatch, с 1.17.0): выбитый встаёт на своей базе, когда растаял труп
// (CORPSE_MS), и 3 с неуязвим (RESPAWN_IFRAME_MS); матч идёт до killLimit выбиваний команды,
// запасной таймер — 10 минут, по нему побеждает больший счёт.

type dmSnap struct {
	Time      float64 `json:"time"`
	Over      bool    `json:"over"`
	KillLimit int     `json:"killLimit"`
	Players   []struct {
		ID     string  `json:"id"`
		Team   string  `json:"team"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		HP     int     `json:"hp"`
		Koed   bool    `json:"koed"`
		Iframe bool    `json:"iframe"`
		Kills  int     `json:"k"`
	} `json:"players"`
}

type dmEvent struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	TargetID string `json:"targetId"`
}

func dmConfig(p *sim.Program, mode, killLimit int) sim.MatchConfig {
	cfg := botsConfig(mode, p.Roles())
	cfg.GameMode = "deathmatch"
	cfg.KillLimit = killLimit
	return cfg
}

func readDmSnap(t *testing.T, m *sim.Match) dmSnap {
	t.Helper()
	raw, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var s dmSnap
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s dmSnap) teamKills(team string) int {
	k := 0
	for _, p := range s.Players {
		if p.Team == team {
			k += p.Kills
		}
	}
	return k
}

type rectObs struct {
	Type string  `json:"type"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	R    float64 `json:"r"`
}

// arenaObstacles — препятствия арены прямо из sim.js, чтобы тест не держал их копию.
func arenaObstacles(t *testing.T, arena int) []rectObs {
	t.Helper()
	vm := goja.New()
	if _, err := vm.RunProgram(sharedAimJS); err != nil {
		t.Fatal(err)
	}
	v, err := vm.RunString(`JSON.stringify(SnowBrawlSim.ARENAS[` + string(rune('0'+arena)) + `].obstacles)`)
	if err != nil {
		t.Fatal(err)
	}
	var obs []rectObs
	if err := json.Unmarshal([]byte(v.String()), &obs); err != nil {
		t.Fatal(err)
	}
	return obs
}

func insideObstacle(obs []rectObs, x, y float64) bool {
	for _, o := range obs {
		if o.Type == "rect" {
			if x >= o.X-o.W/2 && x <= o.X+o.W/2 && y >= o.Y-o.H/2 && y <= o.Y+o.H/2 {
				return true
			}
		} else if math.Hypot(x-o.X, y-o.Y) <= o.R {
			return true
		}
	}
	return false
}

// TestDeathmatchPlaysToKillLimit — матч ботов 1×1 до 5 выбиваний: бойцы встают на своей базе
// с неуязвимостью, в неуязвимых не попадают, а матч кончается на лимите с причиной "kills".
func TestDeathmatchPlaysToKillLimit(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	m, err := p.NewMatch(dmConfig(p, 1, 5), 21)
	if err != nil {
		t.Fatal(err)
	}
	obs := arenaObstacles(t, 0)
	const dt = 1.0 / 20
	respawnedAt := map[string]float64{} // id → время последнего возрождения
	respawns, lastTime := 0, 0.0
	for i := 0; i < 20*600 && !m.IsOver(); i++ {
		raw, err := m.Step(dt)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		lastTime += dt * 1000
		var evs []dmEvent
		if err := json.Unmarshal(raw, &evs); err != nil {
			t.Fatalf("events: %v", err)
		}
		for _, e := range evs {
			switch e.Type {
			case "hit", "ko":
				// Неуязвимость — 3 с; тик — 50 мс, берём с запасом на подшаги.
				if at, ok := respawnedAt[e.TargetID]; ok && lastTime-at < 2900 {
					t.Fatalf("%s: попадание через %.0f мс после возрождения", e.TargetID, lastTime-at)
				}
			case "respawn":
				respawns++
				respawnedAt[e.ID] = lastTime
				s := readDmSnap(t, m)
				for _, pl := range s.Players {
					if pl.ID != e.ID {
						continue
					}
					if pl.Koed || pl.HP != 3 || !pl.Iframe {
						t.Fatalf("после возрождения %+v, ожидались hp=3, koed=false, iframe=true", pl)
					}
					baseX := 160.0
					if pl.Team == "B" {
						baseX = 740
					}
					if math.Abs(pl.X-baseX) > 70 {
						t.Fatalf("%s встал не на своей базе: x=%.0f", pl.ID, pl.X)
					}
					if insideObstacle(obs, pl.X, pl.Y) {
						t.Fatalf("%s встал внутри препятствия: (%.0f, %.0f)", pl.ID, pl.X, pl.Y)
					}
				}
			}
		}
	}
	if !m.IsOver() {
		t.Fatal("deathmatch 1×1 до 5 не закончился за 10 минут")
	}
	if m.Reason() != "kills" {
		t.Fatalf("reason=%q, ожидался kills", m.Reason())
	}
	s := readDmSnap(t, m)
	if s.KillLimit != 5 {
		t.Fatalf("killLimit в снапшоте %d, ожидался 5", s.KillLimit)
	}
	w := m.Winner()
	if w == "" || s.teamKills(w) < 5 {
		t.Fatalf("победитель %q со счётом A=%d B=%d", w, s.teamKills("A"), s.teamKills("B"))
	}
	if respawns == 0 {
		t.Fatal("ни одного возрождения")
	}
	t.Logf("A=%d B=%d, возрождений %d, %.0f с", s.teamKills("A"), s.teamKills("B"), respawns, s.Time/1000)
}

// TestDeathmatchKillLimitDefault — лимит не из списка заменяется значением для размера команд,
// одинаково в sim.js и в Program.KillLimitFor (им лимит нормализует сервер).
func TestDeathmatchKillLimitDefault(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	for _, c := range []struct{ mode, want, got int }{
		{1, 0, 5}, {2, 7, 10}, {3, 0, 15}, {4, 0, 20}, {1, 20, 20}, {3, 5, 5},
	} {
		m, err := p.NewMatch(dmConfig(p, c.mode, c.want), 1)
		if err != nil {
			t.Fatal(err)
		}
		if s := readDmSnap(t, m); s.KillLimit != c.got {
			t.Errorf("mode=%d want=%d: sim дал %d, ожидалось %d", c.mode, c.want, s.KillLimit, c.got)
		}
		if got := p.KillLimitFor(c.mode, c.want); got != c.got {
			t.Errorf("mode=%d want=%d: KillLimitFor дал %d, ожидалось %d", c.mode, c.want, got, c.got)
		}
	}
	// В классическом PvP лимита нет, и в снапшоте поля нет.
	m, err := p.NewMatch(botsConfig(1, p.Roles()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if s := readDmSnap(t, m); s.KillLimit != 0 {
		t.Fatalf("pvp: killLimit=%d, ожидался 0", s.KillLimit)
	}
}

// TestDeathmatchTimeoutByScore — лимит не набран: по таймеру побеждает больший счёт, при
// равенстве ничья; причина — timeout.
func TestDeathmatchTimeoutByScore(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	cfg := dmConfig(p, 2, 20)
	cfg.DurationMs = 40000
	m, err := p.NewMatch(cfg, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20*60 && !m.IsOver(); i++ {
		if _, err := m.Step(1.0 / 20); err != nil {
			t.Fatal(err)
		}
	}
	if !m.IsOver() || m.Reason() != "timeout" {
		t.Fatalf("over=%v reason=%q, ожидался timeout", m.IsOver(), m.Reason())
	}
	s := readDmSnap(t, m)
	a, b := s.teamKills("A"), s.teamKills("B")
	want := ""
	if a > b {
		want = "A"
	} else if b > a {
		want = "B"
	}
	if m.Winner() != want {
		t.Fatalf("счёт A=%d B=%d, победитель %q, ожидался %q", a, b, m.Winner(), want)
	}
}

// TestDeathmatchDeterministic — один сид даёт один и тот же исход: случайные точки возрождения
// берутся из rng состояния, а не из Math.random.
func TestDeathmatchDeterministic(t *testing.T) {
	t.Parallel()
	p := loadProgram(t)
	run := func() string {
		m, err := p.NewMatch(dmConfig(p, 2, 5), 77)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20*120; i++ {
			if _, err := m.Step(1.0 / 20); err != nil {
				t.Fatal(err)
			}
		}
		raw, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if first, second := run(), run(); first != second {
		t.Fatal("deathmatch недетерминирован на одном сиде")
	}
}
