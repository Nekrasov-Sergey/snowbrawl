/* Обучение: скриптованный бой один на один в режиме tutorial симуляции (см. SIM_CONTRACT).
 * Соперник появляется, когда до него доходит очередь, оживает только на шаге про укрытие, а
 * игрока добить не может — правило «не ниже 1 HP» живёт в sim.js. Шаги считаются по хукам
 * ввода (события applyInput до клиента не доходят) и по снапшоту. */
window.SBTutorial = (function () {
  var Sim = window.SnowBrawlSim;

  // Проверенные свободные точки «Классики»: центр арены занят ящиком, спавн — вплотную к колонне.
  var WALK_MARK = { type: 'point', x: 300, y: 190, r: 34 };
  var CRATE = { type: 'rect', x: 438, y: 260, w: 24, h: 60 }; // деревянный ящик, снежком не пробить
  var ENEMY = { role: 'Танк', x: 560, y: 280, botLevel: 0 };

  function create(cfg) {
    var st = { game: null, step: 0, throws: 0, fullThrow: false, special: false, enemyId: null, mark: 0 };

    function drv() { return st.game; }
    function enemyOf(snap) {
      if (!snap || !st.enemyId) return null;
      for (var i = 0; i < snap.players.length; i++) if (snap.players[i].id === st.enemyId) return snap.players[i];
      return null;
    }
    // Соперник должен быть жив: если игрок добил его раньше времени, ставим нового.
    function ensureEnemy(snap, awake) {
      var g = drv(); if (!g || !g.spawnEnemy) return;
      var e = enemyOf(snap);
      if (!e || e.koed) {
        if (st.enemyId) g.removeEnemy(st.enemyId);
        st.enemyId = g.spawnEnemy({ role: ENEMY.role, x: ENEMY.x, y: ENEMY.y, botLevel: ENEMY.botLevel, bot: !!awake });
        return;
      }
      g.setBot(st.enemyId, !!awake);
    }

    var STEPS = [
      {
        text: 'Дойдите до зелёной метки. WASD на клавиатуре, левый стик на телефоне.',
        marks: function () { return [WALK_MARK]; },
        check: function (c) { return !!c.me && Math.hypot(c.me.x - WALK_MARK.x, c.me.y - WALK_MARK.y) < WALK_MARK.r; }
      },
      {
        text: 'Зажмите ЛКМ (на телефоне — правый стик) до полного замаха и отпустите: чем дольше замах, тем дальше летит снежок.',
        enter: function () { st.fullThrow = false; },
        check: function () { return st.fullThrow; }
      },
      {
        text: 'Бросьте ещё раз. После каждого броска боец перезаряжается — пока идёт перезарядка, новый замах не начнётся.',
        enter: function () { st.throws = 0; },
        check: function () { return st.throws > 0; }
      },
      {
        text: 'Попадите снежком в ящик. Укрытия ловят снежки — за ними можно спрятаться.',
        marks: function () { return [CRATE]; },
        check: function (c) { return c.event('wallHit'); }
      },
      {
        text: 'Появился соперник — он пока не двигается. Попадите в него: три попадания выводят бойца из строя.',
        enter: function (c) { ensureEnemy(c.snap, false); },
        marks: function (c) {
          var e = enemyOf(c.snap);
          return e ? [{ type: 'point', x: e.x, y: e.y, r: 28 }] : [];
        },
        check: function (c) {
          return c.event('hit', function (e) { return e.targetId === st.enemyId; }) ||
            c.event('ko', function (e) { return e.targetId === st.enemyId; });
        }
      },
      {
        text: 'Теперь соперник бросает в ответ. Укройтесь: встаньте так, чтобы между вами и ним было препятствие.',
        enter: function (c) { ensureEnemy(c.snap, true); },
        check: function (c) {
          var e = enemyOf(c.snap);
          if (!e || !c.me || e.koed) return false;
          var d = Math.hypot(e.x - c.me.x, e.y - c.me.y);
          var power = Math.min(1, Math.max(0, (d + 20 - 140) / 380));
          return !Sim.canHitTarget(cfg.obstacles(c.snap), e, c.me.x, c.me.y, power);
        },
        leave: function (c) { ensureEnemy(c.snap, false); }
      },
      {
        text: 'Последнее: примените способность (Q, ПКМ или кнопка справа) и добейте соперника.',
        enter: function (c) { st.special = false; ensureEnemy(c.snap, false); },
        check: function (c) {
          var e = enemyOf(c.snap);
          return st.special && (!e || e.koed || c.event('ko', function (ev) { return ev.targetId === st.enemyId; }));
        }
      }
    ];

    function render(c) {
      var step = STEPS[st.step];
      cfg.box.hidden = !st.game || !step;
      if (!step) { cfg.marks([]); return; }
      cfg.stepEl.textContent = 'Шаг ' + (st.step + 1) + ' из ' + STEPS.length + '. ' + step.text;
      cfg.marks(step.marks ? step.marks(c) : []);
    }

    function ctx(fr) {
      var snap = fr && fr.snap ? fr.snap : (st.game ? st.game.lastSnap : null);
      var events = (fr && fr.events) || [];
      var me = null, meId = st.game ? st.game.meId : null;
      if (snap) {
        for (var i = 0; i < snap.players.length; i++) if (snap.players[i].id === meId) me = snap.players[i];
      }
      return {
        me: me, snap: snap,
        event: function (type, pred) {
          for (var k = 0; k < events.length; k++) {
            if (events[k].type === type && (!pred || pred(events[k]))) return true;
          }
          return false;
        }
      };
    }

    function advance(c) {
      var step = STEPS[st.step];
      if (step && step.leave) step.leave(c);
      st.step++;
      if (st.step >= STEPS.length) {
        cfg.box.hidden = true;
        cfg.marks([]);
        cfg.done();
        cfg.finish();
        return;
      }
      cfg.toast('Готово!');
      var next = STEPS[st.step];
      if (next.enter) next.enter(c);
      render(c);
    }

    function onFrame(fr) {
      if (!st.game || st.step >= STEPS.length) return;
      var c = ctx(fr);
      if (STEPS[st.step].check(c)) { advance(c); return; }
      render(c); // метки следуют за соперником
    }

    return {
      start: function () {
        st.step = 0; st.throws = 0; st.fullThrow = false; st.special = false; st.enemyId = null;
        st.game = cfg.start({ onFrame: onFrame, onStop: function () { cfg.box.hidden = true; st.game = null; } });
        var c = ctx(null);
        if (STEPS[0].enter) STEPS[0].enter(c);
        render(c);
      },
      // Хуки слоя намерений: бросок и способность считаем по факту действия.
      onThrow: function (power) {
        if (!st.game) return;
        st.throws++;
        if (power >= 0.9) st.fullThrow = true;
      },
      onSpecial: function () { if (st.game) st.special = true; },
      skip: function () { if (st.game && st.step < STEPS.length) advance(ctx(null)); },
      stop: function () { st.game = null; cfg.box.hidden = true; cfg.marks([]); },
      active: function () { return !!st.game; }
    };
  }

  return { create: create };
})();
