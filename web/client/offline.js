/* Оффлайн-драйвер: та же sim.js исполняется в браузере, соперники и союзники — боты.
 * Нужен разработчику игры для отладки без сервера и как тренировка для игроков. */
window.SBOffline = (function () {
  var Sim = window.SnowBrawlSim;

  function buildPlayers(mode, myRole, rng, botLevel, pve) {
    var lvl = botLevel == null ? 1 : (botLevel | 0);
    var players = [{ id: 'me', team: 'A', role: myRole, bot: false, nick: 'Вы' }];
    var pool = Sim.shuffle(rng, Sim.ALL_ROLES.filter(function (r) { return r !== myRole; }));
    for (var i = 1; i < mode; i++) players.push({ id: 'a' + i, team: 'A', role: pool[(i - 1) % pool.length], bot: true, botLevel: lvl, nick: 'Союзник ' + i });
    if (pve) return players; // врагов создаёт волновой планировщик sim.js
    var poolB = Sim.shuffle(rng, Sim.ALL_ROLES.slice());
    for (var j = 0; j < mode; j++) players.push({ id: 'b' + j, team: 'B', role: poolB[j % poolB.length], bot: true, botLevel: lvl, nick: 'Бот ' + (mode + j) });
    return players;
  }

  var COUNTDOWN_MS = 3000; // как на сервере: матч начинается после отсчёта

  /**
   * start({mode, arena, role, botLevel, gameMode, campaign, difficulty, tutorial}) → драйвер,
   * совместимый с сетевым матчем. tutorial: true — матч обучения (правила см. SIM_CONTRACT):
   * один боец, матч не кончается, соперника ставит клиент через spawnEnemy.
   */
  function start(cfg) {
    var seed = (Math.random() * 0xffffffff) >>> 0;
    var rng = Sim.makeRng(seed);
    var gameMode = cfg.gameMode || 'pvp';
    var pve = gameMode !== 'pvp';
    var players = cfg.tutorial
      ? [{ id: 'me', team: 'A', role: cfg.role, bot: false, nick: 'Вы' }]
      : buildPlayers(cfg.mode, cfg.role, rng, pve ? cfg.difficulty : cfg.botLevel, pve);
    var mcfg = { gameMode: gameMode, mode: cfg.mode, arenaIndex: cfg.arena, players: players };
    if (cfg.tutorial) mcfg.tutorial = true;
    if (pve) { mcfg.difficulty = cfg.difficulty == null ? 1 : (cfg.difficulty | 0); mcfg.campaign = cfg.campaign !== false; }
    var state = Sim.createMatch(mcfg, seed);
    var lastT = performance.now();
    var startAt = lastT + COUNTDOWN_MS;
    return {
      offline: true,
      meId: 'me',
      players: players.map(function (p) { return { id: p.id, nick: p.nick, team: p.team, role: p.role, bot: p.bot }; }),
      mode: cfg.mode, arena: cfg.arena, gameMode: gameMode,
      input: function (kind, x, y, power) {
        if (performance.now() < startAt) return; // идёт отсчёт
        var inp = { kind: kind, x: x, y: y };
        if (power !== undefined) inp.power = power;
        Sim.applyInput(state, 'me', inp);
      },
      /** Продвинуть симуляцию до текущего времени; вернуть {snap, events}. */
      frame: function () {
        var now = performance.now();
        if (now < startAt) { // отсчёт: симуляция стоит, арена и бойцы уже видны
          lastT = now;
          return { snap: Sim.snapshot(state), events: [], countdown: Math.round(startAt - now) };
        }
        var dt = Math.min((now - lastT) / 1000, 0.05);
        lastT = now;
        var events = Sim.step(state, dt);
        return { snap: Sim.snapshot(state), events: events };
      },
      /** Обучение: поставить соперника; возвращает его id. */
      spawnEnemy: function (opts) { return Sim.tutorialSpawn(state, opts); },
      /** Обучение: убрать соперника. */
      removeEnemy: function (id) { return Sim.tutorialRemove(state, id); },
      /** Обучение: держать соперника на 1 HP (последний шаг, до применения способности). */
      tutorialLock: function (on) { return Sim.tutorialLock ? Sim.tutorialLock(state, on) : false; },
      /**
       * Включить или выключить ИИ бойца. Выключенный стоит на месте; замах перед заморозкой
       * гасим, иначе боец навсегда останется в позе замаха (см. SIM_CONTRACT).
       */
      setBot: function (id, on) {
        if (!on) Sim.applyInput(state, id, { kind: 'cancelCharge' });
        return Sim.setBot(state, id, !!on);
      },
      isOver: function () { return Sim.isOver(state); },
      winner: function () { return Sim.winner(state); },
      reason: function () { return Sim.reason ? Sim.reason(state) : ''; },
      stop: function () { }
    };
  }

  return { start: start };
})();
