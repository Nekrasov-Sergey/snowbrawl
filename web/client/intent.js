/* Слой намерений: превращает ввод любого устройства (мышь, виртуальные стики) в команды
 * протокола move / chargeStart / aim / throw / cancelCharge / special и сам ограничивает
 * частоту отправки. Протокол принимает только точки арены, поэтому направления стиков
 * здесь превращаются в точки впереди бойца. Один экземпляр на приложение. */
window.SBIntent = (function () {
  var Sim = window.SnowBrawlSim;
  var MOVE_LEAD = 90;       // px впереди бойца: цель движения по стику (sim идёт к точке с фиксированной скоростью)
  var STOP_LEAD_S = 0.08;   // с: сколько сервер успеет пройти, пока получит «стоп» — чтобы боец не пятился
  var AIM_LEAD = 200;       // px: точка прицела по стику (дальность задаёт power, а не удалённость точки)
  // Частота отправки. Раньше здесь был один порог 66 мс на вид команды, и движение с прицелом
  // уходили по таймеру независимо от того, изменилось ли что-нибудь: 15 + 15 сообщений/с,
  // то есть весь лимит сервера ещё до выстрелов — активный бой отстреливался по rate limit.
  // Теперь шлём по изменению: поворот доезжает сразу, а повтор нужен только как страховка.
  // Цель движения — точка впереди бойца, её надо обновлять (см. tick). Срок считаем по самому
  // быстрому случаю: Раннер 195 px/с, пассив «Второе дыхание» ×1.20 и лёд «Реки» ×1.35 дают
  // 316 px/с, то есть MOVE_LEAD в 90 px съедается за 285 мс. Плюс точка берётся из снапшота,
  // который уже старше на полкруга сети. 120 мс оставляют запас даже на один потерянный пакет.
  var MOVE_KEEPALIVE_MS = 120;
  // Нижний порог для движения оставлен прежним (66 мс): поворот доезжает до сервера не медленнее,
  // чем раньше, а вот бессмысленные повторы одного и того же направления исчезают. Упереться в
  // него может только стик, который крутят быстрее 15 раз в секунду.
  var MOVE_MIN_MS = 66;
  var MOVE_PX = 6;             // смещение точки движения мышью, меньше которого отправка не нужна
  var AIM_MIN_MS = 80;         // не чаще этого шлём прицел
  var AIM_PX = 10;             // смещение точки прицела, меньше которого отправка не нужна
  var TURN_COS = 0.99;         // ~8°: поворот меньше этого не повод для отправки

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
    var sentMoveDir = null;          // направление, которое сервер уже знает (для порога поворота)
    var sentMovePt = null, sentAimPt = null; // последние отправленные точки (для порога смещения)
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
      sentAimPt = { x: x, y: y }; lastAimSend = now(); // chargeStart уже сообщил прицел
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
    // Можно ли начать замах: не идёт пауза между выстрелами и в запасе есть хотя бы одно
    // отделение. Поле am появилось в sim 1.12.0 — на старом снапшоте гварда вырождается в паузу.
    //
    // Третье условие — своя пауза по последнему выстрелу. Снапшот приходит с задержкой сети и
    // тика, поэтому сразу после броска он ещё показывает до-выстрельные rl и am. Без этой
    // проверки очередь из трёх снежков давала фантомный замах: клиент начинал его локально,
    // сервер отклонял, и бросок пропадал целиком вместе с анимацией.
    var SHOT_GAP_MS = (Sim && Sim.SHOT_GAP_MS) || 250;
    var firedAt = -1e9;
    function canShootNow(p) {
      return !(p.rl > 0) && (p.am == null || p.am >= 1) && now() - firedAt >= SHOT_GAP_MS;
    }
    function clearPending() { pending = null; local.pending = false; }

    var api = {
      local: local,

      // ---- абсолютные точки арены (мышь) ----
      moveTo: function (x, y, force) {
        var t = now();
        if (!force) {
          if (t - lastMoveSend < MOVE_MIN_MS) return;
          // Точка абсолютная: пока курсор стоит, серверу нечего сообщать. Страховочный повтор
          // всё равно оставляем — на случай потерянного пакета.
          var still = sentMovePt && Math.hypot(x - sentMovePt.x, y - sentMovePt.y) < MOVE_PX;
          if (still && t - lastMoveSend < MOVE_KEEPALIVE_MS) return;
        }
        lastMoveSend = t; sentMovePt = { x: x, y: y }; sentMoveDir = null;
        send('move', x, y);
      },
      chargeStartAt: function (x, y) {
        var p = me();
        if (!p || !o.canAct(p) || local.charging || pending || blocked()) return false;
        if (!canShootNow(p)) { // запас пуст или идёт пауза: замах начнётся сам по готовности
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
        if (t - lastAimSend < AIM_MIN_MS) return;
        // Прицел абсолютный, сервер держит последний — страховочный повтор не нужен. На точность
        // броска порог не влияет: throw несёт свои x/y и сам выставляет прицел в симуляции,
        // промежуточные aim нужны лишь для того, чтобы ДРУГИЕ видели, куда целится боец.
        if (sentAimPt && Math.hypot(x - sentAimPt.x, y - sentAimPt.y) < AIM_PX) return;
        lastAimSend = t; sentAimPt = { x: x, y: y };
        send('aim', x, y);
      },
      throwAt: function (x, y) {
        if (pending) { clearPending(); return; } // отпустил раньше, чем закончилась перезарядка
        if (!local.charging) return;
        var pw = chargePower();
        endCharge();
        firedAt = now();
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
          sentMoveDir = null; // серверу отправлена точка остановки, а не направление
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
          else if (canShootNow(p)) { // заряд появился — начинаем отложенный замах
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
          // Повернули — шлём сразу; иначе раз в MOVE_KEEPALIVE_MS. Повтор здесь обязателен, а не
          // косметика: цель — точка в MOVE_LEAD (90 px) впереди бойца, и если её не обновлять,
          // боец до неё дойдёт и встанет. 90 px на максимальной скорости 195 px/с — это 460 мс,
          // так что 200 мс дают запас больше двух раз даже при потерянном пакете.
          var turned = !sentMoveDir ||
            sentMoveDir.x * moveDir.x + sentMoveDir.y * moveDir.y < TURN_COS;
          var due = t - lastMoveSend >= MOVE_KEEPALIVE_MS;
          if ((turned || due) && t - lastMoveSend >= MOVE_MIN_MS) {
            lastMoveSend = t;
            sentMoveDir = moveDir; sentMovePt = null;
            send('move', p.x + moveDir.x * MOVE_LEAD, p.y + moveDir.y * MOVE_LEAD);
          }
        }
      },
      /** Есть ли активное намерение движения (для переотправки после возврата вкладки). */
      isMoving: function () { return !!moveDir; },
      reset: function () {
        if (local.charging) endCharge();
        clearPending();
        moveDir = null;
        sentMoveDir = null; sentMovePt = null; sentAimPt = null;
        firedAt = -1e9;
      }
    };
    return api;
  }

  return { create: create };
})();
