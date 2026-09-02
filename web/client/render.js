/* Рендер арены по снапшоту sim.js. Частицы, тряска, снежинки, следы снежков и звуки
 * живут здесь, на клиенте: они косметика и не влияют на симуляцию. */
window.SBRender = (function () {
  var Sim = window.SnowBrawlSim;
  var W = Sim.W, H = Sim.H;

  function create(canvas) {
    var ctx = canvas.getContext('2d');
    var snowflakes = [], particles = [], explosions = [], trails = {};
    var shake = { mag: 0, until: 0, total: 1 };
    var lastFrame = performance.now();

    for (var i = 0; i < 70; i++) {
      snowflakes.push({ x: Math.random() * W, y: Math.random() * H, speed: 20 + Math.random() * 40,
        drift: (Math.random() - 0.5) * 20, size: 1 + Math.random() * 2, alpha: 0.3 + Math.random() * 0.5 });
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
          case 'special': if (e.special === 'wall') audio.shieldThud(); break;
          default: break;
        }
      }
    }

    function drawArena(snap) {
      ctx.clearRect(0, 0, W, H);
      var arena = Sim.ARENAS[snap.arena] || Sim.ARENAS[0];
      for (var i = 0; i < arena.obstacles.length; i++) {
        var ob = arena.obstacles[i];
        if (ob.type === 'rect') {
          var grad = ctx.createLinearGradient(ob.x, ob.y - ob.h / 2, ob.x, ob.y + ob.h / 2);
          grad.addColorStop(0, '#ffffff'); grad.addColorStop(1, '#c9deF5');
          ctx.save(); ctx.shadowColor = 'rgba(0,0,0,0.25)'; ctx.shadowBlur = 6; ctx.shadowOffsetY = 3;
          ctx.fillStyle = grad; ctx.fillRect(ob.x - ob.w / 2, ob.y - ob.h / 2, ob.w, ob.h);
          ctx.restore();
          ctx.strokeStyle = '#9cc0e6'; ctx.lineWidth = 2; ctx.strokeRect(ob.x - ob.w / 2, ob.y - ob.h / 2, ob.w, ob.h);
        } else {
          ctx.fillStyle = '#8a5a3b'; ctx.fillRect(ob.x - 3, ob.y, 6, 12);
          var g2 = ctx.createRadialGradient(ob.x - 4, ob.y - 6, 2, ob.x, ob.y - 2, ob.r);
          g2.addColorStop(0, '#4fae74'); g2.addColorStop(1, '#1f5636');
          ctx.beginPath(); ctx.arc(ob.x, ob.y - 2, ob.r, 0, Math.PI * 2);
          ctx.fillStyle = g2; ctx.fill(); ctx.strokeStyle = '#1f5636'; ctx.stroke();
        }
      }
      for (var k = 0; k < snap.walls.length; k++) {
        var wl = snap.walls[k];
        ctx.globalAlpha = 0.5 + (wl.ttl / wl.life) * 0.5;
        ctx.fillStyle = wl.team === 'A' ? '#bfe0ff' : '#ffc9c9'; ctx.strokeStyle = '#5a7fa8';
        ctx.fillRect(wl.x - wl.w / 2, wl.y - wl.h / 2, wl.w, wl.h); ctx.strokeRect(wl.x - wl.w / 2, wl.y - wl.h / 2, wl.w, wl.h);
        ctx.globalAlpha = 1;
      }
    }
    function drawSnowflakes(dt) {
      for (var i = 0; i < snowflakes.length; i++) {
        var f = snowflakes[i];
        f.y += f.speed * dt; f.x += f.drift * dt;
        if (f.y > H) { f.y = -5; f.x = Math.random() * W; }
        if (f.x < 0) f.x = W; if (f.x > W) f.x = 0;
        ctx.globalAlpha = f.alpha; ctx.fillStyle = '#ffffff'; ctx.beginPath(); ctx.arc(f.x, f.y, f.size, 0, Math.PI * 2); ctx.fill();
      }
      ctx.globalAlpha = 1;
    }
    function radiusOf(p) { return (Sim.ROLE_STATS[p.role] || { radius: 15 }).radius; }

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

      ctx.beginPath(); ctx.arc(vx, vy, r, 0, Math.PI * 2);
      ctx.fillStyle = flashing ? '#ffffff' : (p.stun > 0 ? '#888' : color);
      ctx.fill(); ctx.lineWidth = 2; ctx.strokeStyle = '#0b1622'; ctx.stroke();

      if (isMe) { ctx.beginPath(); ctx.arc(vx, vy, r + 5, 0, Math.PI * 2); ctx.strokeStyle = '#ffe066'; ctx.lineWidth = 2; ctx.stroke(); }

      ctx.fillStyle = '#0b1622'; ctx.font = (isMe ? 'bold ' : '') + '10px Segoe UI, Arial'; ctx.textAlign = 'center';
      ctx.fillText(p.nick + (p.bot ? ' 🤖' : ''), vx, vy - r - 6);
      ctx.fillStyle = '#3c5a7c'; ctx.font = '9px Segoe UI, Arial';
      ctx.fillText(p.role, vx, vy + r + 12);
      ctx.restore();

      if (charging) {
        ctx.beginPath(); ctx.moveTo(p.x, p.y); ctx.lineTo(aimX, aimY);
        ctx.strokeStyle = p.special ? 'rgba(180,120,255,0.9)' : 'rgba(255, ' + Math.round(255 - power * 180) + ', 60, 0.8)';
        ctx.lineWidth = 2; ctx.stroke();
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
      ctx.beginPath(); ctx.arc(s.x, drawY, s.r, 0, Math.PI * 2);
      ctx.fillStyle = s.ex ? '#ffb347' : (s.fr ? '#9fe8ff' : '#ffffff'); ctx.fill();
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

    function reset() { particles = []; explosions = []; trails = {}; shake.until = 0; }

    return { frame: frame, handleEvents: handleEvents, reset: reset };
  }

  /** Линейная интерполяция двух снапшотов (по id игроков и снежков). t ∈ [0,1]. */
  function lerpSnap(a, b, t) {
    if (!a) return b;
    if (!b || t <= 0) return a;
    if (t >= 1) return b;
    var byId = {};
    for (var i = 0; i < a.players.length; i++) byId[a.players[i].id] = a.players[i];
    var players = b.players.map(function (pb) {
      var pa = byId[pb.id];
      if (!pa) return pb;
      var o = {};
      for (var k in pb) o[k] = pb[k];
      o.x = pa.x + (pb.x - pa.x) * t; o.y = pa.y + (pb.y - pa.y) * t;
      o.anim = pa.anim + (pb.anim - pa.anim) * t;
      o.power = pa.power + (pb.power - pa.power) * t;
      return o;
    });
    var ballsA = {};
    for (var j = 0; j < a.balls.length; j++) ballsA[a.balls[j].id] = a.balls[j];
    var balls = b.balls.map(function (sb) {
      var sa = ballsA[sb.id];
      if (!sa) return sb;
      return { id: sb.id, r: sb.r, team: sb.team, ex: sb.ex, fr: sb.fr,
        x: sa.x + (sb.x - sa.x) * t, y: sa.y + (sb.y - sa.y) * t, z: sa.z + (sb.z - sa.z) * t };
    });
    var o2 = {};
    for (var k2 in b) o2[k2] = b[k2];
    o2.players = players; o2.balls = balls;
    o2.time = a.time + (b.time - a.time) * t;
    return o2;
  }

  return { create: create, lerpSnap: lerpSnap };
})();
