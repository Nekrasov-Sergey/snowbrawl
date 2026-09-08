package sim_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/dop251/goja"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
)

// aimPath — чистая функция для клиентского луча прицела, серверный sim.Program её наружу не
// пробрасывает. Дублировать проброс ради теста незачем, поэтому здесь свой goja-рантайм.
func aimVM(t testing.TB) *goja.Runtime {
	t.Helper()
	src, err := snowbrawl.Web.ReadFile(snowbrawl.SimPath)
	if err != nil {
		t.Fatalf("read sim.js: %v", err)
	}
	vm := goja.New()
	if _, err := vm.RunString(string(src)); err != nil {
		t.Fatalf("run sim.js: %v", err)
	}
	// Хелперы: препятствия арены (как их собирает клиент) и вызов aimPath.
	const helpers = `
var Sim = SnowBrawlSim;
function arenaObs(i) {
  return Sim.ARENAS[i].obstacles.filter(function (o) { return o.hp == null; });
}
function aim(role, armed, x, y, tx, ty, power, arena) {
  return Sim.aimPath(arenaObs(arena || 0), { x: x, y: y, role: role, armed: armed }, tx, ty, power);
}`
	if _, err := vm.RunString(helpers); err != nil {
		t.Fatalf("helpers: %v", err)
	}
	return vm
}

type aimPt struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
	Live bool    `json:"live"`
	Hit  bool    `json:"hit"`
}

type aimResult struct {
	N       int     `json:"n"`
	Pts     []aimPt `json:"pts"`
	EndX    float64 `json:"endX"`
	EndY    float64 `json:"endY"`
	Blocked bool    `json:"blocked"`
	Flat    bool    `json:"flat"`
	Travel  float64 `json:"travel"`
	Flight  float64 `json:"flight"`
}

// evalJSON выполняет JS-выражение и разбирает результат через JSON — так же, как это делает
// рантайм симуляции на границе Go и JS.
func evalJSON(t testing.TB, vm *goja.Runtime, expr string, dst any) {
	t.Helper()
	v, err := vm.RunString("JSON.stringify(" + expr + ")")
	if err != nil {
		t.Fatalf("eval %s: %v", expr, err)
	}
	if err := json.Unmarshal([]byte(v.String()), dst); err != nil {
		t.Fatalf("unmarshal %s: %v", expr, err)
	}
}

func aimCall(t testing.TB, vm *goja.Runtime, expr string) aimResult {
	t.Helper()
	var res aimResult
	evalJSON(t, vm, expr, &res)
	if res.N != len(res.Pts) {
		t.Fatalf("n=%d, но точек %d", res.N, len(res.Pts))
	}
	return res
}

func TestAimPathGeometry(t *testing.T) {
	vm := aimVM(t)
	// Чистое поле «Классики»: сверху слева препятствий нет.
	p := aimCall(t, vm, `aim('Танк', null, 100, 100, 700, 100, 1)`)
	if p.Blocked {
		t.Fatal("на чистой линии луч не должен упираться")
	}
	if math.Abs(p.EndX-(100+p.Travel)) > 1 || math.Abs(p.EndY-100) > 1 {
		t.Fatalf("конец луча %.1f,%.1f при дальности %.1f", p.EndX, p.EndY, p.Travel)
	}
	if p.Flat {
		t.Fatal("обычный бросок не настильный")
	}
	// Первая точка — сам боец: в упор снежок задевает.
	if !p.Pts[0].Hit || p.Pts[0].Z != 0 {
		t.Fatalf("первая точка должна быть у ног бойца: %+v", p.Pts[0])
	}
	// Середина дуги выше порога поражения, хвост — ниже.
	mid, tail := p.Pts[len(p.Pts)/2], p.Pts[len(p.Pts)-1]
	if mid.Hit || mid.Z <= 14 {
		t.Fatalf("середина дуги должна идти выше HIT_Z: %+v", mid)
	}
	if !tail.Hit || !tail.Live {
		t.Fatalf("хвост дуги должен поражать: %+v", tail)
	}
}

func TestAimPathBlocked(t *testing.T) {
	vm := aimVM(t)
	// Колонна «Классики»: x 168..192, y 205..295, высота 20.
	p := aimCall(t, vm, `aim('Танк', null, 100, 250, 400, 250, 0.15)`)
	if !p.Blocked {
		t.Fatal("луч должен упереться в колонну")
	}
	if p.EndX < 160 || p.EndX > 200 {
		t.Fatalf("конец луча %.1f, ожидался в габарите колонны", p.EndX)
	}
	after := false
	for _, q := range p.Pts {
		if after && (q.Live || q.Hit) {
			t.Fatalf("после преграды точки должны быть мёртвыми: %+v", q)
		}
		if !q.Live {
			after = true
		}
	}
	if !after {
		t.Fatal("ни одна точка не помечена мёртвой")
	}
}

func TestAimPathOverCover(t *testing.T) {
	vm := aimVM(t)
	// Низкое укрытие (415,448,w70,h22) высотой 16: навес его перелетает.
	p := aimCall(t, vm, `aim('Снайпер', null, 150, 448, 700, 448, 1)`)
	if p.Blocked {
		t.Fatal("навес должен перелететь низкое укрытие")
	}
	over := false
	for _, q := range p.Pts {
		if q.X > 380 && q.X < 450 && q.Z > 16 {
			over = true
		}
	}
	if !over {
		t.Fatal("над укрытием снежок должен быть выше его высоты")
	}
	// Настильный выстрел по той же линии вязнет в укрытии — на этом контрасте построено обучение.
	flat := aimCall(t, vm, `aim('Снайпер', 'snipe', 150, 448, 700, 448, 1)`)
	if !flat.Blocked {
		t.Fatal("настильный выстрел должен упереться в укрытие")
	}
}

