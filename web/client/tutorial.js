/* Обучение: скриптованные бои один на один в режиме tutorial симуляции (см. SIM_CONTRACT).
 *
 * Сценариев семь: «Основы» (всегда Раннер, шесть шагов) плюс по одному на каждого героя —
 * четыре шага одной формы: применить способность → применить её осмысленно → почувствовать
 * пассивку → настоящий бой с добиванием. Последний шаг одинаков не ради симметрии: именно в нём
 * игрок впервые чувствует темп перезарядки своего героя и цену промаха.
 *
 * Движок один, различаются только таблицы шагов. Игрока добить нельзя — правило «не ниже 1 HP»
 * живёт в sim.js. Соперника держим замком (tutorialLock) на шагах-упражнениях и снимаем замок
 * на шаге боя: без замка упражнение можно сделать невыполнимым, добив соперника раньше времени.
 *
 * Шаги проверяются по снапшоту и по событиям step(). События applyInput (special, dash,
 * wallPlaced) до клиента НЕ доходят — step обнуляет state.events, — поэтому применение
 * способности определяется по фронту поля cd в снапшоте: этот признак работает и с мыши, и с
 * сенсорной кнопки, в отличие от хука onSpecial, который зависит от пути ввода.
 */
window.SBTutorial = (function () {
  var Sim = window.SnowBrawlSim;

  // Проверенные точки «Классики». Два места на арене портят шаги, если про них забыть:
  // у спавна (160,280) в двух шагах стоит колонна (x 168–192, y 205–295, высота 20) — почти
  // любой бросок вправо от спавна вязнет в ней, — а ящик (438,260) висит на линии от спавна к
  // центру. Поэтому всё, во что нужно попадать, стоит на свободной полосе y=190 (чиста от 300
  // до 700), и шаги сначала выводят игрока на неё меткой.
  var WALK_MARK = { type: 'point', x: 300, y: 190, r: 34 };
  var SHOOTER = { x: 700, y: 190 };  // соперник, который должен в вас попасть: линия чистая
  var DUMMY = { x: 560, y: 190 };    // манекен: на той же чистой полосе, что и метка игрока
  var FIGHT = { x: 700, y: 380 };    // соперник шага «бой»: он и игрок всё равно двигаются
  var CRATE = { type: 'rect', x: 438, y: 260, w: 24, h: 60 }; // деревянный ящик, снежком не пробить
  var LOW_COVER = { type: 'rect', x: 415, y: 448, w: 70, h: 22 }; // низкое укрытие: навес его перелетает
  var DASH_MARK = { type: 'point', x: 300, y: 400, r: 40 };
  var SNIPE_SPOT = { type: 'point', x: 150, y: 448, r: 34 };
  // Точка в тени ящика от стрелка на (700,190): оттуда он геометрически не может добросить.
  var HIDE_MARK = { type: 'point', x: 380, y: 275, r: 30 };
  // Настильный выстрел идёт у самой земли, а в двух шагах от спавна стоит колонна: с места
  // старта он в неё и упирается. Поэтому шаг ставит игрока на метку.
  var FLAT_SPOT = { type: 'point', x: 300, y: 190, r: 34 };

  function markAt(p, r) { return { type: 'point', x: p.x, y: p.y, r: r || 28 }; }

  // ---- Основы: движение, бросок, перезарядка, укрытия и бой. Способности здесь нет — их
  // раскрывают геройские уроки, и дублировать первый шаг каждого из них незачем. ----
  var BASICS = [
    {
      text: 'Дойдите до метки. WASD на клавиатуре, левый стик на телефоне.',
      marks: function () { return [WALK_MARK]; },
      check: function (c) { return !!c.me && c.dist(c.me, WALK_MARK) < WALK_MARK.r; },
      hint: function (c) { return c.inStep > 10000 ? ' Метка — зелёный круг слева от центра арены.' : ''; }
    },
    {
      text: 'Зажмите ЛКМ (на телефоне — правый стик) и держите до полного замаха, потом отпустите. Чем длиннее замах, тем дальше летит снежок.',
      check: function (c) { return c.flags.fullThrow || (c.flags.chargedFull && c.flags.throws > 0); },
      hint: function (c) {
        return c.flags.throws > 0 && !c.flags.fullThrow ? ' Замах был короткий: держите, пока полоска над бойцом не заполнится.' : '';
      }
    },
    {
      text: 'Бросьте ещё два раза. После броска боец перезаряжается — вокруг него идёт жёлтая дуга, и новый замах не начнётся, пока она не погаснет.',
      check: function (c) { return c.flags.throws >= 2; },
      hint: function (c) { return c.inStep > 9000 ? ' Нажать можно и во время перезарядки: бросок встанет в очередь, дуга станет зелёной.' : ''; }
    },
    {
      text: 'Попадите снежком в деревянный ящик. Укрытия ловят снежки — обычным броском ящик не пробить.',
      marks: function () { return [CRATE]; },
      // Проверяем радиус вокруг ящика: событие wallHit эмитится при попадании в любое
      // препятствие, и раньше шаг закрывался попаданием в колонну у спавна.
      check: function (c) {
        return c.event('wallHit', function (ev) { return Math.hypot(ev.x - CRATE.x, ev.y - CRATE.y) <= 44; });
      },
      hint: function (c) { return c.inStep > 12000 ? ' Подойдите ближе и бросьте вполсилы: на слабом замахе снежок идёт низко.' : ''; }
    },
    {
      text: 'Соперник бросает в вас. Спрячьтесь: встаньте так, чтобы ящик оказался между вами.',
      enemy: { role: 'Танк', x: SHOOTER.x, y: SHOOTER.y, awake: true, level: 1 },
      marks: function () { return [HIDE_MARK, CRATE]; },
      // Сначала нужно оказаться на виду, иначе шаг зачитывается в момент входа: игрок
      // появляется у спавна, и ящик уже стоит на линии.
      check: function (c) {
        if (!c.enemy || !c.me) return false;
        var d = c.dist(c.enemy, c.me);
        var power = Math.min(1, Math.max(0, (d + 20 - 140) / 380));
        var exposed = Sim.canHitTarget(c.obstacles(), c.enemy, c.me.x, c.me.y, power);
        if (exposed) { c.flags.exposed = true; return false; }
        return !!c.flags.exposed;
      },
      hint: function (c) {
        if (!c.flags.exposed) return ' Сначала выйдите на открытое место — пусть соперник вас увидит.';
        return c.inStep > 15000 ? ' Держите ящик ровно между собой и соперником.' : '';
      }
    },
    {
      text: 'Бой. Три попадания выводят бойца из строя — добейте соперника.',
      enemy: { role: 'Танк', x: FIGHT.x, y: FIGHT.y, awake: true, level: 0, lock: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      check: function (c) { return c.flags.enemyKo || (!!c.enemy && c.enemy.koed); },
      hint: function (c) { return c.inStep > 25000 ? ' Бейте на полном замахе с дистанции: Танк медленный, но вблизи опасен.' : ''; }
    }
  ];

  // ---- Геройские сценарии: четыре шага одной формы ----
  // Последний шаг у всех героев одинаков по смыслу — настоящий бой с добиванием. Соперник
  // подобран так, чтобы способность в нём решала: Бомберу — Щит (бот ставит стену при низком
  // HP, а взрыв сносит её целиком), Фризеру — Раннер (замедление отбирает его главное).
  function reloadSec(role) { return ((Sim.RELOAD_MS[role] || 900) / 1000).toFixed(1); }

  // Метки шага, где нужно попасть в манекена: сначала место для игрока, потом сам манекен.
  // Без метки игрок бросает от спавна и не понимает, почему снежок исчезает в колонне.
  function laneMarks(c) { return c.enemy ? [WALK_MARK, markAt(c.enemy)] : [WALK_MARK]; }
  function laneHint(c) {
    return c.me && c.dist(c.me, WALK_MARK) > 90
      ? ' Встаньте на метку: от спавна бросок упирается в колонну.' : '';
  }

  function fightStep(role, enemyRole) {
    return {
      text: 'Настоящий бой: примените способность и добейте соперника. Три попадания — и он выведен из строя, а каждый промах стоит ' + reloadSec(role) + ' с перезарядки.',
      enemy: { role: enemyRole, x: FIGHT.x, y: FIGHT.y, awake: true, level: 1, lock: false },
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      // Оба флага липкие, поэтому порядок «сначала добил, потом применил» тоже считается.
      // Если соперника добили без способности, syncEnemy поставит нового — тупика нет.
      check: function (c) { return c.flags.special && (c.flags.enemyKo || (!!c.enemy && c.enemy.koed)); },
      hint: function (c) {
        if (!c.flags.special && c.inStep > 12000) return ' Способность ещё не применена — Q, ПКМ или кнопка справа.';
        return c.flags.special && c.inStep > 30000 ? ' Бросайте с дистанции и не стойте на месте.' : '';
      }
    };
  }

  var RUNNER = [
    {
      text: 'Нажмите Q (на телефоне — кнопка справа): рывок бросает бойца в сторону прицела.',
      marks: function () { return [DASH_MARK]; },
      check: function (c) { return !!c.me && c.me.dash; },
      hint: function (c) { return c.inStep > 12000 ? ' На ПК работает и ПКМ. В обучении кулдаун всего 2 секунды.' : ''; }
    },
    {
      text: 'Во время рывка вы неуязвимы. Встаньте на метку, дождитесь броска Снайпера и уйдите рывком из-под снежка.',
      enemy: { role: 'Снайпер', x: SHOOTER.x, y: SHOOTER.y, awake: true, level: 2 },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy)] : [WALK_MARK]; },
      check: function (c) { return !!c.me && c.me.iframe && !!c.enemy && (c.enemy.charging || c.ballNear(120)); },
      hint: function (c) {
        return c.me && c.dist(c.me, WALK_MARK) > 60 && c.inStep > 6000
          ? ' Встаньте на метку: только с открытой полосы Снайпер вас видит.' : '';
      }
    },
    {
      text: 'Пассив «Второе дыхание»: на последнем HP Раннер бежит быстрее. Дайте Снайперу попасть дважды и пробегите на одном HP — разницу видно сразу.',
      enemy: { role: 'Снайпер', x: SHOOTER.x, y: SHOOTER.y, awake: true, level: 2 },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy)] : [WALK_MARK]; },
      // Секунда бега, а не просто «hp === 1»: иначе шаг закрывается в момент попадания, и
      // прочувствовать прибавку скорости игрок не успевает. Умереть в обучении нельзя.
      check: function (c) { return c.accum('run', !!c.me && c.me.hp === 1 && c.me.moving, 1000); },
      hint: function (c) {
        if (!c.me) return '';
        if (c.me.hp > 1) return ' Не прячьтесь: сначала нужно получить два попадания.';
        return ' Теперь просто бегите — отсчёт идёт, пока вы двигаетесь.';
      }
    },
    fightStep('Раннер', 'Танк')
  ];

  var TANK = [
    {
      text: 'Нажмите Q: таран несёт бойца вперёд — это и разгон, и удар.',
      check: function (c) { return !!c.me && c.me.dash; }
    },
    {
      text: 'Таран сбивает с ног и оглушает. Подойдите к сопернику и протараньте его.',
      enemy: { role: 'Танк', x: DUMMY.x, y: DUMMY.y, awake: false },
      marks: laneMarks,
      check: function (c) { return c.event('knockback', function (ev) { return ev.targetId === c.enemyId; }); },
      hint: function (c) { return c.inStep > 15000 ? ' Подойдите ближе и цельтесь прямо в него: таран идёт по прицелу.' : ''; }
    },
    {
      text: 'Пассив «Броня»: попадания оглушают Танка вдвое короче, чем других. Встаньте на метку, дайте Снайперу попасть и сразу идите дальше.',
      enemy: { role: 'Снайпер', x: SHOOTER.x, y: SHOOTER.y, awake: true, level: 2 },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy)] : [WALK_MARK]; },
      check: function (c) { return c.event('hit', function (ev) { return ev.targetId === c.meId; }); },
      hint: function (c) { return c.inStep > 15000 ? ' Стойте на открытом месте: из-за ящика соперник не бросит.' : ''; }
    },
    fightStep('Танк', 'Снайпер')
  ];

  var SNIPER = [
    {
      text: 'Нажмите Q: следующий бросок пойдёт по прямой, быстро и далеко — луч прицела станет длиннее.',
      check: function (c) { return !!c.me && c.me.armed === 'snipe'; }
    },
    {
      text: 'Настильный выстрел идёт у самой земли и вязнет в любой преграде. Встаньте на метку — оттуда линия чистая — и попадите заряженным выстрелом.',
      enemy: { role: 'Танк', x: SHOOTER.x, y: SHOOTER.y, awake: false },
      marks: function (c) { return c.enemy ? [FLAT_SPOT, markAt(c.enemy)] : [FLAT_SPOT]; },
      check: function (c) { return c.flags.snipeBall && c.hitEnemy(); },
      hint: function (c) {
        return c.me && c.dist(c.me, FLAT_SPOT) > 60 ? ' У спавна колонна: с места старта выстрел упирается в неё.' : '';
      }
    },
    {
      text: 'Пассив «Точность»: обычный снежок Снайпера летит дальше, чем у любого другого героя. Встаньте на метку и достаньте соперника обычным броском на полном замахе.',
      enemy: { role: 'Танк', x: 700, y: 448, awake: false },
      marks: function (c) { return c.enemy ? [SNIPE_SPOT, LOW_COVER, markAt(c.enemy)] : [SNIPE_SPOT, LOW_COVER]; },
      // Дистанция в проверке не для строгости: 550 px другим ролям недоступны, и попадание
      // с метки доказывает именно пассивку, а не подход вплотную.
      check: function (c) { return c.hitEnemy() && c.dist(c.me, c.enemy) >= 500; },
      hint: function (c) {
        if (c.me && c.dist(c.me, SNIPE_SPOT) > 60) return ' Метка у левого края: с неё до соперника ровно предел броска.';
        return c.flags.snipeBall ? ' Заряженный выстрел здесь не годится — он настильный и вязнет в укрытии. Бросайте обычным.' : '';
      }
    },
    fightStep('Снайпер', 'Танк')
  ];

  var BOMBER = [
    {
      text: 'Нажмите Q: следующий снежок взорвётся при попадании.',
      check: function (c) { return !!c.me && c.me.armed === 'explosive'; }
    },
    {
      text: 'Взрыв бьёт по площади — точное попадание не нужно. Бросьте взрывной снежок рядом с соперником.',
      enemy: { role: 'Танк', x: DUMMY.x, y: DUMMY.y, awake: false },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy, Sim.EXPLOSION_RADIUS)] : [WALK_MARK]; },
      check: function (c) {
        return !!c.enemy && c.event('explosion', function (ev) {
          return Math.hypot(ev.x - c.enemy.x, ev.y - c.enemy.y) <= Sim.EXPLOSION_RADIUS;
        });
      },
      hint: laneHint
    },
    {
      text: 'Пассив «Сапёр»: взрывы ломают дерево, а обычные снежки — нет. Разнесите ящик: он держит два взрыва.',
      marks: function () { return [CRATE]; },
      // Соперника на этом шаге нет: он попал бы в радиус взрыва и ушёл в KO, а шаг тогда
      // пришлось бы проходить между респавнами. Условие — по состоянию ящика, а не по событию
      // разрушения: событие можно пропустить, и шаг стал бы непроходимым.
      check: function (c) { var d = c.destrNear(CRATE.x, CRATE.y); return !!d && d.hp <= 0; },
      hint: function (c) {
        var d = c.destrNear(CRATE.x, CRATE.y);
        if (d && d.hp > 0 && d.hp < d.maxHp) return ' Ящик треснул — нужен ещё один взрыв (Q, затем бросок).';
        return c.inStep > 20000 ? ' Взрыв должен накрыть ящик, попадать точно в него не обязательно.' : '';
      }
    },
    fightStep('Бомбер', 'Щит')
  ];

  var FREEZER = [
    {
      text: 'Нажмите Q: следующий снежок оставит на земле наледь.',
      check: function (c) { return !!c.me && c.me.armed === 'frost'; }
    },
    {
      text: 'Бросьте его в соперника. В наледи враги двигаются заметно медленнее — вокруг замедлённого пляшут снежинки.',
      enemy: { role: 'Танк', x: DUMMY.x, y: DUMMY.y, awake: false },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy, Sim.FROST_R)] : [WALK_MARK]; },
      // Полсекунды внутри зоны, а не мгновение: иначе шаг закрывается до того, как игрок
      // успеет увидеть, что соперник замедлился.
      check: function (c) { return c.accum('iced', !!c.enemy && c.frostOn(c.enemy) && c.enemy.slow, 500); },
      hint: function (c) {
        return laneHint(c) || (c.inStep > 15000 ? ' Зона наледи широкая: достаточно попасть рядом с ним.' : '');
      }
    },
    {
      text: 'Пассив «Стужа»: рядом с Фризером враги медленнее и без наледи. Пунктирное кольцо вокруг вас — граница ауры; отойдите от соперника и заведите его внутрь кольца.',
      enemy: { role: 'Танк', x: DUMMY.x, y: DUMMY.y, awake: false },
      // Метка обычного размера: границу зоны показывает само кольцо вокруг игрока, а метка
      // радиусом ауры была бы кругом на четверть арены, да ещё вокруг чужого бойца.
      marks: function (c) { return c.enemy ? [markAt(c.enemy)] : []; },
      // Два предохранителя. Наледь исключаем: иначе шаг зачитывается остатком с прошлого шага и
      // учит не тому. И требуем сначала оказаться СНАРУЖИ кольца: спецификация соперника здесь
      // та же, что на прошлом шаге, поэтому манекен не переставляется и игрок обычно уже стоит
      // рядом — шаг закрывался сам, стоило наледи растаять, без единого действия.
      check: function (c) {
        if (!c.me || !c.enemy) return false;
        var inside = c.dist(c.me, c.enemy) <= Sim.FREEZER_AURA_R - 8;
        if (!inside) { c.flags.wasOut = true; return false; }
        if (!c.flags.wasOut) return false;
        return c.accum('aura', !c.frostOn(c.enemy) && c.enemy.slow, 700);
      },
      hint: function (c) {
        if (!c.flags.wasOut) return ' Сначала отойдите за кольцо — так видно, как соперник замедляется на границе.';
        if (c.enemy && c.frostOn(c.enemy)) return ' Дождитесь, пока наледь растает: сейчас работает она, а не аура.';
        return c.inStep > 15000 ? ' Ведите его внутрь кольца и подержите там пару мгновений.' : '';
      }
    },
    fightStep('Фризер', 'Раннер')
  ];

  var SHIELD = [
    {
      text: 'Нажмите Q: перед вами встанет снежная стена. Она держит несколько попаданий и растает через несколько секунд.',
      check: function (c) { return c.walls('A').length > 0; }
    },
    {
      text: 'Стена — переносное укрытие. Встаньте так, чтобы она перекрыла линию между вами и соперником.',
      enemy: { role: 'Танк', x: DUMMY.x, y: DUMMY.y, awake: false },
      marks: laneMarks,
      // Два условия. Порог 30, а не 60: стена ставится в 45 px по прицелу, и с широким порогом
      // засчитывалась даже стена, выставленная вбок от линии броска. И стена должна быть
      // поставлена в этом шаге (возраст меньше времени шага): та, что осталась с шага 1, уже
      // стояла как надо, и одно нажатие Q закрывало оба шага подряд.
      check: function (c) {
        if (!c.me || !c.enemy) return false;
        var walls = c.walls('A');
        for (var i = 0; i < walls.length; i++) {
          var age = walls[i].life - walls[i].ttl;
          if (age <= c.inStep && c.segDist(c.me, c.enemy, walls[i]) <= 30) return true;
        }
        return false;
      },
      hint: function (c) {
        if (c.walls('A').length === 0) return ' Поставьте стену (Q) между собой и соперником.';
        return c.inStep > 12000 ? ' Стена встаёт поперёк прицела: наведитесь на соперника и нажмите Q.' : '';
      }
    },
    {
      text: 'Пассив «Закалка»: щитовой пузырь гасит одно попадание целиком. Полное кольцо вокруг бойца — пузырь готов. Встаньте на метку и дайте Снайперу попасть.',
      enemy: { role: 'Снайпер', x: SHOOTER.x, y: SHOOTER.y, awake: true, level: 2 },
      marks: function (c) { return c.enemy ? [WALK_MARK, markAt(c.enemy)] : [WALK_MARK]; },
      // Ловим только событие хлопка: ветка «пузыря нет» закрывала шаг мгновенно, если пузырь
      // сбили ещё на прошлом шаге и 12-секундный реген не успел пройти. После хлопка держим
      // шаг ещё секунду — иначе игрок не успевает увидеть, что дуга пошла по кругу, а весь
      // смысл шага именно в этом.
      check: function (c) {
        if (c.event('bubblePop', function (ev) { return ev.targetId === c.meId; })) c.flags.popAt = c.time;
        return !!c.flags.popAt && c.time - c.flags.popAt >= 1200;
      },
      hint: function (c) {
        if (c.flags.popAt) return ' Пузырь лопнул — смотрите на дугу вокруг бойца: она заполняется.';
        if (c.me && !c.me.bubble && c.me.bb > 0) {
          return ' Пузырь восстанавливается: ' + Math.ceil(c.me.bb * Sim.BUBBLE_REGEN_MS / 1000) +
            ' с. Спрячьтесь и подождите — под обстрелом реген не идёт.';
        }
        var walls = c.walls('A');
        if (c.me && c.enemy) {
          for (var i = 0; i < walls.length; i++) {
            if (c.segDist(c.me, c.enemy, walls[i]) <= 60) return ' Отойдите от своей стены — из-за неё соперник не бросит.';
          }
        }
        return '';
      }
    },
    fightStep('Щит', 'Снайпер')
  ];

  var SCENARIOS = [
    // Роль «Основ» жёсткая: выбор класса на входе только тормозил новичка, а способности
    // раскрывают геройские уроки.
    { id: 'basics', title: 'Основы', role: 'Раннер', about: 'Движение, замах, перезарядка, укрытия и первый бой', steps: BASICS },
    { id: 'dash', title: 'Раннер: рывок', role: 'Раннер', about: 'Рывок с неуязвимостью, второе дыхание и бой', steps: RUNNER },
    { id: 'taram', title: 'Танк: таран', role: 'Танк', about: 'Таран, броня против оглушения и бой', steps: TANK },
    { id: 'snipe', title: 'Снайпер: прицельный выстрел', role: 'Снайпер', about: 'Настильный выстрел, дальность и бой', steps: SNIPER },
    { id: 'explosive', title: 'Бомбер: взрывной снежок', role: 'Бомбер', about: 'Урон по площади, снос укрытий и бой', steps: BOMBER },
    { id: 'frost', title: 'Фризер: ледяная волна', role: 'Фризер', about: 'Наледь, аура холода и бой', steps: FREEZER },
    { id: 'wall', title: 'Щит: снежная стена', role: 'Щит', about: 'Стена-укрытие, щитовой пузырь и бой', steps: SHIELD }
  ];

  function scenarioById(id) {
    for (var i = 0; i < SCENARIOS.length; i++) if (SCENARIOS[i].id === id) return SCENARIOS[i];
    return SCENARIOS[0];
  }

  /**
   * create(cfg):
   *  cfg.box, cfg.setStep  — плашка задачи и запись её текста (на телефоне он уходит в #hint)
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
    // cdPrev и stepAt живут вне flags: те обнуляются при переходе шага, а фронт кулдауна и
    // начало шага нужны как раз через границу шагов.
    var st = { scn: SCENARIOS[0], step: 0, flags: {}, game: null, enemyId: null, awake: null,
      lock: null, level: 0, cdPrev: 0, stepAt: 0 };

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
      // Замком распоряжается шаг: упражнения идут с неубиваемым соперником, шаг боя — с
      // обычным (lock: false).
      setLock(spec.lock !== false);
      var e = enemyOf(snap);
      var level = spec.level || 0;
      // Смена роли и уровня — тоже повод поставить нового: без этого переход «манекен → стрелок»
      // оставлял прежнего бойца, и шаг ждал действий от того, кто их сделать не может.
      var lost = !e || e.koed || e.role !== spec.role || st.level !== level ||
        (!spec.awake && Math.hypot(e.x - spec.x, e.y - spec.y) > 140);
      if (lost) {
        dropEnemy();
        // level задаёт шаг: на шагах «дайте ему попасть» уровень 0 мажет мимо стоящего игрока
        // (разброс прицела Танка ±160 px), поэтому там ставится 2.
        st.enemyId = g.spawnEnemy({ role: spec.role, x: spec.x, y: spec.y, botLevel: level, bot: !!spec.awake });
        st.awake = !!spec.awake;
        st.level = level;
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
        // Сколько мс идёт текущий шаг: по нему показываются подсказки застрявшему игроку.
        inStep: snap ? Math.max(0, snap.time - st.stepAt) : 0,
        dist: function (a, b) { return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : Infinity; },
        /**
         * Накопитель времени по условию: шаг закрывается, когда условие продержалось ms.
         * Нужен там, где мгновенная проверка закрывает шаг раньше, чем игрок успевает увидеть
         * результат (замедление соперника, бег на последнем HP).
         */
        accum: function (key, cond, ms) {
          var prev = st.flags[key + 'At'];
          var now = snap ? snap.time : 0;
          st.flags[key + 'At'] = now;
          if (!cond) { st.flags[key] = 0; return false; }
          var acc = (st.flags[key] || 0) + (prev == null ? 0 : Math.max(0, now - prev));
          st.flags[key] = acc;
          return acc >= ms;
        },
        // Накрыт ли боец вражеской для него наледью. Причину замедления клиент вычисляет сам:
        // в снапшоте есть только суммарный флаг slow, а различать наледь и ауру шагам нужно.
        frostOn: function (p) {
          var zones = (snap && snap.fx) || [];
          for (var z = 0; z < zones.length; z++) {
            var f = zones[z];
            if (f.kind === 'frost' && f.team !== p.team && Math.hypot(f.x - p.x, f.y - p.y) <= f.r) return true;
          }
          return false;
        },
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
      cfg.setStep('Шаг ' + (st.step + 1) + ' из ' + steps().length + '. ' + step.text + hint);
      cfg.marks(step.marks ? step.marks(c) : []);
    }

    function advance(c) {
      st.step++;
      st.stepAt = c && c.snap ? c.snap.time : 0;
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
      // Применение способности определяем по фронту кулдауна в снапшоте, а не по хуку
      // onSpecial: хук зависит от пути ввода, и на телефоне он долгое время не вызывался вовсе
      // (кнопка-стик шла мимо него) — шаг про способность был непроходим.
      var cdNow = c.me ? c.me.cd : 0;
      if (cdNow > st.cdPrev + 0.05) st.flags.special = true;
      st.cdPrev = cdNow;
      // Полный замах: страховка к хуку onThrow, силу считает сама симуляция.
      if (c.me && c.me.charging && c.me.power >= 0.95) st.flags.chargedFull = true;
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
        st.level = 0; st.cdPrev = 0; st.stepAt = 0;
        st.game = cfg.start({ onFrame: onFrame, onStop: function () { cfg.box.hidden = true; st.game = null; } }, st.scn);
        // Сбрасываем кэш текста: при повторном запуске того же урока первый шаг совпал бы со
        // строкой из прошлого раза, и на телефоне в подсказке осталось бы описание управления.
        cfg.setStep('');
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
      // Хук оставлен как быстрый путь: он срабатывает в тот же кадр, а фронт cd в onFrame —
      // на следующем. Замком он больше не распоряжается: это дело шага (spec.lock).
      onSpecial: function () {
        if (!st.game) return;
        st.flags.special = true;
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
