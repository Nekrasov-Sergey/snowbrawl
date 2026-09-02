/* Экраны, ввод, HUD и связка сетевого/оффлайн-матча с рендером. */
(function () {
  'use strict';
  var Sim = window.SnowBrawlSim, Audio_ = window.SBAudio, Net = window.SBNet;
  var $ = function (id) { return document.getElementById(id); };
  var BUILD = document.querySelector('meta[name=build]').content;
  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) { /* игнор */ } }
  };

  var SCREENS = ['nick', 'menu', 'mode', 'character', 'map', 'search', 'createroom', 'joinroom', 'lobby', 'game'];
  var app = {
    screen: 'nick',
    nick: store.get('sb.nick') || '',
    token: store.get('sb.token') || '',
    me: null,                 // playerId с сервера
    net: null,
    connected: false,
    draining: false,
    flow: 'qm',               // qm | offline — куда ведёт экран выбора бойца
    qm: { mode: 3, role: null },
    offline: { mode: 3, role: null, arena: 0 },
    create: { mode: 3, arena: 0 },
    room: null,               // последнее room.state
    game: null                // активный матч (см. startNetMatch / startOfflineMatch)
  };

  // ------------------------------------------------------------
  // Утилиты UI
  // ------------------------------------------------------------
  function goto(name) {
    app.screen = name;
    SCREENS.forEach(function (s) { $('screen-' + s).hidden = (s !== name); });
    if (name === 'mode') buildModeGrid();
    if (name === 'character') buildHeroGrid();
    if (name === 'map') buildMapGrid();
    if (name === 'createroom') buildCreateGrids();
    if (name === 'menu') $('menuNick').textContent = app.nick;
    if (name !== 'game' && app.game) stopGame();
  }
  var toastTimer = null;
  function toast(msg) {
    var t = $('toast'); t.textContent = msg; t.hidden = false;
    clearTimeout(toastTimer); toastTimer = setTimeout(function () { t.hidden = true; }, 3500);
  }
  var ERR_TEXT = {
    bad_nick: 'Ник: 2–16 символов, буквы, цифры, пробел, дефис.',
    room_not_found: 'Комната с таким кодом не найдена.',
    room_full: 'Комната заполнена.',
    room_limit: 'С вашего адреса уже создано слишком много комнат.',
    busy: 'Сначала выйдите из текущей комнаты или матча.',
    draining: 'Сервер скоро перезапустится: новые матчи временно не начинаются.',
    server_full: 'Сервер переполнен, попробуйте позже.',
    bad_version: 'Версия игры устарела, обновите страницу.',
    not_allowed: 'Действие недоступно.',
    kicked: 'Вас выгнали из комнаты.'
  };
  function fmtTime(ms) { var s = Math.ceil(ms / 1000); return Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2); }

  $('soundToggle').textContent = Audio_.isEnabled() ? '🔊' : '🔇';
  $('soundToggle').onclick = function () { Audio_.setEnabled(!Audio_.isEnabled()); $('soundToggle').textContent = Audio_.isEnabled() ? '🔊' : '🔇'; };
  document.addEventListener('pointerdown', function () { Audio_.unlock(); }, { once: true });

  // ------------------------------------------------------------
  // Сеть
  // ------------------------------------------------------------
  function connect() {
    if (app.net) return;
    app.net = Net.connect({
      hello: function () { return { token: app.token, nick: app.nick, build: BUILD }; },
      onState: function (state) {
        app.connected = (state === 'open');
        var el = $('connState');
        el.className = state === 'open' ? 'on' : (state === 'closed' ? 'off' : '');
        el.title = state === 'open' ? 'Соединение установлено' : 'Нет соединения с сервером';
        if (app.game && !app.game.offline) $('reconnectOverlay').hidden = (state === 'open');
        if (state !== 'open' && app.screen === 'search') { /* сервер снял нас с очереди при разрыве */ goto('menu'); toast('Связь потеряна, поиск отменён.'); }
      },
      onMessage: onMessage
    });
  }
  function send(type, data) { if (!app.net || !app.net.send(type, data)) toast('Нет соединения с сервером.'); }

  function onMessage(type, d) {
    switch (type) {
      case 'welcome':
        app.token = d.token; store.set('sb.token', d.token);
        app.me = d.playerId; app.nick = d.nick; store.set('sb.nick', d.nick);
        $('verSim').textContent = d.sim; $('menuNick').textContent = d.nick;
        setDrain(!!d.draining);
        // Восстановление места после переподключения.
        if (d.resume === 'queue') { if (app.screen !== 'search') goto('search'); }
        else if (d.resume === 'room') { if (app.screen !== 'lobby') goto('lobby'); }
        else if (d.resume === 'match') { /* придёт match.start */ }
        else if (app.screen === 'search' || app.screen === 'lobby' || (app.game && !app.game.offline)) { goto('menu'); }
        break;
      case 'error':
        toast(ERR_TEXT[d.code] || ('Ошибка: ' + (d.msg || d.code)));
        if (d.code === 'bad_nick') goto('nick');
        if (d.code === 'room_not_found' || d.code === 'room_full') $('joinMsg').textContent = ERR_TEXT[d.code];
        break;
      case 'reload':
        location.reload();
        break;
      case 'drain':
        setDrain(!!d.active);
        break;
      case 'queue.status':
        if (!d.inQueue) { if (app.screen === 'search') goto('menu'); break; }
        if (app.screen !== 'search') goto('search');
        $('searchCount').textContent = d.players + ' / ' + d.needed;
        $('searchTimer').textContent = Math.ceil(d.waitLeft / 1000);
        break;
      case 'room.state':
        app.room = d;
        // Во время матча и пока показано табло результата лобби не переключаем:
        // игрок сам нажмёт «В лобби» (или оно откроется по кнопке выхода).
        if (app.game && !app.game.offline) break;
        if (app.screen !== 'lobby') goto('lobby');
        renderLobby();
        break;
      case 'room.left':
        app.room = null;
        if (d && d.code === 'kicked') toast(ERR_TEXT.kicked);
        if (app.screen === 'lobby' || app.screen === 'game') goto('menu');
        break;
      case 'match.start':
        startNetMatch(d);
        break;
      case 'snapshot':
        if (app.game && !app.game.offline) app.game.onSnapshot(d);
        break;
      case 'match.end':
        if (app.game && !app.game.offline) app.game.onEnd(d);
        break;
      default: break;
    }
  }
  function setDrain(active) { app.draining = active; $('drainBanner').hidden = !active; }

  // ------------------------------------------------------------
  // Ник и меню
  // ------------------------------------------------------------
  $('nickOk').onclick = function () {
    var v = $('nickInput').value.trim().replace(/\s+/g, ' ');
    if (v.length < 2 || v.length > 16 || !/^[\p{L}\p{N} _\-]+$/u.test(v)) { $('nickMsg').textContent = ERR_TEXT.bad_nick; return; }
    Audio_.uiClick();
    app.nick = v; store.set('sb.nick', v); $('nickMsg').textContent = '';
    if (app.net) app.net.reconnectNow(); else connect();
    goto('menu');
  };
  $('nickInput').addEventListener('keydown', function (e) { if (e.key === 'Enter') $('nickOk').click(); });
  $('changeNick').onclick = function () { Audio_.uiClick(); $('nickInput').value = app.nick; goto('nick'); };

  $('btnQuickMatch').onclick = function () { Audio_.uiClick(); app.flow = 'qm'; goto('mode'); };
  $('btnCreateRoom').onclick = function () { Audio_.uiClick(); goto('createroom'); };
  $('btnJoinRoom').onclick = function () { Audio_.uiClick(); $('joinMsg').textContent = ''; goto('joinroom'); };
  $('btnOffline').onclick = function () { Audio_.uiClick(); app.flow = 'offline'; goto('mode'); };

  // ------------------------------------------------------------
  // Quick Match / оффлайн: режим → боец → (арена)
  // ------------------------------------------------------------
  function buildModeGrid() {
    var grid = $('modeGrid'); grid.innerHTML = '';
    var cur = app.flow === 'qm' ? app.qm : app.offline;
    Sim.MODES.forEach(function (n) {
      var card = document.createElement('div');
      card.className = 'modeCard' + (cur.mode === n ? ' selected' : '');
      card.textContent = n + ' на ' + n;
      card.onclick = function () { Audio_.uiClick(); cur.mode = n; goto('character'); };
      grid.appendChild(card);
    });
  }
  $('backFromMode').onclick = function () { Audio_.uiClick(); goto('menu'); };

  function heroCard(role, selected, onClick) {
    var stats = Sim.ROLE_STATS[role];
    var card = document.createElement('div');
    card.className = 'heroCard' + (selected ? ' selected' : '');
    card.innerHTML = '<div class="heroSwatch" style="background:' + stats.color + '"></div>' +
      '<div class="heroName">' + role + '</div><div class="heroDesc">' + Sim.HERO_DESCRIPTIONS[role] + '</div>';
    card.onclick = onClick;
    return card;
  }
  function buildHeroGrid() {
    var grid = $('heroGrid'); grid.innerHTML = '';
    var cur = app.flow === 'qm' ? app.qm : app.offline;
    Sim.ALL_ROLES.forEach(function (role) {
      grid.appendChild(heroCard(role, cur.role === role, function () { Audio_.uiClick(); cur.role = role; buildHeroGrid(); }));
    });
    $('nextFromCharacter').disabled = !cur.role;
    $('nextFromCharacter').textContent = app.flow === 'qm' ? 'Искать матч' : 'Далее';
  }
  $('backFromCharacter').onclick = function () { Audio_.uiClick(); goto('mode'); };
  $('nextFromCharacter').onclick = function () {
    Audio_.uiClick();
    if (app.flow === 'qm') {
      if (!app.qm.role) return;
      send('queue.join', { mode: app.qm.mode, role: app.qm.role });
      $('searchCount').textContent = '1 / ' + (2 * app.qm.mode);
      goto('search');
    } else {
      goto('map');
    }
  };
  $('cancelSearch').onclick = function () { Audio_.uiClick(); send('queue.leave'); goto('menu'); };

  function mapCard(i, selected, onClick) {
    var arena = Sim.ARENAS[i];
    var card = document.createElement('div');
    card.className = 'mapCard' + (selected ? ' selected' : '');
    card.innerHTML = '<div class="heroName">' + arena.name + '</div><div class="heroDesc">' + arena.obstacles.length + ' укрытий на арене</div>';
    card.onclick = onClick;
    return card;
  }
  function buildMapGrid() {
    var grid = $('mapGrid'); grid.innerHTML = '';
    Sim.ARENAS.forEach(function (_, i) {
      grid.appendChild(mapCard(i, app.offline.arena === i, function () { Audio_.uiClick(); app.offline.arena = i; buildMapGrid(); }));
    });
  }
  $('backFromMap').onclick = function () { Audio_.uiClick(); goto('character'); };
  $('startOfflineBtn').onclick = function () { Audio_.uiClick(); startOfflineMatch(); };

  // ------------------------------------------------------------
  // Комнаты
  // ------------------------------------------------------------
  function buildCreateGrids() {
    var mg = $('createModeGrid'); mg.innerHTML = '';
    Sim.MODES.forEach(function (n) {
      var card = document.createElement('div');
      card.className = 'modeCard' + (app.create.mode === n ? ' selected' : '');
      card.textContent = n + '×' + n;
      card.onclick = function () { Audio_.uiClick(); app.create.mode = n; buildCreateGrids(); };
      mg.appendChild(card);
    });
    var grid = $('createMapGrid'); grid.innerHTML = '';
    Sim.ARENAS.forEach(function (_, i) {
      grid.appendChild(mapCard(i, app.create.arena === i, function () { Audio_.uiClick(); app.create.arena = i; buildCreateGrids(); }));
    });
  }
  $('backFromCreate').onclick = function () { Audio_.uiClick(); goto('menu'); };
  $('createRoomBtn').onclick = function () { Audio_.uiClick(); send('room.create', { mode: app.create.mode, arena: app.create.arena }); };
  $('backFromJoin').onclick = function () { Audio_.uiClick(); goto('menu'); };
  $('btnJoinConfirm').onclick = function () {
    Audio_.uiClick();
    var code = $('joinCodeInput').value.trim().toUpperCase();
    if (!code) { $('joinMsg').textContent = 'Введите код.'; return; }
    $('joinMsg').textContent = '';
    send('room.join', { code: code });
  };
  $('joinCodeInput').addEventListener('keydown', function (e) { if (e.key === 'Enter') $('btnJoinConfirm').click(); });
  $('copyCode').onclick = function () {
    if (!app.room) return;
    if (navigator.clipboard) navigator.clipboard.writeText(app.room.code).then(function () { toast('Код скопирован: ' + app.room.code); });
  };
  $('leaveLobby').onclick = function () { Audio_.uiClick(); send('room.leave'); app.room = null; goto('menu'); };
  $('startRoomBtn').onclick = function () { Audio_.uiClick(); send('room.start'); };
  $('lobbyMode').onchange = function () { send('room.config', { mode: +$('lobbyMode').value, arena: +$('lobbyArena').value }); };
  $('lobbyArena').onchange = $('lobbyMode').onchange;

  function renderLobby() {
    var r = app.room; if (!r) return;
    var isHost = r.hostId === app.me;
    $('lobbyCode').textContent = r.code;
    var modeSel = $('lobbyMode'), arenaSel = $('lobbyArena');
    modeSel.innerHTML = Sim.MODES.map(function (n) { return '<option value="' + n + '"' + (n === r.mode ? ' selected' : '') + '>' + n + '×' + n + '</option>'; }).join('');
    arenaSel.innerHTML = Sim.ARENAS.map(function (a, i) { return '<option value="' + i + '"' + (i === r.arena ? ' selected' : '') + '>' + a.name + '</option>'; }).join('');
    modeSel.disabled = !isHost; arenaSel.disabled = !isHost;
    $('lobbyConfigHint').textContent = isHost ? 'Вы хост: меняйте режим и арену, выгоняйте игроков, запускайте матч.' : 'Режим и арену меняет хост.';
    var res = $('lobbyResult');
    if (r.lastWinner) {
      res.hidden = false;
      res.textContent = r.lastWinner === 'draw' ? 'Прошлый матч: ничья' : 'Прошлый матч выиграла команда ' + r.lastWinner;
    } else res.hidden = true;

    var byTeam = { A: {}, B: {} };
    r.players.forEach(function (p) { if (p.team) byTeam[p.team][p.index] = p; });
    ['A', 'B'].forEach(function (team) {
      var col = $('slots' + team); col.innerHTML = '';
      for (var i = 0; i < r.mode; i++) {
        var p = byTeam[team][i];
        var el = document.createElement('div');
        if (p) {
          el.className = 'slot taken' + (p.id === app.me ? ' me' : '');
          el.innerHTML = '<span>' + escapeHtml(p.nick) + (p.host ? '<span class="host">★ хост</span>' : '') + (p.connected ? '' : ' <span class="off">(нет связи)</span>') + '</span>' +
            '<span class="role">' + (p.role || 'боец случайный') + '</span>' +
            (isHost && p.id !== app.me ? '<button class="kick" data-id="' + p.id + '">выгнать</button>' : '');
        } else {
          el.className = 'slot';
          el.innerHTML = '<span class="botHint">свободно — займёт бот</span>';
          (function (t, idx) { el.onclick = function () { Audio_.uiClick(); send('room.slot', { team: t, index: idx }); }; })(team, i);
        }
        col.appendChild(el);
      }
    });
    Array.prototype.forEach.call(document.querySelectorAll('.slot .kick'), function (b) {
      b.onclick = function (e) { e.stopPropagation(); Audio_.uiClick(); send('room.kick', { playerId: b.getAttribute('data-id') }); };
    });
    var me = r.players.filter(function (p) { return p.id === app.me; })[0];
    var grid = $('lobbyHeroGrid'); grid.innerHTML = '';
    Sim.ALL_ROLES.forEach(function (role) {
      grid.appendChild(heroCard(role, me && me.role === role, function () { Audio_.uiClick(); send('room.role', { role: role }); }));
    });
    $('startRoomBtn').hidden = !isHost;
    $('startRoomBtn').disabled = r.inMatch || app.draining;
    $('lobbyWait').textContent = r.inMatch ? 'Матч идёт…' : (isHost ? '' : 'Ждём, когда хост начнёт матч.');
  }
  function escapeHtml(s) { return String(s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }

  // ------------------------------------------------------------
  // Матч: общая часть (ввод, HUD, рендер)
  // ------------------------------------------------------------
  var canvas = $('c'), overlay = $('overlay'), overlayText = $('overlayText'), overlaySub = $('overlaySub');
  var abilityBtn = $('abilityBtn'), abilityCd = $('abilityCd');
  var render = window.SBRender.create(canvas);
  var lastMouse = { x: 450, y: 280 };
  var rafId = null;

  function toLocal(e) {
    var r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * (Sim.W / r.width), y: (e.clientY - r.top) * (Sim.H / r.height) };
  }
  function myPlayer(snap) {
    if (!snap || !app.game) return null;
    for (var i = 0; i < snap.players.length; i++) if (snap.players[i].id === app.game.meId) return snap.players[i];
    return null;
  }
  function canAct(p) { return p && p.hp > 0 && !p.koed && p.stun <= 0; }

  // Локальный замах для мгновенного отклика (сервер подтвердит через снапшот).
  var local = { charging: false, start: 0, aimX: 0, aimY: 0, power: 0, dragging: false, audioStop: null, lastSent: 0 };
  function throttled() { var now = performance.now(); if (now - local.lastSent < 66) return false; local.lastSent = now; return true; }

  canvas.addEventListener('mousedown', function (e) {
    if (app.screen !== 'game' || !app.game || app.game.over) return;
    var pt = toLocal(e), me = myPlayer(app.game.lastSnap);
    if (!canAct(me)) return;
    var r = (Sim.ROLE_STATS[me.role] || { radius: 15 }).radius;
    if (Math.hypot(pt.x - me.x, pt.y - me.y) <= r + 10) {
      local.charging = true; local.start = performance.now(); local.aimX = pt.x; local.aimY = pt.y; local.power = 0; local.dragging = false;
      local.audioStop = Audio_.chargeLoopStart(function () { return local.power; });
      app.game.input('chargeStart', pt.x, pt.y);
    } else {
      local.dragging = true;
      app.game.input('move', pt.x, pt.y);
    }
  });
  canvas.addEventListener('mousemove', function (e) {
    var pt = toLocal(e); lastMouse = pt;
    if (app.screen !== 'game' || !app.game) return;
    if (local.charging) { local.aimX = pt.x; local.aimY = pt.y; if (throttled()) app.game.input('aim', pt.x, pt.y); }
    else if (local.dragging && throttled()) app.game.input('move', pt.x, pt.y);
  });
  window.addEventListener('mouseup', function () {
    if (!app.game) return;
    if (local.charging) {
      local.charging = false;
      if (local.audioStop) { local.audioStop(); local.audioStop = null; }
      var power = Math.min((performance.now() - local.start) / Sim.CHARGE_FULL_MS, 1);
      app.game.input('throw', local.aimX, local.aimY, power);
    }
    if (local.dragging) { local.dragging = false; app.game.input('move', lastMouse.x, lastMouse.y); }
  });
  window.addEventListener('keydown', function (e) {
    if (app.screen !== 'game' || !app.game) return;
    if (['q', 'Q', 'й', 'Й'].indexOf(e.key) >= 0) app.game.input('special', lastMouse.x, lastMouse.y);
  });
  abilityBtn.onclick = function () { if (app.game) app.game.input('special', lastMouse.x, lastMouse.y); };
  canvas.addEventListener('contextmenu', function (e) { e.preventDefault(); });

  function updateHUD(snap) {
    var me = myPlayer(snap);
    function row(p, right) {
      var pips = '';
      for (var i = 0; i < 3; i++) pips += '<span class="pip ' + (i < p.hp ? 'on ' + p.team.toLowerCase() : '') + '"></span>';
      var cls = 'charname' + (p.id === app.game.meId ? ' me' : '') + (p.bot ? ' bot' : '');
      var name = escapeHtml(p.nick) + ' · ' + p.role + (p.id === app.game.meId ? ' (вы)' : '');
      return right ? '<div class="charrow right"><span class="pips">' + pips + '</span><span class="' + cls + '" style="text-align:right">' + name + '</span></div>'
        : '<div class="charrow"><span class="' + cls + '">' + name + '</span><span class="pips">' + pips + '</span></div>';
    }
    $('teamA').innerHTML = snap.players.filter(function (p) { return p.team === 'A'; }).map(function (p) { return row(p, false); }).join('');
    $('teamB').innerHTML = snap.players.filter(function (p) { return p.team === 'B'; }).map(function (p) { return row(p, true); }).join('');
    $('matchTimer').textContent = fmtTime(snap.timeLeft);
    if (!me) return;
    var hasSpec = !!Sim.SPECIALS[me.role];
    if (!hasSpec) { abilityBtn.disabled = true; abilityBtn.textContent = 'Нет способности'; abilityCd.textContent = ''; }
    else {
      abilityBtn.textContent = me.special ? 'Способность заряжена' : 'Способность (Q)';
      if (me.cd > 0) { abilityBtn.disabled = true; abilityCd.textContent = me.cd.toFixed(1) + ' с'; }
      else { abilityBtn.disabled = false; abilityCd.textContent = me.special ? 'следующий бросок' : 'готова'; }
    }
  }

  function loop() {
    rafId = requestAnimationFrame(loop);
    renderOnce();
  }
  function renderOnce() {
    var g = app.game; if (!g || app.screen !== 'game') return null;
    var fr = g.frame();
    if (!fr || !fr.snap) return null;
    if (fr.events && fr.events.length) render.handleEvents(fr.events, Audio_);
    if (local.charging) local.power = Math.min((performance.now() - local.start) / Sim.CHARGE_FULL_MS, 1);
    var me = myPlayer(fr.snap);
    if (local.charging && !canAct(me)) { // оглушили во время замаха
      local.charging = false; if (local.audioStop) { local.audioStop(); local.audioStop = null; }
    }
    render.frame(fr.snap, g.meId, local);
    updateHUD(fr.snap);
    return fr;
  }

  function showGameScreen(g) {
    app.game = g;
    render.reset();
    overlay.style.display = 'none';
    $('reconnectOverlay').hidden = true;
    var myRole = null;
    g.players.forEach(function (p) { if (p.id === g.meId) myRole = p.role; });
    $('abilityHint').textContent = Sim.ABILITY_HINT_TEXT[myRole] || 'У вашего бойца нет активной способности — играйте позиционированием.';
    $('teamALabel').textContent = 'Команда A' + (g.myTeam === 'A' ? ' (вы)' : '');
    $('teamBLabel').textContent = 'Команда B' + (g.myTeam === 'B' ? ' (вы)' : '');
    $('teamA').innerHTML = ''; $('teamB').innerHTML = '';
    goto('game');
    if (!rafId) loop();
  }
  function showResult(winner, myTeam, reason) {
    var g = app.game; if (!g) return;
    g.over = true;
    if (local.charging) { local.charging = false; if (local.audioStop) { local.audioStop(); local.audioStop = null; } }
    overlay.style.display = 'flex';
    if (reason === 'shutdown') { overlayText.textContent = 'МАТЧ ПРЕРВАН'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Сервер перезапускается для обновления.'; }
    else if (reason === 'abandoned') { overlayText.textContent = 'МАТЧ ЗАВЕРШЁН'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Все игроки покинули матч.'; }
    else if (!winner) { overlayText.textContent = 'НИЧЬЯ'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Время вышло.'; Audio_.drawChord(); }
    else if (winner === myTeam) { overlayText.textContent = 'ПОБЕДА 🎉'; overlayText.style.color = '#7CFFB2'; overlaySub.textContent = 'Команда ' + winner + ' вывела из строя всех соперников.'; Audio_.victoryFanfare(); }
    else { overlayText.textContent = 'ПОРАЖЕНИЕ'; overlayText.style.color = '#ff8080'; overlaySub.textContent = 'Команда ' + winner + ' оказалась сильнее.'; Audio_.defeatChord(); }
    $('againBtn').textContent = g.offline ? 'Играть снова' : (g.roomCode ? 'В лобби' : 'Играть снова');
  }
  function stopGame() {
    var g = app.game; if (!g) return;
    app.game = null;
    if (local.charging) { local.charging = false; if (local.audioStop) { local.audioStop(); local.audioStop = null; } }
    local.dragging = false;
    if (g.stop) g.stop();
  }

  $('btnToMenu').onclick = function () {
    Audio_.uiClick();
    var g = app.game; if (!g) { goto('menu'); return; }
    if (!g.offline && !g.over) send('match.leave');
    else if (!g.offline && g.roomCode) send('room.leave');
    goto('menu');
  };
  $('toMenuBtn').onclick = function () { Audio_.uiClick(); $('btnToMenu').click(); };
  $('againBtn').onclick = function () {
    Audio_.uiClick();
    var g = app.game; if (!g) return;
    if (g.offline) { startOfflineMatch(); return; }
    if (g.roomCode) { stopGame(); goto('lobby'); if (app.room) renderLobby(); return; }
    stopGame();
    send('queue.join', { mode: app.qm.mode, role: app.qm.role });
    $('searchCount').textContent = '1 / ' + (2 * app.qm.mode);
    goto('search');
  };

  // ------------------------------------------------------------
  // Оффлайн-матч
  // ------------------------------------------------------------
  function startOfflineMatch() {
    stopGame();
    var drv = window.SBOffline.start({ mode: app.offline.mode, arena: app.offline.arena, role: app.offline.role });
    var g = {
      offline: true, meId: drv.meId, players: drv.players, myTeam: 'A', roomCode: '', over: false,
      input: function (kind, x, y, power) { if (!g.over) drv.input(kind, x, y, power); },
      frame: function () {
        if (g.over) return { snap: g.lastSnap, events: [] };
        var fr = drv.frame(); g.lastSnap = fr.snap;
        if (drv.isOver()) showResult(drv.winner(), 'A', drv.winner() ? 'ko' : 'timeout');
        return fr;
      },
      stop: function () { drv.stop(); }
    };
    showGameScreen(g);
  }

  // ------------------------------------------------------------
  // Сетевой матч
  // ------------------------------------------------------------
  function startNetMatch(d) {
    if (app.game && !app.game.offline && app.game.matchId === d.matchId) { $('reconnectOverlay').hidden = true; return; } // переподключение
    stopGame();
    var buffer = Net.snapshotBuffer(d.tickRate);
    var pending = [];
    var myTeam = 'A';
    d.players.forEach(function (p) { if (p.id === d.yourId) myTeam = p.team; });
    var g = {
      offline: false, matchId: d.matchId, meId: d.yourId, players: d.players, myTeam: myTeam, roomCode: d.roomCode || '', over: false,
      lastSnap: null,
      input: function (kind, x, y, power) {
        if (g.over) return;
        var inp = { kind: kind, x: Math.round(x * 10) / 10, y: Math.round(y * 10) / 10 };
        if (power !== undefined) inp.power = Math.round(power * 100) / 100;
        app.net.send('input', inp);
      },
      onSnapshot: function (s) {
        buffer.push(s.s);
        if (s.e && s.e.length) pending = pending.concat(s.e);
      },
      onEnd: function (e) {
        if (g.lastSnap == null && buffer.latest()) g.lastSnap = buffer.latest();
        showResult(e.winner, e.yourTeam || myTeam, e.reason);
      },
      frame: function () {
        var snap = buffer.current();
        if (!snap) return null;
        g.lastSnap = snap;
        var ev = pending; pending = [];
        return { snap: snap, events: ev };
      },
      stop: function () { buffer.clear(); }
    };
    showGameScreen(g);
  }

  // ------------------------------------------------------------
  // Старт
  // ------------------------------------------------------------
  // Отладочный хук для DevTools и автотестов: состояние приложения и ручной кадр.
  window.SBApp = { state: app, renderOnce: renderOnce, send: send };

  $('verBuild').textContent = BUILD;
  if (app.nick) { connect(); goto('menu'); }
  else { $('nickInput').value = ''; goto('nick'); }
})();
