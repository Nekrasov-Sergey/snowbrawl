/*
 * SnowBrawl — общий модуль симуляции (sim.js).
 *
 * Исполняется в двух средах: в браузере (оффлайн-режим против ботов) и на сервере
 * внутри Go через goja. Поэтому здесь ЗАПРЕЩЕНЫ: DOM, Web Audio, performance.now,
 * Date.now, Math.random, setTimeout, ES-модули (import/export). Всё время — внутри
 * состояния (state.time, мс с начала матча), вся случайность — через state.rng.
 *
 * Контракт описан в docs/SIM_CONTRACT.md. Владелец файла — разработчик игры.
 */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.SnowBrawlSim = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  var SIM_VERSION = '1.5.0';

  // ============================================================
  // ДАННЫЕ ИГРЫ: роли, арены, способности
  // ============================================================
  var GRAVITY = 500;
  var W = 900, H = 560;
  var CHARGE_FULL_MS = 1200;          // время удержания до максимальной силы
  var DEFAULT_DURATION_MS = 5 * 60 * 1000;
  var KO_ANIM_MS = 500;
  var WALL_LIFETIME_MS = 6000;
  var WALL_HP = 3;                    // снежная стена Щита: столько попаданий держит (взрыв — сразу)
  var EXPLOSION_RADIUS = 58;
  var SUBSTEP_MAX = 1 / 60;           // максимальный подшаг физики, с

  // --- PvE-режим «Волны» (gameMode: 'survival' | 'defense') ---
  var GAME_MODES = ['pvp', 'survival', 'defense'];
  var PVE_MATCH_CAP_MS = 60 * 60 * 1000; // абсолютный потолок PvE-матча (предохранитель)
  var WAVE_BREAK_MS = 12000;             // пауза между волнами
  var MAX_ENEMIES = 24;                  // потолок одновременно живых врагов (размер снапшота, стоимость goja)
  var PVE_RESPAWN_MS = 2500;             // задержка возрождения члена пати, пока есть жизни
  var PVE_LIVES = 3;                     // жизни на волну
  var PVE_FIRST_WAVE_MS = 1500;          // первая волна через столько после старта
  var SNOWMAN_HP = 40;
  var SNOWMAN_R = 26;
  var DEFENSE_AGGRO_R = 120;             // ближе — враг переключается со снеговика на игрока
  var PVE_SPAWN_X0 = 760, PVE_SPAWN_X1 = 884; // полоса появления врагов у правого края
  var CONTACT_DAMAGE_CD = 0.8;           // с, пауза контактного урона одного врага
  var CONTACT_KNOCK = 34;

  // --- Способности (пассив + актив у каждой роли), константы игрового баланса ---
  var DASH_DIST = 118, DASH_MS = 170, DASH_IFRAME_MS = 250, DASH_CD = 8;        // Раннер: Рывок
  var TARAM_DIST = 150, TARAM_MS = 380, TARAM_CD = 14, TARAM_KNOCK = 64, TARAM_STUN = 0.8; // Танк: Таран
  var SNIPE_CD = 12;                                                            // Снайпер: Прицельный выстрел
  var BOMB_CD = 15;                                                             // Бомбер: Взрывной снежок
  var FROST_R = 54, FROST_MS = 3000, FROST_SLOW = 0.40, FREEZER_CD = 12;        // Фризер: Ледяная волна
  var FREEZER_AURA_R = 58, FREEZER_AURA_SLOW = 0.12;                            // Фризер: пассив «Стужа»
  var WALL_CD = 18;                                                             // Щит: Снежная стена
  var BUBBLE_REGEN_MS = 12000;                                                  // Щит: пассив «Закалка» (реген пузыря)
  var RUNNER_LOWHP_SPEEDUP = 1.20;                                             // Раннер: пассив «Второе дыхание»
  var TANK_STUN_FACTOR = 0.5;                                                   // Танк: пассив «Броня»
  var SNIPE_FLIGHT = 0.32, SNIPE_TRAVEL = 620;                                  // настильный быстрый выстрел

  // Перезарядка выстрела: пауза после броска, пока нельзя начать новый замах.
  // По ролям — Раннер частит, Снайпер бьёт редко.
  var RELOAD_MS = { 'Раннер': 500, 'Танк': 900, 'Снайпер': 1600, 'Бомбер': 1000, 'Фризер': 900, 'Щит': 900 };

  // Актив на Q. needsDir — направленная (по прицелу), иначе «заряжает» следующий бросок.
  var ABILITIES = {
    'Раннер':  { active: { id: 'dash',      cooldown: DASH_CD,    needsDir: true  }, passive: 'lowhp_speed' },
    'Танк':    { active: { id: 'taram',     cooldown: TARAM_CD,   needsDir: true  }, passive: 'armor' },
    'Снайпер': { active: { id: 'snipe',     cooldown: SNIPE_CD,   needsDir: false }, passive: 'precision' },
    'Бомбер':  { active: { id: 'explosive', cooldown: BOMB_CD,    needsDir: false }, passive: 'sapper' },
    'Фризер':  { active: { id: 'frost',     cooldown: FREEZER_CD, needsDir: false }, passive: 'chill' },
    'Щит':     { active: { id: 'wall',      cooldown: WALL_CD,    needsDir: true  }, passive: 'bubble' }
  };

  // mat: 'stone' | 'wood' | 'tree' | 'ice' — материал для текстур клиента (рендер).
  // hp — разрушаемое укрытие (снимается взрывом Бомбера); без hp неразрушимо.
  var ARENAS = [
    { name: 'Классика', mat: 'stone', obstacles: [
        { type:'rect', x:415, y:90,  w:70, h:22, height:16 },
        { type:'rect', x:415, y:448, w:70, h:22, height:16 },
        { type:'rect', x:180, y:250, w:24, h:90, height:20 },
        { type:'rect', x:696, y:250, w:24, h:90, height:20 },
        { type:'rect', x:438, y:260, w:24, h:60, height:24, mat:'wood', hp:4 }
    ]},
    { name: 'Крепость', mat: 'stone', obstacles: [
        { type:'rect', x:300, y:140, w:90, h:20, height:18 },
        { type:'rect', x:600, y:140, w:90, h:20, height:18 },
        { type:'rect', x:300, y:420, w:90, h:20, height:18 },
        { type:'rect', x:600, y:420, w:90, h:20, height:18 },
        { type:'rect', x:450, y:280, w:140, h:90, height:32 },
        { type:'rect', x:260, y:280, w:20, h:60, height:20, mat:'wood', hp:4 },
        { type:'rect', x:640, y:280, w:20, h:60, height:20, mat:'wood', hp:4 }
    ]},
    { name: 'Ельник', mat: 'tree', obstacles: [
        { type:'circle', x:300, y:200, r:16, height:22 },
        { type:'circle', x:300, y:360, r:16, height:22 },
        { type:'circle', x:600, y:200, r:16, height:22 },
        { type:'circle', x:600, y:360, r:16, height:22 },
        { type:'circle', x:450, y:140, r:16, height:22 },
        { type:'circle', x:450, y:420, r:16, height:22 },
        { type:'circle', x:230, y:280, r:16, height:18 },
        { type:'circle', x:670, y:280, r:16, height:18 },
        { type:'rect',   x:420, y:200, w:60, h:18, height:14, mat:'wood', hp:3 },
        { type:'rect',   x:480, y:360, w:60, h:18, height:14, mat:'wood', hp:3 }
    ]},
    { name: 'Склад', mat: 'wood', obstacles: [
        { type:'rect',   x:250, y:170, w:46, h:46, height:26, mat:'wood', hp:3 },
        { type:'rect',   x:250, y:390, w:46, h:46, height:26, mat:'wood', hp:3 },
        { type:'rect',   x:650, y:170, w:46, h:46, height:26, mat:'wood', hp:3 },
        { type:'rect',   x:650, y:390, w:46, h:46, height:26, mat:'wood', hp:3 },
        { type:'circle', x:370, y:280, r:15, height:20, mat:'wood', hp:2 },
        { type:'circle', x:530, y:280, r:15, height:20, mat:'wood', hp:2 },
        { type:'rect',   x:450, y:110, w:70, h:20, height:18 },
        { type:'rect',   x:450, y:450, w:70, h:20, height:18 },
        { type:'rect',   x:450, y:280, w:24, h:96, height:30 }
    ]},
    { name: 'Река', mat: 'stone', ice: { y0: 236, y1: 324, slow: 0.28 }, obstacles: [
        { type:'rect',   x:450, y:200, w:120, h:20, height:16, mat:'wood' },
        { type:'rect',   x:450, y:360, w:120, h:20, height:16, mat:'wood' },
        { type:'rect',   x:210, y:150, w:22, h:74, height:20 },
        { type:'rect',   x:690, y:150, w:22, h:74, height:20 },
        { type:'rect',   x:210, y:410, w:22, h:74, height:20, mat:'wood', hp:4 },
        { type:'rect',   x:690, y:410, w:22, h:74, height:20, mat:'wood', hp:4 },
        { type:'circle', x:450, y:280, r:17, height:24 }
    ]}
  ];

  var ROLE_STATS = {
    'Раннер':  { speed: 195, radius: 15, color: '#7fd4ff' },
    'Танк':    { speed: 135, radius: 18, color: '#8aa0c0' },
    'Снайпер': { speed: 160, radius: 15, color: '#c9a6ff' },
    'Бомбер':  { speed: 150, radius: 16, color: '#ffb347' },
    'Фризер':  { speed: 165, radius: 15, color: '#9fe8ff' },
    'Щит':     { speed: 150, radius: 17, color: '#b8f0c8' }
  };

  var ROLE_AI = {
    'Раннер':  { minRange:130, maxRange:300, retreatHp:1, noise:32, decisionEvery:380 },
    'Танк':    { minRange:50,  maxRange:250, retreatHp:0, noise:42, decisionEvery:460 },
    'Снайпер': { minRange:260, maxRange:480, retreatHp:1, noise:14, decisionEvery:520 },
    'Бомбер':  { minRange:150, maxRange:340, retreatHp:1, noise:30, decisionEvery:420 },
    'Фризер':  { minRange:150, maxRange:320, retreatHp:1, noise:28, decisionEvery:400 },
    'Щит':     { minRange:80,  maxRange:260, retreatHp:1, noise:36, decisionEvery:450 }
  };

  // Совместимость: клиент местами читает SPECIALS[role] как «есть ли способность» и
  // SPECIALS[role].type для направленной. Теперь актив есть у всех ролей.
  var SPECIALS = {};
  for (var _r in ABILITIES) SPECIALS[_r] = { type: ABILITIES[_r].active.id, cooldown: ABILITIES[_r].active.cooldown, needsDir: ABILITIES[_r].active.needsDir };

  var HERO_DESCRIPTIONS = {
    'Раннер':  'Пассив «Второе дыхание»: при 1 HP скорость +20%. Актив (Q): рывок с короткой неуязвимостью — уворот от снежка.',
    'Танк':    'Пассив «Броня»: оглушение от попаданий вдвое короче. Актив (Q): таран вперёд — сбивает и оглушает врага на пути.',
    'Снайпер': 'Пассив «Точность»: снежок летит быстрее и настильнее. Актив (Q): прицельный выстрел по прямой на всю дистанцию.',
    'Бомбер':  'Пассив «Сапёр»: взрывы ломают ящики и чужую снежную стену. Актив (Q): взрывной снежок — урон по площади.',
    'Фризер':  'Пассив «Стужа»: враги рядом двигаются медленнее. Актив (Q): ледяная волна — наледь на земле замедляет врагов.',
    'Щит':     'Пассив «Закалка»: щитовой пузырь гасит одно попадание, восстанавливается без урона 12 с. Актив (Q): снежная стена с прочностью.'
  };

  var ABILITY_HINT_TEXT = {
    'Раннер':  '🏃 Способность (Q): рывок в сторону прицела, короткая неуязвимость.',
    'Танк':    '🛡️ Способность (Q): таран вперёд — сбивает и оглушает врага.',
    'Снайпер': '🎯 Способность (Q): следующий бросок летит по прямой на максимум.',
    'Бомбер':  '💣 Способность (Q): взрывной снежок — урон по площади, ломает ящики.',
    'Фризер':  '❄️ Способность (Q): наледь на земле — замедляет врагов в зоне.',
    'Щит':     '🧱 Способность (Q): снежная стена перед собой, держит попадания.'
  };

  var ALL_ROLES = Object.keys(ROLE_STATS);
  var MODES = [1, 2, 3, 4];

  // ============================================================
  // PvE: типы врагов и таблица уровней кампании
  // ============================================================
  // role — базовая роль для ROLE_STATS/ROLE_AI/RELOAD_MS/ABILITIES и модели отрисовки.
  // contact — контактный урон (−1 HP при касании), knockResist — доля гашения откида,
  // scripted — прямолинейное движение (ком). role: '*' у core выбирается случайно при спавне.
  // «Раннер» исключён: у него перезарядка выстрела 500 мс, и рядовой враг-раннер
  // расстреливал пати очередями — в PvE обычные снежколёты не должны частить так.
  var ENEMY_CORE_ROLES = ['Снайпер', 'Бомбер', 'Фризер'];
  var ENEMY_STATS = {
    core:   { role: '*',       speed: 150, radius: 15, hp: 3, contact: 0, knockResist: 0 },
    swarm:  { role: 'Раннер',  speed: 198, radius: 12, hp: 1, contact: 1, knockResist: 0 },
    tank:   { role: 'Танк',    speed: 118, radius: 24, hp: 5, contact: 0, knockResist: 0.8 },
    roller: { role: 'Танк',    speed: 300, radius: 20, hp: 3, contact: 1, knockResist: 1, scripted: true },
    boss:   { role: 'Танк',    speed: 120, radius: 34, hp: 12, contact: 0, knockResist: 1 }
  };
  var BOSS_HP = { golem: 10, blizzard: 12, yeti: 16 };

  // Кампания: 3 уровня, у каждого 4 обычные волны + босс-волна. arena — индекс ARENAS.
  // types — состав волны по типам, botLevel — базовый уровень врагов (сдвигается ручкой
  // сложности), spawnMs — окно, за которое волна выходит на арену.
  var PVE_LEVELS = [
    { arena: 0, boss: 'golem', waves: [
      { types: { core: 4 },                     botLevel: 0, spawnMs: 2600 },
      { types: { core: 6 },                     botLevel: 0, spawnMs: 3000 },
      { types: { core: 5, swarm: 2 },           botLevel: 1, spawnMs: 3000 },
      { types: { core: 5, swarm: 3, tank: 1 },  botLevel: 1, spawnMs: 3600 }
    ] },
    { arena: 1, boss: 'blizzard', waves: [
      { types: { core: 6 },                     botLevel: 1, spawnMs: 2600 },
      { types: { core: 5, swarm: 3 },           botLevel: 1, spawnMs: 3000 },
      { types: { core: 5, tank: 2, roller: 2 }, botLevel: 1, spawnMs: 3200 },
      { types: { core: 5, swarm: 4, tank: 2 },  botLevel: 2, spawnMs: 3600 }
    ] },
    { arena: 3, boss: 'yeti', waves: [
      { types: { core: 6, swarm: 2 },                   botLevel: 1, spawnMs: 2600 },
      { types: { core: 6, tank: 2, roller: 2 },         botLevel: 2, spawnMs: 3000 },
      { types: { core: 6, swarm: 4, tank: 2 },          botLevel: 2, spawnMs: 3200 },
      { types: { core: 6, swarm: 4, tank: 2, roller: 1 }, botLevel: 2, spawnMs: 3800 }
    ] }
  ];

  // ============================================================
  // ДЕТЕРМИНИРОВАННЫЙ RNG (mulberry32)
  // ============================================================
  function makeRng(seed) {
    var s = (seed >>> 0) || 0x9e3779b9;
    return {
      next: function () {
        s = (s + 0x6D2B79F5) >>> 0;
        var t = s;
        t = Math.imul(t ^ (t >>> 15), t | 1);
        t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
        return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
      },
      state: function () { return s; },
      restore: function (v) { s = v >>> 0; }
    };
  }
  function shuffle(rng, arr) {
    var a = arr.slice();
    for (var i = a.length - 1; i > 0; i--) {
      var j = Math.floor(rng.next() * (i + 1));
      var tmp = a[i]; a[i] = a[j]; a[j] = tmp;
    }
    return a;
  }

  // ============================================================
  // СОЗДАНИЕ МАТЧА
  // ============================================================
  function spawnYs(n) {
    if (n === 1) return [H / 2];
    var margin = 110, top = margin, bottom = H - margin, ys = [];
    for (var i = 0; i < n; i++) ys.push(top + i * (bottom - top) / (n - 1));
    return ys;
  }

  function makeChar(id, team, role, x, y, bot, nick, botLevel, opts) {
    var stats = ROLE_STATS[role];
    opts = opts || {};
    var et = opts.enemyType || null;
    var es = et ? ENEMY_STATS[et] : null;
    var hp0 = 3;
    if (es) hp0 = et === 'boss' ? (BOSS_HP[opts.bossKind] || es.hp) : es.hp;
    var c = {
      id: id, team: team, role: role, bot: !!bot, nick: nick || role,
      botLevel: botLevel == null ? 1 : (botLevel | 0),
      x: x, y: y, radius: es ? es.radius : stats.radius, speed: es ? es.speed : stats.speed,
      hp: hp0, maxHp: hp0, stunTimer: 0, koed: false, koAt: 0, hitAt: -1e9,
      moveTarget: { x: x, y: y }, isMoving: false, animPhase: 0,
      charging: false, chargeStart: 0, aimX: x, aimY: y,
      specialCooldown: 0, pendingSpecialThrow: false, armedSpecial: null, reloadUntil: 0,
      // способности: рывок/таран, неуязвимость, замедление, щитовой пузырь Щита
      dashUntil: 0, dashVX: 0, dashVY: 0, dashKind: null, taramHits: null,
      iframeUntil: 0, slowUntil: 0, slowMul: 1, lastDamagedAt: -1e9,
      bubble: role === 'Щит' && !es, bubbleReadyAt: 0,
      // PvE
      enemyType: et, bossKind: et === 'boss' ? (opts.bossKind || 'golem') : null,
      bossPhase: et === 'boss' ? 1 : 0, bossTimer: 0,
      contactDamage: es ? es.contact : 0, knockResist: es ? es.knockResist : 0,
      scripted: !!(es && es.scripted), contactCdUntil: 0,
      lives: null, respawnAt: 0,
      ai: { nextDecisionAt: 0, chargeDuration: 0, dodgeUntil: 0, lastAimX: 0, lastAimY: 0 }
    };
    return c;
  }

  function clampInt(v, lo, hi, dflt) {
    v = (v == null || !isFinite(+v)) ? dflt : (+v | 0);
    return v < lo ? lo : (v > hi ? hi : v);
  }

  /** Свежая копия препятствий арены: разрушаемым (hp) урон снимается по месту. */
  function copyArenaObstacles(arenaIndex) {
    var out = [], src = ARENAS[arenaIndex].obstacles;
    for (var o = 0; o < src.length; o++) {
      var so = src[o], co = {};
      for (var kk in so) co[kk] = so[kk];
      if (co.hp != null) co.maxHp = co.hp;
      out.push(co);
    }
    return out;
  }

  function baseState(seed, rng, gameMode, n, arenaIndex, durationMs) {
    return {
      version: SIM_VERSION,
      seed: seed >>> 0,
      rng: rng,
      time: 0,
      tick: 0,
      gameMode: gameMode,
      mode: n,
      arenaIndex: arenaIndex,
      arenaObstacles: copyArenaObstacles(arenaIndex),
      ice: ARENAS[arenaIndex].ice || null,
      durationMs: durationMs,
      players: [],
      snowballs: [],
      nextSnowballId: 1,
      nextEnemyId: 1,
      dynamicObstacles: [],
      groundFx: [],
      nextFxId: 1,
      gameOver: false,
      winner: null,          // 'A' | 'B' | null (ничья/не закончен)
      endReason: '',          // '' | 'ko' | 'timeout' | PvE: 'cleared'|'wiped'|'objective'|'expired'
      pve: null,
      tutorial: false,       // обучение: матч не кончается, человек не выбывает
      tutorialLockEnemy: false, // обучение: соперник не опускается ниже 1 HP (последний шаг)
      events: []
    };
  }

  /**
   * config = {
   *   gameMode?: 'pvp' | 'survival' | 'defense',   // по умолчанию 'pvp'
   *   mode: 1..4,                        // PvP — размер команды; PvE — размер пати
   *   arenaIndex: 0..ARENAS.length-1,   // PvP; в PvE арену задаёт уровень кампании
   *   durationMs?: number,              // PvP — таймер матча (5 мин); PvE игнорируется
   *   difficulty?: 0|1|2,               // PvE — ручка сложности (боты пати + сдвиг врагов)
   *   campaign?: bool,                  // PvE — true (кампания) или false (эндлесс сразу)
   *   pve?: { levels?, waves? },        // PvE — урезание для тестов
   *   tutorial?: bool,                  // обучение: см. правила ниже
   *   players: [{ id, team, role, bot, nick?, botLevel? }]
   * }
   * PvP: ровно 2*mode бойцов, команды A/B. PvE: 1..4 бойцов, все — команда A (люди + боты).
   *
   * Обучение (`tutorial: true`) — тот же PvP, но с тремя послаблениями: состав может быть
   * неполным (хоть один боец), матч не заканчивается ни по KO, ни по таймеру, и боец-человек
   * не опускается ниже 1 HP. Соперника ставит и убирает клиент через tutorialSpawn/tutorialRemove.
   * Режима нет в GAME_MODES: комнаты с обучением не создаются, он только для оффлайн-клиента.
   */
  function createMatch(config, seed) {
    var rng = makeRng(seed);
    var gameMode = config.gameMode || 'pvp';
    if (GAME_MODES.indexOf(gameMode) < 0) throw new Error('sim: bad gameMode ' + gameMode);
    var n = config.mode;
    if (MODES.indexOf(n) < 0) throw new Error('sim: bad mode ' + n);
    if (gameMode !== 'pvp') return createPveMatch(config, seed, rng, gameMode, n);

    var arenaIndex = config.arenaIndex | 0;
    if (!ARENAS[arenaIndex]) throw new Error('sim: bad arenaIndex ' + arenaIndex);
    var tutorial = !!config.tutorial;
    if (!config.players || !config.players.length) throw new Error('sim: need players');
    if (!tutorial && config.players.length !== 2 * n) throw new Error('sim: need ' + (2 * n) + ' players');

    var state = baseState(seed, rng, 'pvp', n, arenaIndex, config.durationMs || DEFAULT_DURATION_MS);
    state.tutorial = tutorial;
    var players = state.players, countA = 0, countB = 0, ysA = spawnYs(n), ysB = spawnYs(n);
    for (var i = 0; i < config.players.length; i++) {
      var pc = config.players[i];
      if (!ROLE_STATS[pc.role]) throw new Error('sim: bad role ' + pc.role);
      if (pc.team === 'A') {
        if (countA >= n) throw new Error('sim: too many players in team A');
        players.push(makeChar(String(pc.id), 'A', pc.role, 160, ysA[countA++], pc.bot, pc.nick, pc.botLevel));
      } else if (pc.team === 'B') {
        if (countB >= n) throw new Error('sim: too many players in team B');
        players.push(makeChar(String(pc.id), 'B', pc.role, 740, ysB[countB++], pc.bot, pc.nick, pc.botLevel));
      } else throw new Error('sim: bad team ' + pc.team);
    }
    for (var k = 0; k < players.length; k++) players[k].ai.nextDecisionAt = 500 + rng.next() * 600;
    return state;
  }

  /** PvE-матч: пати на команде A, враги волн приходят по ходу матча на команду B. */
  function createPveMatch(config, seed, rng, gameMode, n) {
    var list = config.players || [];
    if (!list.length || list.length > 4) throw new Error('sim: pve needs 1..4 players');
    var campaign = config.campaign !== false;
    var difficulty = clampInt(config.difficulty, 0, 2, 1);
    var levelCount = clampInt(config.pve && config.pve.levels, 1, PVE_LEVELS.length, PVE_LEVELS.length);
    var wavesPerLevel = (config.pve && config.pve.waves) ? clampInt(config.pve.waves, 1, 8, 4) : 0;
    var startArena = campaign ? PVE_LEVELS[0].arena : PVE_LEVELS[PVE_LEVELS.length - 1].arena;

    var state = baseState(seed, rng, gameMode, n, startArena, PVE_MATCH_CAP_MS);
    var ys = spawnYs(list.length), spawnX = gameMode === 'defense' ? 180 : 160;
    for (var i = 0; i < list.length; i++) {
      var pc = list[i];
      if (!ROLE_STATS[pc.role]) throw new Error('sim: bad role ' + pc.role);
      var ch = makeChar(String(pc.id), 'A', pc.role, spawnX, ys[i], pc.bot, pc.nick,
        pc.bot ? difficulty : (pc.botLevel == null ? difficulty : pc.botLevel));
      ch.lives = PVE_LIVES;
      ch.ai.nextDecisionAt = 500 + rng.next() * 600;
      state.players.push(ch);
    }
    state.pve = {
      objective: gameMode,
      campaign: campaign,
      difficulty: difficulty,
      levelCount: levelCount,
      wavesPerLevel: wavesPerLevel,   // 0 = длина таблицы уровня
      level: campaign ? 0 : PVE_LEVELS.length,
      endlessIdx: 0,
      wave: 0,                        // индекс волны внутри уровня; == waveCount → босс
      phase: 'between',
      nextEventAt: PVE_FIRST_WAVE_MS,
      spawnQueue: [],
      bossActive: false,
      wavesSurvived: 0,
      snowman: gameMode === 'defense'
        ? { x: 92, y: H / 2, r: SNOWMAN_R, hp: SNOWMAN_HP, maxHp: SNOWMAN_HP }
        : null
    };
    return state;
  }

  function findPlayer(state, id) {
    id = String(id);
    for (var i = 0; i < state.players.length; i++) if (state.players[i].id === id) return state.players[i];
    return null;
  }
  function alive(p) { return p.hp > 0 && !p.koed; }
  function teamAlive(state, team) {
    var r = [];
    for (var i = 0; i < state.players.length; i++) { var p = state.players[i]; if (p.team === team && alive(p)) r.push(p); }
    return r;
  }
  function emit(state, ev) { state.events.push(ev); }

  // ============================================================
  // ВВОД ИГРОКА
  // ============================================================
  /**
   * input.kind:
   *  'move'        {x, y}               — цель перемещения
   *  'chargeStart' {x, y}               — начать замах, (x,y) — прицел
   *  'aim'         {x, y}               — обновить прицел во время замаха
   *  'throw'       {x, y, power?}       — бросить; power 0..1 от клиента, сверяется с серверным таймингом
   *  'cancelCharge' {}                  — отменить замах без броска (стик вернулся в мёртвую зону)
   *  'special'     {x, y}               — способность (Q), (x,y) — прицел/направление
   * Возвращает true, если ввод принят.
   */
  function applyInput(state, playerId, input) {
    if (state.gameOver || !input) return false;
    var p = findPlayer(state, playerId);
    if (!p || !alive(p)) return false;
    var x = clampNum(input.x, 0, W), y = clampNum(input.y, 0, H);
    switch (input.kind) {
      case 'move':
        p.moveTarget = { x: x, y: y };
        return true;
      case 'chargeStart':
        if (p.stunTimer > 0 || p.charging || state.time < p.reloadUntil) return false;
        p.charging = true; p.chargeStart = state.time; p.aimX = x; p.aimY = y;
        emit(state, { type: 'chargeStart', playerId: p.id });
        return true;
      case 'aim':
        if (!p.charging) return false;
        p.aimX = x; p.aimY = y;
        return true;
      case 'throw':
        if (!p.charging) return false;
        p.aimX = x; p.aimY = y;
        var serverPower = chargePower(state, p);
        var power = serverPower;
        if (typeof input.power === 'number' && isFinite(input.power)) {
          var cp = clampNum(input.power, 0, 1);
          if (Math.abs(cp - serverPower) <= 0.25) power = cp; // допуск на сетевую задержку
        }
        p.charging = false;
        throwSnowball(state, p, p.aimX, p.aimY, power);
        return true;
      case 'cancelCharge':
        if (!p.charging) return false;
        p.charging = false;
        emit(state, { type: 'chargeCancel', playerId: p.id });
        return true;
      case 'special':
        return useSpecial(state, p, x, y);
      default:
        return false;
    }
  }

  function clampNum(v, lo, hi) {
    v = +v; if (!isFinite(v)) return lo;
    return v < lo ? lo : (v > hi ? hi : v);
  }
  function chargePower(state, p) {
    if (!p.charging) return 0;
    return Math.min((state.time - p.chargeStart) / CHARGE_FULL_MS, 1);
  }

  /** Переключить управление бойцом на ИИ (дисконнект, AFK) и обратно. */
  function setBot(state, playerId, isBot) {
    var p = findPlayer(state, playerId);
    if (!p) return false;
    p.bot = !!isBot;
    if (p.bot) { p.charging = false; p.ai.nextDecisionAt = state.time + 300; }
    return true;
  }

  function throwSnowball(state, p, targetX, targetY, power) {
    var dx = targetX - p.x, dy = targetY - p.y, dist = Math.hypot(dx, dy) || 1;
    var dirX = dx / dist, dirY = dy / dist;
    var armed = p.armedSpecial; p.armedSpecial = null; p.pendingSpecialThrow = false;

    var flightDuration = 0.4 + power * 0.35, travelDistance = 140 + power * 380, flat = false;
    if (armed === 'snipe') {
      // прицельный выстрел: по прямой, быстро, на всю дистанцию, без дуги
      flightDuration = SNIPE_FLIGHT; travelDistance = SNIPE_TRAVEL; flat = true;
    } else if (p.role === 'Снайпер') {
      // пассив «Точность»: настильнее и быстрее обычного
      flightDuration *= 0.82; travelDistance *= 1.15;
    }
    var speedH = travelDistance / flightDuration, vz0 = flat ? 0 : 0.5 * GRAVITY * flightDuration;
    state.snowballs.push({
      id: state.nextSnowballId++,
      x: p.x, y: p.y, z: 0, vx: dirX * speedH, vy: dirY * speedH, vz: vz0, t: 0, flightDuration: flightDuration,
      team: p.team, ownerId: p.id, radius: armed === 'explosive' ? 9 : 6,
      explosive: armed === 'explosive', freeze: armed === 'frost', frost: armed === 'frost',
      flat: flat, sniped: armed === 'snipe'
    });
    var reload = RELOAD_MS[p.role] || 900;
    p.reloadUntil = state.time + reload;
    emit(state, { type: 'throw', playerId: p.id, power: power, special: armed, reload: reload });
  }

  // ============================================================
  // СПОСОБНОСТИ (актив на Q)
  // ============================================================
  function useSpecial(state, p, aimX, aimY) {
    if (!alive(p) || p.stunTimer > 0) return false;
    var ab = ABILITIES[p.role];
    if (!ab || p.specialCooldown > 0) return false;
    var id = ab.active.id;
    if (id === 'wall') {
      placeShieldWall(state, p, aimX, aimY);
    } else if (id === 'dash' || id === 'taram') {
      var dx = aimX - p.x, dy = aimY - p.y, d = Math.hypot(dx, dy) || 1;
      var isTaram = id === 'taram';
      var dur = (isTaram ? TARAM_MS : DASH_MS) / 1000;
      var reach = isTaram ? TARAM_DIST : DASH_DIST;
      p.charging = false;
      p.dashVX = (dx / d) * (reach / dur);
      p.dashVY = (dy / d) * (reach / dur);
      p.dashUntil = state.time + (isTaram ? TARAM_MS : DASH_MS);
      p.dashKind = id;
      if (isTaram) { p.taramHits = {}; }
      else { p.iframeUntil = state.time + DASH_IFRAME_MS; }
      emit(state, { type: 'dash', playerId: p.id, kind: id });
    } else {
      // 'explosive' | 'snipe' | 'frost' — заряжает следующий бросок
      p.armedSpecial = id;
      p.pendingSpecialThrow = true; // совместимость со снапшотом
    }
    p.specialCooldown = ab.active.cooldown;
    emit(state, { type: 'special', playerId: p.id, special: id });
    return true;
  }
  function placeShieldWall(state, p, aimX, aimY) {
    var dx = aimX - p.x, dy = aimY - p.y, dist = Math.hypot(dx, dy) || 1;
    var dirX = dx / dist, dirY = dy / dist;
    state.dynamicObstacles.push({ type:'rect', x: p.x + dirX * 45, y: p.y + dirY * 45, w:55, h:16, height:20,
      expiresAt: state.time + WALL_LIFETIME_MS, team: p.team, hp: WALL_HP, maxHp: WALL_HP, mat: 'wood' });
    emit(state, { type: 'wallPlaced', playerId: p.id });
  }

  // ============================================================
  // ГЕОМЕТРИЯ ПРЕПЯТСТВИЙ
  // ============================================================
  function getAllObstacles(state) {
    var res = [];
    var arr = state.arenaObstacles || ARENAS[state.arenaIndex].obstacles;
    for (var j = 0; j < arr.length; j++) { if (arr[j].hp == null || arr[j].hp > 0) res.push(arr[j]); }
    for (var i = 0; i < state.dynamicObstacles.length; i++) {
      var o = state.dynamicObstacles[i];
      if (o.expiresAt > state.time && (o.hp == null || o.hp > 0)) res.push(o);
    }
    return res;
  }
  /** Первое препятствие из obs, которое блокирует точку (для урона по разрушаемым). */
  function firstBlocking(obs, x, y, z) {
    for (var i = 0; i < obs.length; i++) if (obstacleBlocksPoint(obs[i], x, y, z)) return obs[i];
    return null;
  }
  /** Снять hp с разрушаемого препятствия; вернуть true, если разрушено. */
  function damageObstacle(state, ob, amount) {
    if (!ob || ob.hp == null) return false;
    ob.hp -= amount;
    if (ob.hp <= 0) { ob.hp = 0; emit(state, { type: 'obstacleBreak', x: ob.x, y: ob.y }); return true; }
    emit(state, { type: 'obstacleHit', x: ob.x, y: ob.y });
    return false;
  }
  function obstacleBlocksPoint(ob, x, y, z) {
    if (z > ob.height) return false;
    if (ob.type === 'rect') return x >= ob.x - ob.w / 2 && x <= ob.x + ob.w / 2 && y >= ob.y - ob.h / 2 && y <= ob.y + ob.h / 2;
    return Math.hypot(x - ob.x, y - ob.y) <= ob.r;
  }
  function wallBlocks(obs, x, y, z) { for (var i = 0; i < obs.length; i++) if (obstacleBlocksPoint(obs[i], x, y, z)) return true; return false; }
  function canHitTarget(obs, shooter, targetX, targetY, power) {
    var dx = targetX - shooter.x, dy = targetY - shooter.y, dist = Math.hypot(dx, dy) || 1;
    var dirX = dx / dist, dirY = dy / dist;
    var flightDuration = 0.4 + power * 0.35, travelDistance = 140 + power * 380;
    var speedH = travelDistance / flightDuration, vz0 = 0.5 * GRAVITY * flightDuration;
    for (var i = 1; i < 16; i++) {
      var t = (i / 16) * flightDuration;
      var x = shooter.x + dirX * speedH * t, y = shooter.y + dirY * speedH * t;
      var z = vz0 * t - 0.5 * GRAVITY * t * t;
      if (Math.hypot(x - targetX, y - targetY) < 20) return true;
      if (wallBlocks(obs, x, y, z)) return false;
    }
    return true;
  }
  function clampToArena(x, y, r) { return { x: Math.max(r, Math.min(W - r, x)), y: Math.max(r, Math.min(H - r, y)) }; }
  function resolveObstacleCollisions(obs, p) {
    for (var i = 0; i < obs.length; i++) {
      var ob = obs[i];
      if (ob.type === 'circle') {
        var dx = p.x - ob.x, dy = p.y - ob.y, dist = Math.hypot(dx, dy) || 0.001, minDist = ob.r + p.radius;
        if (dist < minDist) { var push = minDist - dist; p.x += (dx / dist) * push; p.y += (dy / dist) * push; }
      } else {
        var cx = Math.max(ob.x - ob.w / 2, Math.min(p.x, ob.x + ob.w / 2)), cy = Math.max(ob.y - ob.h / 2, Math.min(p.y, ob.y + ob.h / 2));
        var dx2 = p.x - cx, dy2 = p.y - cy, dist2 = Math.hypot(dx2, dy2) || 0.001;
        if (dist2 < p.radius) { var push2 = p.radius - dist2; p.x += (dx2 / dist2) * push2; p.y += (dy2 / dist2) * push2; }
      }
    }
    var c = clampToArena(p.x, p.y, p.radius); p.x = c.x; p.y = c.y;
  }
  function nearestObstacle(obs, x, y) {
    var best = null, bestDist = Infinity;
    for (var i = 0; i < obs.length; i++) { var d = Math.hypot(x - obs[i].x, y - obs[i].y); if (d < bestDist) { bestDist = d; best = obs[i]; } }
    return best;
  }

  // ============================================================
  // ИИ БОТОВ
  // ============================================================
  function snowmanTarget(sm) {
    return { x: sm.x, y: sm.y, hp: 1, koed: false, radius: sm.r, team: 'A', role: 'Танк', _snowman: true };
  }
  function findNearestEnemy(state, p) {
    var best = null, bestD = Infinity;
    for (var i = 0; i < state.players.length; i++) {
      var q = state.players[i];
      if (q.team === p.team || !alive(q)) continue;
      var d = Math.hypot(q.x - p.x, q.y - p.y);
      if (d < bestD) { bestD = d; best = q; }
    }
    // PvE «защита»: рядовой враг команды B идёт на снеговика, пока рядом нет игрока
    if (state.pve && state.pve.objective === 'defense' && p.team === 'B' && p.enemyType !== 'boss'
        && state.pve.snowman && state.pve.snowman.hp > 0 && (!best || bestD > DEFENSE_AGGRO_R)) {
      return snowmanTarget(state.pve.snowman);
    }
    return best;
  }
  // Три уровня сложности бота. Множители: разброс прицела, пауза реакции/решений,
  // вероятность уворота и способности, дрожь силы броска, упреждение.
  // «Лёгкий» заметно мажет и вяло реагирует; «Сложный» силён, но обыгрываем.
  var BOT_LEVELS = [
    { aimNoise: 3.8, decision: 2.1,  dodge: 0.10, useAbility: 0.04, chargeJitter: 0.36, lead: 0.15 }, // Лёгкий
    { aimNoise: 1.35, decision: 1.1, dodge: 0.62, useAbility: 0.34, chargeJitter: 0.16, lead: 0.85 }, // Обычный
    { aimNoise: 0.62, decision: 0.85, dodge: 0.9, useAbility: 0.7,  chargeJitter: 0.09, lead: 1.05 }  // Сложный
  ];
  function botLvl(p) { return BOT_LEVELS[p.botLevel] || BOT_LEVELS[1]; }

  function checkDodge(state, p) {
    var now = state.time;
    if (now < p.ai.dodgeUntil) return true;
    var lvl = botLvl(p);
    for (var i = 0; i < state.snowballs.length; i++) {
      var s = state.snowballs[i];
      if (s.team === p.team) continue;
      var remaining = s.flightDuration - s.t;
      if (remaining > 0.35 || remaining <= 0) continue;
      var fx = s.x + s.vx * remaining, fy = s.y + s.vy * remaining;
      if (Math.hypot(fx - p.x, fy - p.y) < p.radius + 28) {
        if (state.rng.next() > lvl.dodge) { p.ai.dodgeUntil = now + 200; return false; } // «прозевал» — по уровню
        // Раннер на сложном уходит рывком с неуязвимостью
        if (p.role === 'Раннер' && p.specialCooldown <= 0 && state.rng.next() < lvl.useAbility) {
          var perpX0 = -s.vy, perpY0 = s.vx, n0 = Math.hypot(perpX0, perpY0) || 1, sd0 = state.rng.next() < 0.5 ? 1 : -1;
          useSpecial(state, p, p.x + (perpX0 / n0) * 60 * sd0, p.y + (perpY0 / n0) * 60 * sd0);
          p.ai.dodgeUntil = now + 260; return true;
        }
        var perpX = -s.vy, perpY = s.vx, norm = Math.hypot(perpX, perpY) || 1, side = state.rng.next() < 0.5 ? 1 : -1;
        p.moveTarget = clampToArena(p.x + (perpX / norm) * 70 * side, p.y + (perpY / norm) * 70 * side, p.radius);
        p.ai.dodgeUntil = now + 350; p.charging = false;
        return true;
      }
    }
    return false;
  }
  function updateAI(state, obs, p) {
    var now = state.time, rng = state.rng, lvl = botLvl(p);
    if (!alive(p) || p.stunTimer > 0) { p.charging = false; return; }
    if (checkDodge(state, p)) return;
    var enemy = findNearestEnemy(state, p);
    if (!enemy) return;
    var cfg = ROLE_AI[p.role], spec = ABILITIES[p.role].active;

    var noise = cfg.noise * lvl.aimNoise, decisionEvery = cfg.decisionEvery * lvl.decision;

    if (p.charging) {
      var held = (now - p.chargeStart) / 1000;
      if (held >= p.ai.chargeDuration) {
        var nx = enemy.x + (rng.next() - 0.5) * noise * 2, ny = enemy.y + (rng.next() - 0.5) * noise * 2;
        p.aimX = nx; p.aimY = ny;
        throwSnowball(state, p, nx, ny, Math.min(p.ai.chargeDuration / 1.2, 1));
        p.charging = false; p.ai.nextDecisionAt = now + decisionEvery;
      } else {
        p.aimX = enemy.x; p.aimY = enemy.y;
      }
      return;
    }
    if (now < p.ai.nextDecisionAt) return;

    // Обход застревания: если с прошлого решения бот почти не сдвинулся, а цель далеко,
    // значит его держит укрытие — уходим вбок, перпендикулярно направлению на цель.
    // (В прототипе бот в 1×1 навсегда застревал за колонной у точки появления.)
    var movedSq = (p.x - p.ai.lastX) * (p.x - p.ai.lastX) + (p.y - p.ai.lastY) * (p.y - p.ai.lastY);
    var wantsMove = Math.hypot(p.moveTarget.x - p.x, p.moveTarget.y - p.y) > 8;
    var stuck = p.ai.lastX !== undefined && movedSq < 9 && wantsMove;
    p.ai.lastX = p.x; p.ai.lastY = p.y;
    if (stuck) {
      var side = Math.atan2(p.moveTarget.y - p.y, p.moveTarget.x - p.x) + (rng.next() < 0.5 ? 1 : -1) * Math.PI / 2;
      p.moveTarget = clampToArena(p.x + Math.cos(side) * 90, p.y + Math.sin(side) * 90, p.radius);
      p.ai.nextDecisionAt = now + 450; return;
    }

    var dist = Math.hypot(enemy.x - p.x, enemy.y - p.y);
    var lowHp = p.hp <= cfg.retreatHp;
    var canAbil = p.specialCooldown <= 0 && rng.next() < lvl.useAbility;
    var canShoot = now >= p.reloadUntil; // перезарядка выстрела

    // Способности по ситуации: стена при опасности, таран для сближения, снайп издалека.
    if (spec.id === 'wall' && p.hp <= 2 && dist < 260 && p.specialCooldown <= 0) {
      useSpecial(state, p, enemy.x, enemy.y);
      p.ai.nextDecisionAt = now + 400; return;
    }
    if (spec.id === 'taram' && canAbil && dist > 90 && dist < TARAM_DIST + 60) {
      useSpecial(state, p, enemy.x, enemy.y);
      p.ai.nextDecisionAt = now + 350; return;
    }
    if (spec.id === 'snipe' && canAbil && canShoot && dist > 300 && canHitTarget(obs, p, enemy.x, enemy.y, 1)) {
      useSpecial(state, p, enemy.x, enemy.y);
      // после заряда — сразу замах
      p.charging = true; p.chargeStart = now; p.ai.chargeDuration = 0.9 + (rng.next() - 0.5) * lvl.chargeJitter;
      p.aimX = enemy.x; p.aimY = enemy.y; emit(state, { type: 'chargeStart', playerId: p.id });
      return;
    }
    if (spec.id === 'frost' && canAbil && canShoot && dist > 120 && dist < 320 && canHitTarget(obs, p, enemy.x, enemy.y, 0.6)) {
      useSpecial(state, p, enemy.x, enemy.y);
      p.charging = true; p.chargeStart = now; p.ai.chargeDuration = 0.6 + (rng.next() - 0.5) * lvl.chargeJitter;
      p.aimX = enemy.x; p.aimY = enemy.y; emit(state, { type: 'chargeStart', playerId: p.id });
      return;
    }
    if (spec.id === 'dash' && canAbil && lowHp && dist < 140) {
      var awayD = Math.atan2(p.y - enemy.y, p.x - enemy.x);
      useSpecial(state, p, p.x + Math.cos(awayD) * 80, p.y + Math.sin(awayD) * 80);
      p.ai.nextDecisionAt = now + 300; return;
    }
    if (lowHp) {
      var ob = nearestObstacle(obs, p.x, p.y);
      if (ob) {
        var ax = ob.x - enemy.x, ay = ob.y - enemy.y, norm = Math.hypot(ax, ay) || 1;
        p.moveTarget = clampToArena(ob.x + (ax / norm) * 40, ob.y + (ay / norm) * 40, p.radius);
      } else {
        var away0 = Math.atan2(p.y - enemy.y, p.x - enemy.x);
        p.moveTarget = clampToArena(p.x + Math.cos(away0) * 120, p.y + Math.sin(away0) * 120, p.radius);
      }
      p.ai.nextDecisionAt = now + 500; return;
    }
    if (dist < cfg.minRange) {
      var away = Math.atan2(p.y - enemy.y, p.x - enemy.x);
      p.moveTarget = clampToArena(p.x + Math.cos(away) * 90, p.y + Math.sin(away) * 90, p.radius);
      p.ai.nextDecisionAt = now + decisionEvery; return;
    }
    if (dist > cfg.maxRange) {
      var toward = Math.atan2(enemy.y - p.y, enemy.x - p.x);
      p.moveTarget = clampToArena(p.x + Math.cos(toward) * 110, p.y + Math.sin(toward) * 110, p.radius);
      p.ai.nextDecisionAt = now + decisionEvery * 0.7; return;
    }
    // Сила броска подбирается так, чтобы снежок приземлился чуть за целью:
    // дальность полёта = 140 + power*380, окно попадания — последние ~50 px дуги.
    // (В прототипе бралось 0.35 + dist/500, и боты систематически перебрасывали цель.)
    var testPower = Math.min(1, Math.max(0, (dist + 20 - 140) / 380));
    if (canHitTarget(obs, p, enemy.x, enemy.y, testPower) && canShoot) {
      if (spec.id === 'explosive' && p.specialCooldown <= 0 && rng.next() < lvl.useAbility) {
        useSpecial(state, p, enemy.x, enemy.y);
      }
      p.charging = true; p.chargeStart = now;
      p.ai.chargeDuration = Math.max(0.25, testPower * 1.2 + (rng.next() - 0.5) * lvl.chargeJitter);
      p.aimX = enemy.x; p.aimY = enemy.y;
      emit(state, { type: 'chargeStart', playerId: p.id });
    } else if (!canShoot && canHitTarget(obs, p, enemy.x, enemy.y, testPower)) {
      // на линии огня, но идёт перезарядка — короткий манёвр вбок, скоро повтор
      var pr = Math.atan2(enemy.y - p.y, enemy.x - p.x) + (rng.next() < 0.5 ? 1 : -1) * Math.PI / 2;
      p.moveTarget = clampToArena(p.x + Math.cos(pr) * 40, p.y + Math.sin(pr) * 40, p.radius);
      p.ai.nextDecisionAt = now + 180;
    } else {
      var perp = Math.atan2(enemy.y - p.y, enemy.x - p.x) + (rng.next() < 0.5 ? 1 : -1) * Math.PI / 2;
      p.moveTarget = clampToArena(p.x + Math.cos(perp) * 90, p.y + Math.sin(perp) * 90, p.radius);
      p.ai.nextDecisionAt = now + decisionEvery * 0.6;
    }
  }

  // ============================================================
  // ФИЗИКА / ОБНОВЛЕНИЕ
  // ============================================================
  /** Множитель скорости: пассив Раннера, наледь Фризера, лёд «Реки», аура вражеского Фризера. */
  function speedMul(state, p) {
    var mul = 1;
    if (p.role === 'Раннер' && p.hp === 1) mul *= RUNNER_LOWHP_SPEEDUP;
    var g = state.groundFx;
    for (var i = 0; i < g.length; i++) {
      var f = g[i];
      if (f.expiresAt > state.time && f.team !== p.team && Math.hypot(p.x - f.x, p.y - f.y) <= f.r) { mul *= (1 - FROST_SLOW); break; }
    }
    if (state.ice && p.y >= state.ice.y0 && p.y <= state.ice.y1) mul *= (1 - state.ice.slow);
    for (var j = 0; j < state.players.length; j++) {
      var q = state.players[j];
      if (q.role === 'Фризер' && q.team !== p.team && alive(q) && Math.hypot(q.x - p.x, q.y - p.y) <= FREEZER_AURA_R) { mul *= (1 - FREEZER_AURA_SLOW); break; }
    }
    return mul;
  }
  function applyTaram(state, p) {
    for (var i = 0; i < state.players.length; i++) {
      var q = state.players[i];
      if (q.team === p.team || !alive(q) || (p.taramHits && p.taramHits[q.id])) continue;
      if (Math.hypot(q.x - p.x, q.y - p.y) <= p.radius + q.radius + 5) {
        var dx = q.x - p.x, dy = q.y - p.y, d = Math.hypot(dx, dy) || 1;
        var kres = 1 - (q.knockResist || 0);
        var kc = clampToArena(q.x + (dx / d) * TARAM_KNOCK * kres, q.y + (dy / d) * TARAM_KNOCK * kres, q.radius);
        q.x = kc.x; q.y = kc.y;
        q.stunTimer = Math.max(q.stunTimer, TARAM_STUN * kres);
        q.charging = false; if (!q.scripted) q.dashUntil = 0;
        if (p.taramHits) p.taramHits[q.id] = 1;
        emit(state, { type: 'knockback', targetId: q.id, x: q.x, y: q.y });
      }
    }
  }
  function moveCharacter(state, obs, p, dt) {
    var rolling = p.dashKind === 'roll' && state.time < p.dashUntil;
    if (!alive(p) || (p.stunTimer > 0 && !rolling)) { p.isMoving = false; if (!rolling) p.dashUntil = 0; return; }
    // Рывок/таран/ком: скриптованное движение вместо хода к цели.
    if (state.time < p.dashUntil) {
      p.isMoving = true;
      var px0 = p.x, py0 = p.y;
      var want = Math.hypot(p.dashVX, p.dashVY) * dt;
      var c0 = clampToArena(p.x + p.dashVX * dt, p.y + p.dashVY * dt, p.radius); p.x = c0.x; p.y = c0.y;
      p.animPhase += dt * 12;
      resolveObstacleCollisions(obs, p);
      if (p.dashKind === 'taram') applyTaram(state, p);
      if (p.dashKind === 'roll' || p.dashKind === 'bossroll') {
        var moved = Math.hypot(p.x - px0, p.y - py0);
        if (want > 0.1 && moved < want * 0.5) {
          // упёрлись в стену/край
          p.dashUntil = 0;
          if (p.enemyType === 'roller') { p.koed = true; p.hp = 0; emit(state, { type: 'ko', targetId: p.id, x: p.x, y: p.y }); }
        }
      }
      return;
    }
    var dx = p.moveTarget.x - p.x, dy = p.moveTarget.y - p.y, dist = Math.hypot(dx, dy);
    p.isMoving = dist > 2;
    if (p.isMoving) {
      var spd = p.speed * speedMul(state, p);
      var step = Math.min(spd * dt, dist);
      var nx = p.x + (dx / dist) * step, ny = p.y + (dy / dist) * step;
      var c = clampToArena(nx, ny, p.radius); p.x = c.x; p.y = c.y;
      p.animPhase += dt * (spd / 25);
    }
    resolveObstacleCollisions(obs, p);
  }
  function applyHit(state, target, freezeBonus, x, y) {
    if (target.bubble) { // Щит: пассив «Закалка» гасит одно попадание целиком
      target.bubble = false; target.bubbleReadyAt = state.time + BUBBLE_REGEN_MS;
      target.stunTimer = Math.max(target.stunTimer, 0.2); target.hitAt = state.time; target.charging = false;
      emit(state, { type: 'bubblePop', targetId: target.id, x: x, y: y });
      return;
    }
    // Обучение: попадание по ученику (команда A) считается и оглушает, но последнее HP не
    // снимается — новичок не должен вылетать из обучения из-за того, что соперник его добил.
    // На последнем шаге то же правило защищает и соперника (команда B, tutorialLockEnemy):
    // добить его можно только после применения способности — клиент снимает замок по onSpecial.
    if (state.tutorial && target.hp <= 1 &&
        (target.team === 'A' || (target.team === 'B' && state.tutorialLockEnemy))) {
      target.hitAt = state.time; target.lastDamagedAt = state.time;
      target.charging = false; target.dashUntil = 0;
      target.stunTimer = (target.role === 'Танк' ? TANK_STUN_FACTOR : 1) * (1.0 + (freezeBonus || 0));
      emit(state, { type: 'hit', targetId: target.id, x: x, y: y, freeze: !!freezeBonus, hp: target.hp });
      return;
    }
    target.hp -= 1;
    target.hitAt = state.time; target.lastDamagedAt = state.time;
    target.charging = false; target.dashUntil = 0; // после попадания боец теряет атаку и рывок
    if (target.hp <= 0) {
      target.hp = 0; target.koed = true; target.stunTimer = 0; target.koAt = state.time;
      emit(state, { type: 'ko', targetId: target.id, x: x, y: y });
    } else {
      var base = (target.hp === 2 ? 0.5 : 1.0) + (freezeBonus || 0);
      if (target.role === 'Танк') base *= TANK_STUN_FACTOR; // пассив «Броня»
      target.stunTimer = base;
      emit(state, { type: 'hit', targetId: target.id, x: x, y: y, freeze: !!freezeBonus, hp: target.hp });
    }
  }
  function updateSnowballs(state, obs, dt) {
    for (var i = state.snowballs.length - 1; i >= 0; i--) {
      var s = state.snowballs[i];
      s.t += dt; s.x += s.vx * dt; s.y += s.vy * dt;
      s.z = s.flat ? 0 : (s.vz * s.t - 0.5 * GRAVITY * s.t * s.t);

      var dead = false, hitWall = false, directHit = false;
      if (s.t >= s.flightDuration || (!s.flat && s.z < 0) || s.x < 0 || s.x > W || s.y < 0 || s.y > H) dead = true;
      if (!dead && wallBlocks(obs, s.x, s.y, s.z)) { dead = true; hitWall = true; }
      if (!dead && s.z <= 14) {
        for (var k = 0; k < state.players.length; k++) {
          var p = state.players[k];
          if (p.team === s.team || !alive(p) || state.time < p.iframeUntil) continue;
          if (Math.hypot(p.x - s.x, p.y - s.y) <= p.radius + s.radius) {
            applyHit(state, p, s.freeze ? 1.0 : 0, s.x, s.y); dead = true; directHit = true; break;
          }
        }
        // защита объекта: вражеский снежок бьёт снеговика
        if (!dead && s.team === 'B' && state.pve && state.pve.snowman && state.pve.snowman.hp > 0) {
          var sm = state.pve.snowman;
          if (Math.hypot(sm.x - s.x, sm.y - s.y) <= sm.r + s.radius) {
            damageSnowman(state, 1, s.x, s.y); dead = true; directHit = true;
          }
        }
      }
      if (dead) {
        if (s.explosive) {
          emit(state, { type: 'explosion', x: s.x, y: s.y });
          for (var m = 0; m < state.players.length; m++) {
            var q = state.players[m];
            if (q.team === s.team || !alive(q) || state.time < q.iframeUntil) continue;
            if (Math.hypot(q.x - s.x, q.y - s.y) <= EXPLOSION_RADIUS) applyHit(state, q, 0, s.x, s.y);
          }
          // пассив «Сапёр»: взрыв ломает разрушаемые укрытия и мгновенно сносит чужую стену
          for (var d = 0; d < obs.length; d++) {
            var ob = obs[d];
            if (Math.hypot(ob.x - s.x, ob.y - s.y) > EXPLOSION_RADIUS + (ob.r || Math.max(ob.w, ob.h) / 2)) continue;
            if (ob.team && ob.team !== s.team) damageObstacle(state, ob, WALL_HP);
            else if (ob.maxHp != null) damageObstacle(state, ob, 3);
          }
        } else if (s.frost) {
          state.groundFx.push({ id: state.nextFxId++, kind: 'frost', x: s.x, y: s.y, r: FROST_R, team: s.team, expiresAt: state.time + FROST_MS });
          emit(state, { type: 'frost', x: s.x, y: s.y });
        } else if (hitWall) {
          var wob = firstBlocking(obs, s.x, s.y, s.z);
          if (wob && wob.team && wob.team !== s.team) damageObstacle(state, wob, 1);
          emit(state, { type: 'wallHit', x: s.x, y: s.y });
        } else if (!directHit) {
          emit(state, { type: 'miss', x: s.x, y: s.y });
        }
        state.snowballs.splice(i, 1);
      }
    }
    // чистим протухшие стены и наледь
    for (var w = state.dynamicObstacles.length - 1; w >= 0; w--) {
      var dob = state.dynamicObstacles[w];
      if (dob.expiresAt <= state.time || (dob.hp != null && dob.hp <= 0)) state.dynamicObstacles.splice(w, 1);
    }
    for (var gf = state.groundFx.length - 1; gf >= 0; gf--) {
      if (state.groundFx[gf].expiresAt <= state.time) state.groundFx.splice(gf, 1);
    }
  }
  function updateTimers(state, p, dt) {
    if (p.stunTimer > 0) p.stunTimer = Math.max(0, p.stunTimer - dt);
    if (p.specialCooldown > 0) p.specialCooldown = Math.max(0, p.specialCooldown - dt);
    if (p.reloadUntil > 0 && state.time >= p.reloadUntil) { p.reloadUntil = 0; emit(state, { type: 'reloadDone', playerId: p.id }); }
    // Щит: пассив «Закалка» — пузырь восстанавливается, если 12 с не получал урона
    if (p.role === 'Щит' && !p.bubble && !p.koed && p.hp > 0 &&
        state.time >= p.bubbleReadyAt && (state.time - p.lastDamagedAt) >= BUBBLE_REGEN_MS) {
      p.bubble = true;
      emit(state, { type: 'bubbleReady', playerId: p.id });
    }
  }
  function checkWin(state) {
    if (state.gameOver) return;
    if (state.tutorial) return;            // обучение заканчивает клиент, когда пройден последний шаг
    if (state.pve) return;                 // исход PvE — в checkPveWin (зовётся из updatePve)
    var aAlive = teamAlive(state, 'A').length, bAlive = teamAlive(state, 'B').length;
    if (bAlive === 0 && aAlive === 0) { state.gameOver = true; state.winner = null; state.endReason = 'timeout'; }
    else if (bAlive === 0) { state.gameOver = true; state.winner = 'A'; state.endReason = 'ko'; }
    else if (aAlive === 0) { state.gameOver = true; state.winner = 'B'; state.endReason = 'ko'; }
    else if (state.time >= state.durationMs) { state.gameOver = true; state.winner = null; state.endReason = 'timeout'; }
    if (state.gameOver) emit(state, { type: 'matchEnd', winner: state.winner });
  }

  // ============================================================
  // PvE: волны, спецтипы врагов, боссы, объект-снеговик
  // ============================================================
  function pveWaveCount(state) {
    var pve = state.pve;
    if (pve.wavesPerLevel) return pve.wavesPerLevel;
    return PVE_LEVELS[pve.level].waves.length;
  }
  function pveIsCampaignLevel(state) {
    return state.pve.campaign && state.pve.level < PVE_LEVELS.length;
  }
  function pveLevelArena(state) {
    return pveIsCampaignLevel(state) ? PVE_LEVELS[state.pve.level].arena
                                     : PVE_LEVELS[PVE_LEVELS.length - 1].arena;
  }
  function pveSpawnPoint(state, idx) {
    var team = teamMembers(state, 'A');
    var ys = spawnYs(team.length || 1);
    var x = state.pve.objective === 'defense' ? 180 : 160;
    return { x: x, y: ys[Math.min(idx, ys.length - 1)] };
  }
  function teamMembers(state, team) {
    var r = [];
    for (var i = 0; i < state.players.length; i++) if (state.players[i].team === team) r.push(state.players[i]);
    return r;
  }
  function aliveEnemies(state) {
    var c = 0;
    for (var i = 0; i < state.players.length; i++) { var p = state.players[i]; if (p.team === 'B' && alive(p)) c++; }
    return c;
  }
  function removeEnemies(state) {
    for (var i = state.players.length - 1; i >= 0; i--) if (state.players[i].team === 'B') state.players.splice(i, 1);
    state.snowballs = [];
    for (var w = state.dynamicObstacles.length - 1; w >= 0; w--) {
      if (state.dynamicObstacles[w].team === 'B') state.dynamicObstacles.splice(w, 1);
    }
  }
  function pveSwitchArena(state, arenaIndex) {
    state.arenaIndex = arenaIndex;
    state.arenaObstacles = copyArenaObstacles(arenaIndex);
    state.ice = ARENAS[arenaIndex].ice || null;
    state.dynamicObstacles = [];
    state.groundFx = [];
  }
  function respawnParty(state) {
    var team = teamMembers(state, 'A');
    for (var i = 0; i < team.length; i++) {
      var p = team[i], sp = pveSpawnPoint(state, i);
      p.hp = 3; p.koed = false; p.stunTimer = 0; p.charging = false; p.lives = PVE_LIVES;
      p.respawnAt = 0; p.dashUntil = 0; p.armedSpecial = null; p.pendingSpecialThrow = false;
      p.reloadUntil = 0; p.iframeUntil = 0; p.bubble = p.role === 'Щит';
      p.x = sp.x; p.y = sp.y; p.moveTarget = { x: sp.x, y: sp.y };
    }
  }
  function damageSnowman(state, amount, x, y) {
    var sm = state.pve && state.pve.snowman;
    if (!sm || sm.hp <= 0) return;
    sm.hp -= amount;
    emit(state, { type: 'objectiveHit', x: x, y: y, hp: Math.max(0, sm.hp) });
    if (sm.hp <= 0) sm.hp = 0;
  }

  /** Состав текущей волны с учётом ручки сложности. */
  function pveCurrentWave(state) {
    var pve = state.pve;
    if (pveIsCampaignLevel(state)) {
      var lvl = PVE_LEVELS[pve.level], cnt = pveWaveCount(state);
      if (pve.wave >= cnt) return { boss: lvl.boss };
      var def = lvl.waves[Math.min(pve.wave, lvl.waves.length - 1)];
      return {
        types: def.types,
        botLevel: clampInt(def.botLevel + (pve.difficulty - 1), 0, 2, def.botLevel),
        spawnMs: def.spawnMs
      };
    }
    // эндлесс: смешанные волны с нарастающим количеством и долей спецтипов
    var idx = pve.endlessIdx;
    var total = Math.min(MAX_ENEMIES, 8 + idx * 2);
    var swarm = Math.min(total - 2, 2 + Math.floor(idx * 0.8));
    var tank = Math.floor(idx / 2);
    var roller = idx >= 3 ? Math.floor((idx - 1) / 3) : 0;
    var core = Math.max(2, total - swarm - tank - roller);
    return { types: { core: core, swarm: swarm, tank: tank, roller: roller }, botLevel: 2, spawnMs: 3600 };
  }

  function pveStartWave(state) {
    var pve = state.pve;
    pve.phase = 'fighting';
    pve.spawnQueue = [];
    pve.bossActive = false;
    respawnParty(state);

    var w = pveCurrentWave(state), q = [];
    if (w.boss) {
      q.push({ enemyType: 'boss', bossKind: w.boss, botLevel: 2, at: 0 });
      for (var e = 0; e < 3; e++) q.push({ enemyType: 'core', botLevel: pve.difficulty, at: 900 + e * 800 });
      pve.bossActive = true;
    } else {
      var order = [];
      for (var t in w.types) for (var c = 0; c < w.types[t]; c++) order.push(t);
      order = shuffle(state.rng, order);
      var span = order.length ? Math.max(2000, Math.min(4200, order.length * 460)) : 0;
      for (var k = 0; k < order.length; k++) {
        var jitter = (state.rng.next() - 0.5) * (span / Math.max(1, order.length));
        q.push({ enemyType: order[k], botLevel: w.botLevel, at: k * span / Math.max(1, order.length) + jitter });
      }
    }
    for (var j = 0; j < q.length; j++) q[j].spawnAt = state.time + Math.max(0, q[j].at);
    pve.spawnQueue = q;
    emit(state, { type: 'waveStart', level: pve.level, wave: pve.wave, boss: w.boss || null });
  }

  function pveSpawnEnemy(state, spec) {
    var pve = state.pve, rng = state.rng;
    var es = ENEMY_STATS[spec.enemyType];
    var role = es.role === '*' ? ENEMY_CORE_ROLES[Math.floor(rng.next() * ENEMY_CORE_ROLES.length)] : es.role;
    var y = 90 + rng.next() * (H - 180);
    var x = PVE_SPAWN_X0 + rng.next() * (PVE_SPAWN_X1 - PVE_SPAWN_X0);
    var id = 'e' + (state.nextEnemyId++);
    var nick = spec.enemyType === 'boss' ? bossName(spec.bossKind) : enemyName(spec.enemyType);
    var ch = makeChar(id, 'B', role, x, y, true, nick, clampInt(spec.botLevel, 0, 2, 1),
      { enemyType: spec.enemyType, bossKind: spec.bossKind });
    ch.ai.nextDecisionAt = state.time + 200 + rng.next() * 300;
    if (ch.scripted) {
      // ком: катится по прямой к ближайшей цели, живёт до стены/попаданий
      var tgt = pveEnemyTarget(state, ch) || { x: 120, y: ch.y };
      var dx = tgt.x - ch.x, dy = tgt.y - ch.y, d = Math.hypot(dx, dy) || 1;
      ch.dashVX = (dx / d) * es.speed; ch.dashVY = (dy / d) * es.speed;
      ch.dashUntil = state.time + 999999; ch.dashKind = 'roll';
    }
    state.players.push(ch);
    emit(state, { type: 'enemySpawn', id: id, enemyType: spec.enemyType, x: x, y: y });
    return ch;
  }
  function enemyName(t) {
    return t === 'swarm' ? 'Рой' : t === 'tank' ? 'Йети' : t === 'roller' ? 'Ком' : 'Снежколёт';
  }
  function bossName(k) {
    return k === 'blizzard' ? 'Вьюга' : k === 'yeti' ? 'Йети-вожак' : 'Снеговик-голем';
  }

  function updatePve(state) {
    var pve = state.pve;
    if (state.gameOver) return;

    // возрождение членов пати, пока есть жизни
    var team = teamMembers(state, 'A');
    for (var i = 0; i < team.length; i++) {
      var p = team[i];
      if (p.koed && p.respawnAt === 0 && p.lives > 0 && pve.phase === 'fighting') {
        // только что слёг: списать жизнь
        p.lives -= 1;
        if (p.lives > 0) p.respawnAt = state.time + PVE_RESPAWN_MS;
        emit(state, { type: 'partyDown', id: p.id, lives: p.lives });
      }
      if (p.koed && p.respawnAt > 0 && state.time >= p.respawnAt) {
        var sp = pveSpawnPoint(state, i);
        p.hp = 3; p.koed = false; p.stunTimer = 0; p.respawnAt = 0; p.iframeUntil = state.time + 1200;
        p.x = sp.x; p.y = sp.y; p.moveTarget = { x: sp.x, y: sp.y }; p.bubble = p.role === 'Щит';
        emit(state, { type: 'partyRespawn', id: p.id });
      }
    }

    if (pve.phase === 'between') {
      if (state.time >= pve.nextEventAt) pveStartWave(state);
      checkPveWin(state);
      return;
    }

    // fighting: выпускаем врагов из очереди по таймингу и потолку
    for (var s = pve.spawnQueue.length - 1; s >= 0; s--) {
      if (aliveEnemies(state) >= MAX_ENEMIES) break;
      if (state.time >= pve.spawnQueue[s].spawnAt) {
        pveSpawnEnemy(state, pve.spawnQueue[s]);
        pve.spawnQueue.splice(s, 1);
      }
    }

    // волна зачищена?
    if (pve.spawnQueue.length === 0 && aliveEnemies(state) === 0) {
      pve.wavesSurvived += 1;
      pve.bossActive = false;
      emit(state, { type: 'waveCleared', level: pve.level, wave: pve.wave });
      pve.wave += 1;
      if (pveIsCampaignLevel(state) && pve.wave > pveWaveCount(state)) {
        // босс уровня повержен
        if (pve.level + 1 >= pve.levelCount) {
          pveEnd(state, 'cleared');
          return;
        }
        pve.level += 1; pve.wave = 0;
        pveSwitchArena(state, pveLevelArena(state));
        emit(state, { type: 'levelStart', level: pve.level });
      } else if (!pveIsCampaignLevel(state)) {
        pve.endlessIdx += 1;
      }
      pve.phase = 'between';
      pve.nextEventAt = state.time + WAVE_BREAK_MS;
      removeStrayBalls(state);
    }
    checkPveWin(state);
  }
  function removeStrayBalls(state) { state.snowballs = []; }

  function checkPveWin(state) {
    var pve = state.pve;
    if (state.gameOver) return;
    if (state.time >= state.durationMs) { pveEnd(state, 'expired'); return; }
    if (pve.objective === 'defense' && pve.snowman && pve.snowman.hp <= 0) { pveFail(state, 'objective'); return; }
    // вайп: все члены пати слегли и жизней не осталось
    var anyUp = false, anyLife = false;
    var team = teamMembers(state, 'A');
    for (var i = 0; i < team.length; i++) {
      if (alive(team[i])) anyUp = true;
      if (team[i].lives > 0) anyLife = true;
    }
    if (!anyUp && !anyLife) pveFail(state, 'wiped');
  }
  function pveEnd(state, reason) {
    state.gameOver = true; state.winner = null; state.endReason = reason;
    emit(state, { type: 'matchEnd', winner: null, reason: reason });
  }
  function pveFail(state, reason) {
    var pve = state.pve;
    if (pveIsCampaignLevel(state)) {
      // рестарт текущего уровня с первой волны
      removeEnemies(state);
      if (pve.snowman) pve.snowman.hp = pve.snowman.maxHp;
      pveSwitchArena(state, pveLevelArena(state));
      respawnParty(state);
      pve.wave = 0; pve.phase = 'between';
      pve.nextEventAt = state.time + WAVE_BREAK_MS;
      pve.spawnQueue = []; pve.bossActive = false;
      emit(state, { type: 'levelRestart', level: pve.level, reason: reason });
    } else {
      pveEnd(state, reason);
    }
  }

  // --- ИИ спецтипов и боссов ---
  // Цель врага: findNearestEnemy уже знает про снеговика в режиме «защита».
  function pveEnemyTarget(state, p) { return findNearestEnemy(state, p); }
  function updateSwarmAI(state, obs, p) {
    if (!alive(p) || p.stunTimer > 0) { p.isMoving = false; return; }
    var t = pveEnemyTarget(state, p);
    if (!t) return;
    p.moveTarget = clampToArena(t.x, t.y, p.radius);
    p.aimX = t.x; p.aimY = t.y;
  }
  function updateBossAI(state, obs, p) {
    if (!alive(p)) return;
    var now = state.time, rng = state.rng;
    var half = p.maxHp / 2;
    if (p.bossPhase === 1 && p.hp <= half) {
      p.bossPhase = 2;
      if (p.bossKind === 'yeti') { p.speed *= 1.3; p.contactDamage = 1; }
      emit(state, { type: 'bossPhase', id: p.id, phase: 2, kind: p.bossKind });
    }
    if (p.stunTimer > 0) { p.charging = false; return; }
    var t = findNearestEnemy(state, p);
    if (!t && state.pve.objective === 'defense' && state.pve.snowman && state.pve.snowman.hp > 0) {
      t = snowmanTarget(state.pve.snowman);
    }
    if (!t) return;
    var dist = Math.hypot(t.x - p.x, t.y - p.y);

    // движение: держим среднюю дистанцию
    if (now >= p.ai.nextDecisionAt && now >= p.dashUntil) {
      if (dist > 320) {
        var tw = Math.atan2(t.y - p.y, t.x - p.x);
        p.moveTarget = clampToArena(p.x + Math.cos(tw) * 120, p.y + Math.sin(tw) * 120, p.radius);
      } else if (dist < 150) {
        var aw = Math.atan2(p.y - t.y, p.x - t.x);
        p.moveTarget = clampToArena(p.x + Math.cos(aw) * 90, p.y + Math.sin(aw) * 90, p.radius);
      } else {
        var pr = Math.atan2(t.y - p.y, t.x - p.x) + (rng.next() < 0.5 ? 1 : -1) * Math.PI / 2;
        p.moveTarget = clampToArena(p.x + Math.cos(pr) * 80, p.y + Math.sin(pr) * 80, p.radius);
      }
      p.ai.nextDecisionAt = now + 520;
    }

    // атаки по таймеру фазы
    if (now < p.bossTimer) return;
    if (p.charging) return;
    var cadence = p.bossPhase === 2 ? 700 : 1050;

    if (p.bossKind === 'golem') {
      // веер из 3 снежков
      bossSpreadThrow(state, p, t, 3, 0.16);
      if (p.bossPhase === 2 && rng.next() < 0.4 && now >= p.dashUntil) {
        var d = Math.hypot(t.x - p.x, t.y - p.y) || 1;
        p.dashVX = (t.x - p.x) / d * 320; p.dashVY = (t.y - p.y) / d * 320;
        p.dashUntil = now + 520; p.dashKind = 'bossroll';
        emit(state, { type: 'dash', playerId: p.id, kind: 'bossroll' });
      }
    } else if (p.bossKind === 'blizzard') {
      p.armedSpecial = 'frost';
      bossSpreadThrow(state, p, t, p.bossPhase === 2 ? 2 : 1, 0.12);
      if (rng.next() < (p.bossPhase === 2 ? 0.5 : 0.3)) bossIceWall(state, p, t);
      if (p.bossPhase === 2 && aliveEnemies(state) < MAX_ENEMIES && rng.next() < 0.6) {
        pveSpawnEnemy(state, { enemyType: 'swarm', botLevel: 2 });
      }
    } else { // yeti
      if (dist > 110 && dist < TARAM_DIST + 90 && now >= p.dashUntil) {
        var dd = Math.hypot(t.x - p.x, t.y - p.y) || 1;
        p.dashVX = (t.x - p.x) / dd * (TARAM_DIST / (TARAM_MS / 1000));
        p.dashVY = (t.y - p.y) / dd * (TARAM_DIST / (TARAM_MS / 1000));
        p.dashUntil = now + TARAM_MS; p.dashKind = 'taram'; p.taramHits = {};
        emit(state, { type: 'dash', playerId: p.id, kind: 'taram' });
      } else {
        p.armedSpecial = 'frost';
        bossSpreadThrow(state, p, t, 1, 0);
      }
      if (p.bossPhase === 2 && !p._summoned) {
        p._summoned = true;
        for (var s2 = 0; s2 < 2 && aliveEnemies(state) < MAX_ENEMIES; s2++) {
          pveSpawnEnemy(state, { enemyType: 'tank', botLevel: 2 });
        }
      }
    }
    p.bossTimer = now + cadence;
  }
  function bossSpreadThrow(state, p, t, count, spreadRad) {
    var base = Math.atan2(t.y - p.y, t.x - p.x);
    var start = -(count - 1) / 2 * spreadRad;
    for (var i = 0; i < count; i++) {
      var a = base + start + i * spreadRad;
      throwSnowball(state, p, p.x + Math.cos(a) * 200, p.y + Math.sin(a) * 200, 0.85);
    }
    emit(state, { type: 'chargeStart', playerId: p.id });
  }
  function bossIceWall(state, p, t) {
    var dx = t.x - p.x, dy = t.y - p.y, d = Math.hypot(dx, dy) || 1;
    state.dynamicObstacles.push({ type: 'rect', x: p.x + dx / d * 70, y: p.y + dy / d * 70,
      w: 70, h: 16, height: 22, expiresAt: state.time + WALL_LIFETIME_MS, team: 'B',
      hp: WALL_HP, maxHp: WALL_HP, mat: 'ice' });
    emit(state, { type: 'wallPlaced', playerId: p.id });
  }

  /** Контактный урон: враги с флагом contactDamage бьют пати и снеговика касанием. */
  function updateContactDamage(state) {
    if (!state.pve) return;
    for (var i = 0; i < state.players.length; i++) {
      var e = state.players[i];
      if (e.team !== 'B' || !alive(e) || !e.contactDamage || state.time < e.contactCdUntil) continue;
      var hitSomething = false;
      for (var k = 0; k < state.players.length; k++) {
        var q = state.players[k];
        if (q.team !== 'A' || !alive(q) || state.time < q.iframeUntil) continue;
        if (Math.hypot(q.x - e.x, q.y - e.y) <= e.radius + q.radius) {
          var dx = q.x - e.x, dy = q.y - e.y, d = Math.hypot(dx, dy) || 1;
          var kc = clampToArena(q.x + dx / d * CONTACT_KNOCK, q.y + dy / d * CONTACT_KNOCK, q.radius);
          q.x = kc.x; q.y = kc.y;
          applyHit(state, q, 0, e.x, e.y);
          emit(state, { type: 'contactHit', id: e.id, targetId: q.id, x: e.x, y: e.y });
          hitSomething = true;
          break;
        }
      }
      if (!hitSomething && state.pve.snowman && state.pve.snowman.hp > 0) {
        var sm = state.pve.snowman;
        if (Math.hypot(sm.x - e.x, sm.y - e.y) <= e.radius + sm.r) {
          damageSnowman(state, 1, e.x, e.y); hitSomething = true;
        }
      }
      if (hitSomething) {
        e.contactCdUntil = state.time + CONTACT_DAMAGE_CD;
        if (e.enemyType === 'roller') { e.koed = true; e.hp = 0; e.dashUntil = 0; emit(state, { type: 'ko', targetId: e.id, x: e.x, y: e.y }); }
      }
    }
  }

  /**
   * Продвинуть симуляцию на dt секунд (сервер зовёт с фиксированным шагом 1/20).
   * Возвращает массив событий, произошедших за шаг.
   */
  // ============================================================
  // Обучение: соперник по команде клиента
  // ============================================================
  /**
   * tutorialSpawn(state, {role, x, y, botLevel?, bot?}) → id бойца команды B.
   * Работает только в матче обучения. bot: false — боец стоит на месте (ИИ выключен).
   */
  function tutorialSpawn(state, opts) {
    if (!state.tutorial) return null;
    opts = opts || {};
    var role = ROLE_STATS[opts.role] ? opts.role : ALL_ROLES[0];
    var id = 'tut' + (state.nextEnemyId++);
    var ch = makeChar(id, 'B', role, opts.x == null ? 740 : opts.x, opts.y == null ? H / 2 : opts.y,
      opts.bot !== false, 'Соперник', clampInt(opts.botLevel, 0, 2, 0));
    ch.ai.nextDecisionAt = state.time + 300;
    state.players.push(ch);
    return id;
  }

  /** tutorialLock(state, on) — на последнем шаге соперник держится на 1 HP, пока on=true. */
  function tutorialLock(state, on) {
    if (!state.tutorial) return false;
    state.tutorialLockEnemy = !!on;
    return true;
  }

  /** tutorialRemove(state, id) — убрать бойца обучения и его снежки. */
  function tutorialRemove(state, id) {
    if (!state.tutorial) return false;
    var found = false;
    for (var i = state.players.length - 1; i >= 0; i--) {
      if (state.players[i].id === id) { state.players.splice(i, 1); found = true; }
    }
    for (var s2 = state.snowballs.length - 1; s2 >= 0; s2--) {
      if (state.snowballs[s2].ownerId === id) state.snowballs.splice(s2, 1);
    }
    return found;
  }

  function step(state, dt) {
    state.events = [];
    if (state.gameOver) return state.events;
    dt = Math.min(Math.max(+dt || 0, 0), 0.1);
    state.tick++;
    // Физика считается подшагами не крупнее 1/60 с: при шаге 1/20 снежок пролетал бы
    // 35 px за тик и окно попадания (z <= 14) терялось бы. Так поведение совпадает
    // с прототипом на 60 fps независимо от частоты тиков сервера.
    var sub = Math.max(1, Math.ceil(dt / SUBSTEP_MAX));
    var sdt = dt / sub;
    for (var k = 0; k < sub && !state.gameOver; k++) {
      state.time += sdt * 1000;
      var obs = getAllObstacles(state);
      for (var i = 0; i < state.players.length; i++) {
        var p = state.players[i];
        updateTimers(state, p, sdt);
        if (p.bot) {
          if (p.enemyType === 'boss') updateBossAI(state, obs, p);
          else if (p.enemyType === 'swarm' || p.enemyType === 'roller') updateSwarmAI(state, obs, p);
          else updateAI(state, obs, p);
        }
        moveCharacter(state, obs, p, sdt);
      }
      updateSnowballs(state, obs, sdt);
      if (state.pve) { updateContactDamage(state); updatePve(state); }
      checkWin(state);
    }
    return state.events;
  }

  // ============================================================
  // СНАПШОТ ДЛЯ РЕНДЕРА / СЕТИ
  // ============================================================
  function round1(v) { return Math.round(v * 10) / 10; }
  function snapshot(state) {
    var players = [];
    for (var i = 0; i < state.players.length; i++) {
      var p = state.players[i];
      var pe = {
        id: p.id, team: p.team, role: p.role, nick: p.nick, bot: p.bot,
        x: round1(p.x), y: round1(p.y), hp: p.hp,
        stun: round1(p.stunTimer), koed: p.koed, koAt: p.koAt, hitAt: p.hitAt,
        moving: p.isMoving, anim: round1(p.animPhase),
        charging: p.charging, power: round1(chargePower(state, p) * 100) / 100,
        aimX: round1(p.aimX), aimY: round1(p.aimY),
        special: p.pendingSpecialThrow, cd: round1(p.specialCooldown),
        // способности: индикаторы для рендера
        armed: p.armedSpecial || null,
        iframe: state.time < p.iframeUntil,
        dash: state.time < p.dashUntil,
        bubble: !!p.bubble,
        slow: speedMul(state, p) < 0.999,
        // перезарядка выстрела: доля 0..1 (1 = только бросил, 0 = готов)
        rl: p.reloadUntil > state.time ? round1((p.reloadUntil - state.time) / (RELOAD_MS[p.role] || 900) * 10) / 10 : 0
      };
      // PvE: жизни пати, тип врага, фаза босса
      if (p.lives != null) pe.lives = p.lives;
      if (p.enemyType) { pe.et = p.enemyType; pe.mhp = p.maxHp; }
      if (p.enemyType === 'boss') { pe.bk = p.bossKind; pe.bph = p.bossPhase; }
      players.push(pe);
    }
    var balls = [];
    for (var k = 0; k < state.snowballs.length; k++) {
      var s = state.snowballs[k];
      balls.push({ id: s.id, x: round1(s.x), y: round1(s.y), z: round1(s.z), r: s.radius, team: s.team, ex: s.explosive, fr: s.freeze, sn: !!s.sniped });
    }
    var walls = [];
    for (var m = 0; m < state.dynamicObstacles.length; m++) {
      var o = state.dynamicObstacles[m];
      walls.push({ x: o.x, y: o.y, w: o.w, h: o.h, team: o.team, hp: o.hp, maxHp: o.maxHp,
        ttl: Math.max(0, o.expiresAt - state.time), life: WALL_LIFETIME_MS });
    }
    // Разрушаемые укрытия арены: рендер рисует их поверх кэша, показывая трещины/руины.
    var destr = [];
    var ao = state.arenaObstacles || [];
    for (var d = 0; d < ao.length; d++) {
      var g = ao[d];
      if (g.maxHp == null) continue;
      destr.push({ i: d, type: g.type, x: g.x, y: g.y, w: g.w || 0, h: g.h || 0, r: g.r || 0,
        mat: g.mat || 'wood', hp: g.hp, maxHp: g.maxHp });
    }
    var fx = [];
    for (var f = 0; f < state.groundFx.length; f++) {
      var q = state.groundFx[f];
      fx.push({ id: q.id, kind: q.kind, x: q.x, y: q.y, r: q.r, team: q.team,
        ttl: Math.max(0, q.expiresAt - state.time), life: FROST_MS });
    }
    var snap = {
      v: SIM_VERSION, tick: state.tick, time: Math.round(state.time),
      timeLeft: Math.max(0, Math.round(state.durationMs - state.time)),
      mode: state.mode, arena: state.arenaIndex, ice: state.ice || null,
      gameMode: state.gameMode, over: state.gameOver, winner: state.winner,
      reason: state.endReason || null,
      players: players, balls: balls, walls: walls, destr: destr, fx: fx
    };
    if (state.pve) snap.pve = pveSnapshot(state);
    return snap;
  }
  function pveSnapshot(state) {
    var pve = state.pve, enemiesLeft = pve.spawnQueue.length, bossHp = 0, bossMax = 0;
    for (var i = 0; i < state.players.length; i++) {
      var p = state.players[i];
      if (p.team === 'B' && alive(p)) {
        enemiesLeft++;
        if (p.enemyType === 'boss') { bossHp = p.hp; bossMax = p.maxHp; }
      }
    }
    var campaignLvl = pve.campaign && pve.level < PVE_LEVELS.length;
    var o = {
      objective: pve.objective, campaign: pve.campaign, endless: !campaignLvl,
      level: campaignLvl ? pve.level : PVE_LEVELS.length, levelCount: pve.levelCount,
      wave: campaignLvl ? pve.wave : pve.endlessIdx,
      waveCount: campaignLvl ? pveWaveCount(state) + 1 : 0,
      enemiesLeft: enemiesLeft, phase: pve.phase,
      nextInMs: pve.phase === 'between' ? Math.max(0, Math.round(pve.nextEventAt - state.time)) : 0,
      wavesSurvived: pve.wavesSurvived, bossHp: round1(bossHp), bossMax: bossMax
    };
    if (pve.snowman) {
      o.objHp = Math.max(0, Math.round(pve.snowman.hp)); o.objMaxHp = pve.snowman.maxHp;
      o.objX = pve.snowman.x; o.objY = pve.snowman.y; o.objR = pve.snowman.r;
    }
    return o;
  }

  return {
    SIM_VERSION: SIM_VERSION,
    W: W, H: H, GRAVITY: GRAVITY, CHARGE_FULL_MS: CHARGE_FULL_MS, KO_ANIM_MS: KO_ANIM_MS,
    ARENAS: ARENAS, ROLE_STATS: ROLE_STATS, SPECIALS: SPECIALS, ABILITIES: ABILITIES,
    RELOAD_MS: RELOAD_MS, MODES: MODES, GAME_MODES: GAME_MODES,
    PVE_LEVEL_COUNT: PVE_LEVELS.length,
    BOT_LEVEL_NAMES: ['Лёгкий', 'Обычный', 'Сложный'],
    HERO_DESCRIPTIONS: HERO_DESCRIPTIONS, ABILITY_HINT_TEXT: ABILITY_HINT_TEXT, ALL_ROLES: ALL_ROLES,
    makeRng: makeRng, shuffle: shuffle,
    canHitTarget: canHitTarget, // чистая функция для клиента: упрётся ли снежок в препятствие (луч прицела)
    createMatch: createMatch,
    applyInput: applyInput,
    setBot: setBot,
    tutorialSpawn: tutorialSpawn,   // обучение: поставить соперника
    tutorialRemove: tutorialRemove, // обучение: убрать соперника
    tutorialLock: tutorialLock,     // обучение: держать соперника на 1 HP (последний шаг)
    step: step,
    snapshot: snapshot,
    isOver: function (state) { return state.gameOver; },
    winner: function (state) { return state.winner; },
    reason: function (state) { return state.endReason || ''; }
  };
});
