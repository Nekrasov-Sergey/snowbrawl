/* Обучение: скриптованные бои один на один в режиме tutorial симуляции (см. SIM_CONTRACT).
 *
 * Сценариев семь: «Основы» плюс по одному на каждого героя. Движок один, различаются только
 * таблицы шагов. Игрока добить нельзя — правило «не ниже 1 HP» живёт в sim.js; соперника в
 * геройских сценариях тоже держим замком (tutorialLock), иначе шаг можно сделать невыполнимым,
 * добив его раньше времени.
 *
 * Шаги проверяются по снапшоту и по событиям step(). События applyInput (special, dash,
 * wallPlaced) до клиента НЕ доходят — step обнуляет state.events, — поэтому применение
 * способности видно только по снапшоту (armed / dash / walls) и по хукам onThrow/onSpecial.
 */
window.SBTutorial = (function () {
  var Sim = window.SnowBrawlSim;

  // Проверенные свободные точки «Классики»: центр арены занят ящиком, спавн — вплотную к колонне.
  var WALK_MARK = { type: 'point', x: 300, y: 190, r: 34 };
  var CRATE = { type: 'rect', x: 438, y: 260, w: 24, h: 60 }; // деревянный ящик, снежком не пробить
  var LOW_COVER = { type: 'rect', x: 415, y: 448, w: 70, h: 22 }; // низкое укрытие: навес его перелетает
  var DASH_MARK = { type: 'point', x: 300, y: 400, r: 40 };
  var SNIPE_SPOT = { type: 'point', x: 150, y: 448, r: 34 };
  // Настильный выстрел идёт у самой земли, а в двух шагах от спавна стоит колонна (x 168–192,
  // y 205–295): с места старта он в неё и упирается. Поэтому шаг ставит игрока на метку.
  var FLAT_SPOT = { type: 'point', x: 300, y: 190, r: 34 };

  function markAt(p, r) { return { type: 'point', x: p.x, y: p.y, r: r || 28 }; }

  // ---- Основы: те же семь шагов, что и раньше ----
  var BASICS = [
    {
      text: 'Дойдите до зелёной метки. WASD на клавиатуре, левый стик на телефоне.',
      marks: function () { return [WALK_MARK]; },
      check: function (c) { return !!c.me && c.dist(c.me, WALK_MARK) < WALK_MARK.r; }
    },
    {
      text: 'Зажмите ЛКМ (на телефоне — правый стик) до полного замаха и отпустите: чем дольше замах, тем дальше летит снежок.',
      check: function (c) { return c.flags.fullThrow; }
    },
    {
      text: 'Бросьте ещё раз. После каждого броска боец перезаряжается — пока идёт перезарядка, новый замах не начнётся.',
      check: function (c) { return c.flags.throws > 0; }
    },
    {
      text: 'Попадите снежком в ящик. Укрытия ловят снежки — за ними можно спрятаться.',
      marks: function () { return [CRATE]; },
      check: function (c) { return c.event('wallHit'); }
    },
    {
      text: 'Появился соперник — он пока не двигается. Попадите в него: три попадания выводят бойца из строя.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      check: function (c) { return c.hitEnemy(); }
    },
    {
      text: 'Теперь соперник бросает в ответ. Укройтесь: встаньте так, чтобы между вами и ним было препятствие.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: true, level: 2 },
      check: function (c) {
        if (!c.enemy || !c.me) return false;
        var d = c.dist(c.enemy, c.me);
        var power = Math.min(1, Math.max(0, (d + 20 - 140) / 380));
        // Здесь именно canHitTarget: соперник бросает без способности, и это ровно та оценка,
        // по которой целятся боты.
        return !Sim.canHitTarget(c.obstacles(), c.enemy, c.me.x, c.me.y, power);
      }
    },
    {
      text: 'Последнее: примените способность (Q, ПКМ или кнопка справа) и добейте соперника. До способности он держится на 1 HP.',
      // Замок снимается в onSpecial: пока способность не применена, добить нельзя.
      enemy: { role: 'Танк', x: 560, y: 280, awake: false, lock: true },
      check: function (c) { return c.flags.special && (c.flags.enemyKo || !c.enemy || c.enemy.koed); }
    }
  ];

  // ---- Геройские сценарии: только способности и тактика, без повтора «Основ» ----
  // Ни один шаг не требует добить соперника, поэтому шаг нельзя сделать невыполнимым.
  var RUNNER = [
    {
      text: 'Нажмите Q (на телефоне — кнопка справа): Раннер делает рывок в сторону прицела.',
      check: function (c) { return !!c.me && c.me.dash; }
    },
    {
      text: 'Рывок проносит бойца на 120 шагов. Подойдите к метке и пролетите через неё рывком.',
      marks: function () { return [DASH_MARK]; },
      check: function (c) { return !!c.me && c.me.dash && c.dist(c.me, DASH_MARK) < DASH_MARK.r + 5; }
    },
    {
      text: 'Снайпер напротив целится в вас. Во время рывка вы неуязвимы — уйдите рывком из-под выстрела.',
      enemy: { role: 'Снайпер', x: 700, y: 280, awake: true, level: 2 },
      check: function (c) {
        if (!c.me || !c.me.iframe || !c.enemy) return false;
        return c.enemy.charging || c.ballNear(120);
      }
    }
  ];

  var TANK = [
    {
      text: 'Нажмите Q: Танк идёт тараном в сторону прицела — это и разгон, и удар.',
      check: function (c) { return !!c.me && c.me.dash; }
    },
    {
      text: 'Таран сбивает с ног. Наведитесь на соперника и протараньте его.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      check: function (c) { return c.event('knockback', function (e) { return e.targetId === c.enemyId; }); }
    },
    {
      text: 'Теперь соперник двигается сам. Встретьте его тараном на ходу: он сбивает и оглушает.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: true },
      check: function (c) { return c.event('knockback', function (e) { return e.targetId === c.enemyId; }); }
    }
  ];

  var SNIPER = [
    {
      text: 'Нажмите Q: следующий бросок пойдёт по прямой, быстро и на всю арену. Луч прицела станет длиннее.',
      check: function (c) { return !!c.me && c.me.armed === 'snipe'; }
    },
    {
      text: 'Встаньте на метку и бросьте заряженный выстрел в соперника: он летит настильно, без дуги, и потому вязнет в любой преграде на пути.',
      enemy: { role: 'Танк', x: 700, y: 190, awake: false },
      marks: function (c) { return c.enemy ? [FLAT_SPOT, markAt(c.enemy)] : [FLAT_SPOT]; },
      check: function (c) { return c.flags.snipeBall && c.hitEnemy(); }
    },
    {
      text: 'Настильный выстрел вязнет в укрытии, а навес его перелетает. Встаньте на метку и попадите в соперника обычным броском на полном замахе.',
      enemy: { role: 'Танк', x: 700, y: 448, awake: false },
      marks: function () { return [SNIPE_SPOT, LOW_COVER]; },
      check: function (c) { return c.hitEnemy(); }
    }
  ];

  var BOMBER = [
    {
      text: 'Нажмите Q: следующий снежок взорвётся при попадании.',
      check: function (c) { return !!c.me && c.me.armed === 'explosive'; }
    },
    {
      text: 'Взрыв бьёт по площади — точное попадание не нужно. Бросьте взрывной снежок рядом с соперником.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy, 58)] : []; },
      check: function (c) {
        if (!c.enemy) return false;
        var e = c.enemy;
        return c.event('explosion', function (ev) { return Math.hypot(ev.x - e.x, ev.y - e.y) <= 58; });
      }
    },
    {
      text: 'Пассив «Сапёр»: взрывы ломают деревянные укрытия. Накройте взрывом ящик в центре арены.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function () { return [CRATE]; },
      check: function (c) {
        var d = c.destrNear(CRATE.x, CRATE.y);
        return !!d && d.hp < d.maxHp;
      }
    }
  ];

  var FREEZER = [
    {
      text: 'Нажмите Q: следующий снежок оставит на земле наледь.',
      check: function (c) { return !!c.me && c.me.armed === 'frost'; }
    },
    {
      text: 'Бросьте его в соперника: на земле останется наледь, и в ней враги двигаются медленнее.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      check: function (c) {
        if (!c.enemy) return false;
        var e = c.enemy, zones = c.fx('frost', 'A');
        for (var i = 0; i < zones.length; i++) {
          if (Math.hypot(zones[i].x - e.x, zones[i].y - e.y) <= zones[i].r) return true;
        }
        return false;
      }
    },
    {
      text: 'Пассив «Стужа»: рядом с Фризером враги и так медленнее. Подойдите к сопернику вплотную.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      // Проверяем именно замедление рядом со мной: флаг slow ставит и наледь, поэтому важна дистанция.
      check: function (c) { return !!c.me && !!c.enemy && c.dist(c.me, c.enemy) <= 52 && c.enemy.slow; }
    }
  ];

  var SHIELD = [
    {
      text: 'Наведитесь на соперника и нажмите Q: перед вами встанет снежная стена и перекроет линию броска.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      check: function (c) {
        if (!c.me || !c.enemy) return false;
        var walls = c.walls('A');
        for (var i = 0; i < walls.length; i++) {
          if (c.segDist(c.me, c.enemy, walls[i]) <= 60) return true;
        }
        return false;
      }
    },
    {
      text: 'Стена ловит снежки. Попадите в свою стену — если она растаяла, поставьте новую (Q).',
      enemy: { role: 'Танк', x: 560, y: 280, awake: false },
      hint: function (c) { return c.walls('A').length ? '' : ' Стена растаяла — поставьте новую (Q).'; },
      check: function (c) {
        var walls = c.walls('A');
        return c.event('wallHit', function (ev) {
          for (var i = 0; i < walls.length; i++) {
            if (Math.hypot(walls[i].x - ev.x, walls[i].y - ev.y) <= 45) return true;
          }
          return false;
        });
      }
    },
    {
      text: 'Пассив «Закалка»: щитовой пузырь гасит одно попадание целиком. Дайте сопернику попасть в вас.',
      enemy: { role: 'Танк', x: 560, y: 280, awake: true, level: 2 },
      // Своя стена перекрывает линию броска и для соперника: пока она между вами, он вообще
      // не бросает (снежок вязнет в стене независимо от команды). Подсказываем это прямо.
      hint: function (c) {
        if (!c.me || !c.enemy) return '';
        var walls = c.walls('A');
        for (var i = 0; i < walls.length; i++) {
          if (c.segDist(c.me, c.enemy, walls[i]) <= 60) return ' Отойдите от своей стены — из-за неё соперник не бросит.';
        }
        return '';
      },
      check: function (c) {
        return c.event('bubblePop', function (ev) { return ev.targetId === c.meId; }) || (!!c.me && c.me.bubble === false);
      }
    }
  ];

  // id совпадает с ABILITIES[role].active.id: годится и для ключа localStorage, и для отладки.
  var SCENARIOS = [
    { id: 'basics', title: 'Основы', role: null, about: 'Ходьба, замах, перезарядка, укрытия и способность', steps: BASICS },
    { id: 'dash', title: 'Раннер: рывок', role: 'Раннер', about: 'Рывок с неуязвимостью и уход из-под выстрела', steps: RUNNER },
    { id: 'taram', title: 'Танк: таран', role: 'Танк', about: 'Таран сбивает и оглушает даже на ходу', steps: TANK },
    { id: 'snipe', title: 'Снайпер: прицельный выстрел', role: 'Снайпер', about: 'Настильный выстрел и когда нужен навес', steps: SNIPER },
    { id: 'explosive', title: 'Бомбер: взрывной снежок', role: 'Бомбер', about: 'Урон по площади и снос укрытий', steps: BOMBER },
    { id: 'frost', title: 'Фризер: ледяная волна', role: 'Фризер', about: 'Наледь на земле и аура холода', steps: FREEZER },
    { id: 'wall', title: 'Щит: снежная стена', role: 'Щит', about: 'Стена как укрытие и щитовой пузырь', steps: SHIELD }
  ];

  function scenarioById(id) {
    for (var i = 0; i < SCENARIOS.length; i++) if (SCENARIOS[i].id === id) return SCENARIOS[i];
    return SCENARIOS[0];
  }

  /**
   * create(cfg):
   *  cfg.box, cfg.stepEl  — плашка задачи и её текст
   *  cfg.start(hooks, scn) → объект матча (обучение всегда оффлайн, роль берётся из сценария)
   *  cfg.marks(list)      — метки на арене, cfg.obstacles(snap) — препятствия для проверок
   *  cfg.flash/cfg.toast  — короткое «Готово!»
   *  cfg.done(scnId)      — сценарий пройден (отметка в localStorage)
   *
   * Шаг: { text, enter?, check, marks?, hint?, enemy? }, где
   * enemy = { role, x, y, awake, lock?, level? } — level 0..2 (по умолчанию 0).
   *  cfg.finish(scn)      — показать табло
   */
  function create(cfg) {
    var st = { scn: SCENARIOS[0], step: 0, flags: {}, game: null, enemyId: null, awake: null, lock: null };

    function drv() { return st.game; }
    function steps() { return st.scn.steps; }
    function enemyOf(snap) {
      if (!snap || !st.enemyId) return null;
      for (var i = 0; i < snap.players.length; i++) if (snap.players[i].id === st.enemyId) return snap.players[i];
      return null;
    }
    function dropEnemy() {
      var g = drv();
      if (g && st.enemyId && g.removeEnemy) g.removeEnemy(st.enemyId);
      st.enemyId = null; st.awake = null;
    }
    function setLock(on) {
      var g = drv(); if (!g || !g.tutorialLock || st.lock === on) return;
      st.lock = on; g.tutorialLock(on);
    }

    /**
     * Привести соперника к тому, что описано в шаге: нет — поставить, добили — поставить нового,
     * неподвижного оттащили тараном — вернуть на место. Одно место вместо «инициализации в enter»:
     * именно из-за неё шаг «укройтесь» когда-то зависал навсегда.
     */
    function syncEnemy(snap) {
      var g = drv(); if (!g || !g.spawnEnemy) return;
      var spec = steps()[st.step] && steps()[st.step].enemy;
      if (!spec) { dropEnemy(); setLock(false); return; }
      // По умолчанию соперник неубиваем. flags.unlocked снимает замок до конца шага —
      // иначе покадровая сверка возвращала бы его сразу после применённой способности.
      setLock(spec.lock !== false && !st.flags.unlocked);
      var e = enemyOf(snap);
      var lost = !e || e.koed || (!spec.awake && Math.hypot(e.x - spec.x, e.y - spec.y) > 140);
      if (lost) {
        dropEnemy();
        // level задаёт шаг: на шагах «дайте ему попасть» уровень 0 мажет мимо стоящего игрока
        // (разброс прицела Танка ±160 px), поэтому там ставится 2.
        st.enemyId = g.spawnEnemy({ role: spec.role, x: spec.x, y: spec.y, botLevel: spec.level || 0, bot: !!spec.awake });
        st.awake = !!spec.awake;
        return;
      }
      if (st.awake !== !!spec.awake) { st.awake = !!spec.awake; g.setBot(st.enemyId, st.awake); }
    }

    function ctx(fr) {
      var snap = fr && fr.snap ? fr.snap : (st.game ? st.game.lastSnap : null);
      var events = (fr && fr.events) || [];
      var meId = st.game ? st.game.meId : null;
      var me = null;
      if (snap) {
        for (var i = 0; i < snap.players.length; i++) if (snap.players[i].id === meId) me = snap.players[i];
      }
      var enemy = enemyOf(snap);
      var c = {
        me: me, meId: meId, snap: snap, enemy: enemy, enemyId: st.enemyId, flags: st.flags,
        time: snap ? snap.time : 0,
        dist: function (a, b) { return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : Infinity; },
        event: function (type, pred) {
          for (var k = 0; k < events.length; k++) {
            if (events[k].type === type && (!pred || pred(events[k]))) return true;
          }
          return false;
        },
        obstacles: function () { return cfg.obstacles(snap); },
        walls: function (team) {
          var out = [], list = (snap && snap.walls) || [];
          for (var i = 0; i < list.length; i++) if (!team || list[i].team === team) out.push(list[i]);
          return out;
        },
        fx: function (kind, team) {
          var out = [], list = (snap && snap.fx) || [];
          for (var i = 0; i < list.length; i++) {
            if ((!kind || list[i].kind === kind) && (!team || list[i].team === team)) out.push(list[i]);
          }
          return out;
        },
        destrNear: function (x, y) {
          var list = (snap && snap.destr) || [], best = null, bd = 40;
          for (var i = 0; i < list.length; i++) {
            var d = Math.hypot(list[i].x - x, list[i].y - y);
            if (d < bd) { bd = d; best = list[i]; }
          }
          return best;
        },
        // Летит ли в меня снежок соперника ближе чем radius.
        ballNear: function (radius) {
          var list = (snap && snap.balls) || [];
          if (!me) return false;
          for (var i = 0; i < list.length; i++) {
            if (list[i].team === 'A') continue;
            if (Math.hypot(list[i].x - me.x, list[i].y - me.y) <= radius) return true;
          }
          return false;
        },
        // Расстояние от точки p до отрезка a—b: нужно проверке «стена перекрыла линию броска».
        segDist: function (a, b, p) {
          var dx = b.x - a.x, dy = b.y - a.y, len2 = dx * dx + dy * dy;
          if (!len2) return Math.hypot(p.x - a.x, p.y - a.y);
          var t = Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2));
          return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
        }
      };
      c.hitEnemy = function () {
        return c.event('hit', function (e) { return e.targetId === st.enemyId; }) ||
          c.event('ko', function (e) { return e.targetId === st.enemyId; });
      };
      c.koEnemy = function () { return c.event('ko', function (e) { return e.targetId === st.enemyId; }); };
      return c;
    }

    function render(c) {
      var step = steps()[st.step];
      cfg.box.hidden = !st.game || !step;
      if (!step) { cfg.marks([]); return; }
      var hint = step.hint ? step.hint(c) : '';
      cfg.stepEl.textContent = 'Шаг ' + (st.step + 1) + ' из ' + steps().length + '. ' + step.text + hint;
      cfg.marks(step.marks ? step.marks(c) : []);
    }

    function advance(c) {
      st.step++;
      st.flags = {}; // счётчики шага: броски, полный замах, способность
      if (st.step >= steps().length) {
        cfg.box.hidden = true;
        cfg.marks([]);
        setLock(false);
        dropEnemy();
        cfg.done(st.scn.id);
        cfg.finish(st.scn);
        return;
      }
      (cfg.flash || cfg.toast)('Готово!');
      syncEnemy(c.snap);
      render(ctx(null));
    }

    function onFrame(fr) {
      if (!st.game || st.step >= steps().length) return;
      var c = ctx(fr);
      // KO соперника отмечаем ДО пересоздания: syncEnemy поставит нового бойца, и событие ko
      // с прежним id уже ни с чем не сойдётся — шаг «добейте соперника» так и не зачлось бы.
      if (c.event('ko', function (e) { return e.targetId === st.enemyId; })) st.flags.enemyKo = true;
      // Заряженный настильный выстрел виден в снапшоте флагом sn. В хук onThrow смотреть
      // нельзя: снежок появляется только в следующем снапшоте, а хук срабатывает раньше.
      var balls = (c.snap && c.snap.balls) || [];
      for (var b = 0; b < balls.length; b++) {
        if (balls[b].team === 'A' && balls[b].sn) { st.flags.snipeBall = true; break; }
      }
      syncEnemy(c.snap); // каждый кадр: соперник обязан быть таким, как описано в шаге
      c = ctx(fr);       // соперник мог быть заменён — берём свежую ссылку
      if (steps()[st.step].check(c)) { advance(c); return; }
      render(c); // метки следуют за соперником
    }

    return {
      scenarios: SCENARIOS,
      /** Запустить сценарий по id ('basics' — роль берёт cfg.role()). */
      start: function (id) {
        st.scn = scenarioById(id);
        st.step = 0; st.flags = {}; st.enemyId = null; st.awake = null; st.lock = null;
        st.game = cfg.start({ onFrame: onFrame, onStop: function () { cfg.box.hidden = true; st.game = null; } }, st.scn);
        var c = ctx(null);
        syncEnemy(c.snap);
        render(c);
      },
      /** Текущий сценарий (для табло). */
      current: function () { return st.scn; },
      // Хуки слоя намерений: бросок и способность считаем по факту действия.
      onThrow: function (power) {
        if (!st.game) return;
        st.flags.throws = (st.flags.throws || 0) + 1;
        if (power >= 0.9) st.flags.fullThrow = true;
      },
      onSpecial: function () {
        if (!st.game) return;
        st.flags.special = true;
        // В «Основах» последний шаг требует добить соперника — замок снимаем только там.
        if (st.scn.id === 'basics' && st.step === steps().length - 1) { st.flags.unlocked = true; setLock(false); }
      },
      skip: function () { if (st.game && st.step < steps().length) advance(ctx(null)); },
      stop: function () {
        setLock(false);
        st.game = null; st.enemyId = null; cfg.box.hidden = true; cfg.marks([]);
      },
      active: function () { return !!st.game; }
    };
  }

  return { create: create, SCENARIOS: SCENARIOS };
})();
