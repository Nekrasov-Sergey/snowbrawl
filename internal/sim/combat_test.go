package sim_test

import (
	"encoding/json"
	"math"
	"testing"
)

// Правила боя 1.18.0: запинка от попадания, заморозка Фризера, удар оземь Танка, поворотный
// щит Щита и сплэш Бомбера. Сценарии идут на JS прямо по состоянию симуляции: так снежок летит
// ровно туда, куда нужно, без замаха и разброса, а тест проверяет правило, а не прицел.

const combatHelpers = `
function duel(roleA, roleB, bx) {
  var st = Sim.createMatch({ gameMode: 'pvp', mode: 1, arenaIndex: 0, players: [
    { id: 'me', team: 'A', role: roleA, bot: false }, { id: 'foe', team: 'B', role: roleB, bot: false }] }, 3);
  var me = st.players[0], foe = st.players[1];
  // Чистая линия y=150 «Классики»: без колонн и ящика между бойцами.
  me.x = 300; me.y = 150; me.moveTarget = { x: 300, y: 150 }; me.specialCooldown = 0;
  foe.x = bx || 500; foe.y = 150; foe.moveTarget = { x: foe.x, y: 150 }; foe.specialCooldown = 0;
  return st;
}
function who(st, id) { return st.players.filter(function (p) { return p.id === id; })[0]; }
// Настильный снежок из (x,y) в сторону (tx,ty) от бойца owner.
function ball(st, owner, x, y, tx, ty, opts) {
  opts = opts || {};
  var d = Math.hypot(tx - x, ty - y) || 1, v = opts.speed || 600;
  var b = { id: st.nextSnowballId++, x: x, y: y, z: opts.z || 0, vx: (tx - x) / d * v, vy: (ty - y) / d * v, vz: 0, t: 0,
    flightDuration: opts.fd || 2, team: owner.team, ownerId: owner.id, radius: opts.explosive ? 9 : 6,
    explosive: !!opts.explosive, freeze: !!opts.freeze, frost: !!opts.freeze, splash: !!opts.splash,
    flat: true, sniped: false };
  st.snowballs.push(b);
  return b;
}
// Шаги по 1/120 с; копит события и останавливается на первом событии type, если оно задано.
function run(st, sec, stopOn) {
  var evs = [];
  for (var i = 0; i < Math.round(sec * 120); i++) {
    var e = Sim.step(st, 1 / 120);
    for (var k = 0; k < e.length; k++) evs.push(e[k]);
    if (stopOn && e.some(function (x) { return x.type === stopOn; })) break;
  }
  return evs;
}
function has(evs, type, pred) { return evs.some(function (e) { return e.type === type && (!pred || pred(e)); }); }
`

func combatRun(t *testing.T, script string, out any) {
	t.Helper()
	vm := aimVM(t)
	if _, err := vm.RunString(combatHelpers); err != nil {
		t.Fatalf("helpers: %v", err)
	}
	v, err := vm.RunString("JSON.stringify((function () {" + script + "})())")
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	if err := json.Unmarshal([]byte(v.String()), out); err != nil {
		t.Fatalf("result %s: %v", v.String(), err)
	}
}

// TestHitFlinch — обычное попадание даёт короткую запинку HIT_FLINCH при любом остатке HP и
// сбивает замах. Танка («Броня») попадание не сбивает вовсе.
func TestHitFlinch(t *testing.T) {
	t.Parallel()
	var r struct {
		Stun1, Stun2 float64
		HP           int
		Charging     bool
		TankStun     float64
		TankHP       int
		TankCharging bool
	}
	combatRun(t, `
    var st = duel('Раннер', 'Раннер'), me = who(st, 'me'), foe = who(st, 'foe');
    Sim.applyInput(st, 'foe', { kind: 'chargeStart', x: 300, y: 150 });
    ball(st, me, 340, 150, 500, 150); run(st, 1, 'hit');
    var s1 = foe.stunTimer, ch = foe.charging;
    run(st, 0.5);
    ball(st, me, 340, 150, 500, 150); run(st, 1, 'hit');
    var s2 = foe.stunTimer, hp = foe.hp;
    var st2 = duel('Раннер', 'Танк'), me2 = who(st2, 'me'), tank = who(st2, 'foe');
    Sim.applyInput(st2, 'foe', { kind: 'chargeStart', x: 300, y: 150 });
    ball(st2, me2, 340, 150, 500, 150); run(st2, 1, 'hit');
    return { Stun1: s1, Stun2: s2, HP: hp, Charging: ch, TankStun: tank.stunTimer, TankHP: tank.hp, TankCharging: tank.charging };`, &r)
	if r.HP != 1 {
		t.Fatalf("после двух попаданий hp=%d, ожидалось 1", r.HP)
	}
	for i, s := range []float64{r.Stun1, r.Stun2} {
		if s <= 0.15 || s > 0.2 {
			t.Fatalf("попадание %d: запинка %.3f с, ожидалось около 0.2", i+1, s)
		}
	}
	if r.Charging {
		t.Fatal("попадание не сбило замах")
	}
	if r.TankHP != 2 || r.TankStun != 0 || !r.TankCharging {
		t.Fatalf("Танк после попадания: hp=%d stun=%.2f charging=%v, ожидалось 2, 0, true", r.TankHP, r.TankStun, r.TankCharging)
	}
}

