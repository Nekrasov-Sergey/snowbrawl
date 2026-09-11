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

  var SCREENS = ['nick', 'menu', 'createroom', 'rooms', 'lobby', 'game', 'settings', 'tutlist'];
  var app = {
    screen: 'nick',
    nav: [],                  // стек экранов для кнопки «Назад»
    nick: store.get('sb.nick') || '',
    token: store.get('sb.token') || '',
    me: null,                 // playerId с сервера
    rank: '',                 // роль модерации: '' | 'admin' | 'creator' (цвет ника, права в чате)
    net: null,
    connected: false,
    ping: { at: 0, rtt: 0, jitter: 0 },  // задержка до сервера: меряется всегда, а не только в матче
    draining: false,
    section: 'pvp',           // pvp | pve — раздел, в котором игрок сейчас ходит
    create: { mode: 3, arena: 0, visibility: 'open', botLevel: 1 },
    // Список комнат: страница приходит пушем от сервера.
    rooms: { page: 0, data: null },
    roomMatch: null,          // последнее room.match — состояние идущего матча для лобби
    chat: { msgs: [], unread: 0, open: false }, // общий чат меню
    offline: { mode: 1, role: 'Раннер', arena: 0, botLevel: 0, gameMode: 'pvp', campaign: true },
    room: null,               // последнее room.state
    game: null                // активный матч (см. startNetMatch / startOfflineMatch)
  };

  // ------------------------------------------------------------
  // Утилиты UI
  // ------------------------------------------------------------
  // Переход на экран. Стек nav нужен кнопке «Назад»: экранов-хабов стало больше, и зашитая
  // в каждую кнопку цель начинала врать (список комнат достижим только из раздела).
  // ---- адрес и история браузера ----
  // Экраны вне боя живут в хэше: перезагрузка страницы возвращает игрока туда, где он был, и
  // работает кнопка «назад» браузера. Лобби и матч в хэш не пишем — их восстанавливает сервер
  // по токену (welcome.resume), а в адресе остаётся тот экран, с которого игрок туда попал.
  var ROUTES = {
    '#/': { screen: 'menu' },
    '#/rooms/pvp': { screen: 'rooms', section: 'pvp' },
    '#/rooms/pve': { screen: 'rooms', section: 'pve' },
    '#/createroom': { screen: 'createroom' },
    '#/tutlist': { screen: 'tutlist' },
    '#/settings': { screen: 'settings' }
  };
  var ROUTABLE = { menu: 1, rooms: 1, createroom: 1, tutlist: 1, settings: 1 };
  var routing = false, navSeq = 0, leavingSite = false, leaveTimer = null;

  function hashOf(name) {
    if (name === 'rooms') return '#/rooms/' + (app.section === 'pve' ? 'pve' : 'pvp');
    for (var h in ROUTES) if (ROUTES[h].screen === name && !ROUTES[h].section) return h;
    return '#/';
  }
  function parseHash(h) { return ROUTES[h] || null; }

  var route = {
    write: function (name, opts) {
      if (routing) return;
      var st = { sb: 1, screen: name, seq: ++navSeq };
      try {
        if (name === 'lobby' || name === 'game') {
          // Лобби и бой в истории записи не занимают. Раньше занимали, и выход из комнаты
          // (goto('rooms', {back:true}) → replaceState) превращал запись лобби во ВТОРУЮ запись
          // списка комнат: «назад» приходилось нажимать дважды — первое нажатие съедало дубль.
          // Отдельная запись им и не нужна: onPop разбирает бой и лобби по app.screen, ещё до
          // обращения к состоянию записи, поэтому «назад» там всё равно работает как «Выйти».
          return;
        }
        if (!ROUTABLE[name]) {
          // Ник адрес не меняет, но запись нужна: «назад» с него возвращает в меню.
          history.pushState(st, '', location.hash || '#/');
          return;
        }
        // Заменяем, а не пушим, если текущая запись уже описывает этот же экран. Так выход из
        // боя или урока на тот экран, с которого игрок в него вошёл, не создаёт дубль записи —
        // иначе «назад» надо нажимать дважды: первое нажатие съедало бы дубль впустую.
        var cur = history.state;
        var same = cur && cur.sb && cur.screen === name;
        if ((opts && opts.back) || same) history.replaceState(st, '', hashOf(name));
        else history.pushState(st, '', hashOf(name));
      } catch (e) { /* приватный режим может запретить историю — игра работает и так */ }
    }
  };

  // Служебная запись-страж в начале истории: её съедает «назад» на корневом экране, и это
  // единственный надёжный признак «дальше выход с сайта».
  function pushGuard() {
    try {
      history.replaceState({ sb: 1, guard: true }, '', location.hash || '#/');
    } catch (e) { /* игнор */ }
  }

  // Попытка уйти с сайта. Перезагрузка страницы не стирает записи истории от прошлой загрузки,
  // поэтому history.back() часто уводит не наружу, а на нашу же старую запись — и события этого
  // перехода надо проигнорировать целиком. Флаг снимает короткий таймер, а не первое событие:
  // один переход поднимает и popstate, и hashchange, и второе из них увело бы по старому адресу.
  // Раньше флаг не снимался вовсе — «назад» умирал до следующей перезагрузки.
  function leaveSite(steps) {
    leavingSite = true;
    clearTimeout(leaveTimer);
    leaveTimer = setTimeout(function () {
      leavingSite = false;
      // Уйти не удалось: приводим адрес к текущему экрану, иначе он врёт. Следующее нажатие
      // «назад» съест следующую запись и в конце всё-таки уведёт с сайта.
      route.write(app.screen, { back: true });
    }, 250);
    history.go(-(steps || 1));
  }

  function onPop() {
    if (leavingSite) return;
    if (app.screen === 'game') { leaveGame(); return; }
    if (app.screen === 'lobby') { leaveLobby(); return; }
    var st = history.state;
    if (st && st.guard) {
      // Ниже стража лежат либо записи прошлой загрузки страницы, либо чужой сайт. Если игрок не
      // на корневом экране, «назад» обязан привести в меню: после F5 в обучении или в списке
      // комнат он иначе не мог вернуться в меню вообще, только перезагрузкой.
      if (app.screen !== 'menu' && app.screen !== 'nick') { goto('menu'); return; }
      if (!Device.isTouch()) {
        // На ПК «назад» — осознанное действие мышью, лишний вопрос там раздражает.
        leaveSite();
        return;
      }
      // Восстанавливаем съеденную запись и спрашиваем: на телефоне одно касание закрывало вкладку.
      route.write(app.screen, null);
      askExit();
      return;
    }
    var target = parseHash(location.hash) || { screen: 'menu' };
    // Сравниваем и раздел: иначе возврат на запись того же списка комнат вызывал перерисовку
    // вместо «ничего не делать», и нажатие выглядело пустым.
    if (target.screen === app.screen && (!target.section || target.section === app.section)) return;
    routing = true;
    if (target.section) app.section = target.section;
    if (app.nav.length && app.nav[app.nav.length - 1] === target.screen) app.nav.pop();
    goto(target.screen, { back: true });
    routing = false;
    if (!parseHash(location.hash)) { try { history.replaceState(history.state, '', '#/'); } catch (e) { /* игнор */ } }
  }
  window.addEventListener('popstate', onPop);
  window.addEventListener('hashchange', onPop);

  function askExit() {
    var box = $('exitAsk');
    if (!box.hidden) return;
    // Закрыть вкладку скриптом нельзя (window.close работает только для окон, открытых
    // скриптом), поэтому если до игры в истории ничего не было — уходить некуда.
    var canLeave = history.length > 2;
    $('exitAskText').textContent = canLeave
      ? 'Вы вернётесь на страницу, с которой пришли. Матч и комната при этом будут потеряны.'
      : 'Игра открыта в новой вкладке, поэтому вернуться некуда — закройте вкладку сами.';
    $('exitGo').hidden = !canLeave;
    box.hidden = false;
  }
  $('exitStay').onclick = function () { Audio_.uiClick(); $('exitAsk').hidden = true; };
  $('exitGo').onclick = function () {
    $('exitAsk').hidden = true;
    leaveSite(2); // страж и восстановленная поверх него запись
  };

  function goto(name, opts) {
    var push = !(opts && opts.back);
    if (push && app.screen && app.screen !== name && CAN_RETURN[app.screen]) app.nav.push(app.screen);
    if (!CAN_RETURN[name]) app.nav = [];   // ник, матч и лобби начинают путь заново
    if (app.screen === 'rooms' && name !== 'rooms') unwatchRooms();
    app.screen = name;
    SCREENS.forEach(function (s) { $('screen-' + s).hidden = (s !== name); });
    if (name === 'createroom') buildCreateScreen();
    if (name === 'rooms') openRooms();
    if (name === 'tutlist') buildTutList();
    if (name === 'menu') {
      $('menuNick').textContent = app.nick; renderOnline(); renderTutorialBadge(); renderAdminBtn();
    }
    if (name === 'settings') renderSettings();
    if (name !== 'game' && app.game) stopGame();
    $('chatPanel').hidden = !chatVisible();
    if (name !== 'game') {
      // Чат виден на всех экранах вне боя, а сообщения приходят и пока смотришь список комнат:
      // при переходе лог надо дорисовать, иначе он остаётся на том, что было видно раньше.
      if (app.chat.open) { app.chat.unread = 0; chatBadge(); chatRender(); }
      // Индикатор связи мог переехать в игровую панель — возвращаем в шапку.
      var conn = $('connState');
      if (conn.parentNode !== $('topLeftBar')) $('topLeftBar').insertBefore(conn, $('fsTopBtn'));
    }
    document.documentElement.classList.toggle('ingame', name === 'game');
    document.documentElement.classList.toggle('tut', name === 'game' && !!(app.game && app.game.tutorial));
    syncFsTop();
    route.write(name, opts);
  }
  // Экраны, на которые имеет смысл возвращаться кнопкой «Назад».
  var CAN_RETURN = { menu: 1, createroom: 1, rooms: 1, settings: 1, tutlist: 1 };
  function goBack() {
    Audio_.uiClick();
    // История браузера — только транспорт: куда возвращаться, решает app.nav, а переход
    // выполняет обработчик popstate. Иначе два стека разъезжаются.
    if (history.state && history.state.sb) { history.back(); return; }
    var to = app.nav.pop() || 'menu';
    goto(to, { back: true });
  }
  // Счётчик онлайна: число приходит по игровому сокету — в welcome и потом при каждом изменении.
  // Раньше оно бралось отдельным запросом /api/online, и тот успевал ответить раньше, чем сервер
  // регистрировал самого игрока: одинокий игрок видел «0». Пока связи нет, показываем прочерк.
  var onlineCount = null;
  function renderOnline() {
    var known = app.connected && onlineCount != null;
    $('onlineInfo').textContent = 'Игроков онлайн: ' + (known ? onlineCount : '—');
  }
  function setOnline(n) { onlineCount = n; renderOnline(); }

  // Задержка до сервера рядом с точкой соединения — на всех экранах, а не только в бою.
  // Пинг раз в 2 с, RTT сглаживается по половине; при обрыве число убираем.
  function renderConn() {
    var el = $('connPing');
    if (!el) return;
    var known = app.connected && app.ping.rtt > 0;
    el.textContent = known ? Math.round(app.ping.rtt) + ' мс' : '';
    // Красный — только про лаг: рваные снапшоты (джиттер) портят бой не меньше самой задержки.
    el.className = known && (app.ping.rtt > 200 || app.ping.jitter > 100) ? 'bad' : '';
  }
  function onPong() {
    if (!app.ping.at) return;
    var sample = performance.now() - app.ping.at;
    app.ping.at = 0;
    app.ping.rtt = app.ping.rtt ? app.ping.rtt * 0.5 + sample * 0.5 : sample;
    renderConn();
  }
  setInterval(function () {
    // app.me появляется из welcome: до него сокет открыт, но сессии нет, и сервер на любое
    // сообщение кроме hello отвечает not_allowed — тост перебивал бы ошибку про ник.
    if (!app.connected || !app.net || !app.me) return;
    app.ping.at = performance.now();
    app.net.send('ping');
  }, 2000);
  var toastTimer = null;
  function toast(msg) {
    var t = $('toast'); t.textContent = msg; t.hidden = false;
    clearTimeout(toastTimer); toastTimer = setTimeout(function () { t.hidden = true; }, 3500);
  }
  var ERR_TEXT = {
    bad_nick: 'Ник: 2–16 символов, буквы, цифры, пробел, дефис.',
    nick_profanity: 'В нике нельзя использовать мат — придумайте другой',
    nick_taken: 'Этот ник уже занят. Возможно, вами с другого адреса — придумайте другой.',
    room_not_found: 'Комната с таким кодом не найдена.',
    room_full: 'Комната заполнена.',
    room_limit: 'С вашего адреса уже создано слишком много комнат.',
    bad_code: 'Код — четыре цифры.',
    too_many_tries: 'Слишком много попыток кода. Подождите минуту.',
    slot_taken: 'Это место уже занял другой игрок.',
    no_slots: 'В матче нет свободных мест.',
    busy: 'Сначала выйдите из текущей комнаты или матча.',
    draining: 'Сервер скоро перезапустится: новые матчи временно не начинаются.',
    server_full: 'Сервер переполнен, попробуйте позже.',
    bad_version: 'Версия игры устарела, обновите страницу.',
    not_allowed: 'Действие недоступно.',
    chat_flood: 'Не так быстро — подождите пару секунд.',
    wrong_section: 'Этот код — от комнаты другого раздела.',
    banned: 'Доступ к игре с этого адреса закрыт.',
    kicked: 'Вас выгнали из комнаты.'
  };
  function fmtTime(ms) { var s = Math.ceil(ms / 1000); return Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2); }

  // Кнопка в углу — быстрый мьют: гасит в ноль и возвращает прежний уровень, как в системе.
  $('soundToggle').onclick = function () {
    Audio_.toggleMute();
    Settings.set('volume', Audio_.getVolume());
    renderVolume();
    if (Audio_.getVolume() > 0) Audio_.uiClick(); // подтверждение на слух
  };
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
        if (state !== 'open') { app.ping.rtt = 0; app.ping.jitter = 0; }
        renderOnline(); renderConn();
        var el = $('connState');
        el.className = state === 'open' ? 'on' : (state === 'closed' ? 'off' : '');
        el.title = state === 'open' ? 'Соединение установлено' : 'Нет соединения с сервером';
        if (app.game && !app.game.offline) $('reconnectOverlay').hidden = (state === 'open');
      },
      onMessage: onMessage
    });
  }
  function send(type, data) { if (!app.net || !app.net.send(type, data)) toast('Нет соединения с сервером.'); }

  // Тренировка с ботами идёт в браузере, сервер о ней не знает. Сообщаем, чтобы админка
  // показывала, чем занят игрок. Молча: без связи тренировка всё равно играется.
  function sendTraining(on) {
    if (!app.net) return;
    app.net.send('training', on
      ? { on: true, mode: app.offline.mode, arena: app.offline.arena, role: app.offline.role }
      : { on: false });
  }

  function onMessage(type, d) {
    switch (type) {
      case 'welcome':
        app.token = d.token; store.set('sb.token', d.token);
        app.me = d.playerId; app.nick = d.nick; store.set('sb.nick', d.nick);
        app.rank = d.rank || '';
        renderAdminBtn();
        $('verSim').textContent = d.sim; $('menuNick').textContent = d.nick;
        setOnline(d.online);
        setDrain(!!d.draining);
        if (app.game && app.game.offline) sendTraining(true);
        // Восстановление места после переподключения.
        if (d.resume === 'room') { if (app.screen !== 'lobby') goto('lobby'); }
        else if (d.resume === 'match') { /* придёт match.start */ }
        else if (app.screen === 'lobby' || (app.game && !app.game.offline)) { goto('menu'); }
        else if (app.screen === 'rooms') watchRooms();
        break;
      case 'error':
        toast(ERR_TEXT[d.code] || ('Ошибка: ' + (d.msg || d.code)));
        if (d.code === 'bad_nick' || d.code === 'nick_profanity' || d.code === 'nick_taken') {
          // Всплывашка живёт 3.5 с, а игрок уходит на экран ника — причину надо оставить там.
          goto('nick');
          $('nickMsg').textContent = ERR_TEXT[d.code];
        }
        // Любая ошибка на введённый код — неудача: чистим форму целиком, как договорились.
        if (codeTry) codeFailed(ERR_TEXT[d.code] || ('Ошибка: ' + (d.msg || d.code)));
        else if (app.screen === 'rooms' && ERR_TEXT[d.code]) $('roomsMsg').textContent = ERR_TEXT[d.code];
        break;
      case 'online':
        setOnline(d.n);
        break;
      case 'reload':
        location.reload();
        break;
      case 'drain':
        setDrain(!!d.active);
        break;
      case 'room.list':
        app.rooms.data = d;
        app.rooms.page = d.page;
        if (app.screen === 'rooms') renderRooms();
        break;
      case 'room.match':
        app.roomMatch = d;
        if (app.screen === 'lobby') renderLobby();
        break;
      case 'match.roster':
        if (app.game && !app.game.offline) {
          app.game.players = d.players;
          app.game.names = nameMap(d.players);
          app.game.ranks = rankMap(d.players);
          resetHudCache();
        }
        break;
      case 'room.state':
        clearCode(); // код принят (или мы вошли из списка): форма ожидания больше не нужна
        app.room = d;
        if (!d.inMatch) app.roomMatch = null;
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
        onPong();
        break;
      case 'ping':
        // Зонд сервера: он мерит задержку сам, чтобы показать её в лобби и в админке.
        app.net.send('pong', { seq: (d && d.seq) || 0 });
        break;
      case 'room.ping':
        onRoomPing(d);
        break;
      case 'match.end':
        if (app.game && !app.game.offline) app.game.onEnd(d);
        break;
      case 'chat.history':
        chatSetHistory(d.messages || []);
        break;
      case 'chat.del':
        chatRemove(d.id);
        break;
      case 'chat.clear':
        app.chat.msgs = []; app.chat.unread = 0; chatBadge();
        if (app.chat.open && chatVisible()) chatRender();
        break;
      case 'rank':
        app.rank = d.rank || '';
        renderAdminBtn();
        if (app.chat.open && chatVisible()) chatRender();
        break;
      case 'chat.msg':
        chatAdd(d);
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

  // Раздел ведёт сразу в список комнат: промежуточный экран был лишним шагом, и игрок
  // не видел, куда вообще можно зайти.
  function openSection(name) { app.section = name; store.set('sb.section', name); goto('rooms'); }
  $('btnPvp').onclick = function () { Audio_.uiClick(); openSection('pvp'); };
  $('btnPve').onclick = function () { Audio_.uiClick(); openSection('pve'); };
  $('btnTutorial').onclick = function () { Audio_.uiClick(); goto('tutlist'); };
  $('btnSettings').onclick = function () { Audio_.uiClick(); goto('settings'); };

  // ------------------------------------------------------------
  // Настройки (localStorage, см. settings.js)
  // ------------------------------------------------------------
  function renderSettings() {
    $('setHaptics').checked = !!Settings.get('haptics');
    $('setTouch').value = Settings.get('touch');
    renderVolume();
  }
  // Громкость живёт в одном месте: значение в настройках, а звук, эмодзи кнопки и оба ползунка
  // (в настройках и в углу экрана) — производные.
  function renderVolume() {
    var v = Audio_.getVolume(), pct = String(Math.round(v * 100));
    $('setVolume').value = pct;
    $('setVolumeVal').textContent = pct + '%';
    $('volMini').value = pct;
    $('volMini').title = 'Громкость ' + pct + '%';
    $('soundToggle').textContent = v > 0 ? '🔊' : '🔇';
  }
  function applyVolume(v) {
    Audio_.setVolume(v);
    Settings.set('volume', Audio_.getVolume());
    renderVolume();
  }
  $('setVolume').addEventListener('input', function () { applyVolume(Number($('setVolume').value) / 100); });
  $('volMini').addEventListener('input', function () { applyVolume(Number($('volMini').value) / 100); });
  $('setHaptics').onchange = function () { Settings.set('haptics', $('setHaptics').checked); };
  $('setTouch').onchange = function () { Settings.set('touch', $('setTouch').value); Device.apply(); };
  $('backFromSettings').onclick = goBack;

  // ------------------------------------------------------------
  // Общие детали интерфейса
  // ------------------------------------------------------------
  // Компактная карточка бойца: цвет, имя и кнопка «i». Описание показывается в общем блоке
  // infoEl под сеткой, чтобы шесть бойцов помещались на экране телефона без прокрутки.
  function heroCard(role, selected, onClick, infoEl) {
    var stats = Sim.ROLE_STATS[role];
    var card = document.createElement('div');
    card.className = 'heroCard' + (selected ? ' selected' : '');
    card.innerHTML = '<div class="heroSwatch" style="background:' + stats.color + '"></div>' +
      '<div class="heroName">' + role + '</div><button type="button" class="heroInfoBtn" title="Описание">i</button>';
    card.onclick = onClick;
    card.querySelector('.heroInfoBtn').onclick = function (e) {
      e.stopPropagation(); Audio_.uiClick();
      toggleInfo(infoEl, role, '<b>' + role + '.</b> ' + Sim.HERO_DESCRIPTIONS[role]);
    };
    return card;
  }
  // Один блок описания на экран: повторный клик по той же «i» его сворачивает.
  function toggleInfo(el, key, html) {
    if (!el) return;
    if (!el.hidden && el.getAttribute('data-key') === key) { el.hidden = true; return; }
    el.setAttribute('data-key', key);
    el.innerHTML = html;
    el.hidden = false;
  }
  function mapCard(i, selected, onClick) {
    var arena = Sim.ARENAS[i];
    var card = document.createElement('div');
    card.className = 'mapCard' + (selected ? ' selected' : '');
    card.innerHTML = '<div class="heroName">' + arena.name + '</div><div class="heroDesc">' + arena.obstacles.length + ' укрытий на арене</div>';
    card.onclick = onClick;
    return card;
  }
  function segRow(el, items, current, onPick) {
    if (!el) return;
    el.innerHTML = '';
    items.forEach(function (it) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'segBtn' + (it.value === current ? ' selected' : '');
      b.textContent = it.label;
      b.onclick = function () { Audio_.uiClick(); onPick(it.value); };
      el.appendChild(b);
    });
  }
  var GAME_MODE_NAMES = { pvp: 'Дуэли (PvP)', survival: 'Волны', defense: 'Защита' };
  var GAME_MODE_DESCRIPTIONS = {
    pvp: 'Две команды бросают снежки. Побеждает команда, которая вывела из строя всех соперников; если время вышло — ничья.',
    survival: 'Волны врагов идут на вашу команду. У команды общие жизни, между волнами есть передышка, каждый уровень заканчивается боссом.',
    defense: 'То же, что «Волны», но на арене стоит снеговик, и врагам нужен он. Разобьют снеговика — забег закончен, даже если команда жива.'
  };
  // «Кампания» и «Эндлесс» ничего не говорили игроку: теперь формат называется словами,
  // а детали — по кнопке «i».
  var FORMAT_NAMES = { campaign: 'Прохождение (3 уровня и босс)', endless: 'Бесконечные волны' };
  var FORMAT_DESCRIPTIONS = {
    campaign: 'Три уровня по четыре волны, в конце каждого — босс. Уровни идут на разных аренах; зачистите все три — кампания пройдена.',
    endless: 'Волны не кончаются и становятся всё сложнее. Играете, пока команда держится; в итогах записывается, сколько волн выстояли.'
  };
  var PVE_GAME_MODES = ['survival', 'defense'];
  var VIS_NAMES = { open: 'Открытая — видна всем', closed: 'Закрытая — только по коду' };
  function isPve(gm) { return gm && gm !== 'pvp'; }
  function botLevelNames() { return Sim.BOT_LEVEL_NAMES || ['Лёгкий', 'Обычный', 'Сложный']; }
  function pveResultText(r) {
    return { cleared: 'Прошлый забег: прохождение завершено 🏆', wiped: 'Прошлый забег: команда повержена',
      objective: 'Прошлый забег: снеговик разбит', expired: 'Прошлый забег: время вышло' }[r] || '';
  }
  function nameMap(players) {
    var out = {};
    (players || []).forEach(function (p) { out[p.id] = p.nick; });
    return out;
  }
  // Роли модерации бойцов: ник в бою рисуется цветом роли (см. render.js).
  function rankMap(players) {
    var out = {};
    (players || []).forEach(function (p) { if (p.rank) out[p.id] = p.rank; });
    return out;
  }
  // Размер PvE-команды: «1 игрок / 2 игрока / 4 игрока» — сокращение «игр.» читалось как ошибка.
  function pveSizeText(n) { return n + ' ' + plural(n, 'игрок', 'игрока', 'игроков'); }
  function escapeHtml(s) { return String(s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
  function plural(n, one, few, many) {
    var m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20)) return few;
    return many;
  }
  function ageText(ms) {
    var min = Math.floor(ms / 60000);
    if (min < 1) return 'только что';
    if (min < 60) return min + ' ' + plural(min, 'минуту', 'минуты', 'минут') + ' назад';
    var h = Math.floor(min / 60);
    return h + ' ' + plural(h, 'час', 'часа', 'часов') + ' назад';
  }

  // ------------------------------------------------------------
  // Создание комнаты
  // ------------------------------------------------------------
  function buildCreateScreen() {
    var pve = app.section === 'pve', c = app.create;
    $('createHead').textContent = pve ? 'Новая PvE-комната' : 'Новая PvP-комната';
    $('createModeLabel').textContent = pve ? 'Размер команды' : 'Размер команд';
    var mg = $('createModeGrid'); mg.innerHTML = '';
    Sim.MODES.forEach(function (n) {
      var card = document.createElement('div');
      card.className = 'modeCard' + (c.mode === n ? ' selected' : '');
      card.textContent = pve ? pveSizeText(n) : n + '×' + n;
      card.onclick = function () { Audio_.uiClick(); c.mode = n; buildCreateScreen(); };
      mg.appendChild(card);
    });
    // В PvE арену задаёт таблица уровней, выбирать нечего.
    $('createArenaLabel').hidden = pve;
    var grid = $('createMapGrid'); grid.hidden = pve; grid.innerHTML = '';
    if (!pve) {
      Sim.ARENAS.forEach(function (_, i) {
        grid.appendChild(mapCard(i, c.arena === i, function () { Audio_.uiClick(); c.arena = i; buildCreateScreen(); }));
      });
    }
    segRow($('createVisSel'), ['open', 'closed'].map(function (v) { return { value: v, label: VIS_NAMES[v] }; }),
      c.visibility, function (v) { c.visibility = v; buildCreateScreen(); });
    segRow($('createBotSel'), botLevelNames().map(function (name, lvl) { return { value: lvl, label: name }; }),
      c.botLevel, function (v) { c.botLevel = v; buildCreateScreen(); });
  }
  $('backFromCreate').onclick = goBack;
  $('createRoomBtn').onclick = function () {
    Audio_.uiClick();
    var c = app.create, pve = app.section === 'pve';
    send('room.create', {
      mode: c.mode, arena: pve ? 0 : c.arena, visibility: c.visibility, difficulty: c.botLevel,
      // Волны или защита выбираются уже в комнате; при создании берём волны.
      gameMode: pve ? 'survival' : 'pvp', campaign: true
    });
  };

  // ------------------------------------------------------------
  // Список комнат: страницу присылает сервер и обновляет её сам, пока экран открыт
  // ------------------------------------------------------------
  function watchRooms() { send('room.list', { section: app.section, page: app.rooms.page }); }
  function unwatchRooms() {
    app.rooms.data = null; app.rooms.slots = null;
    if (app.net) app.net.send('room.unlist', {});
  }
  function openRooms() {
    app.rooms.page = 0;
    $('roomsMsg').textContent = '';
    clearCode();
    $('roomsHead').textContent = app.section === 'pve' ? 'PvE-комнаты' : 'PvP-комнаты';
    renderRooms();
    watchRooms();
  }
  $('backFromRooms').onclick = goBack;
  $('btnCreateRoomTop').onclick = function () { Audio_.uiClick(); goto('createroom'); };

  // Код комнаты — четыре клетки: цифра переводит фокус вперёд, Backspace назад, вставка
  // раскладывает код по клеткам целиком.
  var codeCells = Array.prototype.slice.call(document.querySelectorAll('#codeCells .codeCell'));
  function codeValue() { return codeCells.map(function (c) { return c.value; }).join(''); }
  var codeTry = false, codeTimer = null; // отправлен код, ждём ответ сервера
  function clearCode() {
    codeCells.forEach(function (c) { c.value = ''; c.classList.remove('filled'); });
    codeTry = false;
    clearTimeout(codeTimer);
  }
  function syncCode() {
    codeCells.forEach(function (c) { c.classList.toggle('filled', !!c.value); });
  }
  /**
   * Отправить код. Кнопки подтверждения нет: как только набраны четыре цифры, пробуем войти.
   * Раздел передаём серверу — по коду из другого раздела он не пустит.
   */
  function submitCode() {
    if (codeTry || codeValue().length !== 4) return;
    codeTry = true;
    codeCells[3].blur(); // на телефоне убирает клавиатуру, иначе она прячет сообщение
    $('roomsMsg').textContent = 'Проверяем код…';
    send('room.join', { code: codeValue(), section: app.section });
    // Страховка: если ответа нет (связь пропала), форма не должна залипнуть навсегда.
    clearTimeout(codeTimer);
    codeTimer = setTimeout(function () {
      if (!codeTry) return;
      clearCode(); syncCode();
      $('roomsMsg').textContent = 'Сервер не ответил. Попробуйте ещё раз.';
    }, 4000);
  }
  /** Неудачный код: форма чистится полностью, курсор в первую клетку. */
  function codeFailed(msg) {
    clearCode(); syncCode();
    $('roomsMsg').textContent = msg;
    codeCells[0].focus();
  }
  function fillCode(digits, from) {
    for (var i = 0; i < digits.length && from + i < codeCells.length; i++) codeCells[from + i].value = digits[i];
    var next = Math.min(from + digits.length, codeCells.length - 1);
    codeCells[next].focus();
    syncCode();
  }
  codeCells.forEach(function (cell, i) {
    cell.addEventListener('input', function () {
      var digits = cell.value.replace(/\D/g, '');
      cell.value = '';
      if (!digits) { syncCode(); return; }
      fillCode(digits, i);
      if (codeValue().length === 4) submitCode();
    });
    cell.addEventListener('keydown', function (e) {
      if (e.key === 'Backspace' && !cell.value && i > 0) { e.preventDefault(); codeCells[i - 1].value = ''; codeCells[i - 1].focus(); syncCode(); }
      else if (e.key === 'ArrowLeft' && i > 0) codeCells[i - 1].focus();
      else if (e.key === 'ArrowRight' && i < codeCells.length - 1) codeCells[i + 1].focus();
      else if (e.key === 'Enter') submitCode();
    });
    cell.addEventListener('paste', function (e) {
      var text = (e.clipboardData || window.clipboardData).getData('text') || '';
      var digits = text.replace(/\D/g, '');
      if (!digits) return;
      // preventDefault отменяет штатную вставку, а вместе с ней и событие input — автовход из
      // обработчика input сюда не доезжает. Без строки ниже код оставался в клетках, но вход
      // приходилось подтверждать вручную через Enter.
      e.preventDefault();
      if (digits.length >= 4) { clearCode(); fillCode(digits.slice(0, 4), 0); }
      else fillCode(digits, i);
      if (codeValue().length === 4) submitCode();
    });
    cell.addEventListener('focus', function () { cell.select(); });
  });

  function renderRooms() {
    var list = $('roomsList'); list.innerHTML = '';
    var d = app.rooms.data;
    if (!d) { $('roomsMsg').textContent = 'Загружаем список…'; $('roomsPager').innerHTML = ''; return; }
    if (!d.rooms || !d.rooms.length) {
      $('roomsMsg').textContent = 'Комнат пока нет — создайте свою, к вам зайдут.';
      $('roomsPager').innerHTML = '';
      return;
    }
    if ($('roomsMsg').textContent === 'Загружаем список…') $('roomsMsg').textContent = '';
    d.rooms.forEach(function (r) { list.appendChild(roomRow(r)); });
    var pager = $('roomsPager');
    if (d.pages > 1) {
      var items = [];
      for (var i = 0; i < d.pages; i++) items.push({ value: i, label: String(i + 1) });
      segRow(pager, items, d.page, function (v) { app.rooms.page = v; watchRooms(); });
    } else pager.innerHTML = '';
  }

  function roomRow(r) {
    var el = document.createElement('div');
    el.className = 'roomRow' + (r.inMatch ? ' live' : '');
    var game = isPve(r.gameMode)
      ? (GAME_MODE_NAMES[r.gameMode] || r.gameMode) + ' · ' + (r.campaign ? 'Прохождение' : 'Бесконечные волны')
      : GAME_MODE_NAMES.pvp;
    var size = isPve(r.gameMode) ? pveSizeText(r.mode) : r.mode + '×' + r.mode;
    var arena = (Sim.ARENAS[r.arena] || {}).name || '';
    var meta = [size];
    if (!isPve(r.gameMode) && arena) meta.push(arena);
    meta.push('Игроков ' + r.humans + '/' + r.capacity);
    if (r.bots > 0) meta.push('Ботов ' + r.bots);
    if (r.hostNick) meta.push('Хост ' + escapeHtml(r.hostNick));
    meta.push('Создана ' + ageText(r.ageMs || 0));
    el.innerHTML =
      '<div class="roomMain">' +
        '<span class="roomCode">' + (r.needCode ? '🔒 ••••' : escapeHtml(r.code)) + '</span>' +
        '<span class="roomGame">' + game + '</span>' +
        '<span class="status ' + (r.inMatch ? 'live">Матч' : 'wait">Ожидание') + '</span>' +
      '</div>' +
      '<div class="roomMeta">' + meta.join(' · ') + '</div>';
    var act = document.createElement('div');
    act.className = 'roomAct';
    var btn = document.createElement('button');
    btn.className = 'menuBtn' + (r.joinable ? ' primary' : ' ghost');
    if (!r.joinable) {
      btn.textContent = 'Мест нет';
      btn.disabled = true;
    } else if (r.needCode) {
      btn.textContent = 'По коду';
      btn.onclick = function () {
        Audio_.uiClick();
        $('roomsMsg').textContent = 'Комната закрытая: спросите код у хоста и введите его в строке сверху.';
        $('roomsTools').scrollIntoView({ block: 'nearest' });
        codeCells[0].focus();
      };
    } else {
      // Войти можно и в комнату с идущим матчем: в лобби будет видно бой и кнопка вступить.
      btn.textContent = r.inMatch ? 'Войти в матч' : 'Войти';
      btn.onclick = function () {
        Audio_.uiClick(); $('roomsMsg').textContent = '';
        send('room.join', { code: r.code, section: app.section });
      };
    }
    act.appendChild(btn);
    el.appendChild(act);
    return el;
  }

  // ------------------------------------------------------------
  // Лобби комнаты
  // ------------------------------------------------------------
  $('copyCode').onclick = function () {
    if (!app.room) return;
    if (navigator.clipboard) navigator.clipboard.writeText(app.room.code).then(function () { toast('Код скопирован: ' + app.room.code); });
  };
  function leaveLobby() {
    send('room.leave'); app.room = null; app.roomMatch = null;
    goto('rooms', { back: true });
  }
  $('leaveLobby').onclick = function () { Audio_.uiClick(); leaveLobby(); };
  $('joinMatchBtn').onclick = function () { Audio_.uiClick(); send('match.join'); };
  $('startRoomBtn').onclick = function () { Audio_.uiClick(); send('room.start'); };
  $('readyBtn').onclick = function () {
    Audio_.uiClick();
    send('room.ready', { ready: !myReady() });
  };
  function myReady() {
    var r = app.room; if (!r) return false;
    for (var i = 0; i < r.players.length; i++) if (r.players[i].id === app.me) return !!r.players[i].ready;
    return false;
  }
  function sendLobbyConfig() {
    var r = app.room; if (!r) return;
    var pve = isPve(r.gameMode);
    send('room.config', {
      mode: +$('lobbyMode').value,
      arena: pve ? r.arena : +$('lobbyArena').value,
      gameMode: pve ? ($('lobbyGameMode').value || 'survival') : 'pvp',
      campaign: $('lobbyCampaign').value === '1',
      difficulty: +$('lobbyDifficulty').value,
      visibility: $('lobbyVisibility').value
    });
  }
  ['lobbyGameMode', 'lobbyMode', 'lobbyArena', 'lobbyCampaign', 'lobbyDifficulty', 'lobbyVisibility'].forEach(function (id) {
    $(id).onchange = sendLobbyConfig;
  });
  $('lobbyGameModeInfoBtn').onclick = function (e) {
    e.preventDefault(); Audio_.uiClick();
    var gm = $('lobbyGameMode').value || 'survival';
    toggleInfo($('lobbyModeInfo'), gm, '<b>' + (GAME_MODE_NAMES[gm] || gm) + '.</b> ' + GAME_MODE_DESCRIPTIONS[gm]);
  };
  $('lobbyCampaignInfoBtn').onclick = function (e) {
    e.preventDefault(); Audio_.uiClick();
    var key = $('lobbyCampaign').value === '1' ? 'campaign' : 'endless';
    toggleInfo($('lobbyModeInfo'), key, '<b>' + FORMAT_NAMES[key] + '.</b> ' + FORMAT_DESCRIPTIONS[key]);
  };

  // Задержка участника лобби: её мерит сервер и присылает отдельным room.ping. Поля может не
  // быть (бот, только что зашёл, нет связи) — тогда не рисуем ничего, а не «0 мс».
  function pingHtml(p) {
    if (!p || !p.connected || !(p.ping > 0)) return '';
    return ' <span class="slotPing' + (p.ping > 200 ? ' bad' : '') + '">' + p.ping + ' мс</span>';
  }
  // room.ping приходит чаще room.state: вливаем задержки в уже нарисованный состав.
  function onRoomPing(d) {
    if (!d || !app.room || app.room.code !== d.code) return;
    var by = {};
    for (var i = 0; i < (d.pings || []).length; i++) by[d.pings[i].id] = d.pings[i].ping;
    var players = app.room.players || [];
    var changed = false;
    for (var j = 0; j < players.length; j++) {
      var next = by[players[j].id] || 0;
      if (players[j].ping !== next) { players[j].ping = next; changed = true; }
    }
    if (changed && app.screen === 'lobby') renderLobby();
  }
  function renderLobby() {
    var r = app.room; if (!r) return;
    var isHost = r.hostId === app.me;
    var gm = r.gameMode || 'pvp', pve = isPve(gm);
    $('lobbyCode').textContent = r.code;
    var gmSel = $('lobbyGameMode'), modeSel = $('lobbyMode'), arenaSel = $('lobbyArena');
    var campSel = $('lobbyCampaign'), difSel = $('lobbyDifficulty'), visSel = $('lobbyVisibility');
    // В PvP-комнате выбора игры нет: она только про дуэли.
    $('lobbyGameModeWrap').hidden = !pve;
    gmSel.innerHTML = PVE_GAME_MODES.map(function (m) {
      return '<option value="' + m + '"' + (m === gm ? ' selected' : '') + '>' + (GAME_MODE_NAMES[m] || m) + '</option>';
    }).join('');
    modeSel.innerHTML = Sim.MODES.map(function (n) { return '<option value="' + n + '"' + (n === r.mode ? ' selected' : '') + '>' + (pve ? pveSizeText(n) : n + '×' + n) + '</option>'; }).join('');
    arenaSel.innerHTML = Sim.ARENAS.map(function (a, i) { return '<option value="' + i + '"' + (i === r.arena ? ' selected' : '') + '>' + a.name + '</option>'; }).join('');
    campSel.innerHTML = '<option value="1"' + (r.campaign ? ' selected' : '') + '>' + FORMAT_NAMES.campaign + '</option>' +
      '<option value="0"' + (!r.campaign ? ' selected' : '') + '>' + FORMAT_NAMES.endless + '</option>';
    difSel.innerHTML = botLevelNames().map(function (name, lvl) {
      return '<option value="' + lvl + '"' + (lvl === (r.difficulty || 0) ? ' selected' : '') + '>' + name + '</option>';
    }).join('');
    visSel.innerHTML = ['open', 'closed'].map(function (v) {
      return '<option value="' + v + '"' + (v === (r.visibility || 'open') ? ' selected' : '') + '>' + VIS_NAMES[v] + '</option>';
    }).join('');
    [gmSel, modeSel, arenaSel, campSel, difSel, visSel].forEach(function (sel) { sel.disabled = !isHost || r.inMatch; });
    $('lobbyArenaWrap').hidden = pve;   // в PvE арену задаёт уровень
    $('lobbyCampaignWrap').hidden = !pve;
    $('lobbyConfigHint').textContent = r.inMatch
      ? 'Идёт матч: настройки комнаты меняются после боя.'
      : (isHost
        ? 'Вы хост: настройки комнаты ваши, пустые слоты займут боты. Можно начать матч, не дожидаясь готовности.'
        : 'Настройки комнаты меняет хост.');
    var res = $('lobbyResult');
    if (r.lastWinner) {
      res.hidden = false;
      res.textContent = pveResultText(r.lastWinner) ||
        (r.lastWinner === 'draw' ? 'Прошлый матч: ничья' : 'Прошлый матч выиграла команда ' + r.lastWinner);
    } else res.hidden = true;

    $('slotsBCol').hidden = pve;
    $('slotsALabel').textContent = pve ? 'Команда' : 'Команда A';
    var teams = pve ? ['A'] : ['A', 'B'];
    var byTeam = { A: {}, B: {} };
    r.players.forEach(function (p) { if (p.team) byTeam[p.team][p.index] = p; });
    // Во время матча слот комнаты — это боец матча: сводка приходит отдельным room.match.
    var mSlots = {};
    if (r.inMatch && app.roomMatch && app.roomMatch.code === r.code) {
      (app.roomMatch.slots || []).forEach(function (sl) { mSlots[sl.team + sl.index] = sl; });
    }
    var meSlot = null;
    r.players.forEach(function (p) { if (p.id === app.me) meSlot = p; });
    teams.forEach(function (team) {
      var col = $('slots' + team); col.innerHTML = '';
      for (var i = 0; i < r.mode; i++) {
        var p = byTeam[team][i], sl = mSlots[team + i];
        var el = document.createElement('div');
        var hp = sl ? ' <span class="slotHp">' + (sl.koed ? 'выбит' : sl.hp + ' HP') + '</span>' : '';
        if (p) {
          el.className = 'slot taken' + (p.id === app.me ? ' me' : '') + (!r.inMatch && p.ready ? ' ready' : '');
          var state = '';
          if (r.inMatch) state = ' <span class="slotState">' + (p.inMatch ? 'в бою' : 'в лобби, за него играет бот') + '</span>';
          else if (p.ready) state = ' <span class="readyMark">✓ готов</span>';
          // В матче роль показывает боец слота: у участника в комнате она могла быть не выбрана.
          var role = (r.inMatch && sl ? sl.role : p.role) || 'боец случайный';
          var nickCls = p.rank ? ' class="rank-' + p.rank + '"' : '';
          el.innerHTML = '<span><span' + nickCls + '>' + escapeHtml(p.nick) + '</span>' + pingHtml(p) + (p.host ? '<span class="host">★ хост</span>' : '') + (p.connected ? '' : ' <span class="off">(нет связи)</span>') + '</span>' +
            '<span class="role">' + role + hp + state + '</span>' +
            (isHost && p.id !== app.me && !r.inMatch ? '<button class="kick" data-id="' + p.id + '">выгнать</button>' : '');
        } else if (r.inMatch) {
          el.className = 'slot';
          el.innerHTML = '<span>' + escapeHtml(sl ? sl.nick : 'Бот') + '</span>' +
            '<span class="role">' + (sl ? sl.role : '') + hp + '</span>' +
            '<button class="takeSlot" data-team="' + team + '" data-index="' + i + '">Занять место</button>';
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
    Array.prototype.forEach.call(document.querySelectorAll('.slot .takeSlot'), function (b) {
      b.onclick = function (e) {
        e.stopPropagation(); Audio_.uiClick();
        send('room.slot', { team: b.getAttribute('data-team'), index: +b.getAttribute('data-index') });
      };
    });
    var me = r.players.filter(function (p) { return p.id === app.me; })[0];
    var grid = $('lobbyHeroGrid'); grid.innerHTML = '';
    Sim.ALL_ROLES.forEach(function (role) {
      grid.appendChild(heroCard(role, me && me.role === role, function () { Audio_.uiClick(); send('room.role', { role: role }); }, $('lobbyHeroInfo')));
    });
    var ready = myReady(), humans = r.players.filter(function (p) { return p.connected; }).length;
    var rb = $('readyBtn');
    rb.hidden = r.inMatch;
    rb.textContent = ready ? 'Не готов' : 'Готов';
    rb.className = 'menuBtn' + (ready ? ' ghost' : ' primary');
    // В матче вместо «Готов» — вход в бой за своего бойца.
    var jb = $('joinMatchBtn');
    jb.hidden = !r.inMatch || !meSlot || !!(meSlot && meSlot.inMatch);
    $('startRoomBtn').hidden = !isHost || r.inMatch;
    $('startRoomBtn').disabled = app.draining;
    $('lobbyReadyLine').textContent = r.inMatch ? ''
      : 'Готовы ' + (r.readyCount || 0) + ' из ' + humans + ' — когда готовы все, матч начнётся сам.';
    var info = $('lobbyMatchInfo');
    if (r.inMatch) {
      var left = app.roomMatch && app.roomMatch.code === r.code ? app.roomMatch.timeLeftMs : 0;
      info.hidden = false;
      info.innerHTML = '<span>Идёт матч</span>' + (left > 0 ? '<span>осталось <b>' + fmtTime(left) + '</b></span>' : '') +
        (meSlot ? '<span>ваше место: <b>' + (pve ? 'команда' : 'команда ' + meSlot.team) + ', ' + (meSlot.index + 1) + '</b></span>' : '');
    } else info.hidden = true;
    $('lobbyWait').textContent = r.inMatch
      ? 'Можно занять свободное место и войти в бой — боец достанется со своим HP.'
      : (isHost ? '' : 'Хост может начать матч в любой момент.');
  }

  // ------------------------------------------------------------
  // Матч: общая часть (ввод, HUD, рендер)
  // ------------------------------------------------------------
  var canvas = $('c'), overlay = $('overlay'), overlayText = $('overlayText'), overlaySub = $('overlaySub');
  var countdownEl = $('countdown');
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
  // Ник бойца: в снапшоте у слота, на который подсел живой игрок, остаётся ник бота —
  // переименовывать бойцов симуляция не умеет, поэтому имена берём из состава матча.
  function nickOf(p) {
    var names = app.game && app.game.names;
    return (names && names[p.id]) || p.nick;
  }
  function radiusOf(p) { return (Sim.ROLE_STATS[p.role] || { radius: 15 }).radius; }
  function overMe(pt, me) { return !!me && Math.hypot(pt.x - me.x, pt.y - me.y) <= radiusOf(me) + 10; }

  // Слой намерений: мышь и стики дают команды сюда, он шлёт протокол и держит локальный замах
  // (intent.local) для мгновенного отклика в рендере — сервер подтвердит через снапшот.
  var chargeAudioStop = null;
  var intent = window.SBIntent.create({
    getGame: function () { return app.game; },
    getMe: myPlayer,
    canAct: canAct,
    blocked: function () { return !!(app.game && app.game.countdown > 0); }, // идёт отсчёт перед стартом
    onChargeStart: function () { chargeAudioStop = Audio_.chargeLoopStart(function () { return intent.local.power; }); },
    onChargeEnd: function () { if (chargeAudioStop) { chargeAudioStop(); chargeAudioStop = null; } },
    // Обучение считает шаги по состоявшимся действиям: события applyInput до клиента не доходят.
    onThrow: function (power) { if (tutorial) tutorial.onThrow(power); },
    onSpecial: function () { if (tutorial) tutorial.onSpecial(); },
    // Держит ли игрок кнопку замаха прямо сейчас. Нужно слою намерений: клик во время
    // перезарядки откладывает замах, и начинать его по её окончании можно только если
    // кнопка всё ещё нажата (иначе замах начинался сам и копился до конца).
    holding: function () {
      if (Device.isTouch()) return !!(touch && touch.isCharging && touch.isCharging());
      return mouseDown;
    }
  });
  var local = intent.local;

  // ---- источник намерений: мышь и клавиатура (ПК) ----
  // На ПК управление одно и то же и настройке больше не подлежит: WASD/стрелки — движение,
  // зажать ЛКМ в любой точке — замах в сторону курсора, отпустить — бросок, отпустить над своим
  // бойцом — отмена; Q или ПКМ — способность. Клик-ту-мув убран вместе с настройкой.
  var mouseDown = false;                 // зажата ли ЛКМ (только ПК), см. holding() выше
  function releaseMouse() { mouseDown = false; }
  canvas.addEventListener('mousedown', function (e) {
    if (Device.isTouch() || app.screen !== 'game' || !app.game || app.game.over) return;
    var pt = toLocal(e), me = myPlayer(app.game.lastSnap);
    lastMouse = pt;
    if (!canAct(me)) return;
    if (e.button === 2) { intent.specialAt(pt.x, pt.y); return; }
    if (e.button !== 0) return;
    mouseDown = true;
    intent.chargeStartAt(pt.x, pt.y);
  });
  // Клавиатура: набор зажатых клавиш → нормированное направление в intent.setMoveDir (как стик).
  // По e.code, чтобы русская раскладка работала; повтор клавиши игнорируем.
  var KEYDIR = { KeyW: [0, -1], KeyS: [0, 1], KeyA: [-1, 0], KeyD: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1], ArrowLeft: [-1, 0], ArrowRight: [1, 0] };
  var keys = {};
  function applyKeys() {
    if (!app.game || Device.isTouch()) { intent.setMoveDir(null); return; }
    var x = 0, y = 0;
    for (var k in keys) if (keys[k]) { x += KEYDIR[k][0]; y += KEYDIR[k][1]; }
    intent.setMoveDir(x || y ? { x: x, y: y } : null);
  }
  window.addEventListener('keyup', function (e) { if (keys[e.code]) { keys[e.code] = false; applyKeys(); } });
  window.addEventListener('blur', function () { keys = {}; releaseMouse(); applyKeys(); });
  canvas.addEventListener('mousemove', function (e) {
    var pt = toLocal(e); lastMouse = pt;
    if (Device.isTouch() || app.screen !== 'game' || !app.game) return;
    // Кнопку отпустили вне окна: mouseup до нас не дошёл, лечим по состоянию кнопок мыши.
    if (mouseDown && e.buttons === 0) { releaseMouse(); intent.cancelCharge(); return; }
    if (local.charging) intent.aimAt(pt.x, pt.y);
  });
  window.addEventListener('mouseup', function (e) {
    var was = mouseDown;
    releaseMouse();
    if (!app.game || Device.isTouch() || !was) return;
    // Условия на local.charging здесь нет: если замах ещё отложен перезарядкой, throwAt и
    // cancelCharge просто снимут отложенный — иначе он начинался бы при отпущенной кнопке.
    var pt = toLocal(e), me = myPlayer(app.game.lastSnap);
    if (overMe(pt, me)) intent.cancelCharge();
    else intent.throwAt(local.aimX, local.aimY);
  });
  window.addEventListener('keydown', function (e) {
    if (app.screen !== 'game' || !app.game) return;
    if (KEYDIR[e.code]) {
      e.preventDefault();
      if (!e.repeat && !keys[e.code]) { keys[e.code] = true; applyKeys(); }
      return;
    }
    if (e.code === 'KeyE' || ['e', 'E', 'у', 'У'].indexOf(e.key) >= 0) { releaseMouse(); intent.cancelCharge(); return; }
    if (['q', 'Q', 'й', 'Й'].indexOf(e.key) >= 0) intent.specialAt(lastMouse.x, lastMouse.y);
  });
  abilityBtn.onclick = function () { if (app.game) intent.specialAt(lastMouse.x, lastMouse.y); };
  canvas.addEventListener('contextmenu', function (e) { e.preventDefault(); });

  // ---- источник намерений: стики (сенсорный экран) ----
  var touch = window.SBTouch.create({
    layer: touchLayer, zoneL: $('zoneL'), zoneR: $('zoneR'), stickL: $('stickL'), stickR: $('stickR'),
    ability: touchAbility, stickS: $('stickS'), intent: intent,
    getMe: function () { return app.game ? myPlayer(app.game.lastSnap) : null; },
    hasDirSpecial: function (role) { var sp = Sim.SPECIALS[role]; return !!sp && !!sp.needsDir; }
  });
  var zonesHintTimer = null, abilityHintTimer = null;

  // Полный экран (Android; на iPhone Safari недоступен — там режим «на экран Домой»).
  // Две кнопки: в игровой панели и в шапке страницы — на телефоне полный экран нужен везде,
  // а не только в бою, но одна плавающая кнопка села бы поверх HUD и стиков.
  function toggleFullscreen() {
    if (document.fullscreenElement) { document.exitFullscreen(); return; }
    var el = document.documentElement;
    try { el.requestFullscreen({ navigationUI: 'hide' }).catch(function () { /* отказ — не страшно */ }); } catch (e) { /* игнор */ }
  }
  $('fsBtn').onclick = toggleFullscreen;
  $('fsTopBtn').onclick = function () { Audio_.uiClick(); toggleFullscreen(); };
  function syncFsTop() {
    $('fsTopBtn').hidden = !(Device.isTouch() && Device.fullscreenAvailable() && app.screen !== 'game');
  }

  // Возврат из фона: сервер через 20 с без ввода отдаёт бойца боту; любой ввод возвращает управление.
  document.addEventListener('visibilitychange', function () {
    // Пока вкладка скрыта, браузер тормозит таймеры: измеренный пинг устаревает, а состояние
    // кнопки мыши могло измениться без нас.
    releaseMouse();
    app.ping.rtt = 0; app.ping.jitter = 0; renderConn();
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
  var hudCache = { sig: '', a: '', b: '', timer: '', abil: '', rl: '', pve: '' };
  function resetHudCache() { hudCache.sig = hudCache.a = hudCache.b = hudCache.timer = hudCache.abil = hudCache.rl = hudCache.pve = ''; }
  function updateHUD(snap) {
    var me = myPlayer(snap);
    // Волновой HUD — только если клиент точно в PvE-матче. Иначе хвост snap.pve от прошлого
    // матча (переиспользуемый объект интерполяции) включал «Ур./Волна» прямо в PvP.
    var pve = (app.game && app.game.gameMode && app.game.gameMode !== 'pvp') ? (snap.pve || null) : null;
    function row(p, right) {
      var pips = '';
      for (var i = 0; i < 3; i++) pips += '<span class="pip ' + (i < p.hp ? 'on ' + p.team.toLowerCase() : '') + '"></span>';
      var cls = 'charname' + (p.id === app.game.meId ? ' me' : '') + (p.bot ? ' bot' : '');
      var lives = (pve && p.team === 'A' && p.lives != null) ? ' <span class="lives">♥' + p.lives + '</span>' : '';
      var name = escapeHtml(nickOf(p)) + ' · ' + p.role + (p.id === app.game.meId ? ' (вы)' : '') + lives;
      return right ? '<div class="charrow right"><span class="pips">' + pips + '</span><span class="' + cls + '" style="text-align:right">' + name + '</span></div>'
        : '<div class="charrow"><span class="' + cls + '">' + name + '</span><span class="pips">' + pips + '</span></div>';
    }
    // На телефоне HUD-строки скрыты: в ландшафте они абсолютно позиционированы поверх арены
    // и закрывали игровое поле. HP там рисуется на канвасе под ником бойца. Скрываем из JS,
    // а не правилом CSS: в PvE в #hudB живёт панель волны и полоса босса, её убирать нельзя.
    var hidePips = Device.isTouch();
    $('hudA').hidden = hidePips;
    if (!pve) $('hudB').hidden = hidePips;
    if (!hidePips) {
      var sig = '';
      for (var i = 0; i < snap.players.length; i++) { var q = snap.players[i]; sig += q.id + ':' + q.hp + (q.koed ? 'k' : '') + (q.lives != null ? 'l' + q.lives : '') + ';'; }
      if (sig !== hudCache.sig) {
        hudCache.sig = sig;
        var a = snap.players.filter(function (p) { return p.team === 'A'; }).map(function (p) { return row(p, false); }).join('');
        if (a !== hudCache.a) { hudCache.a = a; $('teamA').innerHTML = a; }
        if (!pve) {
          var b = snap.players.filter(function (p) { return p.team === 'B'; }).map(function (p) { return row(p, true); }).join('');
          if (b !== hudCache.b) { hudCache.b = b; $('teamB').innerHTML = b; }
        }
      }
    }
    if (pve) updatePveHud(snap, pve);
    var tm = fmtTime(snap.timeLeft);
    if (tm !== hudCache.timer) { hudCache.timer = tm; $('matchTimer').textContent = tm; }

    // Перезарядка выстрела (обновляется каждый кадр, вне кэша способности).
    var rlMs = me ? (Sim.RELOAD_MS[me.role] || 900) : 0;
    var rlSec = me && me.rl > 0.02 ? (me.rl * rlMs / 1000).toFixed(1) : '';
    if (rlSec !== hudCache.rl) {
      hudCache.rl = rlSec;
      var rh = $('reloadHud');
      if (rh) { rh.hidden = !rlSec; if (rlSec) rh.textContent = 'перезарядка ' + rlSec + ' с'; }
    }
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
  function updatePveHud(snap, pve) {
    var boss = null;
    for (var i = 0; i < snap.players.length; i++) {
      var p = snap.players[i];
      if (p.et === 'boss' && !p.koed) { boss = p; break; }
    }
    var info;
    if (pve.endless) {
      info = 'Эндлесс · волна ' + ((pve.wave || 0) + 1);
    } else {
      info = 'Ур. ' + ((pve.level || 0) + 1) + ' · Волна ' + Math.min((pve.wave || 0) + 1, pve.waveCount) + '/' + pve.waveCount;
    }
    if (pve.phase === 'between' && pve.nextInMs > 0) info += ' · след. через ' + Math.ceil(pve.nextInMs / 1000) + ' с';
    if (pve.objHp != null) info += '  🛡 ' + pve.objHp + '/' + pve.objMaxHp;

    var panel = pve.phase === 'between'
      ? '<div class="wavePanel">Готовьтесь…</div>'
      : '<div class="wavePanel">Осталось врагов: <b>' + pve.enemiesLeft + '</b></div>';
    if (boss) {
      var frac = boss.mhp ? Math.max(0, Math.round(boss.hp / boss.mhp * 12)) : 0;
      var bar = ''; for (var k = 0; k < 12; k++) bar += k < frac ? '█' : '░';
      panel += '<div class="bossBar">' + escapeHtml(boss.nick) + '<br>' + bar + ' ' + boss.hp + '/' + boss.mhp +
        (boss.bph === 2 ? ' <span class="phase2">ЯРОСТЬ</span>' : '') + '</div>';
    }
    var pveSig = info + '|' + panel;
    if (pveSig === hudCache.pve) return;
    hudCache.pve = pveSig;
    var pi = $('pveInfo'); if (pi) { pi.hidden = false; pi.textContent = info; }
    $('teamBLabel').textContent = 'Волна';
    $('teamB').innerHTML = panel;
  }
  function myHitEvents(events) {
    if (!events || !app.game) return;
    for (var i = 0; i < events.length; i++) {
      var e = events[i];
      if ((e.type === 'hit' || e.type === 'ko') && e.targetId === app.game.meId) { vibrate(e.type === 'ko' ? 80 : 30); return; }
    }
  }

  // Отсчёт перед стартом матча: 3 — 2 — 1 — «БОЙ!». Миллисекунды приходят в снапшоте (поле cd),
  // в оффлайне — из драйвера. Пока идёт отсчёт, симуляция стоит и ввод не принимается.
  var cd = { shown: null, goUntil: 0 };
  function resetCountdown() { cd.shown = null; cd.goUntil = 0; countdownEl.hidden = true; }
  function updateCountdown(ms) {
    var now = performance.now();
    if (ms > 0) {
      var n = Math.max(1, Math.ceil(ms / 1000));
      if (cd.shown !== n) { cd.shown = n; countdownEl.textContent = n; Audio_.countBeep(); }
      cd.goUntil = now + 700;
      countdownEl.hidden = false;
      return;
    }
    if (cd.shown === null) return; // отсчёта не было (переподключение к идущему матчу)
    if (cd.shown !== 'go') { cd.shown = 'go'; countdownEl.textContent = 'БОЙ!'; Audio_.goBeep(); }
    if (now >= cd.goUntil) { countdownEl.hidden = true; cd.shown = null; return; }
    countdownEl.hidden = false;
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
    render.frame(fr.snap, g.meId, local, g.names, g.ranks);
    updateHUD(fr.snap);
    updateCountdown(g.countdown || 0);
    return fr;
  }

  function showGameScreen(g) {
    app.game = g;
    render.reset();
    resetCountdown();
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
      : 'WASD — движение. Зажать ЛКМ — замах, отпустить — бросок, над бойцом или E — отмена. Q или ПКМ — способность.';
    $('tutFlash').hidden = true;
    // В матче комнаты «Выйти» ведёт в лобби, а не в меню; в обучении таймера нет — бой не кончается.
    $('btnToMenu').textContent = (!g.offline && g.roomCode) ? '← В лобби' : '← Выйти';
    $('matchTimer').hidden = !!g.tutorial;
    $('tutorialBox').hidden = !g.tutorial;
    var pve = g.gameMode && g.gameMode !== 'pvp';
    $('pveInfo').hidden = !pve;
    $('hint').hidden = pve && !g.offline; // в сетевом PvE пинг и инфо волн делят панель — подсказку убираем
    if (pve) {
      $('teamALabel').textContent = 'Команда';
      $('teamBLabel').textContent = 'Волна';
    } else {
      $('teamALabel').textContent = 'Команда A' + (g.myTeam === 'A' ? ' (вы)' : '');
      $('teamBLabel').textContent = 'Команда B' + (g.myTeam === 'B' ? ' (вы)' : '');
    }
    $('teamA').innerHTML = ''; $('teamB').innerHTML = ''; resetHudCache();
    Device.apply();
    touchLayer.hidden = !isTouch;
    // HP на канвасе под ником — и на ПК тоже: так здоровье бойцов видно прямо на арене.
    render.setOptions({ pips: true });
    touch.reset(); intent.reset();
    $('fsBtn').hidden = !(isTouch && Device.fullscreenAvailable());
    clearTimeout(zonesHintTimer);
    clearTimeout(abilityHintTimer);
    $('abilityHint').classList.remove('faded');
    if (isTouch) {
      touchLayer.classList.add('showZones');
      zonesHintTimer = setTimeout(function () { touchLayer.classList.remove('showZones'); }, 4000);
      // Подсказка о способности лежит поверх арены — гасим, чтобы не мешала. Отдельного тоста
      // больше нет: он дублировал ровно этот текст.
      abilityHintTimer = setTimeout(function () { $('abilityHint').classList.add('faded'); }, 7000);
      // В сенсорном бою шапка скрыта, поэтому точка соединения с пингом переезжает в панель.
      $('gameConnSlot').appendChild($('connState'));
      if (Device.isPortrait() && !sessionFlag('sb.portraitHint')) toast('В горизонтальном положении телефона играть удобнее.');
    }
    goto('game');
    if (!rafId) loop();
  }
  function sessionFlag(k) {
    try { if (sessionStorage.getItem(k)) return true; sessionStorage.setItem(k, '1'); } catch (e) { /* игнор */ }
    return false;
  }
  var PVE_REASONS = { cleared: 1, wiped: 1, objective: 1, expired: 1 };
  function showResult(winner, myTeam, reason, snap) {
    var g = app.game; if (!g) return;
    g.over = true;
    intent.reset(); touch.reset();
    $('toMenuBtn').textContent = 'Главное меню'; // в обучении подпись другая, см. showTutorialResult
    // Кнопки обучения не должны протекать в обычный матч: табло у них одно.
    $('tutNextBtn').hidden = true;
    $('againBtn').className = '';
    overlay.style.display = 'flex';
    if (PVE_REASONS[reason]) {
      showPveResult(reason, snap || g.lastSnap);
    } else if (reason === 'shutdown') { overlayText.textContent = 'МАТЧ ПРЕРВАН'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Сервер перезапускается для обновления.'; }
    else if (reason === 'abandoned') { overlayText.textContent = 'МАТЧ ЗАВЕРШЁН'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Все игроки покинули матч.'; }
    else if (!winner) { overlayText.textContent = 'НИЧЬЯ'; overlayText.style.color = '#ffd166'; overlaySub.textContent = 'Время вышло.'; Audio_.drawChord(); }
    else if (winner === myTeam) { overlayText.textContent = 'ПОБЕДА 🎉'; overlayText.style.color = '#7CFFB2'; overlaySub.textContent = 'Команда ' + winner + ' вывела из строя всех соперников.'; Audio_.victoryFanfare(); }
    else { overlayText.textContent = 'ПОРАЖЕНИЕ'; overlayText.style.color = '#ff8080'; overlaySub.textContent = 'Команда ' + winner + ' оказалась сильнее.'; Audio_.defeatChord(); }
    $('againBtn').textContent = g.offline ? 'Играть снова' : (g.roomCode ? 'В лобби' : 'Играть снова');
  }
  function showPveResult(reason, snap) {
    var pve = snap && snap.pve;
    var where = pve ? (pve.endless
      ? 'Волн пройдено: ' + (pve.wavesSurvived || 0)
      : 'Уровень ' + ((pve.level || 0) + 1) + ', волна ' + ((pve.wave || 0) + 1)) : '';
    if (reason === 'cleared') {
      overlayText.textContent = 'КАМПАНИЯ ПРОЙДЕНА 🏆'; overlayText.style.color = '#7CFFB2';
      overlaySub.textContent = 'Все уровни зачищены. Попробуйте эндлесс!'; Audio_.victoryFanfare();
    } else if (reason === 'objective') {
      overlayText.textContent = 'СНЕГОВИК РАЗБИТ'; overlayText.style.color = '#ff8080';
      overlaySub.textContent = 'Объект не удержали. ' + where; Audio_.defeatChord();
    } else if (reason === 'expired') {
      overlayText.textContent = 'ВРЕМЯ ВЫШЛО'; overlayText.style.color = '#ffd166';
      overlaySub.textContent = where; Audio_.drawChord();
    } else { // wiped
      overlayText.textContent = 'КОМАНДА ПОВЕРЖЕНА'; overlayText.style.color = '#ff8080';
      overlaySub.textContent = where; Audio_.defeatChord();
    }
  }
  // Матч обучения не заканчивается сам (правило режима), поэтому табло показывает клиент.
  function showTutorialResult(scn) {
    var g = app.game; if (!g) return;
    g.over = true;
    intent.reset(); touch.reset();
    render.setMarks([]);
    overlay.style.display = 'flex';
    var next = firstUndoneTutorial();
    if (scn && scn.id !== 'basics') {
      overlayText.textContent = (scn.role || '').toUpperCase() + ': ОБУЧЕНИЕ ПРОЙДЕНО';
    } else {
      overlayText.textContent = 'ОСНОВЫ ПРОЙДЕНЫ 🎉';
    }
    overlaySub.textContent = next
      ? 'Дальше: ' + next.title
      : 'Пройдены все обучения — вы знаете всех героев.';
    overlayText.style.color = '#7CFFB2';
    // Главная кнопка ведёт к следующему уроку: заходить за ним в список — лишний шаг.
    var nextBtn = $('tutNextBtn');
    nextBtn.hidden = !next;
    if (next) nextBtn.textContent = 'Следующее: ' + next.title;
    $('againBtn').textContent = 'Пройти снова';
    $('againBtn').className = next ? 'secondary' : '';
    $('toMenuBtn').textContent = 'К списку обучений';
    Audio_.victoryFanfare();
  }

  function stopGame() {
    var g = app.game; if (!g) return;
    app.game = null;
    if (g.offline) sendTraining(false);
    intent.reset(); touch.reset(); keys = {}; releaseMouse();
    if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
    if (g.stop) g.stop();
  }

  // «Выйти» из матча возвращает в лобби своей комнаты: место остаётся за игроком, и войти
  // обратно можно за того же бойца. В главное меню ведёт только кнопка на табло результата.
  // Уход с экрана боя: одинаково для кнопки «Выйти» и для «назад» браузера.
  function leaveGame() {
    var g = app.game;
    if (!g) { goto('menu', { back: true }); return; }
    if (g.tutorial) { stopTutorial(); goto('tutlist', { back: true }); return; }
    if (g.offline || !g.roomCode) { goto('menu', { back: true }); return; }
    // Сначала закрываем матч на клиенте: иначе пришедший room.state попадёт в ветку «идёт матч»
    // и лобби не перерисуется — кнопка возврата в бой останется скрытой.
    stopGame();
    if (!g.over) send('match.leave');
    goto('lobby', { back: true });
    if (app.room) renderLobby();
  }
  $('btnToMenu').onclick = function () { Audio_.uiClick(); leaveGame(); };
  $('tutQuit').onclick = function () { Audio_.uiClick(); leaveGame(); };
  $('tutSkipStep').onclick = function () { Audio_.uiClick(); tutorialSkipStep(); };
  $('toMenuBtn').onclick = function () {
    Audio_.uiClick();
    var g = app.game;
    if (g && !g.offline) {
      if (!g.over) send('match.leave');
      if (g.roomCode) send('room.leave');
      app.room = null; app.roomMatch = null;
    }
    if (g && g.tutorial) { stopTutorial(); goto('tutlist'); return; }
    goto('menu');
  };
  $('tutNextBtn').onclick = function () {
    Audio_.uiClick();
    var next = firstUndoneTutorial();
    if (next) startTutorial(next.id); else goto('tutlist');
  };
  $('againBtn').onclick = function () {
    Audio_.uiClick();
    var g = app.game; if (!g) return;
    if (g.tutorial) { startTutorial(tutorial.current().id); return; }
    if (g.offline) { startOfflineMatch({}); return; }
    if (g.roomCode) { stopGame(); goto('lobby'); if (app.room) renderLobby(); return; }
    stopGame(); goto('menu');
  };

  // ------------------------------------------------------------
  // Оффлайн-матч
  // ------------------------------------------------------------
  function startOfflineMatch(opts) {
    stopGame();
    opts = opts || {};
    var o = app.offline;
    var drv = window.SBOffline.start({
      mode: o.mode, arena: o.arena, role: o.role, botLevel: o.botLevel,
      gameMode: o.gameMode, campaign: o.campaign, difficulty: o.botLevel,
      tutorial: !!opts.tutorial
    });
    sendTraining(true);
    var g = {
      offline: true, tutorial: !!opts.tutorial, meId: drv.meId, players: drv.players, names: nameMap(drv.players), myTeam: 'A',
      roomCode: '', over: false, countdown: 0,
      spawnEnemy: drv.spawnEnemy, removeEnemy: drv.removeEnemy, setBot: drv.setBot, tutorialLock: drv.tutorialLock,
      gameMode: drv.gameMode || 'pvp',
      input: function (kind, x, y, power) { if (!g.over) drv.input(kind, x, y, power); },
      frame: function () {
        if (g.over) return { snap: g.lastSnap, events: [] };
        var fr = drv.frame(); g.lastSnap = fr.snap; g.countdown = fr.countdown || 0;
        if (opts.onFrame) opts.onFrame(fr, g);
        if (drv.isOver()) {
          var reason = drv.reason && drv.reason();
          if (!reason) reason = drv.winner() ? 'ko' : 'timeout';
          showResult(drv.winner(), 'A', reason, fr.snap);
        }
        return fr;
      },
      stop: function () { drv.stop(); if (opts.onStop) opts.onStop(); }
    };
    showGameScreen(g);
    return g;
  }

  // ------------------------------------------------------------
  // Обучение (см. client/tutorial.js): оффлайн-бой 1×1 с пошаговыми задачами
  // ------------------------------------------------------------
  var tutorial = window.SBTutorial.create({
    box: $('tutorialBox'), flash: tutFlash,
    // Текст шага: на телефоне он идёт в строку подсказки над ареной вместо описания
    // управления — плашка поверх поля закрывала игру. Кэш обязателен: render зовётся каждый кадр.
    setStep: (function () {
      var last = null;
      return function (text) {
        if (text === last) return;
        last = text;
        $('tutStep').textContent = text;
        if (Device.isTouch()) $('hint').textContent = text;
      };
    })(),
    start: function (hooks, scn) {
      var saved = app.offline;
      // Роль задаёт сценарий — включая «Основы»: выбор класса на входе только тормозил новичка.
      var role = Sim.ROLE_STATS[scn.role] ? scn.role : 'Раннер';
      app.offline = { mode: 1, role: role, arena: 0, botLevel: 0, gameMode: 'pvp', campaign: true };
      var g = startOfflineMatch({ tutorial: true, onFrame: hooks.onFrame, onStop: hooks.onStop });
      app.offline = saved;
      return g;
    },
    marks: function (list) { render.setMarks(list); },
    obstacles: function (snap) { return render.obstaclesOf(snap); },
    toast: toast,
    finish: function (scn) { showTutorialResult(scn); },
    done: function (id) {
      // Один ключ на урок. Старый sb.tutorialDone больше не пишем, но читаем в tutDone:
      // у прошедших «Основы» до 0.9.0 они не должны сброситься.
      store.set('sb.tut.done.' + id, '1');
      renderTutorialBadge();
    }
  });
  function tutDone(id) {
    // Старым игрокам «Основы» считаются пройденными: раньше отметка была одна на всё обучение.
    if (id === 'basics' && store.get('sb.tutorialDone') === '1') return true;
    return store.get('sb.tut.done.' + id) === '1';
  }
  function startTutorial(id) { tutorial.start(id); }
  function stopTutorial() { tutorial.stop(); }
  function tutorialSkipStep() { tutorial.skip(); }
  // Бейдж показывает, сколько уроков осталось: «новое», пока не пройдено ничего, дальше число.
  function renderTutorialBadge() {
    var scns = window.SBTutorial.SCENARIOS;
    var left = scns.filter(function (s) { return !tutDone(s.id); }).length;
    var el = $('tutorialBadge');
    el.hidden = left === 0;
    el.textContent = left === scns.length ? 'новое' : String(left);
  }
  // Админка в меню — только «Создателю». Токен клиенту не выдаётся: сервер пускает его по адресу.
  function renderAdminBtn() { $('btnAdmin').hidden = app.rank !== 'creator'; }
  $('btnAdmin').onclick = function () { Audio_.uiClick(); window.open('/admin/', '_blank'); };

  // Короткая плашка поверх арены (в обучении вместо нижнего тоста, чтобы не улетала за край).
  var tutFlashTimer = null;
  function tutFlash(msg) {
    var el = $('tutFlash'); if (!el) { toast(msg); return; }
    el.textContent = msg; el.hidden = false;
    clearTimeout(tutFlashTimer);
    tutFlashTimer = setTimeout(function () { el.hidden = true; }, 1400);
  }

  /** Первый непройденный урок — для кнопки «Следующее обучение» на табло. */
  function firstUndoneTutorial() {
    var scns = window.SBTutorial.SCENARIOS;
    for (var i = 0; i < scns.length; i++) if (!tutDone(scns[i].id)) return scns[i];
    return null;
  }
  /** Список обучений: «Основы» плюс шесть геройских. Ничего не заблокировано. */
  function buildTutList() {
    var list = $('tutList'); list.innerHTML = '';
    var scns = window.SBTutorial.SCENARIOS, done = 0;
    scns.forEach(function (scn) {
      var passed = tutDone(scn.id);
      if (passed) done++;
      var el = document.createElement('div');
      el.className = 'tutCard' + (passed ? ' done' : '');
      el.innerHTML =
        '<div class="tutTitle">' + escapeHtml(scn.title) +
          '<span class="status ' + (passed ? 'wait">пройдено' : '">' + stepsText(scn.steps.length)) + '</span></div>' +
        '<div class="tutAbout">' + escapeHtml(scn.about) + '</div>';
      var act = document.createElement('div');
      act.className = 'tutAct';
      var btn = document.createElement('button');
      btn.className = 'menuBtn' + (passed ? ' ghost' : ' primary');
      btn.textContent = passed ? 'Пройти снова' : 'Начать';
      btn.onclick = function () { Audio_.uiClick(); startTutorial(scn.id); };
      act.appendChild(btn);
      el.appendChild(act);
      list.appendChild(el);
    });
    $('tutProgress').textContent = 'пройдено ' + done + ' из ' + scns.length;
  }
  $('tutBack').onclick = goBack;
  function stepsText(n) {
    var last = n % 10, tens = n % 100;
    if (tens > 10 && tens < 20) return n + ' шагов';
    if (last === 1) return n + ' шаг';
    if (last >= 2 && last <= 4) return n + ' шага';
    return n + ' шагов';
  }

  // ------------------------------------------------------------
  // Общий чат главного меню
  // ------------------------------------------------------------
  var CHAT_KEEP = 200;
  function chatTime(ts) {
    var d = new Date(ts || Date.now());
    return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2);
  }
  // Права на клиенте — только чтобы показать мусорку; решает всё равно сервер.
  function canDeleteChat(m) {
    if (m.pid && m.pid === app.me) return true;             // своё сообщение
    if (app.rank === 'creator') return true;                // создатель — любое
    return app.rank === 'admin' && !m.rank;                 // админ — только обычных игроков
  }
  function chatRowHtml(m) {
    var mine = !!(m.pid && m.pid === app.me);
    var nickCls = 'chatN' + (m.rank ? ' rank-' + m.rank : '');
    return '<div class="chatRow' + (mine ? ' mine' : '') + '" data-id="' + (m.id || 0) + '">' +
      '<span class="chatT">' + chatTime(m.ts) + '</span> ' +
      '<b class="' + nickCls + '">' + escapeHtml(m.nick) + '</b>: ' +
      '<span class="chatX">' + escapeHtml(m.text) + '</span>' +
      (canDeleteChat(m) ? '<button class="chatDel" type="button" data-id="' + (m.id || 0) + '" title="Удалить сообщение">🗑</button>' : '') +
      '</div>';
  }
  function chatRender() {
    var log = $('chatLog'); if (!log) return;
    log.innerHTML = app.chat.msgs.map(chatRowHtml).join('');
    var btns = log.querySelectorAll('.chatDel');
    for (var i = 0; i < btns.length; i++) {
      btns[i].onclick = function () { send('chat.del', { id: Number(this.dataset.id) }); };
    }
    log.scrollTop = log.scrollHeight;
  }
  /** Убрать одно сообщение (пуш chat.del). Неизвестный id просто игнорируем. */
  function chatRemove(id) {
    for (var i = 0; i < app.chat.msgs.length; i++) {
      if (app.chat.msgs[i].id === id) { app.chat.msgs.splice(i, 1); break; }
    }
    if (app.chat.open && chatVisible()) chatRender();
  }
  function chatSetHistory(list) {
    app.chat.msgs = list.slice(-CHAT_KEEP);
    if (app.chat.open) { app.chat.unread = 0; chatRender(); }
    else app.chat.unread = 0; // история — не «непрочитанное»
    chatBadge();
  }
  function chatAdd(m) {
    if (!m || !m.text) return;
    app.chat.msgs.push(m);
    if (app.chat.msgs.length > CHAT_KEEP) app.chat.msgs.splice(0, app.chat.msgs.length - CHAT_KEEP);
    if (app.chat.open && chatVisible()) { chatRender(); }
    else if (!(m.pid && m.pid === app.me)) { app.chat.unread++; }
    chatBadge();
  }
  function chatBadge() {
    var b = $('chatUnread'); if (!b) return;
    b.hidden = !(app.chat.unread > 0);
    b.textContent = app.chat.unread > 99 ? '99+' : String(app.chat.unread);
  }
  // Чат виден на всех экранах вне боя: на ПК это столбец справа, на телефоне — блок снизу.
  // Кроме экрана ника: сессии там ещё нет, писать некуда, а поле ввода отвлекало бы от имени.
  function chatVisible() { return app.screen !== 'game' && app.screen !== 'nick'; }
  // focus — только при явном раскрытии чата игроком: при загрузке страницы фокус нужен полю
  // ника, а чат теперь виден и на этом экране.
  function chatSetOpen(open, focus) {
    app.chat.open = open;
    $('chatBody').hidden = !open;
    $('chatToggle').classList.toggle('open', open);
    if (open) {
      app.chat.unread = 0; chatBadge(); chatRender();
      if (focus) setTimeout(function () { $('chatInput').focus(); }, 0);
    }
    try { localStorage.setItem('sb.chatOpen', open ? '1' : '0'); } catch (e) { /* игнор */ }
  }
  function chatSubmit() {
    var el = $('chatInput'), text = (el.value || '').trim();
    if (!text) return;
    if (text.length > 300) text = text.slice(0, 300);
    send('chat.send', { text: text });
    el.value = '';
  }
  $('chatToggle').onclick = function () { Audio_.uiClick(); chatSetOpen(!app.chat.open, true); };
  $('chatSend').onclick = function () { chatSubmit(); };
  $('chatInput').addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); chatSubmit(); } });
  // По умолчанию чат открыт: закрытым его почти никто не находил. Выбор игрока помним.
  chatSetOpen(store.get('sb.chatOpen') !== '0');

  // ------------------------------------------------------------
  // Сетевой матч
  // ------------------------------------------------------------
  function startNetMatch(d) {
    if (app.game && !app.game.offline && app.game.matchId === d.matchId) { $('reconnectOverlay').hidden = true; return; } // переподключение
    stopGame();
    var buffer = Net.snapshotBuffer(d.tickRate);
    var pending = [];
    var myTeam = 'A';
    // Задержку показывает единственный индикатор — точка соединения (на телефоне она в бою
    // переезжает в игровую панель). Здесь только джиттер снапшотов: он в indicator не входит,
    // но именно он делает бой рваным, поэтому при RTT > 200 мс или джиттере > 100 мс дольше
    // трёх секунд один раз в минуту показываем тост про VPN.
    var net = { badSince: 0, lastWarn: 0 };
    var statTimer = setInterval(function () {
      if (g.over || app.screen !== 'game') return;
      var jitter = buffer.jitter(), now = performance.now();
      var bad = app.ping.rtt > 200 || jitter > 100;
      if (!bad) net.badSince = 0; else if (!net.badSince) net.badSince = now;
      app.ping.jitter = jitter;
      renderConn();
      if (net.badSince && now - net.badSince >= 3000 && now - net.lastWarn > 60000) {
        net.lastWarn = now;
        toast('Высокая задержка сети. Если включён VPN, попробуйте его выключить.');
      }
    }, 1000);
    d.players.forEach(function (p) { if (p.id === d.yourId) myTeam = p.team; });
    var g = {
      offline: false, matchId: d.matchId, meId: d.yourId, players: d.players,
      names: nameMap(d.players), ranks: rankMap(d.players),
      gameMode: d.gameMode || 'pvp', myTeam: myTeam, roomCode: d.roomCode || '', over: false,
      lastSnap: null, countdown: 0,
      input: function (kind, x, y, power) {
        if (g.over) return;
        var inp = { kind: kind, x: Math.round(x * 10) / 10, y: Math.round(y * 10) / 10 };
        if (power !== undefined) inp.power = Math.round(power * 100) / 100;
        app.net.send('input', inp);
      },
      onSnapshot: function (s) {
        g.countdown = s.cd || 0;
        buffer.push(s.s);
        if (s.e && s.e.length) pending = pending.concat(s.e);
      },
      onEnd: function (e) {
        if (g.lastSnap == null && buffer.latest()) g.lastSnap = buffer.latest();
        showResult(e.winner, e.yourTeam || myTeam, e.reason, g.lastSnap);
      },
      frame: function () {
        var snap = buffer.current();
        if (!snap) return null;
        g.lastSnap = snap;
        var ev = pending; pending = [];
        return { snap: snap, events: ev };
      },
      stop: function () { buffer.clear(); clearInterval(statTimer); }
    };
    showGameScreen(g);
  }

  // ------------------------------------------------------------
  // Старт
  // ------------------------------------------------------------
  // Отладочный хук для DevTools и автотестов: состояние приложения и ручной кадр.
  window.SBApp = { state: app, renderOnce: renderOnce, send: send, intent: intent, tutorial: tutorial };

  Device.apply();
  $('verBuild').textContent = BUILD;
  // Модуль звука грузится раньше настроек, поэтому сохранённую громкость подставляем здесь.
  Audio_.setVolume(Settings.get('volume'));
  renderVolume();
  pushGuard();
  if (app.nick) {
    connect();
    // Восстанавливаем экран из адреса: без этого F5 в списке комнат или в настройках
    // выбрасывал в главное меню. Раздел берём из адреса, иначе из последнего выбранного.
    var start = parseHash(location.hash);
    app.section = (start && start.section) || store.get('sb.section') || 'pvp';
    // Именно push, а не replace: запись стража должна остаться под нами отдельной строкой
    // истории, иначе «назад» на корневом экране сразу уводит с сайта.
    goto(start ? start.screen : 'menu');
  } else {
    // Ника нет — адрес не при чём: сначала имя. Разобранный хэш выбрасываем, иначе после
    // ввода ника игрок телепортируется в раздел, которого не ждёт.
    $('nickInput').value = '';
    goto('nick');
    try { history.replaceState(history.state, '', '#/'); } catch (e) { /* игнор */ }
  }
})();
