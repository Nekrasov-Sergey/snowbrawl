/* Слой намерений: превращает ввод любого устройства (мышь, виртуальные стики) в команды
 * протокола move / chargeStart / aim / throw / cancelCharge / special и сам ограничивает
 * частоту отправки. Протокол принимает только точки арены, поэтому направления стиков
 * здесь превращаются в точки впереди бойца. Один экземпляр на приложение. */
window.SBIntent = (function () {
  var Sim = window.SnowBrawlSim;
  var MOVE_LEAD = 90;       // px впереди бойца: цель движения по стику (sim идёт к точке с фиксированной скоростью)
  var STOP_LEAD_S = 0.08;   // с: сколько сервер успеет пройти, пока получит «стоп» — чтобы боец не пятился
  var AIM_LEAD = 200;       // px: точка прицела по стику (дальность задаёт power, а не удалённость точки)
  var SEND_MS = 66;         // не чаще ~15/с на вид команды; лимит сервера — 30 сообщений/с на всё

  function norm(x, y) { var d = Math.hypot(x, y); return d > 1e-6 ? { x: x / d, y: y / d } : null; }

  /**
   * create(o):
   *  o.getGame()     → активный матч {input(kind,x,y,power), lastSnap, over} или null
   *  o.getMe(snap)   → мой боец из снапшота
   *  o.canAct(p)     → может ли боец действовать (жив, не оглушён)
   *  o.onChargeStart(), o.onChargeEnd() — хуки (звук замаха)
 *  o.onThrow(power), o.onSpecial() — хуки состоявшегося действия: события applyInput до клиента
 *    не доходят (step обнуляет state.events), поэтому обучение считает шаги по ним
   *  o.blocked()     → true, пока команды принимать нельзя (отсчёт перед стартом)
   *  o.holding()     → зажата ли кнопка (стик) замаха прямо сейчас; без неё отложенный
   *    перезарядкой замах начинался бы сам при уже отпущенной кнопке
   * Возвращает api; api.local — {charging, power, aimX, aimY, pending} для мгновенного отклика
   * в рендере. pending — нажатие во время перезарядки: замах начнётся сам, как только она пройдёт.
   */
  function create(o) {
    var local = { charging: false, start: 0, aimX: 0, aimY: 0, power: 0, pending: false };
    var moveDir = null, lastMoveSend = 0, lastAimSend = 0;
    var pending = null; // {x, y, dir} — нажатие, отложенное до конца перезарядки
    var lastAimDir = { x: 1, y: 0 };
    var lastMoveVec = null; // куда боец бежал последний раз: направление тапа по кнопке скилла

    function game() { var g = o.getGame(); return g && !g.over ? g : null; }
    function me() { var g = o.getGame(); return g ? o.getMe(g.lastSnap) : null; }
    function blocked() { return !!(o.blocked && o.blocked()); }
    function send(kind, x, y, power) { var g = game(); if (g && !blocked()) g.input(kind, x, y, power); }
    function now() { return performance.now(); }
    function speedOf(p) { return (Sim.ROLE_STATS[p.role] || { speed: 150 }).speed; }
    function chargePower() { return Math.min((now() - local.start) / Sim.CHARGE_FULL_MS, 1); }
    function aimPoint(dir, p) { return { x: p.x + dir.x * AIM_LEAD, y: p.y + dir.y * AIM_LEAD }; }
    function beginCharge(x, y) {
      local.charging = true; local.start = now(); local.aimX = x; local.aimY = y; local.power = 0;
      send('chargeStart', x, y);
      if (o.onChargeStart) o.onChargeStart();
    }
    function endCharge() { local.charging = false; local.power = 0; if (o.onChargeEnd) o.onChargeEnd(); }
    // Можно ли применить способность прямо сейчас. Гвардия нужна не серверу (он отбросит ввод
    // сам), а хуку обучения: без неё шаг «примените способность» закрывался нажатием на
    // кулдауне, то есть тем, чего в матче не произошло.
    function canSpecial() {
      var p = me();
      return !!p && !blocked() && o.canAct(p) && !(p.cd > 0) && !!Sim.SPECIALS[p.role];
    }
    function clearPending() { pending = null; local.pending = false; }

    var api = {
      local: local,

      // ---- абсолютные точки арены (мышь) ----
      moveTo: function (x, y, force) {
        var t = now();
        if (!force && t - lastMoveSend < SEND_MS) return;
        lastMoveSend = t; send('move', x, y);
      },
      chargeStartAt: function (x, y) {
        var p = me();
        if (!p || !o.canAct(p) || local.charging || pending || blocked()) return false;
        if (p.rl > 0) { // идёт перезарядка: запоминаем нажатие, замах начнётся сам по готовности
          pending = { x: x, y: y, dir: false };
          local.pending = true;
          return true;
        }
        beginCharge(x, y);
        return true;
      },
      aimAt: function (x, y) {
        if (pending) { if (!pending.dir) { pending.x = x; pending.y = y; } return; }
        if (!local.charging) return;
        local.aimX = x; local.aimY = y;
        var t = now();
        if (t - lastAimSend < SEND_MS) return;
        lastAimSend = t; send('aim', x, y);
      },
      throwAt: function (x, y) {
        if (pending) { clearPending(); return; } // отпустил раньше, чем закончилась перезарядка
        if (!local.charging) return;
        var pw = chargePower();
        endCharge();
        send('throw', x, y, pw);
        if (o.onThrow) o.onThrow(pw);
      },
      cancelCharge: function () {
        if (pending) { clearPending(); return; }
        if (!local.charging) return;
        endCharge();
        send('cancelCharge', 0, 0);
      },
      specialAt: function (x, y) {
        if (!canSpecial()) return;
        send('special', x, y); if (o.onSpecial) o.onSpecial();
      },

      // ---- направления (стики); dir — нормированный вектор или null ----
      /** Держать направление движения; null — остановиться. */
      setMoveDir: function (dir) {
        var p = me();
        if (dir) lastMoveVec = norm(dir.x, dir.y) || lastMoveVec;
        if (!dir) {
          if (moveDir && p) {
            var lead = speedOf(p) * STOP_LEAD_S;
            send('move', p.x + moveDir.x * lead, p.y + moveDir.y * lead);
          }
          moveDir = null;
          return;
        }
        var wasIdle = !moveDir;
        moveDir = norm(dir.x, dir.y) || moveDir;
        if (wasIdle) { lastMoveSend = 0; api.tick(); }
      },
      /** Начать замах; dir — начальный прицел, null — последнее направление. */
      chargeStartDir: function (dir) {
        var p = me();
        if (!p) return false;
        if (dir) lastAimDir = dir;
        var pt = aimPoint(lastAimDir, p);
        var ok = api.chargeStartAt(pt.x, pt.y);
        if (ok && pending) pending.dir = true; // стик крутится, точку пересчитаем в момент старта
        return ok;
      },
      setAimDir: function (dir) {
        var p = me();
        if (!p || !dir) return;
        if (pending) { lastAimDir = dir; return; }
        if (!local.charging) return;
        lastAimDir = dir;
        var pt = aimPoint(dir, p);
        api.aimAt(pt.x, pt.y);
      },
      throwDir: function (dir) {
        var p = me();
        if (pending) { clearPending(); return; }
        if (!p) { api.cancelCharge(); return; }
        if (dir) lastAimDir = dir;
        var pt = aimPoint(lastAimDir, p);
        local.aimX = pt.x; local.aimY = pt.y;
        api.throwAt(pt.x, pt.y);
      },
      /**
       * Способность по направлению. null — короткий тап без отвода пальца: целимся туда, куда
       * боец бежит (а не по последнему прицелу: в бою это чаще всего «назад через плечо»).
       */
      specialDir: function (dir) {
        var p = me();
        if (!p || !canSpecial()) return;
        if (dir) lastAimDir = dir;
        else if (lastMoveVec) lastAimDir = lastMoveVec;
        send('special', p.x + lastAimDir.x * AIM_LEAD, p.y + lastAimDir.y * AIM_LEAD);
        // Хук обучения: раньше его звал только specialAt, то есть путь мыши. На телефоне
        // способность применяется исключительно отсюда, и шаг «примените способность»
        // не закрывался вообще, а замок соперника не снимался — тот вечно висел на 1 HP.
        if (o.onSpecial) o.onSpecial();
      },

      /** Раз в кадр: переотправка цели движения, сила замаха, сброс замаха при оглушении/KO. */
      tick: function () {
        var p = me();
        if (pending) {
          if (!p || !o.canAct(p) || blocked()) clearPending();
          else if (o.holding && !o.holding()) clearPending(); // кнопку уже отпустили — замаха не будет
          else if (!(p.rl > 0)) { // перезарядка закончилась — начинаем отложенный замах
            var pt = pending.dir ? aimPoint(lastAimDir, p) : { x: pending.x, y: pending.y };
            clearPending();
            beginCharge(pt.x, pt.y);
          }
        }
        if (local.charging) {
          local.power = chargePower();
          if (!p || !o.canAct(p)) endCharge();
        }
        if (moveDir && p) {
          var t = now();
          if (t - lastMoveSend >= SEND_MS) {
            lastMoveSend = t;
            send('move', p.x + moveDir.x * MOVE_LEAD, p.y + moveDir.y * MOVE_LEAD);
          }
        }
      },
      /** Есть ли активное намерение движения (для переотправки после возврата вкладки). */
      isMoving: function () { return !!moveDir; },
      reset: function () { if (local.charging) endCharge(); clearPending(); moveDir = null; }
    };
    return api;
  }

  return { create: create };
})();
