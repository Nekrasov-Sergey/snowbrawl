/* Сетевой слой: WebSocket с автопереподключением и очередь снапшотов с интерполяцией. */
window.SBNet = (function () {
  var PROTO = 1;

  function connect(opts) {
    var ws = null, closedByUser = false, attempt = 0, timer = null, state = 'connecting';
    var api = {
      get state() { return state; },
      send: function (type, data) {
        if (!ws || ws.readyState !== 1) return false;
        ws.send(JSON.stringify({ t: type, d: data === undefined ? null : data }));
        return true;
      },
      close: function () { closedByUser = true; clearTimeout(timer); if (ws) ws.close(1000, 'bye'); },
      reconnectNow: function () { clearTimeout(timer); if (ws) { try { ws.close(); } catch (e) { /* игнор */ } } else open(); }
    };
    function url() {
      var proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
      return proto + location.host + '/ws';
    }
    function open() {
      state = 'connecting'; opts.onState && opts.onState(state);
      try { ws = new WebSocket(url()); } catch (e) { schedule(); return; }
      ws.onopen = function () {
        attempt = 0; state = 'open'; opts.onState && opts.onState(state);
        var hello = opts.hello();
        hello.proto = PROTO;
        api.send('hello', hello);
      };
      ws.onmessage = function (ev) {
        var msg;
        try { msg = JSON.parse(ev.data); } catch (e) { return; }
        if (!msg || !msg.t) return;
        opts.onMessage(msg.t, msg.d);
      };
      ws.onclose = function (ev) {
        ws = null; state = 'closed'; opts.onState && opts.onState(state, ev.reason);
        if (!closedByUser) schedule();
      };
      ws.onerror = function () { /* onclose придёт следом */ };
    }
    function schedule() {
      attempt++;
      var delay = Math.min(10000, 500 * Math.pow(2, Math.min(attempt, 5)));
      timer = setTimeout(open, delay);
    }
    open();
    return api;
  }

  /** Буфер снапшотов и интерполяция с задержкой interpDelayMs (≈2 тика). */
  function snapshotBuffer(tickRate) {
    var buf = [], delay = Math.round(2000 / (tickRate || 20));
    var latest = null;
    // Джиттер: отклонение интервала между приходами снапшотов от номинала, максимум за 3 с.
    var tickMs = 1000 / (tickRate || 20), jit = [];
    return {
      push: function (snap) {
        var now = performance.now();
        if (latest) {
          jit.push({ t: now, dev: Math.abs(now - latest.recvAt - tickMs) });
          while (jit.length && now - jit[0].t > 3000) jit.shift();
        }
        buf.push({ recvAt: now, snap: snap });
        latest = buf[buf.length - 1];
        // Оставляем ~1.5 с истории.
        while (buf.length > 2 && now - buf[0].recvAt > 1500) buf.shift();
      },
      latest: function () { return latest ? latest.snap : null; },
      /** Интерполированный снапшот на «сейчас минус задержка». */
      current: function () {
        if (!latest) return null;
        var now = performance.now();
        var serverNow = latest.snap.time + (now - latest.recvAt);
        var renderTime = serverNow - delay;
        if (renderTime >= latest.snap.time) return latest.snap;
        for (var i = buf.length - 1; i > 0; i--) {
          var b = buf[i], a = buf[i - 1];
          if (a.snap.time <= renderTime && renderTime <= b.snap.time) {
            var span = b.snap.time - a.snap.time || 1;
            return window.SBRender.lerpSnap(a.snap, b.snap, (renderTime - a.snap.time) / span);
          }
        }
        return buf[0].snap;
      },
      /** Максимальный джиттер прихода снапшотов за последние 3 с, мс. */
      jitter: function () { var m = 0; for (var i = 0; i < jit.length; i++) if (jit[i].dev > m) m = jit[i].dev; return m; },
      clear: function () { buf = []; latest = null; jit = []; }
    };
  }

  return { connect: connect, snapshotBuffer: snapshotBuffer, PROTO: PROTO };
})();