// TestFreezeStun — прямое попадание ледяным снежком замораживает на FREEZE_STUN (Танка — вдвое
// короче) и оставляет наледь.
func TestFreezeStun(t *testing.T) {
	t.Parallel()
	var r struct {
		Stun, TankStun float64
		Frost          bool
		Freeze         bool
	}
	combatRun(t, `
    var st = duel('Фризер', 'Раннер'), me = who(st, 'me'), foe = who(st, 'foe');
    ball(st, me, 340, 150, 500, 150, { freeze: true }); var evs = run(st, 1, 'hit');
    var st2 = duel('Фризер', 'Танк'), me2 = who(st2, 'me'), tank = who(st2, 'foe');
    ball(st2, me2, 340, 150, 500, 150, { freeze: true }); run(st2, 1, 'hit');
    return { Stun: foe.stunTimer, TankStun: tank.stunTimer, Frost: st.groundFx.length > 0,
      Freeze: has(evs, 'hit', function (e) { return e.freeze; }) };`, &r)
	if r.Stun <= 0.95 || r.Stun > 1 {
		t.Fatalf("заморозка %.3f с, ожидалось около 1", r.Stun)
	}
	if r.TankStun <= 0.45 || r.TankStun > 0.5 {
		t.Fatalf("заморозка Танка %.3f с, ожидалось около 0.5", r.TankStun)
	}
	if !r.Frost || !r.Freeze {
		t.Fatalf("наледь=%v, hit.freeze=%v — ожидалось оба", r.Frost, r.Freeze)
	}
}

// TestSlam — удар оземь срабатывает после замаха: цель в радиусе отброшена и оглушена (у края
// откат слабее), дальняя — нет, вражеский снежок рядом с Танком гибнет. HP удар не снимает.
func TestSlam(t *testing.T) {
	t.Parallel()
	var r struct {
		EarlyX, NearX, NearStun float64
		NearHP                  int
		FarX, FarStun           float64
		EdgeX, EdgeStun         float64
		BallGone, Moved         bool
	}
	combatRun(t, `
    var st = duel('Танк', 'Раннер', 360), me = who(st, 'me'), foe = who(st, 'foe');
    Sim.applyInput(st, 'me', { kind: 'special', x: 360, y: 150 });
    Sim.applyInput(st, 'me', { kind: 'move', x: 200, y: 150 });
    var b = ball(st, foe, 340, 200, 340, 900, { speed: 5, z: 5, fd: 5 });
    run(st, 0.4); var early = foe.x, moved = Math.abs(me.x - 300) > 0.5;
    run(st, 0.1);
    var near = { x: foe.x, stun: foe.stunTimer, hp: foe.hp };
    var st2 = duel('Танк', 'Раннер', 500), foe2 = who(st2, 'foe');
    Sim.applyInput(st2, 'me', { kind: 'special', x: 500, y: 150 });
    run(st2, 0.55);
    var st3 = duel('Танк', 'Раннер', 440), foe3 = who(st3, 'foe');
    Sim.applyInput(st3, 'me', { kind: 'special', x: 440, y: 150 });
    run(st3, 0.55);
    return { EarlyX: early, NearX: near.x, NearStun: near.stun, NearHP: near.hp, FarX: foe2.x, FarStun: foe2.stunTimer, EdgeX: foe3.x, EdgeStun: foe3.stunTimer,
      BallGone: st.snowballs.indexOf(b) < 0, Moved: moved };`, &r)
	if r.EarlyX != 360 {
		t.Fatalf("до конца замаха цель сдвинулась: x=%.1f", r.EarlyX)
	}
	if r.Moved {
		t.Fatal("во время замаха Танк двигался")
	}
	if r.NearX < 400 || r.NearStun <= 0.9 || r.NearHP != 3 {
		t.Fatalf("цель вплотную: x=%.1f stun=%.2f hp=%d, ожидался отлёт ~50 px, оглушение ~1, hp 3", r.NearX, r.NearStun, r.NearHP)
	}
	if r.FarX != 500 || r.FarStun != 0 {
		t.Fatalf("цель за радиусом задета: x=%.1f stun=%.2f", r.FarX, r.FarStun)
	}
	// У края круга откат слабее, чем вплотную, а оглушение то же.
	if edge, near := r.EdgeX-440, r.NearX-360; edge <= 15 || edge >= near || r.EdgeStun <= 0.9 {
		t.Fatalf("цель у края: откат %.1f px (вплотную %.1f), stun=%.2f", edge, near, r.EdgeStun)
	}
	if !r.BallGone {
		t.Fatal("снежок рядом с Танком пережил удар оземь")
	}
}

