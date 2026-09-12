/* Рендер арены по снапшоту sim.js. Частицы, тряска, снежинки, следы снежков и звуки
 * живут здесь, на клиенте: они косметика и не влияют на симуляцию. */
window.SBRender = (function () {
  var Sim = window.SnowBrawlSim;
  var W = Sim.W, H = Sim.H;

  function create(canvas) {
    // alpha:false — слой непрозрачный, композитору не нужно блендить его с фоном страницы;
    // фон арены рисуется в кэше арены (см. buildArena).
    var ctx = canvas.getContext('2d', { alpha: false }) || canvas.getContext('2d');
    var snowflakes = [], particles = [], explosions = [], trails = {};
    var shake = { mag: 0, until: 0, total: 1 };
    var lastFrame = performance.now();
    var coarse = false;
    try { coarse = window.matchMedia && window.matchMedia('(pointer: coarse)').matches; } catch (e) { /* игнор */ }

    // Внутреннее разрешение канваса тянем под фактический размер на экране (×DPR): при крупной
    // арене на ПК рисование в 900×560 и апскейл браузером мылит спрайты и текст. RS — множитель
    // бэкстора; вся отрисовка остаётся в логических координатах 900×560 через setTransform(RS).
    var RS = 1;
    function computeRS() {
      var dpr = window.devicePixelRatio || 1;
      var cssW = canvas.clientWidth || (canvas.getBoundingClientRect && canvas.getBoundingClientRect().width) || W;
      var want = Math.max(1, Math.min(2.5, cssW * dpr / W));
      return Math.round(want * 4) / 4; // шаг 0.25 — реже пересобираем кэши арены и подписей
    }
    function applyRS() {
      var next = computeRS();
      if (next === RS && canvas.width === Math.round(W * next)) return;
      RS = next;
      canvas.width = Math.round(W * RS);
      canvas.height = Math.round(H * RS);
      arenaCache.index = -1; labels = {};
    }
    try { window.addEventListener('resize', applyRS); } catch (e) { /* игнор */ }

    // Снежинки в трёх «корзинах» прозрачности: три fill на кадр вместо одного на снежинку.
    var SNOW_ALPHA = [0.35, 0.55, 0.75];
    for (var i = 0, n = coarse ? 28 : 40; i < n; i++) {
      snowflakes.push({ x: Math.random() * W, y: Math.random() * H, speed: 20 + Math.random() * 40,
        drift: (Math.random() - 0.5) * 20, size: 1 + Math.random() * 2, bucket: i % 3 });
    }

    // Статичная арена (фон + препятствия) рисуется один раз в offscreen-canvas: градиенты и
    // shadowBlur на каждое препятствие каждый кадр на телефонах стоят дороже всего остального.
    var arenaCache = { index: -1, canvas: null };
    function buildArena(index) {
      var oc = document.createElement('canvas'); oc.width = Math.round(W * RS); oc.height = Math.round(H * RS);
      var c = oc.getContext('2d');
      c.setTransform(RS, 0, 0, RS, 0, 0);
      var bg = c.createLinearGradient(0, 0, 0, H); bg.addColorStop(0, '#dfeeff'); bg.addColorStop(1, '#c3ddf7');
      c.fillStyle = bg; c.fillRect(0, 0, W, H);
      // Крапинка снега — убирает «мёртвую» заливку, помогает силуэтам. Запекается один раз.
      var rnd = 20260910;
      function nrand() { rnd = (rnd * 1103515245 + 12345) & 0x7fffffff; return rnd / 0x7fffffff; }
      for (var sp = 0; sp < 340; sp++) {
        var sx = nrand() * W, sy = nrand() * H, ss = 0.6 + nrand() * 1.1;
        c.fillStyle = nrand() < 0.5 ? 'rgba(255,255,255,0.55)' : 'rgba(150,175,205,0.35)';
        c.beginPath(); c.arc(sx, sy, ss, 0, Math.PI * 2); c.fill();
      }
      var arena = Sim.ARENAS[index] || Sim.ARENAS[0];
      // «Река»: глянцевая ледяная полоса (статичная часть в кэше, бегущий блик — в drawArena).
      if (arena.ice) {
        var y0 = arena.ice.y0, ih = arena.ice.y1 - arena.ice.y0;
        var ig = c.createLinearGradient(0, y0, 0, y0 + ih);
        ig.addColorStop(0, '#7fd0e6'); ig.addColorStop(0.45, '#a6e6f2'); ig.addColorStop(0.55, '#8ad8ea'); ig.addColorStop(1, '#5cb8d4');
        c.fillStyle = ig; c.fillRect(0, y0, W, ih);
        // морозная крошка-паутина
        c.strokeStyle = 'rgba(255,255,255,0.5)'; c.lineWidth = 1;
        for (var il = 0; il < 9; il++) {
          var iy = y0 + 4 + il * (ih - 8) / 8;
          c.beginPath(); c.moveTo(10 + il * 34, iy); c.lineTo(W - 30 + il * 10, iy - 7); c.stroke();
        }
        for (var iv = 0; iv < 14; iv++) {
          var ix = 40 + iv * (W - 80) / 13;
          c.beginPath(); c.moveTo(ix, y0 + 3); c.lineTo(ix + 8, y0 + ih - 3); c.stroke();
        }
        // блики-полосы
        c.fillStyle = 'rgba(255,255,255,0.35)';
        c.fillRect(0, y0 + ih * 0.18, W, 3);
        c.fillRect(0, y0 + ih * 0.62, W, 2);
        c.strokeStyle = NINK; c.lineWidth = 2;
        c.beginPath(); c.moveTo(0, y0); c.lineTo(W, y0); c.moveTo(0, y0 + ih); c.lineTo(W, y0 + ih); c.stroke();
      }
      // Кант арены.
      c.strokeStyle = 'rgba(19,41,63,0.35)'; c.lineWidth = 4;
      c.strokeRect(2, 2, W - 4, H - 4);
      // Препятствия и ёлки в кэш НЕ пишем: они «высокие», их рисует y-sorted слой в frame().
      arenaCache.index = index; arenaCache.canvas = oc;
    }

    // Разрушаемое укрытие — деревянный ящик/бочка (коричневый = «ломай»), cel + трещины по HP.
    function drawDestructible(d) {
      var frac = d.maxHp ? d.hp / d.maxHp : 1;
      ctx.save();
      ctx.lineJoin = 'round';
      if (d.type === 'circle') {
        contactShadow(d.x, d.y + d.r * 0.9, d.r * 0.95, d.r * 0.3);
        ctx.beginPath(); ctx.arc(d.x, d.y, d.r, 0, Math.PI * 2);
        var g = ctx.createLinearGradient(0, d.y - d.r, 0, d.y + d.r);
        g.addColorStop(0, '#d79a5e'); g.addColorStop(1, '#a56a34');
        ctx.fillStyle = g; ctx.fill();
        ctx.lineWidth = 3; ctx.strokeStyle = NINK; ctx.stroke();
        ctx.strokeStyle = 'rgba(60,35,15,0.6)'; ctx.lineWidth = 2;
        ctx.beginPath(); ctx.arc(d.x, d.y, d.r * 0.62, 0, Math.PI * 2); ctx.stroke();
        ctx.beginPath(); ctx.moveTo(d.x - d.r + 2, d.y); ctx.lineTo(d.x + d.r - 2, d.y); ctx.stroke();
      } else {
        var x0 = d.x - d.w / 2, y0 = d.y - d.h / 2;
        contactShadow(d.x, y0 + d.h + 2, d.w * 0.55, 5);
        roundRectPath(x0, y0, d.w, d.h, 3);
        var g2 = ctx.createLinearGradient(0, y0, 0, y0 + d.h);
        g2.addColorStop(0, '#d79a5e'); g2.addColorStop(1, '#a56a34');
        ctx.fillStyle = g2; ctx.fill();
        ctx.lineWidth = 3; ctx.strokeStyle = NINK; ctx.stroke();
        ctx.strokeStyle = 'rgba(60,35,15,0.55)'; ctx.lineWidth = 2;
        for (var pl = 1; pl < 3; pl++) { ctx.beginPath(); ctx.moveTo(x0 + d.w * pl / 3, y0 + 2); ctx.lineTo(x0 + d.w * pl / 3, y0 + d.h - 2); ctx.stroke(); }
        ctx.beginPath(); ctx.moveTo(x0 + 2, d.y); ctx.lineTo(x0 + d.w - 2, d.y); ctx.stroke();
      }
      if (frac < 0.999) {
        ctx.strokeStyle = 'rgba(30,15,5,0.8)'; ctx.lineWidth = 1.8;
        var cr = (d.type === 'circle' ? d.r : Math.min(d.w, d.h) / 2);
        var n = frac < 0.4 ? 4 : 2;
        for (var ci = 0; ci < n; ci++) {
          var a = ci * 2.2 + 0.6;
          ctx.beginPath();
          ctx.moveTo(d.x + Math.cos(a) * cr * 0.2, d.y + Math.sin(a) * cr * 0.2);
          ctx.lineTo(d.x + Math.cos(a + 0.5) * cr * 0.9, d.y + Math.sin(a + 0.5) * cr * 0.9);
          ctx.stroke();
        }
      }
      ctx.restore();
    }

    // --- «Высокие» объекты арены: cel-стиль под модели, рисуются в y-sorted слое (frame()). ---
    var NINK = '#13293f'; // общая навигационная обводка мира
    function contactShadow(x, y, rx, ry) {
      ctx.fillStyle = 'rgba(20,40,70,0.16)';
      ctx.beginPath(); ctx.ellipse(x, y, rx, ry, 0, 0, Math.PI * 2); ctx.fill();
    }
    function roundRectPath(x, y, w, h, r) {
      r = Math.min(r, w / 2, h / 2);
      ctx.beginPath();
      ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
    }
    // Несокрушимое укрытие — глыба льда: без чёрной обводки, стеклянный вид, светлый кант.
    function drawCoverBlock(ob) {
      var x0 = ob.x - ob.w / 2, y0 = ob.y - ob.h / 2;
      contactShadow(ob.x, y0 + ob.h + 3, ob.w * 0.52, 6);
      roundRectPath(x0, y0, ob.w, ob.h, 5);
      var g = ctx.createLinearGradient(0, y0, 0, y0 + ob.h);
      g.addColorStop(0, '#e2f5fb'); g.addColorStop(0.45, '#a9e2ee'); g.addColorStop(0.55, '#8fd4e4'); g.addColorStop(1, '#63b3cc');
      ctx.fillStyle = g; ctx.fill();
      ctx.lineWidth = 1.5; ctx.strokeStyle = 'rgba(70,150,180,0.55)'; ctx.lineJoin = 'round'; ctx.stroke();
      ctx.save();
      roundRectPath(x0, y0, ob.w, ob.h, 5); ctx.clip();
      // блик по верхней грани
      ctx.fillStyle = 'rgba(255,255,255,0.75)'; ctx.fillRect(x0, y0, ob.w, 3);
      // косые «стеклянные» полосы
      ctx.strokeStyle = 'rgba(255,255,255,0.35)'; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(x0 + ob.w * 0.2, y0 + ob.h); ctx.lineTo(x0 + ob.w * 0.42, y0); ctx.stroke();
      ctx.beginPath(); ctx.moveTo(x0 + ob.w * 0.55, y0 + ob.h); ctx.lineTo(x0 + ob.w * 0.7, y0); ctx.stroke();
      // тень у нижней грани — объём
      ctx.fillStyle = 'rgba(40,90,120,0.25)'; ctx.fillRect(x0, y0 + ob.h - 4, ob.w, 4);
      ctx.restore();
    }
    // Ёлка — округлая крона по кругу коллизии + снежная шапка.
    function drawTree(ob) {
      var cx = ob.x, cy = ob.y - 2, r = ob.r;
      contactShadow(ob.x, ob.y + 11, r * 0.9, r * 0.28);
      ctx.fillStyle = '#7a5230'; ctx.strokeStyle = NINK; ctx.lineWidth = 2.5; ctx.lineJoin = 'round';
      ctx.beginPath(); ctx.rect(ob.x - 3.5, ob.y, 7, 13); ctx.fill(); ctx.stroke();
      ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2);
      var g = ctx.createRadialGradient(cx - r * 0.35, cy - r * 0.4, r * 0.2, cx, cy, r);
      g.addColorStop(0, '#4f9a68'); g.addColorStop(1, '#245c3c');
      ctx.fillStyle = g; ctx.fill();
      ctx.lineWidth = 3; ctx.strokeStyle = NINK; ctx.stroke();
      // снежная шапка сверху
      ctx.save(); ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.clip();
      ctx.fillStyle = '#eef6ff';
      ctx.beginPath(); ctx.ellipse(cx, cy - r * 0.72, r * 0.9, r * 0.5, 0, 0, Math.PI * 2); ctx.fill();
      ctx.restore();
    }
    // Снежная стена Щита — блок как укрытие, но с кантом в цвет команды и трещинами по HP.
    function drawWall(wl) {
      var x0 = wl.x - wl.w / 2, y0 = wl.y - wl.h / 2;
      ctx.globalAlpha = 0.55 + (wl.ttl / wl.life) * 0.45;
      roundRectPath(x0, y0, wl.w, wl.h, 4);
      var g = ctx.createLinearGradient(0, y0, 0, y0 + wl.h);
      g.addColorStop(0, '#d8e4f0'); g.addColorStop(1, '#a8bdd4');
      ctx.fillStyle = g; ctx.fill();
      ctx.lineWidth = 3; ctx.strokeStyle = NINK; ctx.lineJoin = 'round'; ctx.stroke();
      roundRectPath(x0 + 2.5, y0 + 2.5, wl.w - 5, wl.h - 5, 3);
      ctx.lineWidth = 2; ctx.strokeStyle = wl.team === 'A' ? '#2e7aff' : '#f23a3a'; ctx.stroke();
      if (wl.maxHp && wl.hp < wl.maxHp) {
        ctx.strokeStyle = 'rgba(20,40,70,0.75)'; ctx.lineWidth = 1.5;
        for (var wc = 0; wc < (wl.maxHp - wl.hp); wc++) {
          var wx = x0 + (wc + 1) * wl.w / (wl.maxHp + 1);
          ctx.beginPath(); ctx.moveTo(wx, y0 + 3); ctx.lineTo(wx + 3, y0 + wl.h - 3); ctx.stroke();
        }
      }
      ctx.globalAlpha = 1;
    }

    // Подписи бойцов (ник, под ним полоска HP с числом) — спрайт на бойца, fillText со сменой
    // шрифта каждый кадр на телефонах заметно дорог (особенно эмодзи бота). Спрайт пересобирается
    // при смене HP: состояний мало даже у босса.
    var labels = {};
    var names = null; // карта id → ник из состава матча (match.start / match.roster)
    var ranks = null; // карта id → роль модерации: ник рисуется её цветом
    // Роли модерации в бою: на белом снегу цветной ник читается только с тёмной обводкой.
    var RANK_COLORS = { creator: '#ffc94d', admin: '#b478ff' };
    function nickOf(p) { return (names && names[p.id]) || p.nick; }
    function rankOf(p) { return (ranks && ranks[p.id]) || ''; }
    // Масштаб риг-модели: тем же множителем считается её высота (Rig.topOf), чтобы подпись
    // вставала точно над макушкой.
    var RIG_SCALE = 1.32;
    // Цвет подписи и полоски HP — по принадлежности, а не по команде: свой зелёный, союзник
    // голубой, противник красный. Так здоровье читается одинаково за обе стороны и в PvE.
    function sideColor(p, isMe, myTeam) {
      if (isMe) return '#3ddc84';
      return p.team === myTeam ? '#62b8ff' : '#ff5b5b';
    }

    // Спрайтовая модель бойца (web/client/rig.js). Пер-игроковое состояние: гистерезис
    // зеркала + метки времени броска и скилла (ставятся из handleEvents по playerId).
    var Rig = window.SBRig || null;
    var rigCache = {};
    var HIT_MS = 200, THROW_MS = 300, ABIL_MS = 420;
    function rigC(id) {
      return rigCache[id] || (rigCache[id] = { flip: 1, throwAt: -1e9, abilAt: -1e9, prevX: null, ph: (parseInt(id, 36) || 0) % 997 });
    }
    function rigStateFor(snap, p, isMe, local) {
      var rc = rigC(p.id);
      var now = performance.now() + rc.ph * 3;
      var charging = isMe && local && local.charging ? true : p.charging;
      var power = isMe && local && local.charging ? local.power : (p.power || 0);
      var aX = isMe && local && local.charging ? local.aimX : p.aimX;
      var aY = isMe && local && local.charging ? local.aimY : p.aimY;
      var aim = (aX != null) ? Math.atan2(aY - p.y, aX - p.x) : (p.team === 'A' ? 0 : Math.PI);
      // Сторона взгляда меняется МГНОВЕННО: при замахе — по прицелу, иначе — по движению.
      if (charging) {
        var cs = Math.cos(aim);
        if (cs > 0.02) rc.flip = 1; else if (cs < -0.02) rc.flip = -1;
      } else {
        var dx = rc.prevX == null ? 0 : p.x - rc.prevX;
        if (dx > 0.35) rc.flip = 1; else if (dx < -0.35) rc.flip = -1;
      }
      rc.prevX = p.x;
      var throwT = (now - rc.throwAt) / THROW_MS;
      var abilT = (now - rc.abilAt) / ABIL_MS;
      // Труп тает 4 с и стартует полупрозрачным — и у PvE-мобов, и у союзников в PvE (p.lives —
      // признак члена пати; вплотную до конца жизней он лежит так же, как враг). Боец PvP,
      // где p.lives нет, просто валится до конца раунда.
      var dissolve = !!p.et || p.lives != null;
      var koMs = dissolve ? 4000 : (Sim.KO_ANIM_MS || 500), hitAge = snap.time - p.hitAt;
      var mode = 'idle';
      if (p.koed) mode = 'ko';
      else if (hitAge >= 0 && hitAge < HIT_MS) mode = 'hit';
      else if (abilT >= 0 && abilT < 1) mode = 'ability';
      else if (throwT >= 0 && throwT < 1) mode = 'throw';
      else if (charging) mode = 'charge';
      else if (p.moving) mode = 'run';
      return {
        mode: mode, t: now, role: p.role, flip: rc.flip,
        // Любой не-A читается как враг (красный): в сетевом снапшоте team у врага иногда пуст,
        // и без этого модель оставалась серой.
        team: p.team === 'A' ? 'A' : 'B', corpse: dissolve, stun: p.stun > 0,
        corrupt: p.et === 'core' || p.et === 'swarm', lod: p.et === 'swarm',
        aimLocal: rc.flip < 0 ? Math.PI - aim : aim, aimScreen: aim,
        power: power, speed: 1, outline: 2.4, scaleMul: 1.32, variantB: false, back: false, noShadow: true,
        throwT: mode === 'throw' ? Math.min(1, throwT) : 0,
        abilityT: mode === 'ability' ? Math.min(1, abilT) : 0,
        hitT: mode === 'hit' ? Math.min(1, hitAge / HIT_MS) : 0,
        koT: mode === 'ko' ? Math.min(1, (snap.time - p.koAt) / koMs) : 0,
        recoil: mode === 'hit' ? Math.sin(hitAge / HIT_MS * 40) * (1 - hitAge / HIT_MS) * 5 : 0,
        flyProp: (mode === 'throw' && throwT > 0.34) ? (throwT - 0.34) / 0.66
               : (mode === 'ability' && abilT > 0.42 && p.role === 'Бомбер') ? (abilT - 0.42) / 0.58 : null
      };
    }

    function labelOf(p, isMe, myTeam) {
      var base = nickOf(p), rank = rankOf(p);
      var maxHp = p.mhp || 3, hp = Math.max(0, Math.min(maxHp, p.hp));
      var bar = !p.koed;                       // у выбитых полоски нет
      var col = sideColor(p, isMe, myTeam);
      var key = base + '|' + (p.bot ? 1 : 0) + '|' + (isMe ? 1 : 0) + '|' + rank + '|' + col +
        '|' + (bar ? hp + '/' + maxHp : 'ko');
      var l = labels[p.id];
      if (l && l.key === key) return l;
      var nick = base + (p.bot ? ' 🤖' : '');
      var oc = document.createElement('canvas'), c = oc.getContext('2d');
      c.font = (isMe ? 'bold ' : '') + '10px Segoe UI, Arial';
      var w = Math.ceil(Math.max(c.measureText(nick).width, 20)) + 8;
      var NICK_H = 12, BAR_H = 9, GAP = 2;     // ник сверху, под ним полоска
      if (bar) w = Math.max(w, 38);
      var h = NICK_H + (bar ? GAP + BAR_H : 0);
      // Спрайт подписи растеризуем во внутреннем разрешении канваса (RS), рисуется он с явным
      // логическим размером (w×h) — иначе на крупной арене текст мылится вместе с апскейлом.
      oc.width = Math.max(1, Math.round(w * RS)); oc.height = Math.max(1, Math.round(h * RS));
      c = oc.getContext('2d'); c.setTransform(RS, 0, 0, RS, 0, 0); c.textAlign = 'center';
      c.font = (isMe ? 'bold ' : '') + '10px Segoe UI, Arial';
      // Роль модерации перебивает цвет принадлежности: создателя и админа видно по нику, а
      // сторону всё равно показывает полоска под ним.
      c.lineWidth = 2.5; c.strokeStyle = '#0b1622'; c.strokeText(nick, w / 2, NICK_H - 2);
      c.fillStyle = RANK_COLORS[rank] || col;
      c.fillText(nick, w / 2, NICK_H - 2);
      if (bar) {
        // Полоска цельная: длина заливки — доля здоровья, цвет постоянный (по принадлежности),
        // по центру число оставшихся HP. Одинаково у бойцов (3 HP) и у мобов PvE (до 12).
        var bw = w - 8, bx = 4, by = NICK_H + GAP;
        c.fillStyle = 'rgba(11,22,34,0.62)';
        c.fillRect(bx, by, bw, BAR_H);
        c.fillStyle = col;
        c.fillRect(bx + 1, by + 1, Math.max(0, (bw - 2) * (hp / maxHp)), BAR_H - 2);
        c.strokeStyle = 'rgba(11,22,34,0.85)'; c.lineWidth = 1;
        c.strokeRect(bx + 0.5, by + 0.5, bw - 1, BAR_H - 1);
        c.font = 'bold 8px Segoe UI, Arial';
        c.lineWidth = 2.5; c.strokeStyle = 'rgba(11,22,34,0.9)';
        c.strokeText(String(hp), w / 2, by + BAR_H - 2);
        c.fillStyle = '#ffffff';
        c.fillText(String(hp), w / 2, by + BAR_H - 2);
      }
      l = labels[p.id] = { key: key, canvas: oc, w: w, h: h };
      return l;
    }

    // Почему боец замедлен: наледь под ним, аура вражеского Фризера или лёд арены. Всё это
    // выводится из уже приходящих данных, поэтому отдельного признака в снапшоте нет.
    function slowCause(snap, p) {
      if (snap.fx) for (var i = 0; i < snap.fx.length; i++) {
        var z = snap.fx[i];
        if (z.kind === 'frost' && z.team !== p.team && Math.hypot(z.x - p.x, z.y - p.y) <= z.r) return 'frost';
      }
      for (var j = 0; j < snap.players.length; j++) {
        var q = snap.players[j];
        if (q.role === 'Фризер' && q.team !== p.team && !q.koed && q.hp > 0 &&
            Math.hypot(q.x - p.x, q.y - p.y) <= Sim.FREEZER_AURA_R) return 'aura';
      }
      return snap.ice ? 'ice' : null;
    }

    function triggerShake(mag, durationMs) { shake.mag = mag; shake.until = performance.now() + durationMs; shake.total = durationMs; }
    function spawnParticles(x, y, color, n, spd0, spd1, size0, size1, life) {
      for (var i = 0; i < n; i++) {
        var ang = Math.random() * Math.PI * 2, spd = spd0 + Math.random() * (spd1 - spd0);
        particles.push({ x: x, y: y, vx: Math.cos(ang) * spd, vy: Math.sin(ang) * spd - 30, life: life + Math.random() * 0.2, maxLife: life + 0.1, color: color, size: size0 + Math.random() * (size1 - size0) });
      }
    }

    /** Обработать события шага симуляции: звук и эффекты. meId — свой боец (для оценки громкости не используется). */
    function handleEvents(events, audio) {
      if (!events) return;
      for (var i = 0; i < events.length; i++) {
        var e = events[i];
        // Метки времени для спрайтовой модели: бросок и применение скилла — по playerId.
        if (e.playerId) {
          if (e.type === 'throw') rigC(e.playerId).throwAt = performance.now();
          else if (e.type === 'dash' || e.type === 'special' || e.type === 'wallPlaced') rigC(e.playerId).abilAt = performance.now();
        }
        switch (e.type) {
          case 'throw': audio.throwWhoosh(e.power || 0); break;
          case 'hit':
            audio.hitPoof(); audio.stunSound(); if (e.freeze) audio.freezeChime();
            spawnParticles(e.x, e.y, '#eaf4ff', 7, 40, 100, 2, 4, 0.4); triggerShake(4, 120); break;
          case 'ko':
            audio.hitPoof(); audio.koSound(); spawnParticles(e.x, e.y, '#eaf4ff', 9, 40, 100, 2, 4, 0.5); triggerShake(5, 150); break;
          case 'wallHit': audio.wallThud(); spawnParticles(e.x, e.y, '#cfe2fb', 7, 40, 100, 2, 4, 0.4); break;
          case 'explosion':
            audio.explosionBoom(); triggerShake(10, 250);
            explosions.push({ x: e.x, y: e.y, start: performance.now(), duration: 320, maxR: Sim.EXPLOSION_RADIUS });
            spawnParticles(e.x, e.y, '#ffb347', 16, 80, 200, 3, 6, 0.5); break;
          case 'special':
            if (e.special === 'wall') audio.shieldThud();
            else if (e.special === 'frost' || e.special === 'snipe') audio.uiClick();
            break;
          case 'dash': audio.throwWhoosh(e.kind === 'taram' ? 0.9 : 0.5); break;
          case 'knockback':
            audio.wallThud(); triggerShake(6, 160);
            spawnParticles(e.x, e.y, '#dfeaff', 8, 50, 130, 2, 4, 0.4); break;
          case 'bubblePop':
            audio.shieldThud(); spawnParticles(e.x, e.y, '#bff0d0', 10, 40, 120, 2, 4, 0.4); break;
          case 'bubbleReady': audio.freezeChime(); break;
          case 'frost':
            audio.freezeChime(); spawnParticles(e.x, e.y, '#c8f0ff', 10, 20, 90, 2, 4, 0.5); break;
          case 'obstacleHit':
            spawnParticles(e.x, e.y, '#c58a4a', 6, 30, 110, 2, 4, 0.35); break;
          case 'obstacleBreak':
            audio.wallThud(); triggerShake(6, 180);
            spawnParticles(e.x, e.y, '#b5793e', 16, 50, 170, 2, 5, 0.55); break;
          // --- PvE ---
          case 'enemySpawn':
            spawnParticles(e.x, e.y, '#ffd1dc', 8, 30, 110, 2, 4, 0.35); break;
          case 'waveStart':
            audio.goBeep && audio.goBeep(); triggerShake(5, 160); break;
          case 'waveCleared':
            audio.freezeChime && audio.freezeChime(); break;
          case 'bossPhase':
            audio.explosionBoom && audio.explosionBoom(); triggerShake(12, 320); break;
          case 'contactHit':
            audio.hitPoof && audio.hitPoof(); triggerShake(5, 130);
            spawnParticles(e.x, e.y, '#ffd1dc', 8, 40, 120, 2, 4, 0.4); break;
          case 'objectiveHit':
            audio.wallThud && audio.wallThud(); triggerShake(6, 150);
            spawnParticles(e.x, e.y, '#eaf4ff', 9, 40, 120, 2, 4, 0.45); break;
          case 'levelStart':
            audio.goBeep && audio.goBeep(); break;
          case 'levelRestart':
            audio.defeatChord && audio.defeatChord(); triggerShake(8, 220); break;
          default: break;
        }
      }
    }

    // myTeam — команда своего бойца: по ней аура Фризера красится «своей» или «чужой».
    // Может быть null (наблюдение без своего бойца) — тогда все ауры считаются чужими.
    function drawArena(snap, myTeam) {
      if (arenaCache.index !== snap.arena) buildArena(snap.arena);
      // При тряске экрана края сдвигаются: подложка тем же фоном закрывает щели.
      ctx.fillStyle = '#c3ddf7'; ctx.fillRect(-12, -12, W + 24, H + 24);
      ctx.drawImage(arenaCache.canvas, 0, 0, W, H);
      // «Река»: бегущий блик по льду — сразу читается «скользко».
      var arn = Sim.ARENAS[snap.arena] || Sim.ARENAS[0];
      if (arn.ice) {
        var iy0 = arn.ice.y0, iih = arn.ice.y1 - arn.ice.y0;
        var gx = ((snap.time / 26) % (W + 220)) - 110;
        var sg = ctx.createLinearGradient(gx - 60, 0, gx + 60, 0);
        sg.addColorStop(0, 'rgba(255,255,255,0)'); sg.addColorStop(0.5, 'rgba(255,255,255,0.28)'); sg.addColorStop(1, 'rgba(255,255,255,0)');
        ctx.fillStyle = sg; ctx.fillRect(gx - 60, iy0, 120, iih);
      }
      // Наледь Фризера на земле (под бойцами).
      if (snap.fx) for (var f = 0; f < snap.fx.length; f++) {
        var fx = snap.fx[f], fa = Math.min(1, fx.ttl / 700);
        ctx.globalAlpha = 0.5 * fa;
        var rg = ctx.createRadialGradient(fx.x, fx.y, 2, fx.x, fx.y, fx.r);
        rg.addColorStop(0, '#eaffff'); rg.addColorStop(1, 'rgba(150,220,240,0)');
        ctx.fillStyle = rg; ctx.beginPath(); ctx.arc(fx.x, fx.y, fx.r, 0, Math.PI * 2); ctx.fill();
        ctx.globalAlpha = 0.55 * fa; ctx.strokeStyle = '#bfe8f5'; ctx.lineWidth = 1;
        for (var sp = 0; sp < 6; sp++) { var a = sp * Math.PI / 3; ctx.beginPath(); ctx.moveTo(fx.x, fx.y); ctx.lineTo(fx.x + Math.cos(a) * fx.r * 0.8, fx.y + Math.sin(a) * fx.r * 0.8); ctx.stroke(); }
        ctx.globalAlpha = 1;
      }
      // Пассив Фризера «Стужа»: враги в этом радиусе медленнее и дольше перезаряжаются. Граница
      // зоны — медленно вращающийся пунктир. Пунктир тут уже был и не читался на снегу; чтобы
      // это не повторилось, держим два условия: цвет без globalAlpha (раньше он гасил линию
      // вдвое) и тёмная подложка под ней — фон местами светлее самой линии (снег #c3ddf7,
      // наледь #eaffff, лёд «Реки» #d9f2fb). Заливки нет: на радиусе 110 она накрыла бы
      // четверть арены, а в PvE-волне несколько вражеских Фризеров залили бы поле целиком.
      var dash = 20; // период штриха; при длине окружности ~690 px это около 35 штрихов
      for (var fz = 0; fz < snap.players.length; fz++) {
        var fp = snap.players[fz];
        if (fp.role !== 'Фризер' || fp.koed || fp.hp <= 0) continue;
        var mine = !!myTeam && fp.team === myTeam;
        var R = Sim.FREEZER_AURA_R;
        ctx.save();
        ctx.setLineDash([dash / 2, dash / 2]);
        // Вращение от времени матча, а не от performance.now: в записи и при паузе кадров
        // кольцо не должно дёргаться. ~29 px/с — полный оборот примерно за 24 секунды.
        ctx.lineDashOffset = -(snap.time / 35) % dash;
        ctx.beginPath(); ctx.arc(fp.x, fp.y, R, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(20,45,70,0.30)'; ctx.lineWidth = 2.5; ctx.stroke();
        ctx.beginPath(); ctx.arc(fp.x, fp.y, R, 0, Math.PI * 2);
        ctx.strokeStyle = mine ? 'rgba(120,200,255,0.95)' : 'rgba(255,120,120,0.95)';
        ctx.lineWidth = 1.2; ctx.stroke();
        ctx.restore();
      }
      // Разрушаемые укрытия и стены Щита теперь рисует y-sorted слой в frame().
      // Режим «Защита»: снеговик-объект.
      var pv = snap.pve;
      if (pv && pv.objX != null) {
        var ox = pv.objX, oy = pv.objY, orr = pv.objR || 26;
        var frac = pv.objMaxHp ? Math.max(0, pv.objHp / pv.objMaxHp) : 1;
        ctx.save();
        ctx.beginPath(); ctx.ellipse(ox, oy + orr * 0.9, orr * 1.1, orr * 0.35, 0, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(0,0,0,0.15)'; ctx.fill();
        ctx.fillStyle = frac > 0.33 ? '#ffffff' : '#e6eef5';
        ctx.strokeStyle = '#5a7fa8'; ctx.lineWidth = 2;
        ctx.beginPath(); ctx.arc(ox, oy + orr * 0.7, orr * 0.9, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
        ctx.beginPath(); ctx.arc(ox, oy - orr * 0.2, orr * 0.62, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
        ctx.beginPath(); ctx.arc(ox, oy - orr, orr * 0.42, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
        ctx.fillStyle = '#0b1622';
        ctx.beginPath(); ctx.arc(ox - 5, oy - orr - 2, 2, 0, Math.PI * 2); ctx.arc(ox + 5, oy - orr - 2, 2, 0, Math.PI * 2); ctx.fill();
        ctx.fillStyle = '#ff8c3b';
        ctx.beginPath(); ctx.moveTo(ox, oy - orr + 2); ctx.lineTo(ox + 12, oy - orr + 5); ctx.lineTo(ox, oy - orr + 8); ctx.closePath(); ctx.fill();
        if (frac < 1) { // трещины по урону
          ctx.strokeStyle = 'rgba(40,60,80,0.7)'; ctx.lineWidth = 1.5;
          var cracks = Math.round((1 - frac) * 6);
          for (var ci = 0; ci < cracks; ci++) {
            var ca = ci * 1.7;
            ctx.beginPath(); ctx.moveTo(ox, oy);
            ctx.lineTo(ox + Math.cos(ca) * orr * 0.9, oy + Math.sin(ca) * orr * 0.9);
            ctx.stroke();
          }
        }
        // полоса HP над снеговиком
        var hbw = orr * 2.4;
        ctx.fillStyle = 'rgba(0,0,0,0.55)'; ctx.fillRect(ox - hbw / 2, oy - orr * 2, hbw, 5);
        ctx.fillStyle = frac > 0.33 ? '#7CFFB2' : '#ff5b5b';
        ctx.fillRect(ox - hbw / 2, oy - orr * 2, hbw * frac, 5);
        ctx.restore();
      }
    }
    function drawSnowflakes(dt) {
      for (var i = 0; i < snowflakes.length; i++) {
        var f = snowflakes[i];
        f.y += f.speed * dt; f.x += f.drift * dt;
        if (f.y > H) { f.y = -5; f.x = Math.random() * W; }
        if (f.x < 0) f.x = W; if (f.x > W) f.x = 0;
      }
      ctx.fillStyle = '#ffffff';
      for (var b = 0; b < SNOW_ALPHA.length; b++) {
        ctx.globalAlpha = SNOW_ALPHA[b]; ctx.beginPath();
        for (var j = 0; j < snowflakes.length; j++) {
          var s = snowflakes[j];
          if (s.bucket !== b) continue;
          ctx.moveTo(s.x + s.size, s.y); ctx.arc(s.x, s.y, s.size, 0, Math.PI * 2);
        }
        ctx.fill();
      }
      ctx.globalAlpha = 1;
    }
    var ENEMY_RADII = { core: 15, swarm: 12, tank: 24, roller: 20, boss: 34 };
    var ENEMY_COLORS = { core: '#ff5b5b', swarm: '#ff9ec4', tank: '#b5793e', roller: '#cfe6ff', boss: '#c0263a' };
    function radiusOf(p) {
      if (p.et && ENEMY_RADII[p.et] != null) return ENEMY_RADII[p.et];
      return (Sim.ROLE_STATS[p.role] || { radius: 15 }).radius;
    }

    // Кастомная «модель» под роль поверх круга-тела: силуэтные метки и иконки способностей,
    // чтобы бойцы отличались друг от друга, а не только цветом команды. Всё векторное и мелкое
    // (корпус ~15 px), без теней и градиентов — дёшево для телефонов. Локальные координаты
    // повёрнуты так, что +x смотрит по направлению взгляда (прицел) или движения.
    function drawRoleModel(c2, role, cx, cy, r, ang) {
      var D = '#0b1622', WHT = '#ffffff';
      c2.save();
      c2.translate(cx, cy);
      c2.rotate(ang);
      c2.lineJoin = 'round'; c2.lineCap = 'round';
      if (role === 'Раннер') {
        // лёгкий бегун: повязка поперёк корпуса, хвостики и след скорости назад
        c2.strokeStyle = D; c2.lineWidth = 3;
        c2.beginPath(); c2.moveTo(1, -r + 3); c2.lineTo(-2, r - 3); c2.stroke();
        c2.lineWidth = 2;
        c2.beginPath();
        c2.moveTo(-2, -2); c2.lineTo(-r - 4, -7);
        c2.moveTo(-2, 2); c2.lineTo(-r - 4, 7);
        c2.stroke();
        c2.strokeStyle = 'rgba(255,255,255,0.85)'; c2.lineWidth = 2;
        c2.beginPath();
        c2.moveTo(-r + 1, -5); c2.lineTo(-r - 7, -5);
        c2.moveTo(-r + 1, 0); c2.lineTo(-r - 10, 0);
        c2.moveTo(-r + 1, 5); c2.lineTo(-r - 7, 5);
        c2.stroke();
      } else if (role === 'Танк') {
        // тяжёлая броня: внутренний контур, нагрудный шеврон, заклёпки
        c2.strokeStyle = D; c2.lineWidth = 2.5;
        c2.beginPath(); c2.arc(0, 0, r - 4, 0, Math.PI * 2); c2.stroke();
        c2.lineWidth = 3.5;
        c2.beginPath(); c2.moveTo(r - 9, -7); c2.lineTo(r - 1, 0); c2.lineTo(r - 9, 7); c2.stroke();
        c2.fillStyle = D;
        for (var ti = 0; ti < 4; ti++) {
          var taa = Math.PI / 4 + ti * Math.PI / 2;
          c2.beginPath(); c2.arc(Math.cos(taa) * (r - 4), Math.sin(taa) * (r - 4), 1.8, 0, Math.PI * 2); c2.fill();
        }
      } else if (role === 'Снайпер') {
        // прицел-перекрестие на корпусе и тонкий ствол вперёд
        c2.strokeStyle = D; c2.lineWidth = 2;
        c2.beginPath(); c2.arc(0, 0, r - 5, 0, Math.PI * 2); c2.stroke();
        c2.lineWidth = 1.5;
        c2.beginPath();
        c2.moveTo(-(r - 5), 0); c2.lineTo(r - 5, 0);
        c2.moveTo(0, -(r - 5)); c2.lineTo(0, r - 5);
        c2.stroke();
        c2.lineWidth = 2;
        c2.beginPath(); c2.moveTo(r - 2, 0); c2.lineTo(r + 9, 0); c2.stroke();
      } else if (role === 'Бомбер') {
        // тёмная перевязь через корпус и заклёпки-заряды
        c2.strokeStyle = D; c2.lineWidth = 4;
        c2.beginPath(); c2.moveTo(-r + 2, -4); c2.lineTo(r - 2, 4); c2.stroke();
        c2.fillStyle = D;
        c2.beginPath();
        c2.arc(-3, -5, 1.6, 0, Math.PI * 2);
        c2.arc(3, -1, 1.6, 0, Math.PI * 2);
        c2.arc(-6, 2, 1.6, 0, Math.PI * 2);
        c2.fill();
        // фитиль с искрой — всегда вверх экрана, поэтому гасим поворот модели
        c2.save(); c2.rotate(-ang);
        c2.strokeStyle = D; c2.lineWidth = 1.5;
        c2.beginPath(); c2.moveTo(0, -r + 2); c2.quadraticCurveTo(5, -r - 3, 1, -r - 6); c2.stroke();
        c2.fillStyle = '#ffd166';
        c2.beginPath(); c2.arc(1, -r - 7, 2, 0, Math.PI * 2); c2.fill();
        c2.restore();
      } else if (role === 'Фризер') {
        // ледяной кристалл в центре и шипы инея по ободу
        c2.strokeStyle = WHT; c2.lineWidth = 2;
        for (var si = 0; si < 3; si++) {
          var sa = si * Math.PI / 3;
          c2.beginPath();
          c2.moveTo(-Math.cos(sa) * (r - 4), -Math.sin(sa) * (r - 4));
          c2.lineTo(Math.cos(sa) * (r - 4), Math.sin(sa) * (r - 4));
          c2.stroke();
        }
        c2.strokeStyle = D; c2.lineWidth = 1.5;
        for (var fi = 0; fi < 6; fi++) {
          var fa = fi * Math.PI / 3 + Math.PI / 6;
          c2.beginPath();
          c2.moveTo(Math.cos(fa) * r, Math.sin(fa) * r);
          c2.lineTo(Math.cos(fa) * (r + 4), Math.sin(fa) * (r + 4));
          c2.stroke();
        }
      } else if (role === 'Щит') {
        // отставленная дуга щита со стороны взгляда: двойная линия и хват-скоба
        c2.strokeStyle = D; c2.lineWidth = 3.5;
        c2.beginPath(); c2.arc(0, 0, r + 5, -0.95, 0.95); c2.stroke();
        c2.lineWidth = 1.5;
        c2.beginPath(); c2.arc(0, 0, r + 2, -0.8, 0.8); c2.stroke();
        c2.beginPath(); c2.moveTo(r - 1, 0); c2.lineTo(r + 5, 0); c2.stroke();
      }
      c2.restore();
    }

    // Препятствия арены плюс живые стены Щита в формате sim.js — для проверки луча прицела.
    // Собирается только когда я замахиваюсь (один вызов за кадр).
    function obstaclesOf(snap) {
      var obs = [];
      var src = Sim.ARENAS[snap.arena].obstacles;
      for (var j = 0; j < src.length; j++) if (src[j].hp == null) obs.push(src[j]); // неразрушимые — как есть
      if (snap.destr) for (var d = 0; d < snap.destr.length; d++) {
        var g = snap.destr[d];
        // height берём у самого препятствия арены по индексу g.i: у разрушаемых она разная
        // (16..30), и подставленная «на глаз» врала бы про перелёт снежка над укрытием.
        if (g.hp > 0) obs.push({ type: g.type, x: g.x, y: g.y, w: g.w, h: g.h, r: g.r, height: (src[g.i] && src[g.i].height) || 24 });
      }
      for (var i = 0; i < snap.walls.length; i++) {
        var w = snap.walls[i];
        obs.push({ type: 'rect', x: w.x, y: w.y, w: w.w, h: w.h, height: 20 });
      }
      return obs;
    }

    // Луч прицела: путь снежка считает симуляция (Sim.aimPath), поэтому конец луча — настоящее
    // место падения, с учётом пассива Снайпера и заряженного выстрела. Насыщенным рисуются
    // участки, где снежок идёт у земли и может задеть бойца, полупрозрачным — где он высоко или
    // уже сбит преградой. Буфер переиспользуется: луч рисуется каждый кадр замаха, и свежий
    // массив точек давал GC-паузы на телефонах.
    var AIM_BLUE = 'rgba(74,168,255,0.95)';      // долетит
    var AIM_BLUE_DIM = 'rgba(74,168,255,0.30)';  // высоко над землёй или уже после преграды
    var AIM_RED = 'rgba(255,80,80,0.9)';         // упрётся в препятствие
    var AIM_RED_DIM = 'rgba(255,80,80,0.32)';
    var aimBuf = {};
    function drawAim(snap, p, aimX, aimY, power) {
      var path = Sim.aimPath(obstaclesOf(snap), p, aimX, aimY, power, aimBuf);
      var col = path.blocked ? AIM_RED : AIM_BLUE;
      // Два stroke на кадр: сперва тусклый слой, потом насыщенный поверх него.
      ctx.lineWidth = 2; ctx.strokeStyle = path.blocked ? AIM_RED_DIM : AIM_BLUE_DIM;
      strokeAimRun(path, false);
      ctx.lineWidth = 3; ctx.strokeStyle = col;
      strokeAimRun(path, true);
      ctx.strokeStyle = col; ctx.fillStyle = col;
      ctx.beginPath(); ctx.arc(path.endX, path.endY, path.blocked ? 6 : 12, 0, Math.PI * 2);
      ctx.lineWidth = 1.5; ctx.stroke();
      ctx.beginPath(); ctx.arc(path.endX, path.endY, 2.5, 0, Math.PI * 2); ctx.fill();
    }
    /** Одним stroke обвести все отрезки пути, попадающие (want=true) или нет. */
    function strokeAimRun(path, want) {
      ctx.beginPath();
      var open = false;
      for (var i = 1; i < path.n; i++) {
        var a = path.pts[i - 1], b = path.pts[i];
        if ((a.hit && b.hit) !== want) { open = false; continue; }
        if (!open) { ctx.moveTo(a.x, a.y); open = true; }
        ctx.lineTo(b.x, b.y);
      }
      ctx.stroke();
    }

    // Мохнатый силуэт: рваная «звезда» из чередующихся длинных/коротких лучей (клочья шерсти).
    function furPath(ctx, cx, cy, rx, ry, n, spike) {
      ctx.beginPath();
      for (var i = 0; i < n; i++) {
        var a = i / n * Math.PI * 2 - Math.PI / 2;
        var m = (i % 2) ? (1 + spike) : (1 - spike * 0.25);
        var x = cx + Math.cos(a) * rx * m, y = cy + Math.sin(a) * ry * m;
        i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
      }
      ctx.closePath();
    }
    function furBlob(ctx, x, y, rx, ry, fill, ink) {
      furPath(ctx, x, y, rx, ry, 12, 0.18);
      ctx.fillStyle = fill; ctx.fill(); ctx.lineWidth = 3; ctx.strokeStyle = ink; ctx.stroke();
    }
    function yetiArm(ctx, x, y, ang, len, fill, ink) {
      ctx.save(); ctx.translate(x, y); ctx.rotate(ang); ctx.lineCap = 'round';
      ctx.strokeStyle = ink; ctx.lineWidth = len * 0.5 + 4;
      ctx.beginPath(); ctx.moveTo(0, 0); ctx.lineTo(0, len); ctx.stroke();
      ctx.strokeStyle = fill; ctx.lineWidth = len * 0.5;
      ctx.beginPath(); ctx.moveTo(0, 0); ctx.lineTo(0, len); ctx.stroke();
      furBlob(ctx, 0, len + len * 0.12, len * 0.42, len * 0.42, fill, ink); // кулак
      ctx.restore();
    }

    // Снежные големы PvE: roller — ком, boss — чёрный лёд; tank («Йети») — мохнатый монстр.
    var TAU2 = Math.PI * 2;
    function drawGolem(ctx, p, snap) {
      var rc = rigC(p.id);
      var r = radiusOf(p), kind = p.et, boss = kind === 'boss';
      var dead = p.koed ? Math.min(1, (snap.time - (p.koAt || snap.time)) / 4000) : -1;
      var now = dead >= 0 ? 0 : performance.now() + rc.ph * 3; // труп не дёргается
      var hitAge = snap.time - p.hitAt;
      var hitT = (dead < 0 && hitAge >= 0 && hitAge < 260) ? hitAge / 260 : -1;
      var recoil = hitT >= 0 ? Math.sin(hitT * 34) * (1 - hitT) * 5 : 0;
      var feet = p.y + r;
      var MAT = boss ? { fill: '#242a36', hi: '#3b4560', ink: '#0a0d14', vein: true }
                     : { fill: '#5a5a62', hi: '#767680', ink: '#1c1c22', vein: false };
      if (dead >= 0) { // труп — обесцвеченный, светлее: сразу видно, что это не живой
        MAT = { fill: boss ? '#575d6b' : '#7b7d88', hi: boss ? '#6d7488' : '#9498a2', ink: '#2b2f38', vein: false };
      }
      ctx.save();
      ctx.translate(p.x + recoil, feet);
      ctx.fillStyle = 'rgba(20,40,70,0.18)';
      ctx.beginPath(); ctx.ellipse(0, -2, r * 1.05, r * 0.34, 0, 0, TAU2); ctx.fill();
      if (dead >= 0) { // труп: оседает, заваливается, тает за 4 с (и уже стартует прозрачным)
        ctx.translate(0, dead * 6); ctx.rotate(dead * 0.5);
        ctx.globalAlpha *= Math.max(0, 0.6 - dead * 0.6);
      }

      if (kind === 'roller') {
        var dxr = rc.prevX == null ? 0 : p.x - rc.prevX; rc.prevX = p.x;
        rc.spin = (rc.spin || 0) + dxr * 0.06;
        var R = r; ctx.translate(0, -R);
        ctx.beginPath(); ctx.arc(0, 0, R, 0, TAU2);
        var rg = ctx.createRadialGradient(-R * 0.3, -R * 0.35, R * 0.2, 0, 0, R);
        rg.addColorStop(0, MAT.hi); rg.addColorStop(1, MAT.fill);
        ctx.fillStyle = rg; ctx.fill(); ctx.lineWidth = 3.5; ctx.strokeStyle = MAT.ink; ctx.stroke();
        ctx.save(); ctx.beginPath(); ctx.arc(0, 0, R, 0, TAU2); ctx.clip(); ctx.rotate(rc.spin);
        ctx.strokeStyle = 'rgba(255,255,255,0.3)'; ctx.lineWidth = 2.5;
        for (var a1 = 0; a1 < 3; a1++) { var an = a1 * TAU2 / 3; ctx.beginPath(); ctx.moveTo(Math.cos(an) * R, Math.sin(an) * R); ctx.lineTo(-Math.cos(an) * R, -Math.sin(an) * R); ctx.stroke(); }
        ctx.strokeStyle = MAT.ink; ctx.lineWidth = 2;
        for (var b1 = 0; b1 < 6; b1++) { var bb = b1 * TAU2 / 6 + 0.4; ctx.beginPath(); ctx.moveTo(Math.cos(bb) * R * 0.35, Math.sin(bb) * R * 0.35); ctx.lineTo(Math.cos(bb) * R * 0.95, Math.sin(bb) * R * 0.95); ctx.stroke(); }
        ctx.restore();
        ctx.fillStyle = MAT.ink;
        ctx.beginPath(); ctx.arc(-R * 0.3, -R * 0.12, 3, 0, TAU2); ctx.arc(R * 0.3, -R * 0.12, 3, 0, TAU2); ctx.fill();
        ctx.strokeStyle = MAT.ink; ctx.lineWidth = 3; ctx.lineCap = 'round';
        ctx.beginPath(); ctx.moveTo(-R * 0.38, R * 0.28); ctx.quadraticCurveTo(0, R * 0.02, R * 0.38, R * 0.28); ctx.stroke();
        if (Math.abs(dxr) > 0.4) {
          ctx.fillStyle = 'rgba(255,255,255,0.5)';
          for (var t1 = 0; t1 < 4; t1++) { ctx.beginPath(); ctx.arc((dxr > 0 ? -1 : 1) * (R + 6 + t1 * 7), R * 0.55, 3 - t1 * 0.5, 0, TAU2); ctx.fill(); }
        }
      } else if (kind === 'tank') {
        // «Йети» — мохнатый снежный монстр: сгорбленный, лапы до земли, злая морда.
        var dxy = rc.prevX == null ? 0 : p.x - rc.prevX; rc.prevX = p.x;
        var FUR = dead >= 0 ? '#c2cad2' : '#eef4f8', FINK = dead >= 0 ? '#4a525c' : '#26323e';
        var sway = Math.sin(now * 0.003 + rc.ph) * 0.05 + (dxy > 0.4 ? 0.05 : dxy < -0.4 ? -0.05 : 0);
        var breathe = Math.sin(now * 0.004 + rc.ph) * r * 0.03;
        ctx.rotate(sway);
        // ступни-клочья
        furBlob(ctx, -r * 0.44, -r * 0.5, r * 0.5, r * 0.62, FUR, FINK);
        furBlob(ctx, r * 0.44, -r * 0.5, r * 0.5, r * 0.62, FUR, FINK);
        var bcx = 0, bcy = -r * 1.32 + breathe, bw = r * 1.12, bh = r * 1.24;
        // руки до земли (задняя рука за корпусом)
        var armSw = Math.sin(now * 0.004 + rc.ph) * 0.12;
        yetiArm(ctx, -bw * 0.72, bcy - bh * 0.05, -0.28 + armSw, r * 1.35, FUR, FINK);
        // корпус
        ctx.save();
        furPath(ctx, bcx, bcy, bw, bh, 20, 0.14);
        var bg = ctx.createRadialGradient(bcx - bw * 0.3, bcy - bh * 0.4, r * 0.3, bcx, bcy, bw * 1.35);
        bg.addColorStop(0, '#ffffff'); bg.addColorStop(1, FUR);
        ctx.fillStyle = bg; ctx.fill();
        ctx.lineWidth = 3.5; ctx.strokeStyle = FINK; ctx.lineJoin = 'round'; ctx.stroke();
        furPath(ctx, bcx, bcy, bw, bh, 20, 0.14); ctx.clip();
        ctx.fillStyle = 'rgba(150,180,200,0.45)';
        ctx.beginPath(); ctx.ellipse(bcx, bcy + bh * 0.55, bw * 1.15, bh * 0.5, 0, 0, TAU2); ctx.fill();
        ctx.restore();
        yetiArm(ctx, bw * 0.72, bcy - bh * 0.05, 0.28 - armSw, r * 1.35, FUR, FINK);
        // голова, вжата в плечи
        var hy = bcy - bh * 0.66, hr = r * 0.6;
        furPath(ctx, 0, hy, hr * 1.06, hr, 12, 0.12);
        ctx.fillStyle = FUR; ctx.fill(); ctx.lineWidth = 3.5; ctx.strokeStyle = FINK; ctx.stroke();
        // ледяная корка-надбровье
        ctx.fillStyle = '#cfe8f2'; ctx.strokeStyle = FINK; ctx.lineWidth = 2; ctx.lineJoin = 'round';
        ctx.beginPath();
        ctx.moveTo(-hr * 0.98, hy - hr * 0.12); ctx.lineTo(hr * 0.98, hy - hr * 0.12);
        ctx.lineTo(hr * 0.66, hy - hr * 0.62); ctx.lineTo(hr * 0.12, hy - hr * 0.28);
        ctx.lineTo(-hr * 0.3, hy - hr * 0.62); ctx.lineTo(-hr * 0.74, hy - hr * 0.3);
        ctx.closePath(); ctx.fill(); ctx.stroke();
        // тёмная маска глаз
        ctx.fillStyle = 'rgba(18,28,38,0.5)';
        ctx.beginPath(); ctx.ellipse(0, hy + hr * 0.08, hr * 0.82, hr * 0.46, 0, 0, TAU2); ctx.fill();
        // злые светящиеся глаза
        ctx.fillStyle = '#bfefff';
        if (dead < 0) { ctx.shadowColor = '#8fdcff'; ctx.shadowBlur = 6; }
        ctx.beginPath(); ctx.ellipse(-hr * 0.4, hy + hr * 0.08, hr * 0.22, hr * 0.16, 0.3, 0, TAU2);
        ctx.ellipse(hr * 0.4, hy + hr * 0.08, hr * 0.22, hr * 0.16, -0.3, 0, TAU2); ctx.fill();
        ctx.shadowBlur = 0;
        ctx.fillStyle = '#12303c';
        ctx.beginPath(); ctx.arc(-hr * 0.36, hy + hr * 0.09, hr * 0.09, 0, TAU2); ctx.arc(hr * 0.44, hy + hr * 0.09, hr * 0.09, 0, TAU2); ctx.fill();
        // разинутая пасть с клыками
        ctx.fillStyle = '#3a2230'; ctx.strokeStyle = FINK; ctx.lineWidth = 2;
        ctx.beginPath(); ctx.ellipse(0, hy + hr * 0.66, hr * 0.5, hr * 0.32, 0, 0, TAU2); ctx.fill(); ctx.stroke();
        ctx.fillStyle = '#fff';
        for (var fg = -1; fg <= 1; fg += 2) {
          ctx.beginPath();
          ctx.moveTo(fg * hr * 0.3 - hr * 0.11, hy + hr * 0.52);
          ctx.lineTo(fg * hr * 0.3 + hr * 0.11, hy + hr * 0.52);
          ctx.lineTo(fg * hr * 0.3, hy + hr * 0.86); ctx.closePath(); ctx.fill();
        }
        if (hitT >= 0) {
          ctx.globalAlpha = 0.5 * (1 - hitT); ctx.fillStyle = '#fff';
          furPath(ctx, bcx, bcy, bw * 1.05, bh * 1.05, 20, 0.14); ctx.fill(); ctx.globalAlpha = 1;
        }
      } else {
        rc.prevX = p.x;
        var n = boss ? 4 : 3;
        var sz = boss ? [r * 1.05, r * 0.85, r * 0.62, r * 0.42] : [r * 1.0, r * 0.72, r * 0.5];
        var lean = Math.sin(now * 0.0025 + rc.ph) * r * 0.06, yy = 0;
        for (var i = 0; i < n; i++) {
          var rr = sz[i]; yy -= rr; var bx = lean * (i / n);
          ctx.beginPath(); ctx.arc(bx, yy, rr, 0, TAU2);
          var gg = ctx.createRadialGradient(bx - rr * 0.35, yy - rr * 0.4, rr * 0.2, bx, yy, rr);
          gg.addColorStop(0, MAT.hi); gg.addColorStop(1, MAT.fill);
          ctx.fillStyle = gg; ctx.fill(); ctx.lineWidth = 3.5; ctx.strokeStyle = MAT.ink; ctx.stroke();
          if (MAT.vein) {
            ctx.strokeStyle = 'rgba(74,123,216,0.55)'; ctx.lineWidth = 1.5;
            for (var v = 0; v < 3; v++) { var va = v * 2.1 + i; ctx.beginPath(); ctx.moveTo(bx + Math.cos(va) * rr * 0.2, yy + Math.sin(va) * rr * 0.2); ctx.lineTo(bx + Math.cos(va + 0.6) * rr * 0.85, yy + Math.sin(va + 0.6) * rr * 0.85); ctx.stroke(); }
          }
          yy -= rr - rr * 0.35;
        }
        var headY = yy + sz[n - 1] * 0.7, hbx = lean * 0.9;
        var rage = boss && p.bph === 2;
        ctx.fillStyle = rage ? '#ff3b3b' : MAT.ink;
        if (rage) { ctx.shadowColor = '#ff3b3b'; ctx.shadowBlur = 7; }
        ctx.beginPath(); ctx.arc(hbx - 5, headY, 3, 0, TAU2); ctx.arc(hbx + 5, headY, 3, 0, TAU2); ctx.fill();
        ctx.shadowBlur = 0;
        ctx.strokeStyle = MAT.ink; ctx.lineWidth = 2.5; ctx.lineCap = 'round';
        ctx.beginPath(); ctx.moveTo(hbx - 6, headY + 8); ctx.lineTo(hbx - 2, headY + 6); ctx.lineTo(hbx + 2, headY + 9); ctx.lineTo(hbx + 6, headY + 6); ctx.stroke();
        var armY = -(sz[0] * 0.65 + sz[1]), aw = Math.sin(now * 0.004 + rc.ph) * 0.18;
        ctx.strokeStyle = '#5a4230'; ctx.lineWidth = 3.5;
        ctx.beginPath();
        ctx.moveTo(-sz[1] * 0.7, armY); ctx.lineTo(-sz[1] * 0.7 - 15, armY - 6 + aw * 12);
        ctx.moveTo(sz[1] * 0.7, armY); ctx.lineTo(sz[1] * 0.7 + 15, armY - 6 - aw * 12);
        ctx.stroke();
        if (boss) {
          var crY = yy - 1;
          ctx.fillStyle = '#bfe6f5'; ctx.strokeStyle = MAT.ink; ctx.lineWidth = 2;
          for (var c = -1; c <= 1; c++) { var chh = c === 0 ? 17 : 11; ctx.beginPath(); ctx.moveTo(hbx + c * 8 - 4, crY); ctx.lineTo(hbx + c * 8, crY - chh); ctx.lineTo(hbx + c * 8 + 4, crY); ctx.closePath(); ctx.fill(); ctx.stroke(); }
          if (rage) {
            ctx.strokeStyle = 'rgba(255,90,60,0.85)'; ctx.lineWidth = 2; ctx.shadowColor = '#ff5a3c'; ctx.shadowBlur = 5;
            for (var cr = 0; cr < 5; cr++) { var cra = cr * 1.4; ctx.beginPath(); ctx.moveTo(0, -r * 0.9); ctx.lineTo(Math.cos(cra) * r * 0.8, -r * 0.9 + Math.sin(cra) * r * 0.8); ctx.stroke(); }
            ctx.shadowBlur = 0; ctx.fillStyle = 'rgba(180,210,240,0.7)';
            for (var sh = 0; sh < 4; sh++) { var sa = now * 0.001 + sh * TAU2 / 4; ctx.beginPath(); ctx.arc(Math.cos(sa) * (r + 16), -r + Math.sin(sa) * (r + 16) * 0.5, 3, 0, TAU2); ctx.fill(); }
          }
        }
      }
      if (hitT >= 0 && kind !== 'tank') {
        ctx.globalAlpha = 0.5 * (1 - hitT); ctx.fillStyle = '#fff';
        ctx.beginPath(); ctx.arc(0, -r, r * 1.2, 0, TAU2); ctx.fill(); ctx.globalAlpha = 1;
        ctx.fillStyle = MAT.fill; ctx.strokeStyle = MAT.ink; ctx.lineWidth = 2;
        var chd = hitT * 22;
        ctx.beginPath(); ctx.arc(r * 0.5 + chd, -r * 1.3 - chd * 0.5, 5 * (1 - hitT * 0.5), 0, TAU2); ctx.fill(); ctx.stroke();
      }
      ctx.restore();
    }

    function drawCharacter(snap, p, isMe, local, myTeam) {
      var r = radiusOf(p), vx = p.x, vy = p.y;
      var charging = isMe && local && local.charging ? true : p.charging;
      var power = isMe && local && local.charging ? local.power : p.power;
      var aimX = isMe && local && local.charging ? local.aimX : p.aimX;
      var aimY = isMe && local && local.charging ? local.aimY : p.aimY;
      var alive = p.hp > 0 && !p.koed;
      // Труп истаял (см. koFade / drawGolem, 4 с) — кадр на него не тратим. У союзника в PvE
      // (p.lives не null) действует то же правило, что у врага (p.et).
      if (p.koed && (p.et || p.lives != null) && (snap.time - (p.koAt || snap.time)) > 4400) return;
      if (p.moving && alive) vy -= Math.abs(Math.sin(p.anim)) * 2;
      if (charging) {
        var dx = aimX - p.x, dy = aimY - p.y, d = Math.hypot(dx, dy) || 1;
        vx -= (dx / d) * power * 4; vy -= (dy / d) * power * 4;
      }
      var color = p.team === 'A' ? '#4aa8ff' : (p.et && ENEMY_COLORS[p.et] ? ENEMY_COLORS[p.et] : '#ff5b5b');
      var flashing = (snap.time - p.hitAt) < 150;

      var golemKind = (p.et === 'tank' || p.et === 'roller' || p.et === 'boss') ? p.et : null;
      var useRig = Rig && !golemKind && Rig.GEAR[p.role]; // игроки + порченые core/swarm

      ctx.save();
      if (p.koed && !useRig && !golemKind) {
        var isDissolve = !!p.et || p.lives != null;
        var progress = Math.min(1, (snap.time - p.koAt) / (isDissolve ? 4000 : Sim.KO_ANIM_MS));
        ctx.globalAlpha = 1 - (isDissolve ? 0.6 : 0.22) * progress; // враг/союзник PvE тает, боец PvP только тускнеет
        ctx.translate(p.x, p.y); ctx.rotate(progress * Math.PI / 2.2); ctx.translate(-p.x, -p.y);
      }
      ctx.beginPath(); ctx.ellipse(p.x, p.y + r * 0.6, r * 0.9, r * 0.35, 0, 0, Math.PI * 2);
      // Тень тает вместе с трупом (тело гасит koFade/drawGolem) — у врага и у союзника в PvE.
      var shA = (p.koed && (p.et || p.lives != null)) ? 0.15 * Math.max(0, 1 - (snap.time - (p.koAt || snap.time)) / 4000) : 0.15;
      ctx.fillStyle = 'rgba(0,0,0,' + shA + ')'; ctx.fill();

      var faceAng = charging ? Math.atan2(aimY - p.y, aimX - p.x) : (p.team === 'A' ? 0 : Math.PI);

      // Рывок/таран: призрачный след и приподнятая прозрачность корпуса.
      if (p.dash || p.iframe) {
        var bdx = Math.cos(faceAng), bdy = Math.sin(faceAng);
        for (var gi = 1; gi <= 3; gi++) {
          ctx.globalAlpha = 0.16 * (4 - gi);
          ctx.beginPath(); ctx.arc(vx - bdx * gi * 6, vy - bdy * gi * 6, r * (1 - gi * 0.12), 0, Math.PI * 2);
          ctx.fillStyle = color; ctx.fill();
        }
        ctx.globalAlpha = 1;
      }

      var slowIced = false;
      if (p.slow) {
        var _c = slowCause(snap, p);
        slowIced = _c === 'frost' || _c === 'aura';
      }

      if (golemKind) {
        try { drawGolem(ctx, p, snap); }
        catch (e) {
          if (!drawGolem._warned) { console.error('drawGolem failed:', e); drawGolem._warned = 1; }
          ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2); ctx.fillStyle = color; ctx.fill(); ctx.strokeStyle = '#0b1622'; ctx.lineWidth = 2; ctx.stroke();
        }
        if (p.slow) { ctx.fillStyle = 'rgba(150,216,255,0.35)'; ctx.beginPath(); ctx.arc(vx, vy, r * 1.1, 0, Math.PI * 2); ctx.fill(); }
      } else if (useRig) {
        try { Rig.drawFighter(ctx, p.x, p.y + r * 0.9, r, rigStateFor(snap, p, isMe, local)); }
        catch (e) { Rig = null; drawRoleModel(ctx, p.role, vx, vy, r, faceAng); }
        if (p.slow) {
          ctx.fillStyle = slowIced ? 'rgba(150,216,255,0.42)' : 'rgba(150,216,255,0.30)';
          ctx.beginPath(); ctx.arc(vx, vy, r * 1.1, 0, Math.PI * 2); ctx.fill();
        }
      } else {
        ctx.save();
        if (p.iframe) ctx.globalAlpha *= 0.6;
        ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2);
        ctx.fillStyle = flashing ? '#ffffff' : (p.stun > 0 ? '#888' : color);
        ctx.fill(); ctx.lineWidth = 2; ctx.strokeStyle = '#0b1622'; ctx.stroke();
        // Замедление: наледь и аура Фризера — это ледяная корка со снежинками, лёд арены —
        // прежний слабый тон. Различать важно: иначе в уроке Фризера не понять, что сработало.
        if (p.slow) {
          ctx.fillStyle = slowIced ? 'rgba(150,216,255,0.55)' : 'rgba(150,216,255,0.42)';
          ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2); ctx.fill();
          if (slowIced) {
            ctx.strokeStyle = 'rgba(200,240,255,0.9)'; ctx.lineWidth = 1.5;
            ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2); ctx.stroke();
            ctx.fillStyle = 'rgba(235,250,255,0.95)';
            for (var fl = 0; fl < 3; fl++) {
              var fa2 = snap.time / 420 + fl * Math.PI * 2 / 3;
              ctx.beginPath(); ctx.arc(vx + Math.cos(fa2) * (r + 5), vy + Math.sin(fa2) * (r + 5) * 0.6, 1.6, 0, Math.PI * 2);
              ctx.fill();
            }
          }
        }
        ctx.restore();

        drawRoleModel(ctx, p.role, vx, vy, r, faceAng);
      }

      // PvE-босс: корона рисуется в drawGolem; здесь только кольцо ярости в фазе 2.
      if (p.et === 'boss' && !golemKind) {
        ctx.save();
        ctx.translate(vx, vy);
        ctx.fillStyle = '#ffd24a'; ctx.strokeStyle = '#0b1622'; ctx.lineWidth = 1.5;
        ctx.beginPath();
        for (var sp2 = 0; sp2 < 5; sp2++) {
          var aa = -Math.PI / 2 + sp2 * (Math.PI * 2 / 5);
          ctx.lineTo(Math.cos(aa) * (r + 6), Math.sin(aa) * (r + 6));
          ctx.lineTo(Math.cos(aa + Math.PI / 5) * (r + 1), Math.sin(aa + Math.PI / 5) * (r + 1));
        }
        ctx.closePath(); ctx.fill(); ctx.stroke();
        ctx.restore();
      }
      if (p.et === 'boss' && p.bph === 2) {
        ctx.save();
        ctx.globalAlpha = 0.35 + 0.15 * Math.sin(snap.time / 90);
        ctx.beginPath(); ctx.arc(vx, vy, r + 14, 0, Math.PI * 2);
        ctx.strokeStyle = '#ff3b3b'; ctx.lineWidth = 4; ctx.stroke();
        ctx.restore();
      }
      // Заряженная способность: цветной ореол вокруг бойца.
      if (p.armed) {
        var ac = p.armed === 'explosive' ? '#ffb347' : (p.armed === 'frost' ? '#9fe8ff' : '#c9a6ff');
        ctx.beginPath(); ctx.arc(vx, vy, r + 3.5, 0, Math.PI * 2);
        ctx.strokeStyle = ac; ctx.lineWidth = 2.5; ctx.globalAlpha = 0.85; ctx.stroke(); ctx.globalAlpha = 1;
      }
      // Щит, пассив «Закалка»: полное кольцо — пузырь готов, гасит следующее попадание целиком.
      // После хлопка кольцо заполняется по кругу за 12 с — так видно, когда защита вернётся, и
      // своя, и чужая (знание о чужом Щите — часть тактики). Радиус r+4 отличает его от дуги
      // перезарядки (r+10) и от ореола заряженной способности (r+3.5).
      if (p.bubble) {
        var pulse = 0.75 + 0.25 * Math.sin(snap.time / 300);
        ctx.beginPath(); ctx.arc(vx, vy, r + 4, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(150,240,190,0.9)'; ctx.lineWidth = 2; ctx.stroke();
        ctx.globalAlpha = pulse * 0.5;
        ctx.beginPath(); ctx.arc(vx, vy, r + 6, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(150,240,190,0.8)'; ctx.lineWidth = 2; ctx.stroke();
        ctx.globalAlpha = 1;
        ctx.beginPath(); ctx.arc(vx, vy, r + 4, -0.6, 0.5);
        ctx.strokeStyle = 'rgba(255,255,255,0.9)'; ctx.lineWidth = 1.5; ctx.stroke();
      } else if (p.role === 'Щит' && p.bb > 0 && alive) {
        // Дугу отката рисуем тёмной дорожкой и почти белой заливкой, а не зелёным: тело Щита
        // само светло-зелёное (#b8f0c8), и зелёная дуга на нём не читалась бы.
        ctx.beginPath(); ctx.arc(vx, vy, r + 4, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(11,22,34,0.35)'; ctx.lineWidth = 2.5; ctx.stroke();
        var done = 1 - p.bb;
        if (done > 0) {
          ctx.beginPath(); ctx.arc(vx, vy, r + 4, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * done);
          ctx.strokeStyle = 'rgba(245,255,250,0.95)'; ctx.lineWidth = 2.5; ctx.lineCap = 'round';
          ctx.stroke(); ctx.lineCap = 'butt';
        }
      }

      if (isMe) { ctx.beginPath(); ctx.arc(vx, vy, r + 6.5, 0, Math.PI * 2); ctx.strokeStyle = '#ffe066'; ctx.lineWidth = 2; ctx.stroke(); }

      // Перезарядка выстрела: убывающая дуга вокруг своего бойца (полная сразу после броска).
      // Зелёная — выстрел уже поставлен в очередь: замах начнётся сам, как только дуга исчезнет.
      if (isMe && p.rl > 0) {
        var rr = r + 10;
        ctx.beginPath(); ctx.arc(vx, vy, rr, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(255,255,255,0.15)'; ctx.lineWidth = 3; ctx.stroke();
        ctx.beginPath(); ctx.arc(vx, vy, rr, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * p.rl);
        ctx.strokeStyle = local && local.pending ? '#7CFFB2' : '#ffcf5b';
        ctx.lineWidth = 3; ctx.lineCap = 'round'; ctx.stroke();
        ctx.lineCap = 'butt';
      }

      var lb = labelOf(p, isMe, myTeam);
      // Подпись встаёт НАД макушкой, а не над центром бойца: модели разной высоты (риг втрое
      // выше прежнего кружка, Йети и босс ещё выше), и от центра ник с полоской ложились на лицо.
      var above = golemKind === 'boss' ? r * 4.6 : golemKind === 'tank' ? r * 2.5
                : golemKind === 'roller' ? r * 1.2
                : (useRig && Rig.topOf ? Rig.topOf(r, p.role, RIG_SCALE) - r * 0.9 : r + 4);
      ctx.drawImage(lb.canvas, Math.round(p.x - lb.w / 2),
        Math.max(2, Math.round(p.y - above - 4 - lb.h)), lb.w, lb.h);
      ctx.restore();

      if (charging) {
        if (isMe) drawAim(snap, p, aimX, aimY, power);
        var bw = 40;
        ctx.fillStyle = '#0b1622'; ctx.fillRect(p.x - bw / 2, p.y - r - 20, bw, 6);
        ctx.fillStyle = p.special ? '#b478ff' : (power > 0.7 ? '#ff5b5b' : '#ffd166');
        ctx.fillRect(p.x - bw / 2, p.y - r - 20, bw * power, 6);
      }
    }
    // Единый y-sorted проход: «высокие» объекты (укрытия, ёлки, разрушаемые, стены, бойцы,
    // мобы) рисуются в порядке нижней кромки — кто ниже по экрану, тот ближе и рисуется поверх.
    function drawTallLayer(snap, meId, local, myTeam) {
      var arena = Sim.ARENAS[snap.arena] || Sim.ARENAS[0], items = [];
      for (var i = 0; i < arena.obstacles.length; i++) {
        var ob = arena.obstacles[i];
        if (ob.hp != null) continue;
        if (ob.type === 'rect') items.push({ y: ob.y + ob.h / 2, f: drawCoverBlock, a: ob });
        else items.push({ y: ob.y + 12, f: drawTree, a: ob });
      }
      if (snap.destr) for (var d = 0; d < snap.destr.length; d++) {
        var dd = snap.destr[d];
        if (dd.hp > 0) items.push({ y: dd.y + (dd.h ? dd.h / 2 : dd.r), f: drawDestructible, a: dd });
      }
      for (var w = 0; w < snap.walls.length; w++) items.push({ y: snap.walls[w].y + snap.walls[w].h / 2, f: drawWall, a: snap.walls[w] });
      for (var p = 0; p < snap.players.length; p++) {
        var pl = snap.players[p];
        items.push({ y: pl.y + radiusOf(pl) * 0.9, pl: pl, isMe: pl.id === meId });
      }
      items.sort(function (u, v) { return u.y - v.y; });
      for (var k = 0; k < items.length; k++) {
        var it = items[k];
        if (it.pl) drawCharacter(snap, it.pl, it.isMe, it.isMe ? local : null, myTeam);
        else it.f(it.a);
      }
    }
    function drawSnowball(s) {
      var tr = trails[s.id];
      if (!tr) tr = trails[s.id] = [];
      tr.push({ x: s.x, y: s.y, z: s.z }); if (tr.length > 5) tr.shift();
      for (var i = 0; i < tr.length; i++) {
        var t = tr[i], a = (i + 1) / tr.length * 0.4;
        ctx.globalAlpha = a; ctx.beginPath(); ctx.arc(t.x, t.y - t.z, s.r * 0.6, 0, Math.PI * 2);
        ctx.fillStyle = s.ex ? '#ffb347' : '#ffffff'; ctx.fill();
      }
      ctx.globalAlpha = 1;
      var drawY = s.y - s.z;
      ctx.beginPath(); ctx.ellipse(s.x, s.y, 5, 2, 0, 0, Math.PI * 2); ctx.fillStyle = 'rgba(0,0,0,0.2)'; ctx.fill();
      if (s.sn && tr.length >= 2) { // прицельный выстрел: короткий яркий след по направлению полёта
        var t0 = tr[0], tdx = s.x - t0.x, tdy = s.y - t0.y, tl = Math.hypot(tdx, tdy) || 1;
        ctx.strokeStyle = 'rgba(201,166,255,0.8)'; ctx.lineWidth = s.r * 1.4; ctx.lineCap = 'round';
        ctx.beginPath(); ctx.moveTo(s.x, drawY); ctx.lineTo(s.x - tdx / tl * 22, drawY - tdy / tl * 22); ctx.stroke();
      }
      ctx.beginPath(); ctx.arc(s.x, drawY, s.r, 0, Math.PI * 2);
      ctx.fillStyle = s.ex ? '#ffb347' : (s.fr ? '#9fe8ff' : (s.sn ? '#e6d8ff' : '#ffffff')); ctx.fill();
      ctx.strokeStyle = '#a9c2e0'; ctx.stroke();
    }
    function drawExplosions(now) {
      explosions = explosions.filter(function (e) { return now - e.start < e.duration; });
      for (var i = 0; i < explosions.length; i++) {
        var e = explosions[i], progress = (now - e.start) / e.duration, r = e.maxR * progress;
        ctx.beginPath(); ctx.arc(e.x, e.y, r, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(255,150,50,' + (1 - progress) + ')'; ctx.lineWidth = 4; ctx.stroke();
      }
    }
    function drawParticles(dt) {
      for (var i = particles.length - 1; i >= 0; i--) {
        var p = particles[i];
        p.x += p.vx * dt; p.y += p.vy * dt; p.vy += 250 * dt; p.life -= dt;
        if (p.life <= 0) { particles.splice(i, 1); continue; }
        ctx.globalAlpha = Math.max(0, p.life / p.maxLife);
        ctx.fillStyle = p.color; ctx.beginPath(); ctx.arc(p.x, p.y, p.size, 0, Math.PI * 2); ctx.fill();
      }
      ctx.globalAlpha = 1;
    }

    /**
     * Нарисовать кадр.
     * snap — снапшот sim.js (возможно интерполированный), meId — свой боец,
     * local — локальное состояние замаха {charging, power, aimX, aimY} для мгновенного отклика.
     */
    function frame(snap, meId, local, nameMap, rankMap) {
      names = nameMap || null;
      ranks = rankMap || null;
      var now = performance.now();
      var dt = Math.min((now - lastFrame) / 1000, 0.05);
      lastFrame = now;
      applyRS(); // подстроить внутреннее разрешение под текущий размер канваса
      var shakeX = 0, shakeY = 0;
      if (now < shake.until) { var rem = (shake.until - now) / shake.total; shakeX = (Math.random() - 0.5) * shake.mag * rem; shakeY = (Math.random() - 0.5) * shake.mag * rem; }
      ctx.setTransform(RS, 0, 0, RS, 0, 0);
      ctx.save(); ctx.translate(shakeX, shakeY);
      var myTeam = null;
      for (var t = 0; t < snap.players.length; t++) if (snap.players[t].id === meId) { myTeam = snap.players[t].team; break; }
      drawArena(snap, myTeam); drawSnowflakes(dt);
      if (marks.length) drawMarks(now);
      drawTallLayer(snap, meId, local, myTeam);
      var seen = {};
      for (var k = 0; k < snap.balls.length; k++) { drawSnowball(snap.balls[k]); seen[snap.balls[k].id] = true; }
      for (var id in trails) if (!seen[id]) delete trails[id];
      drawExplosions(now); drawParticles(dt);
      ctx.restore();
    }

    // Метки обучения: пульсирующие подсказки «иди сюда» и «целься сюда». К симуляции
    // отношения не имеют. point — круг-цель, rect — подсветка препятствия.
    var marks = [];
    function setMarks(list) { marks = list && list.length ? list : []; }
    function drawMarks(now) {
      var puls = 1 + 0.12 * Math.sin(now / 260);
      ctx.save();
      ctx.lineWidth = 3;
      for (var i = 0; i < marks.length; i++) {
        var m = marks[i];
        ctx.strokeStyle = 'rgba(124,255,178,0.95)';
        if (m.type === 'rect') {
          var pad = 6 * puls;
          ctx.strokeRect(m.x - m.w / 2 - pad, m.y - m.h / 2 - pad, m.w + pad * 2, m.h + pad * 2);
          continue;
        }
        var r = (m.r || 30) * puls;
        ctx.beginPath(); ctx.arc(m.x, m.y, r, 0, Math.PI * 2); ctx.stroke();
        ctx.strokeStyle = 'rgba(124,255,178,0.45)';
        ctx.beginPath(); ctx.arc(m.x, m.y, r * 0.55, 0, Math.PI * 2); ctx.stroke();
      }
      ctx.restore();
    }

    function reset() { particles = []; explosions = []; trails = {}; labels = {}; shake.until = 0; marks = []; }

    return { frame: frame, handleEvents: handleEvents, reset: reset, setMarks: setMarks,
      obstaclesOf: obstaclesOf };
  }

  /**
   * Линейная интерполяция двух снапшотов (по id игроков и снежков). t ∈ [0,1].
   * Результат — один переиспользуемый объект: вызов идёт каждый кадр, и свежие объекты на
   * каждого игрока давали заметные GC-паузы на телефонах. Держать ссылку на результат дольше
   * кадра нельзя, следующий вызов его перепишет.
   */
  var lerpOut = { players: [], balls: [] };
  function findById(arr, id, hint) {
    if (hint < arr.length && arr[hint].id === id) return arr[hint];
    for (var i = 0; i < arr.length; i++) if (arr[i].id === id) return arr[i];
    return null;
  }
  function lerpSnap(a, b, t) {
    if (!a) return b;
    if (!b || t <= 0) return a;
    if (t >= 1) return b;
    var o2 = lerpOut;
    // Снять ключи, которых в свежем снапшоте больше нет: иначе необязательные поля (напр. pve)
    // от прошлого матча «протекают» в следующий — PvP после PvE включал волновой HUD.
    for (var kd in o2) if (kd !== 'players' && kd !== 'balls' && !(kd in b)) delete o2[kd];
    for (var k2 in b) if (k2 !== 'players' && k2 !== 'balls') o2[k2] = b[k2];
    o2.time = a.time + (b.time - a.time) * t;
    var players = o2.players; players.length = b.players.length;
    for (var i = 0; i < b.players.length; i++) {
      var pb = b.players[i], pa = findById(a.players, pb.id, i);
      var o = players[i] || (players[i] = {});
      // Ключи, исчезнувшие из свежего снапшота, снимаем и у бойца: объекты переиспользуются по
      // индексу, и боец, попавший на место моба из прошлого матча, наследовал его mhp и et —
      // рисовался големом и с чужой полоской HP.
      for (var kp in o) if (!(kp in pb)) delete o[kp];
      for (var k in pb) o[k] = pb[k];
      if (pa) {
        o.x = pa.x + (pb.x - pa.x) * t; o.y = pa.y + (pb.y - pa.y) * t;
        o.anim = pa.anim + (pb.anim - pa.anim) * t;
        o.power = pa.power + (pb.power - pa.power) * t;
        if (typeof pa.rl === 'number' && typeof pb.rl === 'number') o.rl = pa.rl + (pb.rl - pa.rl) * t;
      }
    }
    var balls = o2.balls; balls.length = b.balls.length;
    for (var j = 0; j < b.balls.length; j++) {
      var sb = b.balls[j], sa = findById(a.balls, sb.id, j);
      var s = balls[j] || (balls[j] = {});
      s.id = sb.id; s.r = sb.r; s.team = sb.team; s.ex = sb.ex; s.fr = sb.fr; s.sn = sb.sn;
      if (sa) { s.x = sa.x + (sb.x - sa.x) * t; s.y = sa.y + (sb.y - sa.y) * t; s.z = sa.z + (sb.z - sa.z) * t; }
      else { s.x = sb.x; s.y = sb.y; s.z = sb.z; }
    }
    return o2;
  }

  return { create: create, lerpSnap: lerpSnap };
})();
