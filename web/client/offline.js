/* Оффлайн-драйвер: та же sim.js исполняется в браузере, соперники и союзники — боты.
 * Нужен разработчику игры для отладки без сервера и как тренировка для игроков. */
window.SBOffline = (function () {
  var Sim = window.SnowBrawlSim;

  function buildPlayers(mode, myRole, rng) {
    var players = [{ id: 'me', team: 'A', role: myRole, bot: false, nick: 'Вы' }];
    var pool = Sim.shuffle(rng, Sim.ALL_ROLES.filter(function (r) { return r !== myRole; }));
    for (var i = 1; i < mode; i++) players.push({ id: 'a' + i, team: 'A', role: pool[(i - 1) % pool.length], bot: true, nick: 'Бот ' + i });
    var poolB = Sim.shuffle(rng, Sim.ALL_ROLES.slice());
    for (var j = 0; j < mode; j++) players.push({ id: 'b' + j, team: 'B', role: poolB[j % poolB.length], bot: true, nick: 'Бот ' + (mode + j) });
    return players;
  }

  /** start({mode, arena, role}) → драйвер с интерфейсом, совместимым с сетевым матчем. */
  function start(cfg) {
    var seed = (Math.random() * 0xffffffff) >>> 0;
    var rng = Sim.makeRng(seed);
    var players = buildPlayers(cfg.mode, cfg.role, rng);
    var state = Sim.createMatch({ mode: cfg.mode, arenaIndex: cfg.arena, players: players }, seed);
    var lastT = performance.now();
    return {
      offline: true,
      meId: 'me',
      players: players.map(function (p) { return { id: p.id, nick: p.nick, team: p.team, role: p.role, bot: p.bot }; }),
      mode: cfg.mode, arena: cfg.arena,
      input: function (kind, x, y, power) {
        var inp = { kind: kind, x: x, y: y };
        if (power !== undefined) inp.power = power;
        Sim.applyInput(state, 'me', inp);
      },
      /** Продвинуть симуляцию до текущего времени; вернуть {snap, events}. */
      frame: function () {
        var now = performance.now();
        var dt = Math.min((now - lastT) / 1000, 0.05);
        lastT = now;
        var events = Sim.step(state, dt);
        return { snap: Sim.snapshot(state), events: events };
      },
      isOver: function () { return Sim.isOver(state); },
      winner: function () { return Sim.winner(state); },
      stop: function () { }
    };
  }

  return { start: start };
})();