// TestBastionBlocksFront — поднятый щит гасит снежки спереди, сзади они попадают, а к новому
// прицелу щит доворачивает плавно, не быстрее BASTION_TURN.
func TestBastionBlocksFront(t *testing.T) {
	t.Parallel()
	var r struct {
		Front, FrontHit, Bubble, RearPop, Slow bool
		Turn01, Turn1                          float64
		Bst                                    float64
	}
	combatRun(t, `
    var st = duel('Щит', 'Снайпер'), me = who(st, 'me'), foe = who(st, 'foe');
    Sim.applyInput(st, 'me', { kind: 'special', x: 500, y: 160 });
    ball(st, foe, 450, 160, 300, 160); var evs = run(st, 0.5);
    var front = has(evs, 'shieldBlock', function (e) { return e.targetId === 'me'; });
    var frontHit = has(evs, 'hit') || has(evs, 'bubblePop');
    var snap = Sim.snapshot(st).players[0];
    ball(st, foe, 150, 160, 300, 160); var evs2 = run(st, 0.5);
    var rear = has(evs2, 'bubblePop', function (e) { return e.targetId === 'me'; });
    var a0 = me.bastionAng;
    Sim.applyInput(st, 'me', { kind: 'aim', x: 300, y: 400 });
    run(st, 0.1); var t01 = Math.abs(me.bastionAng - a0);
    run(st, 1); var t1 = me.bastionAng;
    return { Front: front, FrontHit: frontHit, Bubble: snap.bubble, RearPop: rear, Slow: snap.slow,
      Turn01: t01, Turn1: t1, Bst: snap.bst };`, &r)
	if !r.Front || r.FrontHit || !r.Bubble {
		t.Fatalf("снежок спереди: shieldBlock=%v попал=%v пузырь цел=%v", r.Front, r.FrontHit, r.Bubble)
	}
	if !r.RearPop {
		t.Fatal("снежок сзади не дошёл до бойца")
	}
	if !r.Slow || r.Bst <= 0 {
		t.Fatalf("под щитом slow=%v bst=%.1f", r.Slow, r.Bst)
	}
	if r.Turn01 < 0.3 || r.Turn01 > 0.41 {
		t.Fatalf("за 0.1 с щит повернул на %.2f рад, ожидалось около 0.4", r.Turn01)
	}
	if math.Abs(r.Turn1-math.Pi/2) > 0.1 {
		t.Fatalf("через секунду щит смотрит на %.2f рад, ожидалось около π/2", r.Turn1)
	}
}

