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

  var SIM_VERSION = '1.1.1';

  // ============================================================
  // ДАННЫЕ ИГРЫ: роли, арены, способности
  // ============================================================
  var GRAVITY = 500;
  var W = 900, H = 560;
  var CHARGE_FULL_MS = 1200;          // время удержания до максимальной силы
  var DEFAULT_DURATION_MS = 5 * 60 * 1000;
  var KO_ANIM_MS = 500;
  var WALL_LIFETIME_MS = 6000;
  var EXPLOSION_RADIUS = 58;
  var SUBSTEP_MAX = 1 / 60;           // максимальный подшаг физики, с

  var ARENAS = [
    { name: 'Классика', obstacles: [
        { type:'rect', x:415, y:90,  w:70, h:22, height:16 },
        { type:'rect', x:415, y:448, w:70, h:22, height:16 },
        { type:'rect', x:180, y:250, w:24, h:90, height:20 },
        { type:'rect', x:696, y:250, w:24, h:90, height:20 },
        { type:'rect', x:438, y:260, w:24, h:60, height:24 }
    ]},
    { name: 'Крепость', obstacles: [
        { type:'rect', x:300, y:140, w:90, h:20, height:18 },
        { type:'rect', x:600, y:140, w:90, h:20, height:18 },
        { type:'rect', x:300, y:420, w:90, h:20, height:18 },
        { type:'rect', x:600, y:420, w:90, h:20, height:18 },
        { type:'rect', x:450, y:280, w:140, h:90, height:32 },
        { type:'rect', x:260, y:280, w:20, h:60, height:20 },
        { type:'rect', x:640, y:280, w:20, h:60, height:20 }
    ]},
    { name: 'Ельник', obstacles: [
        { type:'circle', x:300, y:200, r:16, height:22 },
        { type:'circle', x:300, y:360, r:16, height:22 },
        { type:'circle', x:600, y:200, r:16, height:22 },
        { type:'circle', x:600, y:360, r:16, height:22 },
        { type:'circle', x:450, y:140, r:16, height:22 },
        { type:'circle', x:450, y:420, r:16, height:22 },
        { type:'circle', x:230, y:280, r:16, height:18 },
        { type:'circle', x:670, y:280, r:16, height:18 },
        { type:'rect',   x:420, y:200, w:60, h:18, height:14 },
        { type:'rect',   x:480, y:360, w:60, h:18, height:14 }
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

  var SPECIALS = {
    'Бомбер': { type: 'explosive', cooldown: 15 },
    'Фризер': { type: 'freeze',    cooldown: 12 },
    'Щит':    { type: 'wall',      cooldown: 18 }
  };

  var HERO_DESCRIPTIONS = {
    'Раннер':  'Высокая скорость передвижения. Без активной способности — берёт манёвренностью.',
    'Танк':    'Медленный, но крупный — тяжелее объехать в ближнем бою. Без активной способности.',
    'Снайпер': 'Держит дистанцию, бросок точнее и с меньшим разбросом. Без активной способности.',
    'Бомбер':  'Способность (Q): следующий снежок взрывной — урон по площади вокруг попадания.',
    'Фризер':  'Способность (Q): следующий снежок при попадании даёт дополнительное время оглушения.',
    'Щит':     'Способность (Q): мгновенно ставит временную снежную стену перед собой.'
  };

  var ABILITY_HINT_TEXT = {
    'Бомбер': '💣 Способность (Q): взрывной снежок — урон по площади.',
    'Фризер': '❄️ Способность (Q): снежок с заморозкой — доп. время оглушения.',
    'Щит':    '🛡️ Способность (Q): временная снежная стена перед собой.'
  };

  var ALL_ROLES = Object.keys(ROLE_STATS);
  var MODES = [1, 2, 3, 4];

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

  function makeChar(id, team, role, x, y, bot, nick) {
    var stats = ROLE_STATS[role];
    return {
      id: id, team: team, role: role, bot: !!bot, nick: nick || role,
      x: x, y: y, radius: stats.radius, speed: stats.speed,
      hp: 3, stunTimer: 0, koed: false, koAt: 0, hitAt: -1e9,
      moveTarget: { x: x, y: y }, isMoving: false, animPhase: 0,
      charging: false, chargeStart: 0, aimX: x, aimY: y,
      specialCooldown: 0, pendingSpecialThrow: false,
      ai: { nextDecisionAt: 0, chargeDuration: 0, dodgeUntil: 0, lastAimX: 0, lastAimY: 0 }
    };
  }

  /**
   * config = {
   *   mode: 1..4,                       // размер команды
   *   arenaIndex: 0..ARENAS.length-1,
   *   durationMs?: number,              // таймер матча, по умолчанию 5 минут
   *   players: [{ id, team: 'A'|'B', role, bot: bool, nick? }]  // ровно 2*mode штук
   * }
   */
  function createMatch(config, seed) {
    var rng = makeRng(seed);
    var n = config.mode;
    if (MODES.indexOf(n) < 0) throw new Error('sim: bad mode ' + n);
    var arenaIndex = config.arenaIndex | 0;
    if (!ARENAS[arenaIndex]) throw new Error('sim: bad arenaIndex ' + arenaIndex);
    if (!config.players || config.players.length !== 2 * n) throw new Error('sim: need ' + (2 * n) + ' players');

    var players = [], countA = 0, countB = 0, ysA = spawnYs(n), ysB = spawnYs(n);
    for (var i = 0; i < config.players.length; i++) {
      var pc = config.players[i];
      if (!ROLE_STATS[pc.role]) throw new Error('sim: bad role ' + pc.role);
      if (pc.team === 'A') {
        if (countA >= n) throw new Error('sim: too many players in team A');
        players.push(makeChar(String(pc.id), 'A', pc.role, 160, ysA[countA++], pc.bot, pc.nick));
      } else if (pc.team === 'B') {
        if (countB >= n) throw new Error('sim: too many players in team B');
        players.push(makeChar(String(pc.id), 'B', pc.role, 740, ysB[countB++], pc.bot, pc.nick));
      } else throw new Error('sim: bad team ' + pc.team);
    }
    for (var k = 0; k < players.length; k++) players[k].ai.nextDecisionAt = 500 + rng.next() * 600;

    return {
      version: SIM_VERSION,
      seed: seed >>> 0,
      rng: rng,
      time: 0,
      tick: 0,
      mode: n,
      arenaIndex: arenaIndex,
      durationMs: config.durationMs || DEFAULT_DURATION_MS,
      players: players,
      snowballs: [],
      nextSnowballId: 1,
      dynamicObstacles: [],
      gameOver: false,
      winner: null,          // 'A' | 'B' | null (ничья/не закончен)
      events: []
    };
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
        if (p.stunTimer > 0 || p.charging) return false;
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
    var flightDuration = 0.4 + power * 0.35, travelDistance = 140 + power * 380;
    var speedH = travelDistance / flightDuration, vz0 = 0.5 * GRAVITY * flightDuration;
    var special = p.pendingSpecialThrow ? SPECIALS[p.role].type : null;
    p.pendingSpecialThrow = false;
    state.snowballs.push({
      id: state.nextSnowballId++,
      x: p.x, y: p.y, z: 0, vx: dirX * speedH, vy: dirY * speedH, vz: vz0, t: 0, flightDuration: flightDuration,
      team: p.team, ownerId: p.id, radius: special === 'explosive' ? 9 : 6,
      explosive: special === 'explosive', freeze: special === 'freeze'
    });
    emit(state, { type: 'throw', playerId: p.id, power: power, special: special });
  }

  // ============================================================
  // СПЕЦСПОСОБНОСТИ
  // ============================================================
  function useSpecial(state, p, aimX, aimY) {
    if (!alive(p) || p.stunTimer > 0) return false;
    var spec = SPECIALS[p.role];
    if (!spec || p.specialCooldown > 0) return false;
    if (spec.type === 'wall') placeShieldWall(state, p, aimX, aimY);
    else p.pendingSpecialThrow = true;
    p.specialCooldown = spec.cooldown;
    emit(state, { type: 'special', playerId: p.id, special: spec.type });
    return true;
  }
  function placeShieldWall(state, p, aimX, aimY) {
    var dx = aimX - p.x, dy = aimY - p.y, dist = Math.hypot(dx, dy) || 1;
    var dirX = dx / dist, dirY = dy / dist;
    state.dynamicObstacles.push({ type:'rect', x: p.x + dirX * 45, y: p.y + dirY * 45, w:55, h:16, height:20, expiresAt: state.time + WALL_LIFETIME_MS, team: p.team });
    emit(state, { type: 'wallPlaced', playerId: p.id });
  }

  // ============================================================
  // ГЕОМЕТРИЯ ПРЕПЯТСТВИЙ
  // ============================================================
  function getAllObstacles(state) {
    var res = ARENAS[state.arenaIndex].obstacles.slice();
    for (var i = 0; i < state.dynamicObstacles.length; i++) {
      var o = state.dynamicObstacles[i];
      if (o.expiresAt > state.time) res.push(o);
    }
    return res;
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
  function findNearestEnemy(state, p) {
    var best = null, bestD = Infinity;
    for (var i = 0; i < state.players.length; i++) {
      var q = state.players[i];
      if (q.team === p.team || !alive(q)) continue;
      var d = Math.hypot(q.x - p.x, q.y - p.y);
      if (d < bestD) { bestD = d; best = q; }
    }
    return best;
  }
  function checkDodge(state, p) {
    var now = state.time;
    if (now < p.ai.dodgeUntil) return true;
    for (var i = 0; i < state.snowballs.length; i++) {
      var s = state.snowballs[i];
      if (s.team === p.team) continue;
      var remaining = s.flightDuration - s.t;
      if (remaining > 0.35 || remaining <= 0) continue;
      var fx = s.x + s.vx * remaining, fy = s.y + s.vy * remaining;
      if (Math.hypot(fx - p.x, fy - p.y) < p.radius + 28) {
        var perpX = -s.vy, perpY = s.vx, norm = Math.hypot(perpX, perpY) || 1, side = state.rng.next() < 0.5 ? 1 : -1;
        p.moveTarget = clampToArena(p.x + (perpX / norm) * 70 * side, p.y + (perpY / norm) * 70 * side, p.radius);
        p.ai.dodgeUntil = now + 350; p.charging = false;
        return true;
      }
    }
    return false;
  }
  function updateAI(state, obs, p) {
    var now = state.time, rng = state.rng;
    if (!alive(p) || p.stunTimer > 0) { p.charging = false; return; }
    if (checkDodge(state, p)) return;
    var enemy = findNearestEnemy(state, p);
    if (!enemy) return;
    var cfg = ROLE_AI[p.role], spec = SPECIALS[p.role];

    if (p.charging) {
      var held = (now - p.chargeStart) / 1000;
      if (held >= p.ai.chargeDuration) {
        var nx = enemy.x + (rng.next() - 0.5) * cfg.noise * 2, ny = enemy.y + (rng.next() - 0.5) * cfg.noise * 2;
        p.aimX = nx; p.aimY = ny;
        throwSnowball(state, p, nx, ny, Math.min(p.ai.chargeDuration / 1.2, 1));
        p.charging = false; p.ai.nextDecisionAt = now + cfg.decisionEvery;
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

    if (spec && spec.type === 'wall' && p.hp <= 2 && dist < 260 && p.specialCooldown <= 0) {
      useSpecial(state, p, enemy.x, enemy.y);
      p.ai.nextDecisionAt = now + 400; return;
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
      p.ai.nextDecisionAt = now + cfg.decisionEvery; return;
    }
    if (dist > cfg.maxRange) {
      var toward = Math.atan2(enemy.y - p.y, enemy.x - p.x);
      p.moveTarget = clampToArena(p.x + Math.cos(toward) * 110, p.y + Math.sin(toward) * 110, p.radius);
      p.ai.nextDecisionAt = now + cfg.decisionEvery * 0.7; return;
    }
    // Сила броска подбирается так, чтобы снежок приземлился чуть за целью:
    // дальность полёта = 140 + power*380, окно попадания — последние ~50 px дуги.
    // (В прототипе бралось 0.35 + dist/500, и боты систематически перебрасывали цель.)
    var testPower = Math.min(1, Math.max(0, (dist + 20 - 140) / 380));
    if (canHitTarget(obs, p, enemy.x, enemy.y, testPower)) {
      if (spec && (spec.type === 'explosive' || spec.type === 'freeze') && p.specialCooldown <= 0 && rng.next() < 0.6) {
        useSpecial(state, p, enemy.x, enemy.y);
      }
      p.charging = true; p.chargeStart = now;
      p.ai.chargeDuration = Math.max(0.25, testPower * 1.2 + (rng.next() - 0.5) * 0.12);
      p.aimX = enemy.x; p.aimY = enemy.y;
      emit(state, { type: 'chargeStart', playerId: p.id });
    } else {
      var perp = Math.atan2(enemy.y - p.y, enemy.x - p.x) + (rng.next() < 0.5 ? 1 : -1) * Math.PI / 2;
      p.moveTarget = clampToArena(p.x + Math.cos(perp) * 90, p.y + Math.sin(perp) * 90, p.radius);
      p.ai.nextDecisionAt = now + cfg.decisionEvery * 0.6;
    }
  }

  // ============================================================
  // ФИЗИКА / ОБНОВЛЕНИЕ
  // ============================================================
  function moveCharacter(obs, p, dt) {
    if (!alive(p) || p.stunTimer > 0) { p.isMoving = false; return; }
    var dx = p.moveTarget.x - p.x, dy = p.moveTarget.y - p.y, dist = Math.hypot(dx, dy);
    p.isMoving = dist > 2;
    if (p.isMoving) {
      var step = Math.min(p.speed * dt, dist);
      var nx = p.x + (dx / dist) * step, ny = p.y + (dy / dist) * step;
      var c = clampToArena(nx, ny, p.radius); p.x = c.x; p.y = c.y;
      p.animPhase += dt * (p.speed / 25);
    }
    resolveObstacleCollisions(obs, p);
  }
  function applyHit(state, target, freezeBonus, x, y) {
    target.hp -= 1;
    target.hitAt = state.time;
    target.charging = false; // после попадания боец теряет возможность атаковать
    if (target.hp <= 0) {
      target.hp = 0; target.koed = true; target.stunTimer = 0; target.koAt = state.time;
      emit(state, { type: 'ko', targetId: target.id, x: x, y: y });
    } else {
      target.stunTimer = (target.hp === 2 ? 0.5 : 1.0) + (freezeBonus || 0);
      emit(state, { type: 'hit', targetId: target.id, x: x, y: y, freeze: !!freezeBonus, hp: target.hp });
    }
  }
  function updateSnowballs(state, obs, dt) {
    for (var i = state.snowballs.length - 1; i >= 0; i--) {
      var s = state.snowballs[i];
      s.t += dt; s.x += s.vx * dt; s.y += s.vy * dt;
      s.z = s.vz * s.t - 0.5 * GRAVITY * s.t * s.t;

      var dead = false, hitWall = false, directHit = false;
      if (s.t >= s.flightDuration || s.z < 0 || s.x < 0 || s.x > W || s.y < 0 || s.y > H) dead = true;
      if (!dead && wallBlocks(obs, s.x, s.y, s.z)) { dead = true; hitWall = true; }
      if (!dead && s.z <= 14) {
        for (var k = 0; k < state.players.length; k++) {
          var p = state.players[k];
          if (p.team === s.team || !alive(p)) continue;
          if (Math.hypot(p.x - s.x, p.y - s.y) <= p.radius + s.radius) {
            applyHit(state, p, s.freeze ? 1.0 : 0, s.x, s.y); dead = true; directHit = true; break;
          }
        }
      }
      if (dead) {
        if (s.explosive) {
          emit(state, { type: 'explosion', x: s.x, y: s.y });
          for (var m = 0; m < state.players.length; m++) {
            var q = state.players[m];
            if (q.team === s.team || !alive(q)) continue;
            if (Math.hypot(q.x - s.x, q.y - s.y) <= EXPLOSION_RADIUS) applyHit(state, q, 0, s.x, s.y);
          }
        } else if (hitWall) {
          emit(state, { type: 'wallHit', x: s.x, y: s.y });
        } else if (!directHit) {
          emit(state, { type: 'miss', x: s.x, y: s.y });
        }
        state.snowballs.splice(i, 1);
      }
    }
    // чистим протухшие стены
    for (var w = state.dynamicObstacles.length - 1; w >= 0; w--) {
      if (state.dynamicObstacles[w].expiresAt <= state.time) state.dynamicObstacles.splice(w, 1);
    }
  }
  function updateTimers(p, dt) {
    if (p.stunTimer > 0) p.stunTimer = Math.max(0, p.stunTimer - dt);
    if (p.specialCooldown > 0) p.specialCooldown = Math.max(0, p.specialCooldown - dt);
  }
  function checkWin(state) {
    if (state.gameOver) return;
    var aAlive = teamAlive(state, 'A').length, bAlive = teamAlive(state, 'B').length;
    if (bAlive === 0 && aAlive === 0) { state.gameOver = true; state.winner = null; }
    else if (bAlive === 0) { state.gameOver = true; state.winner = 'A'; }
    else if (aAlive === 0) { state.gameOver = true; state.winner = 'B'; }
    else if (state.time >= state.durationMs) { state.gameOver = true; state.winner = null; }
    if (state.gameOver) emit(state, { type: 'matchEnd', winner: state.winner });
  }

  /**
   * Продвинуть симуляцию на dt секунд (сервер зовёт с фиксированным шагом 1/20).
   * Возвращает массив событий, произошедших за шаг.
   */
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
        updateTimers(p, sdt);
        if (p.bot) updateAI(state, obs, p);
        moveCharacter(obs, p, sdt);
      }
      updateSnowballs(state, obs, sdt);
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
      players.push({
        id: p.id, team: p.team, role: p.role, nick: p.nick, bot: p.bot,
        x: round1(p.x), y: round1(p.y), hp: p.hp,
        stun: round1(p.stunTimer), koed: p.koed, koAt: p.koAt, hitAt: p.hitAt,
        moving: p.isMoving, anim: round1(p.animPhase),
        charging: p.charging, power: round1(chargePower(state, p) * 100) / 100,
        aimX: round1(p.aimX), aimY: round1(p.aimY),
        special: p.pendingSpecialThrow, cd: round1(p.specialCooldown)
      });
    }
    var balls = [];
    for (var k = 0; k < state.snowballs.length; k++) {
      var s = state.snowballs[k];
      balls.push({ id: s.id, x: round1(s.x), y: round1(s.y), z: round1(s.z), r: s.radius, team: s.team, ex: s.explosive, fr: s.freeze });
    }
    var walls = [];
    for (var m = 0; m < state.dynamicObstacles.length; m++) {
      var o = state.dynamicObstacles[m];
      walls.push({ x: o.x, y: o.y, w: o.w, h: o.h, team: o.team, ttl: Math.max(0, o.expiresAt - state.time), life: WALL_LIFETIME_MS });
    }
    return {
      v: SIM_VERSION, tick: state.tick, time: Math.round(state.time),
      timeLeft: Math.max(0, Math.round(state.durationMs - state.time)),
      mode: state.mode, arena: state.arenaIndex,
      over: state.gameOver, winner: state.winner,
      players: players, balls: balls, walls: walls
    };
  }

  return {
    SIM_VERSION: SIM_VERSION,
    W: W, H: H, GRAVITY: GRAVITY, CHARGE_FULL_MS: CHARGE_FULL_MS, KO_ANIM_MS: KO_ANIM_MS,
    ARENAS: ARENAS, ROLE_STATS: ROLE_STATS, SPECIALS: SPECIALS, MODES: MODES,
    HERO_DESCRIPTIONS: HERO_DESCRIPTIONS, ABILITY_HINT_TEXT: ABILITY_HINT_TEXT, ALL_ROLES: ALL_ROLES,
    makeRng: makeRng, shuffle: shuffle,
    canHitTarget: canHitTarget, // чистая функция для клиента: упрётся ли снежок в препятствие (луч прицела)
    createMatch: createMatch,
    applyInput: applyInput,
    setBot: setBot,
    step: step,
    snapshot: snapshot,
    isOver: function (state) { return state.gameOver; },
    winner: function (state) { return state.winner; }
  };
});
