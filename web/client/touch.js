/* Сенсорное управление: два плавающих стика и кнопка способности поверх SBIntent.
 * Левая зона — движение, правая — замах/прицел/бросок (отпустить в мёртвой зоне — отмена).
 * Для Щита кнопка способности работает как мини-стик: отвёл — поставил стену по направлению. */
window.SBTouch = (function () {
  var RADIUS = 60;   // CSS px: ход ручки стика
  var DEAD = 12;     // CSS px: мёртвая зона (в ней стик «не отклонён»)

  /**
   * create(o):
   *  o.layer, o.zoneL, o.zoneR, o.stickL, o.stickR — элементы слоя управления
   *  o.ability, o.stickS — кнопка способности и её мини-стик
   *  o.intent — SBIntent
   *  o.getMe() → мой боец из последнего снапшота
   *  o.hasDirSpecial(role) → нужна ли способности направление
   */
  function create(o) {
    var L = null, R = null, S = null; // активные касания: {id, cx, cy, dir, inDead}

    function layerPos(e) {
      var r = o.layer.getBoundingClientRect();
      return { x: e.clientX - r.left, y: e.clientY - r.top };
    }
    function showStick(stick, x, y) {
      stick.style.left = x + 'px'; stick.style.top = y + 'px';
      knob(stick, 0, 0); stick.hidden = false;
    }
    function knob(stick, dx, dy) { stick.firstElementChild.style.transform = 'translate(' + dx + 'px,' + dy + 'px)'; }
    /** Прочитать отклонение от центра касания в p и подвинуть ручку. */
    function read(p, stick, e) {
      var dx = e.clientX - p.cx, dy = e.clientY - p.cy, len = Math.hypot(dx, dy);
      var k = len > RADIUS ? RADIUS / len : 1;
      knob(stick, dx * k, dy * k);
      if (len > DEAD) { p.dir = { x: dx / len, y: dy / len }; p.inDead = false; }
      else { p.dir = null; p.inDead = true; }
    }
    function start(e, zone) {
      var pos = layerPos(e);
      try { zone.setPointerCapture(e.pointerId); } catch (err) { /* не критично */ }
      return { id: e.pointerId, cx: e.clientX, cy: e.clientY, lx: pos.x, ly: pos.y, dir: null, inDead: true };
    }
    function bind(el, handlers) {
      el.addEventListener('pointerdown', function (e) { e.preventDefault(); handlers.down(e); });
      el.addEventListener('pointermove', function (e) { handlers.move(e); });
      el.addEventListener('pointerup', function (e) { handlers.up(e, false); });
      el.addEventListener('pointercancel', function (e) { handlers.up(e, true); });
      el.addEventListener('contextmenu', function (e) { e.preventDefault(); });
    }

    // ---- левый стик: движение ----
    bind(o.zoneL, {
      down: function (e) {
        if (L) return;
        L = start(e, o.zoneL);
        showStick(o.stickL, L.lx, L.ly);
      },
      move: function (e) {
        if (!L || e.pointerId !== L.id) return;
        read(L, o.stickL, e);
        o.intent.setMoveDir(L.dir);
      },
      up: function (e) {
        if (!L || e.pointerId !== L.id) return;
        o.stickL.hidden = true;
        o.intent.setMoveDir(null);
        L = null;
      }
    });

    // ---- правый стик: замах → прицел → бросок / отмена ----
    bind(o.zoneR, {
      down: function (e) {
        if (R) return;
        R = start(e, o.zoneR);
        showStick(o.stickR, R.lx, R.ly);
        R.charging = o.intent.chargeStartDir(null);
      },
      move: function (e) {
        if (!R || e.pointerId !== R.id) return;
        read(R, o.stickR, e);
        if (R.charging && R.dir) o.intent.setAimDir(R.dir);
      },
      up: function (e, cancelled) {
        if (!R || e.pointerId !== R.id) return;
        o.stickR.hidden = true;
        if (R.charging) {
          if (!cancelled && R.dir && !R.inDead) o.intent.throwDir(R.dir);
          else o.intent.cancelCharge();
        }
        R = null;
      }
    });

    // ---- кнопка способности; для направленной способности — мини-стик ----
    bind(o.ability, {
      down: function (e) {
        if (S) return;
        var me = o.getMe();
        S = start(e, o.ability);
        S.directional = !!(me && o.hasDirSpecial(me.role));
        if (S.directional) {
          var r = o.ability.getBoundingClientRect(), lr = o.layer.getBoundingClientRect();
          S.cx = r.left + r.width / 2; S.cy = r.top + r.height / 2;
          showStick(o.stickS, S.cx - lr.left, S.cy - lr.top);
        }
      },
      move: function (e) {
        if (!S || e.pointerId !== S.id || !S.directional) return;
        read(S, o.stickS, e);
      },
      up: function (e, cancelled) {
        if (!S || e.pointerId !== S.id) return;
        o.stickS.hidden = true;
        if (!cancelled) {
          if (!S.directional) o.intent.specialDir(null);
          else if (S.dir && !S.inDead) o.intent.specialDir(S.dir);
        }
        S = null;
      }
    });

    return {
      /** Сбросить касания (конец матча, смена экрана). */
      reset: function () {
        L = R = S = null;
        o.stickL.hidden = true; o.stickR.hidden = true; o.stickS.hidden = true;
      }
    };
  }

  return { create: create, RADIUS: RADIUS, DEAD: DEAD };
})();