// TestBastionExplosion — взрыв перед щитом не ранит Щита, зато ломает щит.
func TestBastionExplosion(t *testing.T) {
	t.Parallel()
	var r struct {
		HP                 int
		Bubble, Broken, Up bool
	}
	combatRun(t, `
    var st = duel('Щит', 'Бомбер'), me = who(st, 'me'), foe = who(st, 'foe');
    Sim.applyInput(st, 'me', { kind: 'special', x: 500, y: 160 });
    ball(st, foe, 450, 160, 300, 160, { explosive: true }); var evs = run(st, 0.5);
    return { HP: me.hp, Bubble: me.bubble, Broken: has(evs, 'bastionBreak'), Up: Sim.snapshot(st).players[0].bst > 0 };`, &r)
	if r.HP != 3 || !r.Bubble {
		t.Fatalf("взрыв перед щитом задел бойца: hp=%d пузырь=%v", r.HP, r.Bubble)
	}
	if !r.Broken || r.Up {
		t.Fatalf("щит после взрыва: bastionBreak=%v поднят=%v", r.Broken, r.Up)
	}
}

// TestBomberSplash — пассив «Осколки»: снежок Бомбера ранит в BOMBER_SPLASH_R от точки падения,
// дальше — нет; прямое попадание не удваивает урон; ящики сплэш не ломает.
func TestBomberSplash(t *testing.T) {
	t.Parallel()
	var r struct {
		NearHP, FarHP, DirectHP, Crate int
		SplashFlag                     bool
	}
	combatRun(t, `
    function drop(st, owner, x, y) { ball(st, owner, x, y, x + 1, y, { speed: 0.01, fd: 0.01, splash: true }); return run(st, 0.1); }
    var st = duel('Бомбер', 'Раннер'), me = who(st, 'me'), foe = who(st, 'foe');
    var evs = drop(st, me, 520, 150);
    var st2 = duel('Бомбер', 'Раннер'); drop(st2, who(st2, 'me'), 540, 150);
    var st3 = duel('Бомбер', 'Раннер'); ball(st3, who(st3, 'me'), 340, 150, 500, 150, { splash: true }); run(st3, 1);
    var st4 = duel('Бомбер', 'Раннер'); drop(st4, who(st4, 'me'), 438, 260);
    var crate = st4.arenaObstacles.filter(function (o) { return o.x === 438; })[0];
    return { NearHP: foe.hp, FarHP: who(st2, 'foe').hp, DirectHP: who(st3, 'foe').hp, Crate: crate.hp,
      SplashFlag: has(evs, 'hit', function (e) { return e.splash; }) };`, &r)
	if r.NearHP != 2 || !r.SplashFlag {
		t.Fatalf("падение в 20 px: hp=%d hit.splash=%v, ожидалось 2 и true", r.NearHP, r.SplashFlag)
	}
	if r.FarHP != 3 {
		t.Fatalf("падение в 40 px ранило: hp=%d", r.FarHP)
	}
	if r.DirectHP != 2 {
		t.Fatalf("прямое попадание со сплэшем: hp=%d, ожидалось 2", r.DirectHP)
	}
	if r.Crate != 4 {
		t.Fatalf("сплэш повредил ящик: %d/4", r.Crate)
	}
}

// TestFreezerSlowEqual — наледь и аура «Стужа» замедляют одинаково, на FREEZER_SLOW, и вместе не
// складываются: путь бойца за полсекунды в наледи, в ауре и в обеих сразу одинаков и вдвое короче
// свободного.
func TestFreezerSlowEqual(t *testing.T) {
	t.Parallel()
	var r struct{ Free, Frost, Aura, Both float64 }
	combatRun(t, `
    function walk(frost, aura) {
      var st = duel('Раннер', 'Фризер', aura ? 360 : 800), me = who(st, 'me'), fz = who(st, 'foe');
      fz.y = aura ? 150 : 500; fz.moveTarget = { x: fz.x, y: fz.y };
      if (frost) st.groundFx.push({ id: 1, kind: 'frost', x: 300, y: 150, r: 500, team: 'B', expiresAt: 1e9 });
      me.moveTarget = { x: 300, y: 530 };
      var y0 = me.y; run(st, 0.5); return me.y - y0;
    }
    return { Free: walk(false, false), Frost: walk(true, false), Aura: walk(false, true), Both: walk(true, true) };`, &r)
	for name, d := range map[string]float64{"наледь": r.Frost, "аура": r.Aura, "наледь и аура": r.Both} {
		if math.Abs(d-r.Free/2) > 3 {
			t.Fatalf("%s: путь %.1f px, свободный %.1f — ожидалось вдвое короче", name, d, r.Free)
		}
	}
}
