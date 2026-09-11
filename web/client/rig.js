/* SnowBrawl — процедурная модель бойца на Canvas 2D (см. tools/canvas-model.html — песочница).
   Экспортирует window.SBRig.drawFighter(ctx, cx, cy, R, s). Косметика, к sim не относится. */
window.SBRig = (function () {
  "use strict";
  var TAU = Math.PI * 2, D2R = Math.PI / 180;
  var INK = "#13293f";
  var TEAM = { A: "#4aa8ff", B: "#ff6a6a" };
  var GEAR = {
    "Раннер":  { accent: "#7fd4ff", boot: "#eef4fb", mitt: "#eef4fb", w: 0.84, big: 0.95, tint: "#f7fbff", leg: [7, 6] },
    "Снайпер": { accent: "#9b8cff", boot: "#8f86c9", mitt: "#8f86c9", w: 0.90, big: 1.02, tint: "#f1effb", leg: [7, 6] },
    "Фризер":  { accent: "#4fc9ff", boot: "#cfeefb", mitt: "#a9e3f4", w: 1.02, big: 0.98, tint: "#e9f7ff", leg: [10, 9] },
    "Бомбер":  { accent: "#ff9e2e", boot: "#ff9e2e", mitt: "#ff9e2e", w: 1.10, big: 1.0,  tint: "#fff4e6", leg: [11, 10] },
    "Щит":     { accent: "#5fd08a", boot: "#57b981", mitt: "#57b981", w: 1.16, big: 1.02, tint: "#ecfbf1", leg: [11, 10] },
    "Танк":    { accent: "#8ea3bd", boot: "#5f7488", mitt: "#8ea3bd", w: 1.14, big: 1.12, tint: "#c8d4e1", leg: [12, 11] }
  };

  // мягкая cel-заливка формы: белое тело, нижняя лужа тени, блик сверху, толстая обводка
  function shaded(ctx, pathFn, fill, shade, hl, ow) {
    ctx.save();
    pathFn(); ctx.fillStyle = fill; ctx.fill();
    pathFn(); ctx.clip();
    // тень — узкий серп по нижнему краю, не центральное пятно
    ctx.fillStyle = shade;
    ctx.beginPath(); ctx.ellipse(hl.sx, hl.sy + hl.sr * 0.55, hl.sr * 1.05, hl.sr * 0.6, 0, 0, TAU); ctx.fill();
    // блик сверху-справа
    ctx.fillStyle = "rgba(255,255,255,0.85)";
    ctx.beginPath(); ctx.ellipse(hl.hx, hl.hy, hl.hr, hl.hr * 0.62, -0.5, 0, TAU); ctx.fill();
    ctx.restore();
    ctx.save();
    pathFn(); ctx.lineJoin = "round"; ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
    ctx.restore();
  }

  // толстая капсула-конечность из 2 сегментов; вернуть точку кисти
  function twoBone(ctx, root, a0, l0, a1, l1, r0, r1, ow, fill, shade) {
    var ex = root[0] + Math.cos(a0) * l0, ey = root[1] + Math.sin(a0) * l0;
    var hx = ex + Math.cos(a1) * l1, hy = ey + Math.sin(a1) * l1;
    ctx.lineCap = "round";
    ctx.strokeStyle = INK; ctx.lineWidth = r0 * 2 + ow;
    ctx.beginPath(); ctx.moveTo(root[0], root[1]); ctx.lineTo(ex, ey); ctx.stroke();
    ctx.lineWidth = r1 * 2 + ow;
    ctx.beginPath(); ctx.moveTo(ex, ey); ctx.lineTo(hx, hy); ctx.stroke();
    ctx.strokeStyle = fill; ctx.lineWidth = r0 * 2;
    ctx.beginPath(); ctx.moveTo(root[0], root[1]); ctx.lineTo(ex, ey); ctx.stroke();
    ctx.lineWidth = r1 * 2;
    ctx.beginPath(); ctx.moveTo(ex, ey); ctx.lineTo(hx, hy); ctx.stroke();
    ctx.strokeStyle = shade; ctx.lineWidth = r1 * 0.7;
    ctx.beginPath(); ctx.moveTo(ex, ey + r1 * 0.5); ctx.lineTo(hx, hy + r1 * 0.5); ctx.stroke();
    return [hx, hy, ex, ey];
  }
  function mitten(ctx, x, y, r, fill, ow) {
    ctx.beginPath(); ctx.arc(x, y, r, 0, TAU);
    ctx.fillStyle = fill; ctx.fill();
    ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
  }
  function snowball(ctx, x, y, r, ow) {
    ctx.beginPath(); ctx.arc(x, y, r, 0, TAU);
    ctx.fillStyle = "#fff"; ctx.fill(); ctx.strokeStyle = "#9db6d2"; ctx.lineWidth = ow * 0.7; ctx.stroke();
  }
  function drawBomb(ctx, x, y, r, t, lit) {
    ctx.beginPath(); ctx.arc(x, y, r, 0, TAU);
    ctx.fillStyle = "#20303f"; ctx.fill();
    ctx.strokeStyle = INK; ctx.lineWidth = 2; ctx.stroke();
    ctx.fillStyle = "#43566a"; ctx.beginPath(); ctx.ellipse(x - r * 0.32, y - r * 0.34, r * 0.32, r * 0.2, -0.5, 0, TAU); ctx.fill();
    ctx.fillStyle = "#8a7a5a"; ctx.strokeStyle = INK; ctx.lineWidth = 1.4;
    ctx.beginPath(); ctx.rect(x - 2.5, y - r - 3, 5, 4); ctx.fill(); ctx.stroke();
    ctx.strokeStyle = "#7c4a22"; ctx.lineWidth = 2.2; ctx.lineCap = "round";
    ctx.beginPath(); ctx.moveTo(x, y - r - 2); ctx.quadraticCurveTo(x + 9, y - r - 10, x + 4, y - r - 15); ctx.stroke();
    if (lit) {
      var fl = 2.6 + Math.sin(t * 0.05) * 1.1, sx = x + 4, sy = y - r - 16;
      ctx.fillStyle = "#ffd24a"; ctx.beginPath(); ctx.arc(sx, sy, fl, 0, TAU); ctx.fill();
      ctx.fillStyle = "#ff8c2e"; ctx.beginPath(); ctx.arc(sx, sy + 0.5, fl * 0.55, 0, TAU); ctx.fill();
      ctx.fillStyle = "rgba(255,220,120,0.5)"; ctx.beginPath(); ctx.arc(sx, sy, fl * 2, 0, TAU); ctx.fill();
    }
  }

  function mix(hex, rgb, t) {
    var n = parseInt(hex.slice(1), 16);
    var r = (n >> 16) & 255, gg = (n >> 8) & 255, b = n & 255;
    r = Math.round(r + (rgb[0] - r) * t);
    gg = Math.round(gg + (rgb[1] - gg) * t);
    b = Math.round(b + (rgb[2] - b) * t);
    return "rgb(" + r + "," + gg + "," + b + ")";
  }

  // Упрощённый порченый моб (swarm): тело+голова+капюшон+глаза-щёлочки, без конечностей.
  function drawLod(ctx, cx, cy, k, s, body, shade, ow, BW, BH, HR, headCY) {
    var p = s.t * 0.012 * s.speed;
    var bob = s.mode === "run" ? -Math.abs(Math.sin(p)) * 3 : Math.sin(s.t * 0.003) * 1.5;
    var lean = s.mode === "run" ? Math.sin(p) * 0.06 * s.flip : 0;
    ctx.save();
    ctx.translate(cx + (s.recoil || 0), cy);
    ctx.scale(s.flip * k, k);
    ctx.rotate(lean);
    ctx.translate(0, bob);
    koFade(ctx, s);
    // тело
    ctx.beginPath();
    ctx.moveTo(0, -BH * 0.45);
    ctx.bezierCurveTo(BW * 0.42, -BH * 0.45, BW * 0.46, BH * 0.5 - 10, 0, BH * 0.5);
    ctx.bezierCurveTo(-BW * 0.46, BH * 0.5 - 10, -BW * 0.42, -BH * 0.45, 0, -BH * 0.45);
    ctx.closePath();
    ctx.fillStyle = body; ctx.fill();
    ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.lineJoin = "round"; ctx.stroke();
    // рваный нижний край
    ctx.fillStyle = body; ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
    for (var i = -2; i <= 2; i++) {
      ctx.beginPath(); ctx.moveTo(i * 9 - 5, BH * 0.5 - 3);
      ctx.lineTo(i * 9, BH * 0.5 + 6); ctx.lineTo(i * 9 + 5, BH * 0.5 - 3); ctx.closePath();
      ctx.fill(); ctx.stroke();
    }
    // голова
    ctx.beginPath(); ctx.arc(0, headCY + 4, HR * 0.82, 0, TAU);
    ctx.fillStyle = body; ctx.fill(); ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
    // капюшон
    ctx.beginPath(); ctx.arc(0, headCY + 4, HR * 0.92, Math.PI * 0.95, TAU + Math.PI * 0.05);
    ctx.strokeStyle = "#4b4557"; ctx.lineWidth = 7; ctx.stroke();
    ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.stroke();
    // глаза-щёлочки
    ctx.strokeStyle = "#1b1622"; ctx.lineWidth = 3; ctx.lineCap = "round";
    ctx.beginPath(); ctx.moveTo(-8, headCY + 3); ctx.lineTo(-3, headCY + 4);
    ctx.moveTo(3, headCY + 4); ctx.lineTo(8, headCY + 3); ctx.stroke();
    if (s.mode === "hit" && s.hitT < 1) {
      ctx.globalAlpha = 0.5 * (1 - s.hitT); ctx.fillStyle = "#fff";
      ctx.beginPath(); ctx.ellipse(0, -6, BW * 0.5, BH * 0.55, 0, 0, TAU); ctx.fill(); ctx.globalAlpha = 1;
    }
    ctx.restore();
  }

  // Труп: PvE-мобы (s.corpse) тают за 4 c и стартуют полупрозрачными. Павший боец PvP
  // остаётся лежать до конца раунда — заваливаем набок, но цвет команды держим (лёгкое
  // затемнение): иначе синий труп союзника читается как чёрный «пвешный» враг.
  function koFade(ctx, s) {
    if (!s.koT) return;
    ctx.rotate(s.koT * (s.corpse ? 1.1 : 1.3));
    ctx.globalAlpha = s.corpse ? Math.max(0, 0.6 - 0.6 * s.koT) : 1 - 0.18 * s.koT;
  }

  // ---------------- модель ----------------
  function drawModel(ctx, cx, cy, R, s) {
    var g = GEAR[s.role];
    var isTank = s.role === "Танк";
    var body = s.variantB ? g.tint : "#f7fbfe", shade = "rgba(160,195,228,0.38)";
    if (isTank) { body = "#c8d4e1"; shade = "rgba(110,135,165,0.4)"; }
    // подкрас тела в цвет команды — насыщенно, чтобы синие и красные различались издалека.
    // Красный домешиваем сильнее синего: слабый замес давал грязно-розовый, и враги в бою
    // читались серыми (как порченые PvE-мобы).
    var TCOL = { A: [30, 110, 245], B: [232, 40, 40] };
    var TSHD = { A: "rgba(16,60,150,0.5)", B: "rgba(140,14,14,0.55)" };
    var TMIX = { A: isTank ? 0.42 : 0.58, B: isTank ? 0.68 : 0.78 };
    if (s.corrupt) {                     // порченый снеговик — враг PvE
      body = "#8a8496"; shade = "rgba(52,44,64,0.5)";
    } else if (s.team && TCOL[s.team]) {
      body = mix(body, TCOL[s.team], TMIX[s.team]);
      shade = TSHD[s.team];
    }
    if (s.stun) { body = mix(body, [140, 140, 148], 0.55); shade = "rgba(90,90,100,0.4)"; }  // оглушение — серый
    var ow = s.outline;
    var k = R / 46 * s.scaleMul * (g.big || 1);

    // размеры модели
    var BW = 60 * (g.w || 1), BH = 62, HR = 26;
    var headCY = -BH * 0.5 - HR * 0.14;

    if (s.lod) { drawLod(ctx, cx, cy, k, s, body, shade, ow, BW, BH, HR, headCY); return; }

    var shY = -BH * 0.24, shX = BW * 0.40;
    var hipY = BH * 0.42, hipX = BW * 0.22;

    // ---- поза ----
    var p = s.t * 0.012 * s.speed;
    var bob = 0, lean = 0, legL = 0, legR = 0;
    var aimA = s.aimLocal;
    // базовые углы рук: висят вниз-в стороны, слегка наружу
    var uL = 128 * D2R, fL = 118 * D2R, uR = 52 * D2R, fR = 62 * D2R;
    var prop = null;
    var actMode = (s.mode === "charge" || s.mode === "throw" || s.mode === "ability");
    // рука броска: Щит бросает задней (левой); Бомбер задней только на скилле
    var thL = (s.role === "Щит") || (s.role === "Бомбер" && s.mode === "ability");
    function setThrow(u, f) { if (thL) { uL = u; fL = f; } else { uR = u; fR = f; } }

    if (s.mode === "run") {
      legL = Math.sin(p) * 0.55; legR = Math.sin(p + Math.PI) * 0.55;
      uL += Math.sin(p + Math.PI) * 0.45; uR += Math.sin(p) * 0.45;
      bob = -Math.abs(Math.sin(p)) * 4; lean = Math.sin(p) * 0.05;
    } else if (s.mode === "idle") {
      bob = Math.sin(s.t * 0.002) * 1.4;
      uL += Math.sin(s.t * 0.002) * 0.05; uR -= Math.sin(s.t * 0.002) * 0.05;
    } else if (s.mode === "charge") {
      var wc = aimA + Math.PI * (0.6 + 0.28 * s.power);
      setThrow(wc, wc - 0.35);
      if (!thL) uL = aimA * 0.3 + 96 * D2R;
      lean = -0.12 * s.power * s.flip * (thL ? -1 : 1);
      prop = "hand";
    } else if (s.mode === "throw") {
      var e = 1 - (1 - s.throwT) * (1 - s.throwT);
      var w2 = aimA + Math.PI * 0.8;
      var ta = w2 + (aimA - 0.2 - w2) * e;
      setThrow(ta, ta - 0.25);
      lean = (-0.12 + 0.26 * e) * s.flip * (thL ? -1 : 1);
      if (s.throwT < 0.34) prop = "hand";
    } else if (s.mode === "ability") {
      var ea = 1 - (1 - s.abilityT) * (1 - s.abilityT);
      var wa = aimA + Math.PI * 0.85;
      var aa = wa + (aimA - 0.15 - wa) * ea;
      setThrow(aa, aa - 0.25);
      lean = (-0.15 + 0.30 * ea) * s.flip * (thL ? -1 : 1);
      if (s.abilityT < 0.42) prop = (s.role === "Бомбер" ? "bomb" : "hand");
    } else if (s.mode === "hit") {
      lean = 0.14 * (1 - s.hitT) * s.flip;
    }
    var backArmLate = thL && actMode;   // рука броска рисуется поверх тела

    // «ветер» для болтающихся частей (помпон, хвосты повязки)
    var wob = (s.mode === "run" ? Math.sin(p) * 0.6 : 0)
            + (s.mode === "idle" ? Math.sin(s.t * 0.003) * 0.1 : 0) - lean * 3;

    ctx.save();
    ctx.translate(cx + (s.recoil || 0), cy);
    ctx.scale(s.flip * k, k);
    koFade(ctx, s);
    ctx.translate(0, bob);
    ctx.rotate(lean);

    var shLp = [-shX, shY], shRp = [shX, shY];
    var hipLp = [-hipX, hipY], hipRp = [hipX, hipY];

    // ==== ЗА ТЕЛОМ: задняя нога, задняя рука ====
    var legAng = 90 * D2R, lr0 = g.leg[0], lr1 = g.leg[1], br = lr1 + 1.5;
    function drawLeg(hip, ang) {
      twoBone(ctx, hip, legAng + ang, 11, legAng + ang * 0.5, 12, lr0, lr1, ow, body, shade);
      var kx = hip[0] + Math.cos(legAng + ang) * 11, ky = hip[1] + Math.sin(legAng + ang) * 11;
      var fx = kx + Math.cos(legAng + ang * 0.5) * 12, fy = ky + Math.sin(legAng + ang * 0.5) * 12;
      if (s.role === "Раннер") {
        // босиком: обуви и крылышек нет — только голая пятка в цвет тела
        ctx.beginPath(); ctx.ellipse(fx, fy, lr1 * 0.75, lr1 * 0.6, 0, 0, TAU);
        ctx.fillStyle = body; ctx.fill();
        ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
        return;
      }
      ctx.beginPath(); ctx.ellipse(fx, fy + 2, br, br * 0.68, 0, 0, TAU);
      ctx.fillStyle = g.boot; ctx.fill(); ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
    }
    drawLeg(hipLp, legL);

    // задняя рука. У Снайпера её нет (обе на винтовке).
    function drawBackArm() {
      if (s.role === "Снайпер") return;
      var bH = twoBone(ctx, shLp, uL, 15, fL, 13, 10, 8.5, ow, body, shade);
      var mc = s.role === "Бомбер" ? "#ff9e2e" : g.mitt;
      mitten(ctx, bH[0], bH[1], 8, mc, ow);
      if (s.role === "Бомбер" && !(s.mode === "ability" && s.abilityT >= 0.42)) {
        drawBomb(ctx, bH[0] + 2, bH[1] - 2, 6.5, s.t, true);   // бомба с горящим фитилём всегда в руке
      } else if (thL && prop === "bomb") {
        drawBomb(ctx, bH[0] + 2, bH[1] - 2, 7.5, s.t, true);
      } else if (thL && prop === "hand") {
        snowball(ctx, bH[0], bH[1], 7, ow);
      }
    }
    if (!backArmLate) drawBackArm();

    // ==== ТЕЛО ====
    var bodyPath = function () {
      ctx.beginPath();
      ctx.moveTo(0, -BH * 0.5);
      ctx.bezierCurveTo(BW * 0.5, -BH * 0.5, BW * 0.5 + 6, BH * 0.5 - 12, 0, BH * 0.5);
      ctx.bezierCurveTo(-BW * 0.5 - 6, BH * 0.5 - 12, -BW * 0.5, -BH * 0.5, 0, -BH * 0.5);
      ctx.closePath();
    };
    shaded(ctx, bodyPath, body, shade, {
      sx: -BW * 0.16, sy: BH * 0.16, sr: BW * 0.5,
      hx: BW * 0.2, hy: -BH * 0.22, hr: BW * 0.2
    }, ow);
    if (s.corrupt) {  // рваный нижний край + подтёки
      ctx.fillStyle = body; ctx.strokeStyle = INK; ctx.lineWidth = 2; ctx.lineJoin = "round";
      for (var ce = -2; ce <= 2; ce++) {
        ctx.beginPath(); ctx.moveTo(ce * 12 - 6, BH * 0.5 - 4);
        ctx.lineTo(ce * 12, BH * 0.5 + 7); ctx.lineTo(ce * 12 + 6, BH * 0.5 - 4); ctx.closePath();
        ctx.fill(); ctx.stroke();
      }
      ctx.fillStyle = "rgba(70,60,88,0.55)";
      ctx.beginPath(); ctx.ellipse(-BW * 0.24, BH * 0.28, 4, 9, 0, 0, TAU); ctx.fill();
      ctx.beginPath(); ctx.ellipse(BW * 0.18, BH * 0.34, 3, 7, 0, 0, TAU); ctx.fill();
    }

    // ==== ГОЛОВА / ШЛЕМ ====
    if (isTank) {
      var helmPath = function () { ctx.beginPath(); ctx.arc(0, headCY + 2, HR + 2, Math.PI * 0.98, TAU + Math.PI * 0.02); ctx.lineTo(HR + 1, headCY + 8); ctx.lineTo(-HR - 1, headCY + 8); ctx.closePath(); };
      shaded(ctx, helmPath, "#b9c7d8", shade, { sx: -6, sy: headCY + 4, sr: HR, hx: 7, hy: headCY - 6, hr: 8 }, ow);
      if (s.back) { ctx.strokeStyle = "rgba(40,58,78,0.4)"; ctx.lineWidth = 2; ctx.beginPath(); ctx.moveTo(0, headCY - HR); ctx.lineTo(0, headCY + 6); ctx.stroke(); }
      else { ctx.fillStyle = "#2a3a4e"; ctx.beginPath(); ctx.rect(-HR * 0.7, headCY - 2, HR * 1.4, 5); ctx.fill(); }
    } else {
      var headPath = function () { ctx.beginPath(); ctx.arc(0, headCY, HR, 0, TAU); };
      shaded(ctx, headPath, body, shade, { sx: -6, sy: headCY + 5, sr: HR * 0.9, hx: 7, hy: headCY - 7, hr: HR * 0.42 }, ow);
    }

    // ==== ПЕРЕДНЯЯ НОГА ====
    drawLeg(hipRp, legR);

    // ==== ГОЛОВНОЙ УБОР (главный признак роли) ====
    if (!isTank) headwear(ctx, s, BW, BH, HR, headCY, aimA, ow, wob);

    // ==== ГИР РОЛИ (на теле) ====
    gearBody(ctx, s, BW, BH, HR, headCY, aimA, ow);

    // ==== ПЕРЕДНЯЯ РУКА + ПРОП ====
    if (s.role === "Снайпер") {
      // винтовка: приклад + ствольная коробка + длинный ствол + оптика, в двух руках вдоль прицела
      ctx.save(); ctx.translate(0, shY + 2); ctx.rotate(aimA);
      var col = s.corrupt ? "#3a3444" : "#5a6373", colD = s.corrupt ? "#2a2533" : "#464e5c";
      // приклад (за спиной)
      ctx.fillStyle = col; ctx.strokeStyle = INK; ctx.lineWidth = ow; ctx.lineJoin = "round";
      ctx.beginPath();
      ctx.moveTo(-24, -2); ctx.lineTo(-8, -5); ctx.lineTo(-8, 7); ctx.lineTo(-20, 8);
      ctx.quadraticCurveTo(-26, 6, -24, -2); ctx.closePath(); ctx.fill(); ctx.stroke();
      // ствольная коробка
      ctx.beginPath(); ctx.rect(-9, -6, 20, 12); ctx.fillStyle = colD; ctx.fill(); ctx.stroke();
      // магазин
      ctx.beginPath(); ctx.moveTo(-1, 6); ctx.lineTo(4, 18); ctx.lineTo(-4, 18); ctx.lineTo(-7, 6);
      ctx.closePath(); ctx.fillStyle = col; ctx.fill(); ctx.stroke();
      // спусковая скоба
      ctx.strokeStyle = INK; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(-1, 9, 4, 0.1, Math.PI - 0.1); ctx.stroke();
      // ствол
      ctx.strokeStyle = INK; ctx.lineWidth = 7 + ow; ctx.lineCap = "round";
      ctx.beginPath(); ctx.moveTo(9, -1); ctx.lineTo(52, -1); ctx.stroke();
      ctx.strokeStyle = col; ctx.lineWidth = 7; ctx.beginPath(); ctx.moveTo(9, -1); ctx.lineTo(52, -1); ctx.stroke();
      ctx.fillStyle = colD; ctx.beginPath(); ctx.arc(52, -1, 3.4, 0, TAU); ctx.fill();
      // оптический прицел сверху
      ctx.fillStyle = "#2b3340"; ctx.strokeStyle = INK; ctx.lineWidth = ow * 0.8;
      ctx.beginPath(); ctx.rect(-2, -15, 20, 6); ctx.fill(); ctx.stroke();
      ctx.beginPath(); ctx.moveTo(-2, -12); ctx.lineTo(-6, -6); ctx.moveTo(18, -12); ctx.lineTo(22, -6); ctx.stroke();
      ctx.fillStyle = "#7fd4ff"; ctx.beginPath(); ctx.arc(22, -9, 2, 0, TAU); ctx.fill();
      // снежок заряжен в дуле
      if (prop === "hand") snowball(ctx, 52, -1, 5, ow);
      ctx.restore();
      // руки на цевье и у скобы
      mitten(ctx, Math.cos(aimA) * 26 - Math.sin(aimA) * 2, (shY + 2) + Math.sin(aimA) * 26 + Math.cos(aimA) * 2, 7.5, g.mitt, ow);
      mitten(ctx, Math.cos(aimA) * 2 + Math.sin(aimA) * 6, (shY + 2) + Math.sin(aimA) * 2 - Math.cos(aimA) * 6, 7.5, g.mitt, ow);
    } else {
      var fH = twoBone(ctx, shRp, uR, 15, fR, 13, 10, 8.5, ow, body, shade);
      var mcol = s.role === "Бомбер" ? "#ff9e2e" : g.mitt;
      mitten(ctx, fH[0], fH[1], 8, mcol, ow);
      if (s.role === "Щит") {
        // щит на передней (целящейся) руке
        ctx.save(); ctx.translate(fH[0], fH[1]); ctx.rotate(aimA * 0.4);
        var shP = function () { ctx.beginPath(); ctx.ellipse(4, 0, 14, 27, 0, 0, TAU); };
        shaded(ctx, shP, "#c7d3e0", "rgba(120,145,175,0.5)", { sx: 0, sy: 9, sr: 14, hx: 8, hy: -11, hr: 6 }, ow);
        ctx.fillStyle = "#8ea3bd";
        for (var ri = -1; ri <= 1; ri++) { ctx.beginPath(); ctx.arc(4, ri * 16, 1.8, 0, TAU); ctx.fill(); }
        ctx.strokeStyle = "#8ea3bd"; ctx.lineWidth = 2;
        ctx.beginPath(); ctx.moveTo(4, -23); ctx.lineTo(4, 23); ctx.stroke();
        ctx.restore();
      } else if (!thL && prop === "bomb") {
        drawBomb(ctx, fH[0] + 2, fH[1] - 2, 7.5, s.t, true);
      } else if (!thL && prop === "hand") {
        snowball(ctx, fH[0], fH[1], 7, ow);
      }
    }
    if (backArmLate) drawBackArm();

    // ==== ЛИЦО ====
    if (!isTank) face(ctx, s, headCY, HR, aimA);

    // ==== ВСПЫШКА ПОПАДАНИЯ ====
    if (s.mode === "hit" && s.hitT < 1) {
      ctx.globalAlpha = 0.5 * (1 - s.hitT); ctx.fillStyle = "#fff";
      bodyPath(); ctx.fill(); ctx.globalAlpha = 1;
    }
    ctx.restore();

    // мяч в полёте (экранные координаты): у Снайпера — из дула, у прочих — из руки/груди
    if (s.flyProp != null) {
      var d = s.flyProp * 120, ox, oy;
      if (s.role === "Снайпер") {
        ox = cx + k * 52 * Math.cos(s.aimScreen);
        oy = cy + k * ((-BH * 0.24 + 2) + 52 * Math.sin(s.aimScreen));
      } else {
        ox = cx + Math.cos(s.aimScreen) * 22;
        oy = cy - R * s.scaleMul + Math.sin(s.aimScreen) * 22;
      }
      var px = ox + Math.cos(s.aimScreen) * d, py = oy + Math.sin(s.aimScreen) * d;
      if (s.role === "Бомбер" && s.mode === "ability") drawBomb(ctx, px, py, 7, s.t, true);
      else snowball(ctx, px, py, 6 * (1 - s.flyProp * 0.4), ow);
    }
    // контактная тень (в игре её рисует render.js — тогда s.noShadow)
    if (!s.noShadow) {
      ctx.save(); ctx.fillStyle = "rgba(0,0,0,0.14)";
      ctx.beginPath(); ctx.ellipse(cx, cy, R * 0.95 * s.scaleMul * (g.big || 1), R * 0.28 * s.scaleMul, 0, 0, TAU); ctx.fill();
      ctx.restore();
    }
  }

  function face(ctx, s, headCY, HR, aimA) {
    if (s.back) { // со спины — затылок
      ctx.strokeStyle = "rgba(120,150,185,0.5)"; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(0, headCY + 2, HR * 0.5, Math.PI * 0.2, Math.PI * 0.8); ctx.stroke();
      return;
    }
    var lx = Math.max(-3, Math.min(3, Math.cos(aimA) * 3));
    if (s.corrupt) {  // злые глаза-щёлочки, без румянца
      ctx.strokeStyle = "#1b1622"; ctx.lineWidth = 3.2; ctx.lineCap = "round";
      ctx.beginPath();
      ctx.moveTo(-9 + lx, headCY - 2); ctx.lineTo(-3 + lx, headCY + 1);
      ctx.moveTo(3 + lx, headCY + 1); ctx.lineTo(9 + lx, headCY - 2);
      ctx.stroke();
      if (s.role === "Фризер") {
        ctx.fillStyle = "rgba(180,220,240,0.7)";
        for (var ci = 0; ci < 3; ci++) { ctx.beginPath(); ctx.arc(10 + ci * 5, headCY + 6 + ci * 2, 2.4 - ci * 0.5, 0, TAU); ctx.fill(); }
      }
      return;
    }
    ctx.fillStyle = INK;
    ctx.beginPath(); ctx.arc(-5 + lx, headCY, 2, 0, TAU); ctx.arc(5 + lx, headCY, 2, 0, TAU); ctx.fill();
    ctx.fillStyle = "rgba(255,150,160,0.55)";
    ctx.beginPath(); ctx.ellipse(-9, headCY + 5, 3.2, 2.1, 0, 0, TAU); ctx.ellipse(9, headCY + 5, 3.2, 2.1, 0, 0, TAU); ctx.fill();
    if (s.role === "Фризер") { // морозный выдох
      ctx.fillStyle = "rgba(200,240,255,0.8)";
      for (var i = 0; i < 3; i++) { ctx.beginPath(); ctx.arc(10 + i * 5, headCY + 6 + i * 2, 2.4 - i * 0.5, 0, TAU); ctx.fill(); }
    }
  }

  // цепочка-«хвост» с инерцией: состояние по ключу, возвращает точки в локальных координатах
  var CH = {};
  function chain(key, ax, ay, restAng, seg, n, wob) {
    var st = CH[key] || (CH[key] = []);
    while (st.length < n) st.push(restAng);
    var pts = [[ax, ay]], x = ax, y = ay;
    for (var i = 0; i < n; i++) {
      var target = restAng + wob * (i + 1) * 0.6;
      st[i] += (target - st[i]) * 0.20;
      x += Math.cos(st[i]) * seg; y += Math.sin(st[i]) * seg;
      pts.push([x, y]);
    }
    return pts;
  }
  function stroke2(ctx, pts, w, col, ow) {
    ctx.lineCap = "round"; ctx.lineJoin = "round";
    ctx.strokeStyle = INK; ctx.lineWidth = w + ow;
    ctx.beginPath(); ctx.moveTo(pts[0][0], pts[0][1]);
    for (var i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
    ctx.stroke();
    ctx.strokeStyle = col; ctx.lineWidth = w;
    ctx.beginPath(); ctx.moveTo(pts[0][0], pts[0][1]);
    for (var j = 1; j < pts.length; j++) ctx.lineTo(pts[j][0], pts[j][1]);
    ctx.stroke();
  }

  // ГОЛОВНОЙ УБОР — главный признак роли (кроме Танка, у него шлем в drawModel)
  function headwear(ctx, s, BW, BH, HR, headCY, aimA, ow, wob) {
    var g = GEAR[s.role], acc = g.accent, top = headCY - HR;
    ctx.lineJoin = "round"; ctx.lineCap = "round";

    if (s.corrupt) {  // рваный капюшон вместо гира роли
      ctx.beginPath();
      ctx.moveTo(-HR - 2, headCY + 4);
      ctx.quadraticCurveTo(-HR - 4, headCY - HR - 8, 0, headCY - HR - 6);
      ctx.quadraticCurveTo(HR + 4, headCY - HR - 8, HR + 2, headCY + 4);
      ctx.quadraticCurveTo(HR - 4, headCY - 2, 0, headCY - 1);
      ctx.quadraticCurveTo(-HR + 4, headCY - 2, -HR - 2, headCY + 4);
      ctx.closePath();
      ctx.fillStyle = "#5a5368"; ctx.fill();
      ctx.lineWidth = ow; ctx.strokeStyle = INK; ctx.stroke();
      // рваные зубцы по нижней кромке капюшона
      ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
      for (var hh = -1; hh <= 1; hh++) {
        ctx.beginPath(); ctx.moveTo(hh * 10 - 4, headCY - 1);
        ctx.lineTo(hh * 10, headCY + 5); ctx.lineTo(hh * 10 + 4, headCY - 1); ctx.stroke();
      }
      return;
    }

    if (s.role === "Раннер") {
      // детская кепи-пропеллер: разноцветный купол + вертушка сверху
      var cols = ["#ff6b6b", "#ffd24a", "#4aa8ff", "#5fd08a"];
      var capR = HR + 1, capY = headCY - 2;
      var domePath = function () { ctx.beginPath(); ctx.arc(0, capY, capR, Math.PI, TAU); ctx.closePath(); };
      ctx.save(); domePath(); ctx.clip();
      for (var i = 0; i < cols.length; i++) {
        var a0 = Math.PI + i * Math.PI / cols.length, a1 = Math.PI + (i + 1) * Math.PI / cols.length;
        ctx.fillStyle = cols[s.flip < 0 ? cols.length - 1 - i : i];
        ctx.beginPath(); ctx.moveTo(0, capY); ctx.arc(0, capY, capR + 2, a0, a1); ctx.closePath(); ctx.fill();
      }
      ctx.restore();
      domePath(); ctx.strokeStyle = INK; ctx.lineWidth = ow; ctx.stroke();
      if (!s.back) { // козырёк вперёд
        var bd = s.flip < 0 ? -1 : 1;
        ctx.beginPath(); ctx.ellipse(bd * (HR - 3), capY + 1, 11, 4, bd * 0.12, 0, TAU);
        ctx.fillStyle = "#e8e0d4"; ctx.fill(); ctx.strokeStyle = INK; ctx.lineWidth = ow * 0.8; ctx.stroke();
      }
      // пуговка + стойка + пропеллер
      var tipY = capY - capR - 7;
      ctx.strokeStyle = INK; ctx.lineWidth = 2.4;
      ctx.beginPath(); ctx.moveTo(0, capY - capR + 2); ctx.lineTo(0, tipY); ctx.stroke();
      var spin = s.t * (s.mode === "run" ? 0.045 : s.mode === "ability" ? 0.06 : 0.012);
      ctx.save(); ctx.translate(0, tipY); ctx.rotate(spin);
      var pcol = ["#ff6b6b", "#4aa8ff", "#ffd24a"];
      for (var b = 0; b < 3; b++) {
        ctx.rotate(TAU / 3);
        ctx.fillStyle = pcol[b]; ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
        ctx.beginPath(); ctx.ellipse(9, 0, 9, 3.4, 0, 0, TAU); ctx.fill(); ctx.stroke();
      }
      ctx.restore();
      ctx.fillStyle = "#f4efe6"; ctx.beginPath(); ctx.arc(0, tipY + 1, 3, 0, TAU); ctx.fill();
      ctx.strokeStyle = INK; ctx.lineWidth = 1.4; ctx.stroke();
    } else if (s.role === "Снайпер") {
      // кепи: низкий купол + козырёк по направлению взгляда
      ctx.fillStyle = "#6f7a8c"; ctx.strokeStyle = INK; ctx.lineWidth = ow;
      ctx.beginPath(); ctx.arc(0, headCY - 3, HR - 1, Math.PI * 1.02, TAU - 0.02); ctx.closePath(); ctx.fill(); ctx.stroke();
      if (!s.back) {
        var bd = s.flip < 0 ? -1 : 1;
        ctx.beginPath(); ctx.ellipse(bd * (HR - 4), headCY - 5, 12, 4, bd * 0.15, 0, TAU);
        ctx.fillStyle = "#5b6577"; ctx.fill(); ctx.stroke();
      }
    } else if (s.role === "Фризер") {
      // ледяная корона из кристаллов + иней по ободу
      ctx.fillStyle = acc; ctx.strokeStyle = INK; ctx.lineWidth = ow;
      for (var c = -1; c <= 1; c++) {
        var bx = c * 11, hgt = c === 0 ? 20 : 13;
        ctx.beginPath();
        ctx.moveTo(bx - 6, top + 6); ctx.lineTo(bx, top + 6 - hgt); ctx.lineTo(bx + 6, top + 6);
        ctx.closePath(); ctx.fill(); ctx.stroke();
      }
      ctx.fillStyle = "#dff6ff";
      for (var r2 = 0; r2 < 5; r2++) { var a2 = Math.PI + r2 * Math.PI / 4; ctx.beginPath(); ctx.arc(Math.cos(a2) * HR, headCY + Math.sin(a2) * HR, 2.4, 0, TAU); ctx.fill(); }
    } else if (s.role === "Бомбер") {
      // вязаная шапка с помпоном, помпон болтается
      var capPath = function () { ctx.beginPath(); ctx.arc(0, headCY - 2, HR, Math.PI * 1.06, TAU - Math.PI * 0.06); ctx.closePath(); };
      ctx.save(); capPath(); ctx.fillStyle = "#c9433a"; ctx.fill();
      capPath(); ctx.clip();
      ctx.strokeStyle = "rgba(0,0,0,0.12)"; ctx.lineWidth = 2;
      for (var kn = -HR; kn < HR; kn += 5) { ctx.beginPath(); ctx.moveTo(kn, top - 4); ctx.lineTo(kn, headCY); ctx.stroke(); }
      ctx.restore();
      capPath(); ctx.strokeStyle = INK; ctx.lineWidth = ow; ctx.stroke();
      // отворот
      ctx.strokeStyle = "#e8e0d4"; ctx.lineWidth = 7;
      ctx.beginPath(); ctx.moveTo(-HR + 1, headCY - 4); ctx.quadraticCurveTo(0, headCY + 2, HR - 1, headCY - 4); ctx.stroke();
      ctx.strokeStyle = INK; ctx.lineWidth = 1.4; ctx.stroke();
      // стойка помпона + помпон
      var pc = chain("bomb_pom", 0, top + 2, -Math.PI / 2, 7, 2, wob);
      stroke2(ctx, pc, 3.5, "#e8e0d4", ow);
      var end = pc[pc.length - 1];
      ctx.beginPath(); ctx.arc(end[0], end[1], 6.5, 0, TAU);
      ctx.fillStyle = "#f4efe6"; ctx.fill(); ctx.strokeStyle = INK; ctx.lineWidth = ow; ctx.stroke();
    } else if (s.role === "Щит") {
      // стальной шлем-каска с наносником
      var helm = function () { ctx.beginPath(); ctx.arc(0, headCY - 1, HR + 1, Math.PI * 1.0, TAU); ctx.lineTo(HR + 1, headCY + 1); ctx.lineTo(-HR - 1, headCY + 1); ctx.closePath(); };
      shaded(ctx, helm, "#aeb9c6", "rgba(120,140,165,0.5)", { sx: -6, sy: headCY - 4, sr: HR * 0.9, hx: 7, hy: headCY - 12, hr: 7 }, ow);
      if (!s.back) {
        ctx.fillStyle = "#8b97a6"; ctx.strokeStyle = INK; ctx.lineWidth = ow * 0.8;
        ctx.beginPath(); ctx.rect(-3, headCY - 2, 6, 12); ctx.fill(); ctx.stroke();
      }
    }
  }

  function gearBody(ctx, s, BW, BH, HR, headCY, aimA, ow) {
    if (s.corrupt) return;  // у порченых нательного гира нет
    ctx.lineJoin = "round"; ctx.lineCap = "round";

    if (s.role === "Раннер") {
      if (s.mode === "run") {
        ctx.strokeStyle = "rgba(255,255,255,0.7)"; ctx.lineWidth = 2.5;
        for (var r = 0; r < 3; r++) { var yy = -BH * 0.15 + r * 12; ctx.beginPath(); ctx.moveTo(-BW * 0.5, yy); ctx.lineTo(-BW * 0.5 - 16 - r * 5, yy); ctx.stroke(); }
      }
      if (s.back) { // со спины: лямки маленького рюкзачка
        ctx.strokeStyle = "#4aa8ff"; ctx.lineWidth = 5;
        ctx.beginPath(); ctx.moveTo(-BW * 0.22, -BH * 0.34); ctx.lineTo(-BW * 0.14, BH * 0.06);
        ctx.moveTo(BW * 0.22, -BH * 0.34); ctx.lineTo(BW * 0.14, BH * 0.06); ctx.stroke();
        ctx.fillStyle = "#3a8fe0"; ctx.strokeStyle = INK; ctx.lineWidth = ow * 0.8;
        ctx.beginPath(); ctx.roundRect ? ctx.roundRect(-BW * 0.22, -BH * 0.18, BW * 0.44, BH * 0.34, 6) : ctx.rect(-BW * 0.22, -BH * 0.18, BW * 0.44, BH * 0.34);
        ctx.fill(); ctx.stroke();
      }
      return;
    }

    if (s.back) return;   // со спины нательный гир не виден

    // нательный гир клипуем по силуэту тела, чтобы ничего не торчало
    ctx.save();
    ctx.beginPath();
    ctx.moveTo(0, -BH * 0.5);
    ctx.bezierCurveTo(BW * 0.5, -BH * 0.5, BW * 0.5 + 6, BH * 0.5 - 12, 0, BH * 0.5);
    ctx.bezierCurveTo(-BW * 0.5 - 6, BH * 0.5 - 12, -BW * 0.5, -BH * 0.5, 0, -BH * 0.5);
    ctx.clip();

    if (s.role === "Фризер") {
      ctx.strokeStyle = "#4fb8d6"; ctx.lineWidth = 3.4;
      var cys = BH * 0.06, rr = 13;
      for (var a = 0; a < 3; a++) {
        var ang = a * Math.PI / 3;
        ctx.beginPath();
        ctx.moveTo(-Math.cos(ang) * rr, cys - Math.sin(ang) * rr);
        ctx.lineTo(Math.cos(ang) * rr, cys + Math.sin(ang) * rr);
        ctx.stroke();
        ctx.beginPath();
        ctx.moveTo(Math.cos(ang) * rr * 0.55, cys + Math.sin(ang) * rr * 0.55);
        ctx.lineTo(Math.cos(ang) * rr * 0.85 + Math.cos(ang + 1) * 4, cys + Math.sin(ang) * rr * 0.85 + Math.sin(ang + 1) * 4);
        ctx.stroke();
      }
    } else if (s.role === "Бомбер") {
      ctx.strokeStyle = "#7c4a22"; ctx.lineWidth = 7;
      ctx.beginPath(); ctx.moveTo(-BW * 0.34, -BH * 0.26); ctx.lineTo(BW * 0.32, BH * 0.06); ctx.stroke();
      ctx.lineWidth = 6;
      ctx.beginPath(); ctx.moveTo(-BW * 0.4, BH * 0.24); ctx.quadraticCurveTo(0, BH * 0.30, BW * 0.4, BH * 0.24); ctx.stroke();
      ctx.fillStyle = "#d0a040"; ctx.fillRect(-4, BH * 0.21, 8, 8); ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.strokeRect(-4, BH * 0.21, 8, 8);
      for (var b = 0; b < 3; b++) {
        var t = b / 2, bx = -BW * 0.24 + t * BW * 0.4, by = -BH * 0.12 + t * BH * 0.26;
        ctx.beginPath(); ctx.arc(bx, by, 5, 0, TAU); ctx.fillStyle = "#20303f"; ctx.fill();
        ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.stroke();
        ctx.fillStyle = "#8a7a5a"; ctx.fillRect(bx - 1.5, by - 6.5, 3, 3);   // горловина, без искры
      }
    } else if (s.role === "Танк") {
      ctx.strokeStyle = "rgba(40,58,78,0.5)"; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(-BW * 0.42, -BH * 0.1); ctx.quadraticCurveTo(0, -BH * 0.02, BW * 0.42, -BH * 0.1); ctx.stroke();
      ctx.beginPath(); ctx.moveTo(-BW * 0.42, BH * 0.16); ctx.quadraticCurveTo(0, BH * 0.24, BW * 0.42, BH * 0.16); ctx.stroke();
      ctx.strokeStyle = "#e6edf4"; ctx.lineWidth = 5;
      ctx.beginPath(); ctx.moveTo(-BW * 0.24, -BH * 0.18); ctx.lineTo(0, -BH * 0.02); ctx.lineTo(BW * 0.24, -BH * 0.18); ctx.stroke();
      ctx.fillStyle = "#8ea3bd";
      for (var rv = 0; rv < 5; rv++) { ctx.beginPath(); ctx.arc(-BW * 0.4 + rv * BW * 0.2, BH * 0.30, 1.8, 0, TAU); ctx.fill(); }
    }
    ctx.restore();
  }

  return { drawFighter: drawModel, GEAR: GEAR, ROLES: Object.keys(GEAR) };
})();
