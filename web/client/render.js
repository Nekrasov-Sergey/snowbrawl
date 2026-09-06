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
      var oc = document.createElement('canvas'); oc.width = W; oc.height = H;
      var c = oc.getContext('2d');
      var bg = c.createLinearGradient(0, 0, 0, H); bg.addColorStop(0, '#dfeeff'); bg.addColorStop(1, '#c3ddf7');
      c.fillStyle = bg; c.fillRect(0, 0, W, H);
      var arena = Sim.ARENAS[index] || Sim.ARENAS[0];
      // «Река»: полоса льда по центру рисуется в кэш (статична).
      if (arena.ice) {
        var ig = c.createLinearGradient(0, arena.ice.y0, 0, arena.ice.y1);
        ig.addColorStop(0, '#bfe6f5'); ig.addColorStop(0.5, '#d9f2fb'); ig.addColorStop(1, '#bfe6f5');
        c.fillStyle = ig; c.fillRect(0, arena.ice.y0, W, arena.ice.y1 - arena.ice.y0);
        c.strokeStyle = 'rgba(120,170,200,0.5)'; c.lineWidth = 1;
        for (var il = 0; il < 7; il++) {
          var iy = arena.ice.y0 + 6 + il * (arena.ice.y1 - arena.ice.y0 - 12) / 6;
          c.beginPath(); c.moveTo(30 + il * 40, iy); c.lineTo(W - 40 + il * 12, iy - 6); c.stroke();
        }
        c.strokeStyle = 'rgba(90,150,190,0.6)'; c.lineWidth = 1.5;
        c.strokeRect(0, arena.ice.y0, W, arena.ice.y1 - arena.ice.y0);
      }
      for (var i = 0; i < arena.obstacles.length; i++) {
        var ob = arena.obstacles[i];
        if (ob.hp != null) continue; // разрушаемые рисует drawArena по снапшоту
        if (ob.type === 'rect') {
          var grad = c.createLinearGradient(ob.x, ob.y - ob.h / 2, ob.x, ob.y + ob.h / 2);
          grad.addColorStop(0, '#ffffff'); grad.addColorStop(1, '#c9deF5');
          c.save(); c.shadowColor = 'rgba(0,0,0,0.25)'; c.shadowBlur = 6; c.shadowOffsetY = 3;
          c.fillStyle = grad; c.fillRect(ob.x - ob.w / 2, ob.y - ob.h / 2, ob.w, ob.h);
          c.restore();
          c.strokeStyle = '#9cc0e6'; c.lineWidth = 2; c.strokeRect(ob.x - ob.w / 2, ob.y - ob.h / 2, ob.w, ob.h);
        } else {
          c.fillStyle = '#8a5a3b'; c.fillRect(ob.x - 3, ob.y, 6, 12);
          var g2 = c.createRadialGradient(ob.x - 4, ob.y - 6, 2, ob.x, ob.y - 2, ob.r);
          g2.addColorStop(0, '#4fae74'); g2.addColorStop(1, '#1f5636');
          c.beginPath(); c.arc(ob.x, ob.y - 2, ob.r, 0, Math.PI * 2);
          c.fillStyle = g2; c.fill(); c.strokeStyle = '#1f5636'; c.stroke();
        }
      }
      arenaCache.index = index; arenaCache.canvas = oc;
    }

    // Разрушаемое укрытие: ящик/бочка из «дерева», трещины по мере урона.
    function drawDestructible(d) {
      var frac = d.maxHp ? d.hp / d.maxHp : 1;
      ctx.save();
      ctx.fillStyle = '#c58a4a'; ctx.strokeStyle = '#7c4a22'; ctx.lineWidth = 2;
      if (d.type === 'circle') {
        ctx.beginPath(); ctx.arc(d.x, d.y, d.r, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
        ctx.strokeStyle = '#8f5c30'; ctx.lineWidth = 1.5;
        ctx.beginPath(); ctx.arc(d.x, d.y, d.r * 0.62, 0, Math.PI * 2); ctx.stroke();
        ctx.beginPath(); ctx.moveTo(d.x - d.r, d.y); ctx.lineTo(d.x + d.r, d.y); ctx.stroke();
      } else {
        var x0 = d.x - d.w / 2, y0 = d.y - d.h / 2;
        ctx.fillRect(x0, y0, d.w, d.h); ctx.strokeRect(x0, y0, d.w, d.h);
        ctx.strokeStyle = '#8f5c30'; ctx.lineWidth = 1.5;
        for (var pl = 1; pl < 3; pl++) { ctx.beginPath(); ctx.moveTo(x0 + d.w * pl / 3, y0); ctx.lineTo(x0 + d.w * pl / 3, y0 + d.h); ctx.stroke(); }
        ctx.beginPath(); ctx.moveTo(x0, d.y); ctx.lineTo(x0 + d.w, d.y); ctx.stroke();
      }
      if (frac < 0.999) { // трещины
        ctx.strokeStyle = 'rgba(40,20,10,0.75)'; ctx.lineWidth = 1.5;
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

    // Подписи бойцов (ник сверху, роль снизу) — спрайт на бойца, fillText со сменой шрифта
    // каждый кадр на телефонах заметно дорог (особенно эмодзи бота).
    var labels = {};
    function labelOf(p, isMe, r) {
      var key = p.nick + '|' + p.role + '|' + (p.bot ? 1 : 0) + '|' + (isMe ? 1 : 0) + '|' + r;
      var l = labels[p.id];
      if (l && l.key === key) return l;
      var nick = p.nick + (p.bot ? ' 🤖' : '');
      var oc = document.createElement('canvas'), c = oc.getContext('2d');
      c.font = (isMe ? 'bold ' : '') + '10px Segoe UI, Arial';
      var w = Math.ceil(Math.max(c.measureText(nick).width, 20)) + 8;
      c.font = '9px Segoe UI, Arial';
      w = Math.max(w, Math.ceil(c.measureText(p.role).width) + 8);
      var top = r + 16, h = top + r + 16; // ник на базовой линии top-10, роль на top + 2r + 12
      oc.width = w; oc.height = h;
      c = oc.getContext('2d'); c.textAlign = 'center';
      c.fillStyle = '#0b1622'; c.font = (isMe ? 'bold ' : '') + '10px Segoe UI, Arial'; c.fillText(nick, w / 2, top - 6);
      c.fillStyle = '#3c5a7c'; c.font = '9px Segoe UI, Arial'; c.fillText(p.role, w / 2, top + r + 12);
      l = labels[p.id] = { key: key, canvas: oc, w: w, top: top };
      return l;
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
            explosions.push({ x: e.x, y: e.y, start: performance.now(), duration: 320, maxR: 58 });
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
          default: break;
        }
      }
    }

    function drawArena(snap) {
      if (arenaCache.index !== snap.arena) buildArena(snap.arena);
      // При тряске экрана края сдвигаются: подложка тем же фоном закрывает щели.
      ctx.fillStyle = '#c3ddf7'; ctx.fillRect(-12, -12, W + 24, H + 24);
      ctx.drawImage(arenaCache.canvas, 0, 0);
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
      // Разрушаемые укрытия арены (не в кэше — меняются).
      if (snap.destr) for (var d = 0; d < snap.destr.length; d++) { if (snap.destr[d].hp > 0) drawDestructible(snap.destr[d]); }
      for (var k = 0; k < snap.walls.length; k++) {
        var wl = snap.walls[k];
        ctx.globalAlpha = 0.5 + (wl.ttl / wl.life) * 0.5;
        ctx.fillStyle = wl.team === 'A' ? '#bfe0ff' : '#ffc9c9'; ctx.strokeStyle = '#5a7fa8';
        ctx.fillRect(wl.x - wl.w / 2, wl.y - wl.h / 2, wl.w, wl.h); ctx.strokeRect(wl.x - wl.w / 2, wl.y - wl.h / 2, wl.w, wl.h);
        if (wl.maxHp && wl.hp < wl.maxHp) { // трещины по прочности
          ctx.strokeStyle = 'rgba(50,70,90,0.7)'; ctx.lineWidth = 1;
          for (var wc = 0; wc < (wl.maxHp - wl.hp); wc++) {
            var wx = wl.x - wl.w / 2 + (wc + 1) * wl.w / (wl.maxHp + 1);
            ctx.beginPath(); ctx.moveTo(wx, wl.y - wl.h / 2); ctx.lineTo(wx + 3, wl.y + wl.h / 2); ctx.stroke();
          }
        }
        ctx.globalAlpha = 1;
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
    function radiusOf(p) { return (Sim.ROLE_STATS[p.role] || { radius: 15 }).radius; }

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
        if (g.hp > 0) obs.push({ type: g.type, x: g.x, y: g.y, w: g.w, h: g.h, r: g.r, height: 24 });
      }
      for (var i = 0; i < snap.walls.length; i++) {
        var w = snap.walls[i];
        obs.push({ type: 'rect', x: w.x, y: w.y, w: w.w, h: w.h, height: 20 });
      }
      return obs;
    }

    function drawCharacter(snap, p, isMe, local) {
      var r = radiusOf(p), vx = p.x, vy = p.y;
      var charging = isMe && local && local.charging ? true : p.charging;
      var power = isMe && local && local.charging ? local.power : p.power;
      var aimX = isMe && local && local.charging ? local.aimX : p.aimX;
      var aimY = isMe && local && local.charging ? local.aimY : p.aimY;
      var alive = p.hp > 0 && !p.koed;
      if (p.moving && alive) vy -= Math.abs(Math.sin(p.anim)) * 2;
      if (charging) {
        var dx = aimX - p.x, dy = aimY - p.y, d = Math.hypot(dx, dy) || 1;
        vx -= (dx / d) * power * 4; vy -= (dy / d) * power * 4;
      }
      var color = p.team === 'A' ? '#4aa8ff' : '#ff5b5b';
      var flashing = (snap.time - p.hitAt) < 150;

      ctx.save();
      if (p.koed) {
        var progress = Math.min(1, (snap.time - p.koAt) / Sim.KO_ANIM_MS);
        ctx.globalAlpha = 1 - 0.65 * progress;
        ctx.translate(p.x, p.y); ctx.rotate(progress * Math.PI / 2.2); ctx.translate(-p.x, -p.y);
      }
      ctx.beginPath(); ctx.ellipse(p.x, p.y + r * 0.6, r * 0.9, r * 0.35, 0, 0, Math.PI * 2);
      ctx.fillStyle = 'rgba(0,0,0,0.15)'; ctx.fill();

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

      ctx.save();
      if (p.iframe) ctx.globalAlpha *= 0.6;
      ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2);
      ctx.fillStyle = flashing ? '#ffffff' : (p.stun > 0 ? '#888' : color);
      ctx.fill(); ctx.lineWidth = 2; ctx.strokeStyle = '#0b1622'; ctx.stroke();
      if (p.slow) { ctx.fillStyle = 'rgba(150,216,255,0.42)'; ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2); ctx.fill(); }
      ctx.restore();

      drawRoleModel(ctx, p.role, vx, vy, r, faceAng);

      // Заряженная способность: цветной ореол вокруг бойца.
      if (p.armed) {
        var ac = p.armed === 'explosive' ? '#ffb347' : (p.armed === 'frost' ? '#9fe8ff' : '#c9a6ff');
        ctx.beginPath(); ctx.arc(vx, vy, r + 3.5, 0, Math.PI * 2);
        ctx.strokeStyle = ac; ctx.lineWidth = 2.5; ctx.globalAlpha = 0.85; ctx.stroke(); ctx.globalAlpha = 1;
      }
      // Щит: пассивный щитовой пузырь.
      if (p.bubble) {
        ctx.beginPath(); ctx.arc(vx, vy, r + 4, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(150,240,190,0.9)'; ctx.lineWidth = 2; ctx.stroke();
        ctx.beginPath(); ctx.arc(vx, vy, r + 4, -0.6, 0.5);
        ctx.strokeStyle = 'rgba(255,255,255,0.9)'; ctx.lineWidth = 1.5; ctx.stroke();
      }

      if (isMe) { ctx.beginPath(); ctx.arc(vx, vy, r + 6.5, 0, Math.PI * 2); ctx.strokeStyle = '#ffe066'; ctx.lineWidth = 2; ctx.stroke(); }

      // Перезарядка выстрела: убывающая дуга вокруг своего бойца (полная сразу после броска).
      if (isMe && p.rl > 0) {
        var rr = r + 10;
        ctx.beginPath(); ctx.arc(vx, vy, rr, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(255,255,255,0.15)'; ctx.lineWidth = 3; ctx.stroke();
        ctx.beginPath(); ctx.arc(vx, vy, rr, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * p.rl);
        ctx.strokeStyle = '#ffcf5b'; ctx.lineWidth = 3; ctx.lineCap = 'round'; ctx.stroke();
        ctx.lineCap = 'butt';
      }

      var lb = labelOf(p, isMe, r);
      ctx.drawImage(lb.canvas, Math.round(vx - lb.w / 2), Math.round(vy - lb.top));
      ctx.restore();

      if (charging) {
        if (isMe) {
          // Луч только у себя: длина равна дальности полёта (140 + power·380), конец — место падения
          // снежка; красный, если по дороге снежок упрётся в препятствие или стену.
          var adx = aimX - p.x, ady = aimY - p.y, ad = Math.hypot(adx, ady) || 1;
          var range = 140 + power * 380, ex = p.x + adx / ad * range, ey = p.y + ady / ad * range;
          var blocked = !Sim.canHitTarget(obstaclesOf(snap), p, ex, ey, power);
          var col = blocked ? 'rgba(255,80,80,0.9)' : 'rgba(255,224,102,0.9)'; // красный — не долетит, жёлтый — долетит
          ctx.strokeStyle = col; ctx.fillStyle = col;
          ctx.beginPath(); ctx.moveTo(p.x, p.y); ctx.lineTo(ex, ey); ctx.lineWidth = 2; ctx.stroke();
          ctx.beginPath(); ctx.arc(ex, ey, 12, 0, Math.PI * 2); ctx.lineWidth = 1.5; ctx.stroke();
          ctx.beginPath(); ctx.arc(ex, ey, 2.5, 0, Math.PI * 2); ctx.fill();
        }
        var bw = 40;
        ctx.fillStyle = '#0b1622'; ctx.fillRect(p.x - bw / 2, p.y - r - 20, bw, 6);
        ctx.fillStyle = p.special ? '#b478ff' : (power > 0.7 ? '#ff5b5b' : '#ffd166');
        ctx.fillRect(p.x - bw / 2, p.y - r - 20, bw * power, 6);
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
    function frame(snap, meId, local) {
      var now = performance.now();
      var dt = Math.min((now - lastFrame) / 1000, 0.05);
      lastFrame = now;
      var shakeX = 0, shakeY = 0;
      if (now < shake.until) { var rem = (shake.until - now) / shake.total; shakeX = (Math.random() - 0.5) * shake.mag * rem; shakeY = (Math.random() - 0.5) * shake.mag * rem; }
      ctx.save(); ctx.translate(shakeX, shakeY);
      drawArena(snap); drawSnowflakes(dt);
      var me = null;
      for (var i = 0; i < snap.players.length; i++) {
        var p = snap.players[i];
        if (p.id === meId) { me = p; continue; }
        drawCharacter(snap, p, false, null);
      }
      if (me) drawCharacter(snap, me, true, local);
      var seen = {};
      for (var k = 0; k < snap.balls.length; k++) { drawSnowball(snap.balls[k]); seen[snap.balls[k].id] = true; }
      for (var id in trails) if (!seen[id]) delete trails[id];
      drawExplosions(now); drawParticles(dt);
      ctx.restore();
    }

    function reset() { particles = []; explosions = []; trails = {}; labels = {}; shake.until = 0; }

    return { frame: frame, handleEvents: handleEvents, reset: reset };
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
    for (var k2 in b) if (k2 !== 'players' && k2 !== 'balls') o2[k2] = b[k2];
    o2.time = a.time + (b.time - a.time) * t;
    var players = o2.players; players.length = b.players.length;
    for (var i = 0; i < b.players.length; i++) {
      var pb = b.players[i], pa = findById(a.players, pb.id, i);
      var o = players[i] || (players[i] = {});
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
