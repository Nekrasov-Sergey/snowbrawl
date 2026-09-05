/* Экраны, ввод, HUD и связка сетевого/оффлайн-матча с рендером. */
(function () {
  'use strict';
  var Sim = window.SnowBrawlSim, Audio_ = window.SBAudio, Net = window.SBNet;
  var Settings = window.SBSettings, Device = window.SBDevice;
  var $ = function (id) { return document.getElementById(id); };
  var BUILD = document.querySelector('meta[name=build]').content;
  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) { /* игнор */ } }
  };

  var SCREENS = ['nick', 'menu', 'mode', 'character', 'map', 'search', 'createroom', 'joinroom', 'lobby', 'game', 'settings'];
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
    if (name === 'settings') renderSettings();
    if (name !== 'game' && app.game) stopGame();
    document.documentElement.classList.toggle('ingame', name === 'game');
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
      case 'pong':
        if (app.game && app.game.onPong) app.game.onPong();
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
  $('btnSettings').onclick = function () { Audio_.uiClick(); goto('settings'); };

  // ------------------------------------------------------------
  // Настройки (localStorage, см. settings.js)
  // ------------------------------------------------------------
  function renderSettings() {
    $('setAssist').checked = !!Settings.get('aimAssist');
    $('setHaptics').checked = !!Settings.get('haptics');
    $('setTouch').value = Settings.get('touch');
    $('setPc').value = pcMode();
    $('pcRow').hidden = Device.isTouch();
  }
  $('setPc').onchange = function () { Settings.set('pcControls', $('setPc').value); };
  $('setAssist').onchange = function () { Settings.set('aimAssist', $('setAssist').checked); };
  $('setHaptics').onchange = function () { Settings.set('haptics', $('setHaptics').checked); };
  $('setTouch').onchange = function () { Settings.set('touch', $('setTouch').value); Device.apply(); };
  $('backFromSettings').onclick = function () { Audio_.uiClick(); goto('menu'); };

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

  // Компактная карточка: цвет, имя и кнопка «i». Описание показывается в общем блоке infoEl
  // под сеткой, чтобы шесть бойцов помещались на экране телефона без прокрутки.
  function heroCard(role, selected, onClick, infoEl) {
    var stats = Sim.ROLE_STATS[role];
    var card = document.createElement('div');
    card.className = 'heroCard' + (selected ? ' selected' : '');
    card.innerHTML = '<div class="heroSwatch" style="background:' + stats.color + '"></div>' +
      '<div class="heroName">' + role + '</div><button type="button" class="heroInfoBtn" title="Описание">i</button>';
    card.onclick = onClick;
    card.querySelector('.heroInfoBtn').onclick = function (e) { e.stopPropagation(); Audio_.uiClick(); toggleHeroInfo(infoEl, role); };
    return card;
  }
  function toggleHeroInfo(el, role) {
    if (!el) return;
    if (!el.hidden && el.getAttribute('data-role') === role) { el.hidden = true; return; }
    el.setAttribute('data-role', role);
    el.innerHTML = '<b>' + role + '.</b> ' + Sim.HERO_DESCRIPTIONS[role];
    el.hidden = false;
  }
  function buildHeroGrid() {
    var grid = $('heroGrid'); grid.innerHTML = '';
    var cur = app.flow === 'qm' ? app.qm : app.offline;
    Sim.ALL_ROLES.forEach(function (role) {
      grid.appendChild(heroCard(role, cur.role === role, function () { Audio_.uiClick(); cur.role = role; buildHeroGrid(); }, $('heroInfo')));
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
      grid.appendChild(heroCard(role, me && me.role === role, function () { Audio_.uiClick(); send('room.role', { role: role }); }, $('lobbyHeroInfo')));
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
  var touchAbility = $('touchAbility'), touchAbilityCd = $('touchAbilityCd'), touchLayer = $('touchLayer');
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
  function radiusOf(p) { return (Sim.ROLE_STATS[p.role] || { radius: 15 }).radius; }
  function overMe(pt, me) { return !!me && Math.hypot(pt.x - me.x, pt.y - me.y) <= radiusOf(me) + 10; }

  // Слой намерений: мышь и стики дают команды сюда, он шлёт протокол и держит локальный замах
  // (intent.local) для мгновенного отклика в рендере — сервер подтвердит через снапшот.
  var chargeAudioStop = null;
  var intent = window.SBIntent.create({
    getGame: function () { return app.game; },
    getMe: myPlayer,
    canAct: canAct,
    useAssist: function () { return Device.isTouch() && !!Settings.get('aimAssist'); },
    onChargeStart: function () { chargeAudioStop = Audio_.chargeLoopStart(function () { return intent.local.power; }); },
    onChargeEnd: function () { if (chargeAudioStop) { chargeAudioStop(); chargeAudioStop = null; } }
  });
  var local = intent.local;

  // ---- источник намерений: мышь и клавиатура (ПК) ----
  // Режим wasd (по умолчанию): WASD/стрелки — движение, зажать ЛКМ в любой точке — замах в сторону
  // курсора, отпустить — бросок, отпустить над своим бойцом — отмена; Q или ПКМ — способность.
  // Режим classic: ЛКМ по бойцу — замах, отпустить снова над бойцом — отмена; ЛКМ мимо — идти в точку.
  function pcMode() { return Settings.get('pcControls') === 'classic' ? 'classic' : 'wasd'; }
  var mouse = { dragging: false };
  canvas.addEventListener('mousedown', function (e) {
    if (Device.isTouch() || app.screen !== 'game' || !app.game || app.game.over) return;
    var pt = toLocal(e), me = myPlayer(app.game.lastSnap);
    lastMouse = pt;
    if (!canAct(me)) return;
    if (e.button === 2) { if (pcMode() === 'wasd') intent.specialAt(pt.x, pt.y); return; }
    if (e.button !== 0) return;
    if (pcMode() === 'wasd' || overMe(pt, me)) intent.chargeStartAt(pt.x, pt.y);
    else { mouse.dragging = true; intent.moveTo(pt.x, pt.y, true); }
  });
  // Клавиатура: набор зажатых клавиш → нормированное направление в intent.setMoveDir (как стик).
  // По e.code, чтобы русская раскладка работала; повтор клавиши игнорируем.
  var KEYDIR = { KeyW: [0, -1], KeyS: [0, 1], KeyA: [-1, 0], KeyD: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1], ArrowLeft: [-1, 0], ArrowRight: [1, 0] };
  var keys = {};
  function applyKeys() {
    if (!app.game || Device.isTouch() || pcMode() !== 'wasd') { intent.setMoveDir(null); return; }
    var x = 0, y = 0;
    for (var k in keys) if (keys[k]) { x += KEYDIR[k][0]; y += KEYDIR[k][1]; }
    intent.setMoveDir(x || y ? { x: x, y: y } : null);
  }
  window.addEventListener('keyup', function (e) { if (keys[e.code]) { keys[e.code] = false; applyKeys(); } });
  window.addEventListener('blur', function () { keys = {}; applyKeys(); });
  canvas.addEventListener('mousemove', function (e) {
    var pt = toLocal(e); lastMouse = pt;
    if (Device.isTouch() || app.screen !== 'game' || !app.game) return;
    if (local.charging) intent.aimAt(pt.x, pt.y);
    else if (mouse.dragging) intent.moveTo(pt.x, pt.y, false);
  });
  window.addEventListener('mouseup', function (e) {
    if (!app.game) return;
    if (local.charging && !Device.isTouch()) {
      var pt = toLocal(e), me = myPlayer(app.game.lastSnap);
      if (overMe(pt, me)) intent.cancelCharge();
      else intent.throwAt(local.aimX, local.aimY);
    }
    if (mouse.dragging) { mouse.dragging = false; intent.moveTo(lastMouse.x, lastMouse.y, true); }
  });
  window.addEventListener('keydown', function (e) {
    if (app.screen !== 'game' || !app.game) return;
    if (KEYDIR[e.code]) {
      e.preventDefault();
      if (!e.repeat && !keys[e.code]) { keys[e.code] = true; applyKeys(); }
      return;
    }
    if (['q', 'Q', 'й', 'Й'].indexOf(e.key) >= 0) intent.specialAt(lastMouse.x, lastMouse.y);
  });
  abilityBtn.onclick = function () { if (app.game) intent.specialAt(lastMouse.x, lastMouse.y); };
  canvas.addEventListener('contextmenu', function (e) { e.preventDefault(); });

  // ---- источник намерений: стики (сенсорный экран) ----
  var touch = window.SBTouch.create({
    layer: touchLayer, zoneL: $('zoneL'), zoneR: $('zoneR'), stickL: $('stickL'), stickR: $('stickR'),
    ability: touchAbility, stickS: $('stickS'), intent: intent,
    getMe: function () { return app.game ? myPlayer(app.game.lastSnap) : null; },
    hasDirSpecial: function (role) { var sp = Sim.SPECIALS[role]; return !!sp && sp.type === 'wall'; }
  });
  var zonesHintTimer = null;

  // Полный экран (Android; на iPhone Safari недоступен — там режим «на экран Домой»).
  $('fsBtn').onclick = function () {
    if (document.fullscreenElement) { document.exitFullscreen(); return; }
    var el = document.documentElement;
    try { el.requestFullscreen({ navigationUI: 'hide' }).catch(function () { /* отказ — не страшно */ }); } catch (e) { /* игнор */ }
  };

  // Возврат из фона: сервер через 20 с без ввода отдаёт бойца боту; любой ввод возвращает управление.
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState !== 'visible' || !app.game || app.game.over || app.screen !== 'game') return;
    touch.reset(); intent.reset();
    var me = myPlayer(app.game.lastSnap);
    if (me) intent.moveTo(me.x, me.y, true);
    toast('Вы снова в игре.');
  });

  function vibrate(ms) {
    if (!Settings.get('haptics') || !Device.canVibrate()) return;
    try { navigator.vibrate(ms); } catch (e) { /* игнор */ }
  }

  // HUD пишется в DOM только при изменении: сигнатура составов/HP, секунда таймера, состояние способности.
  var hudCache = { sig: '', a: '', b: '', timer: '', abil: '' };
  function resetHudCache() { hudCache.sig = hudCache.a = hudCache.b = hudCache.timer = hudCache.abil = ''; }
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
    var sig = '';
    for (var i = 0; i < snap.players.length; i++) { var q = snap.players[i]; sig += q.id + ':' + q.hp + (q.koed ? 'k' : '') + ';'; }
    if (sig !== hudCache.sig) {
      hudCache.sig = sig;
      var a = snap.players.filter(function (p) { return p.team === 'A'; }).map(function (p) { return row(p, false); }).join('');
      var b = snap.players.filter(function (p) { return p.team === 'B'; }).map(function (p) { return row(p, true); }).join('');
      if (a !== hudCache.a) { hudCache.a = a; $('teamA').innerHTML = a; }
      if (b !== hudCache.b) { hudCache.b = b; $('teamB').innerHTML = b; }
    }
    var tm = fmtTime(snap.timeLeft);
    if (tm !== hudCache.timer) { hudCache.timer = tm; $('matchTimer').textContent = tm; }
    if (!me) return;
    var hasSpec = !!Sim.SPECIALS[me.role];
    var abil = (hasSpec ? '1' : '0') + (me.special ? 's' : '-') + (me.cd > 0 ? me.cd.toFixed(1) : '0');
    if (abil === hudCache.abil) return;
    hudCache.abil = abil;
    if (!hasSpec) {
      abilityBtn.disabled = true; abilityBtn.textContent = 'Нет способности'; abilityCd.textContent = '';
      touchAbility.hidden = true;
    } else {
      abilityBtn.textContent = me.special ? 'Способность заряжена' : 'Способность (Q)';
      touchAbility.hidden = !Device.isTouch();
      touchAbility.className = me.cd > 0 ? 'off' : (me.special ? 'armed' : 'ready');
      if (me.cd > 0) { abilityBtn.disabled = true; abilityCd.textContent = me.cd.toFixed(1) + ' с'; touchAbilityCd.textContent = Math.ceil(me.cd) + 'с'; }
      else { abilityBtn.disabled = false; abilityCd.textContent = me.special ? 'следующий бросок' : 'готова'; touchAbilityCd.textContent = ''; }
    }
  }
  function myHitEvents(events) {
    if (!events || !app.game) return;
    for (var i = 0; i < events.length; i++) {
      var e = events[i];
      if ((e.type === 'hit' || e.type === 'ko') && e.targetId === app.game.meId) { vibrate(e.type === 'ko' ? 80 : 30); return; }
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
    if (fr.events && fr.events.length) { render.handleEvents(fr.events, Audio_); myHitEvents(fr.events); }
    intent.tick(); // сила замаха, сброс при оглушении, переотправка цели движения по стику
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
    var isTouch = Device.isTouch();
    var abilityHint = Sim.ABILITY_HINT_TEXT[myRole] || 'У вашего бойца нет активной способности — играйте позиционированием.';
    if (isTouch) abilityHint = abilityHint.replace('(Q)', '(кнопка справа)');
    $('abilityHint').textContent = abilityHint;
    $('hint').textContent = isTouch
      ? 'Левая половина — движение, правая — замах и бросок; вернуть палец в центр — отмена.'
      : (pcMode() === 'wasd'
        ? 'WASD — движение. Зажать ЛКМ — замах, отпустить — бросок, над бойцом — отмена. Q или ПКМ — способность.'
        : 'ЛКМ на бойце — заряд броска, отпустить над бойцом — отмена. ЛКМ мимо — перемещение.');
    $('netStat').hidden = !!g.offline; $('netStat').textContent = ''; $('netStat').className = '';
    $('teamALabel').textContent = 'Команда A' + (g.myTeam === 'A' ? ' (вы)' : '');
    $('teamBLabel').textContent = 'Команда B' + (g.myTeam === 'B' ? ' (вы)' : '');
    $('teamA').innerHTML = ''; $('teamB').innerHTML = ''; resetHudCache();
    Device.apply();
    touchLayer.hidden = !isTouch;
    touch.reset(); intent.reset();
    $('fsBtn').hidden = !(isTouch && Device.fullscreenAvailable());
    clearTimeout(zonesHintTimer);
    if (isTouch) {
      touchLayer.classList.add('showZones');
      zonesHintTimer = setTimeout(function () { touchLayer.classList.remove('showZones'); }, 4000);
      if (Sim.SPECIALS[myRole]) toast(abilityHint);
      if (Device.isPortrait() && !sessionFlag('sb.portraitHint')) toast('В горизонтальном положении телефона играть удобнее.');
    }
    goto('game');
    if (!rafId) loop();
  }
  function sessionFlag(k) {
    try { if (sessionStorage.getItem(k)) return true; sessionStorage.setItem(k, '1'); } catch (e) { /* игнор */ }
    return false;
  }
  function showResult(winner, myTeam, reason) {
    var g = app.game; if (!g) return;
    g.over = true;
    intent.reset(); touch.reset();
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
    intent.reset(); touch.reset(); mouse.dragging = false; keys = {};
    if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
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
    // Задержка: ping раз в 2 с (сервер отвечает pong) + джиттер снапшотов из буфера.
    // Индикатор раз в секунду; если RTT > 200 мс или джиттер > 100 мс держатся 3 с — тост про VPN.
    var net = { pingAt: 0, rtt: 0, badSince: 0, lastWarn: 0 };
    var pingTimer = setInterval(function () { net.pingAt = performance.now(); app.net.send('ping'); }, 2000);
    var statTimer = setInterval(function () {
      if (g.over || app.screen !== 'game') return;
      var jitter = buffer.jitter(), el = $('netStat'), now = performance.now();
      var bad = net.rtt > 200 || jitter > 100;
      if (!bad) net.badSince = 0; else if (!net.badSince) net.badSince = now;
      el.textContent = net.rtt ? '⇄ ' + Math.round(net.rtt) + ' мс' : '';
      el.className = bad ? 'bad' : '';
      if (net.badSince && now - net.badSince >= 3000 && now - net.lastWarn > 60000) {
        net.lastWarn = now;
        toast('Высокая задержка сети. Если включён VPN, попробуйте его выключить.');
      }
    }, 1000);
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
      onPong: function () {
        if (!net.pingAt) return;
        var sample = performance.now() - net.pingAt;
        net.rtt = net.rtt ? net.rtt * 0.5 + sample * 0.5 : sample;
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
      stop: function () { buffer.clear(); clearInterval(pingTimer); clearInterval(statTimer); }
    };
    showGameScreen(g);
  }

  // ------------------------------------------------------------
  // Старт
  // ------------------------------------------------------------
  // Отладочный хук для DevTools и автотестов: состояние приложения и ручной кадр.
  window.SBApp = { state: app, renderOnce: renderOnce, send: send, intent: intent };

  Device.apply();
  $('verBuild').textContent = BUILD;
  if (app.nick) { connect(); goto('menu'); }
  else { $('nickInput').value = ''; goto('nick'); }
})();