func TestAimPathRoleAndSnipe(t *testing.T) {
	vm := aimVM(t)
	tank := aimCall(t, vm, `aim('Танк', null, 100, 100, 700, 100, 1)`)
	sniper := aimCall(t, vm, `aim('Снайпер', null, 100, 100, 700, 100, 1)`)
	if math.Abs(sniper.Travel/tank.Travel-1.15) > 0.001 {
		t.Fatalf("у Снайпера дальность %.1f против %.1f", sniper.Travel, tank.Travel)
	}
	if math.Abs(sniper.Flight/tank.Flight-0.82) > 0.001 {
		t.Fatalf("у Снайпера время полёта %.3f против %.3f", sniper.Flight, tank.Flight)
	}
	snipe := aimCall(t, vm, `aim('Снайпер', 'snipe', 100, 100, 700, 100, 1)`)
	if !snipe.Flat || snipe.Travel != 620 {
		t.Fatalf("заряженный выстрел: flat=%v travel=%.1f", snipe.Flat, snipe.Travel)
	}
	for _, q := range snipe.Pts {
		if q.Z != 0 {
			t.Fatalf("настильный выстрел идёт по прямой, а не по дуге: %+v", q)
		}
		if q.Live && !q.Hit {
			t.Fatalf("настильный выстрел всю дорогу на уровне поражения: %+v", q)
		}
	}
}

// TestAimPathMatchesFlight — главный тест: предсказание луча сверяется с настоящим полётом
// снежка в матче обучения. Ловит любое расхождение прицела с симуляцией.
func TestAimPathMatchesFlight(t *testing.T) {
	vm := aimVM(t)
	const scenario = `
function flight(role, armed, tx, ty, power) {
  var st = Sim.createMatch({ gameMode: 'pvp', mode: 1, arenaIndex: 0, tutorial: true,
    players: [{ id: 'me', team: 'A', role: role, bot: false, nick: 'Вы' }] }, 7);
  var me = Sim.snapshot(st).players[0];
  var pred = Sim.aimPath(arenaObs(0), { x: me.x, y: me.y, role: role, armed: armed }, tx, ty, power);
  if (armed) Sim.applyInput(st, 'me', { kind: 'special', x: tx, y: ty });
  Sim.applyInput(st, 'me', { kind: 'chargeStart', x: tx, y: ty });
  for (var acc = 0; acc < power * Sim.CHARGE_FULL_MS; acc += 50) Sim.step(st, 0.05);
  Sim.applyInput(st, 'me', { kind: 'throw', x: tx, y: ty, power: power });
  // Мелкий шаг: иначе последний снимок быстрого снежка отстаёт от точки гибели на десятки px.
  var last = null, wall = false;
  for (var i = 0; i < 4000; i++) {
    var evs = Sim.step(st, 1 / 480);
    for (var e = 0; e < evs.length; e++) if (evs[e].type === 'wallHit') { wall = true; last = evs[e]; }
    var balls = Sim.snapshot(st).balls;
    if (!balls.length) break;
    if (!wall) last = balls[0];
  }
  return { pred: pred, last: last, wall: wall, from: { x: me.x, y: me.y } };
}`
	if _, err := vm.RunString(scenario); err != nil {
		t.Fatalf("scenario: %v", err)
	}

	type flightResult struct {
		Pred aimResult `json:"pred"`
		Last *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"last"`
		Wall bool `json:"wall"`
		From struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"from"`
	}

	cases := []struct {
		role, armed string
		tx, ty      float64
		power       float64
	}{
		{"Танк", "", 900, 280, 1},        // вправо от спавна — колонна на пути
		{"Танк", "", 160, 40, 1},         // вверх по чистому полю
		{"Танк", "", 160, 40, 0.3},       // слабый бросок
		{"Снайпер", "", 160, 40, 1},      // пассив «Точность»
		{"Снайпер", "snipe", 160, 40, 1}, // настильный выстрел
		{"Раннер", "", 900, 200, 0.6},
	}
	for _, c := range cases {
		name := fmt.Sprintf("%s/%s/%.1f", c.role, c.armed, c.power)
		t.Run(name, func(t *testing.T) {
			var res flightResult
			armed := "null"
			if c.armed != "" {
				armed = "'" + c.armed + "'"
			}
			evalJSON(t, vm, fmt.Sprintf("flight('%s', %s, %v, %v, %v)", c.role, armed, c.tx, c.ty, c.power), &res)
			if res.Last == nil {
				t.Fatal("снежок не найден: сценарий не бросил")
			}
			if res.Wall != res.Pred.Blocked {
				t.Fatalf("преграда: симуляция %v, луч %v (конец луча %.1f,%.1f)", res.Wall, res.Pred.Blocked, res.Pred.EndX, res.Pred.EndY)
			}
			d := math.Hypot(res.Last.X-res.Pred.EndX, res.Last.Y-res.Pred.EndY)
			limit := 8.0
			if res.Wall {
				limit = 15 // шаг сэмпла луча грубее шага физики
			}
			if d > limit {
				t.Fatalf("конец полёта %.1f,%.1f, луч обещал %.1f,%.1f (расхождение %.1f px)",
					res.Last.X, res.Last.Y, res.Pred.EndX, res.Pred.EndY, d)
			}
		})
	}
}
