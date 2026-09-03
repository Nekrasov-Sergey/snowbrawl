/* Звук: синтез через Web Audio API, без внешних файлов. Только клиент. */
window.SBAudio = (function () {
  var ctx = null, noiseBuffer = null, enabled = true;
  try { enabled = localStorage.getItem('sb.sound') !== 'off'; } catch (e) { /* приватный режим */ }

  function ensureCtx() {
    if (!ctx) {
      ctx = new (window.AudioContext || window.webkitAudioContext)();
      noiseBuffer = ctx.createBuffer(1, ctx.sampleRate, ctx.sampleRate);
      var data = noiseBuffer.getChannelData(0);
      for (var i = 0; i < data.length; i++) data[i] = Math.random() * 2 - 1;
    }
    if (ctx.state === 'suspended') ctx.resume();
    return ctx;
  }
  function tone(o) {
    if (!enabled) return;
    var c = ensureCtx();
    var freq = o.freq || 440, duration = o.duration || 0.15, type = o.type || 'sine', gain = o.gain || 0.2, delay = o.delay || 0;
    var osc = c.createOscillator(), g = c.createGain();
    osc.type = type;
    osc.frequency.setValueAtTime(freq, c.currentTime + delay);
    if (o.glideTo != null) osc.frequency.exponentialRampToValueAtTime(Math.max(o.glideTo, 1), c.currentTime + delay + duration);
    g.gain.setValueAtTime(gain, c.currentTime + delay);
    g.gain.exponentialRampToValueAtTime(0.001, c.currentTime + delay + duration);
    osc.connect(g); g.connect(c.destination);
    osc.start(c.currentTime + delay); osc.stop(c.currentTime + delay + duration + 0.03);
  }
  function noiseBurst(o) {
    if (!enabled) return;
    var c = ensureCtx();
    var duration = o.duration || 0.15, filterFreq = o.filterFreq || 1200, gain = o.gain || 0.3, delay = o.delay || 0;
    var src = c.createBufferSource(); src.buffer = noiseBuffer;
    var filter = c.createBiquadFilter(); filter.type = o.type || 'lowpass'; filter.frequency.value = filterFreq;
    var g = c.createGain();
    g.gain.setValueAtTime(gain, c.currentTime + delay);
    g.gain.exponentialRampToValueAtTime(0.001, c.currentTime + delay + duration);
    src.connect(filter); filter.connect(g); g.connect(c.destination);
    src.start(c.currentTime + delay); src.stop(c.currentTime + delay + duration + 0.03);
  }
  return {
    unlock: function () { try { ensureCtx(); } catch (e) { /* без звука */ } },
    setEnabled: function (v) { enabled = v; try { localStorage.setItem('sb.sound', v ? 'on' : 'off'); } catch (e) { /* игнор */ } },
    isEnabled: function () { return enabled; },
    uiClick: function () { tone({ freq: 900, duration: 0.05, type: 'square', gain: 0.07 }); },
    /** Замах = лепка снежка: череда мягких «шлепков» ладонями по снегу. Не тон, а ритмичный
     *  шум — каждый шлепок это короткий всплеск шума через bandpass плюс низкий уплотняющий
     *  толчок. Темп и громкость растут с силой. Бёрсты планируются по таймеру (~5–9/с), а не
     *  каждый кадр: на телефонах аудиопоток чувствителен к потоку событий. */
    chargeLoopStart: function (getPower) {
      if (!enabled) return function () {};
      var c = ensureCtx();
      var stopped = false, raf, nextAt = c.currentTime;
      function pack(power) {
        var t0 = c.currentTime;
        // мягкий скрип-хруст сжимаемого снега
        var dur = 0.05 + Math.random() * 0.05;
        var src = c.createBufferSource(); src.buffer = noiseBuffer;
        src.playbackRate.value = 0.7 + Math.random() * 0.35;
        var bp = c.createBiquadFilter(); bp.type = 'bandpass';
        bp.frequency.value = 460 + power * 520 + Math.random() * 160; bp.Q.value = 0.8;
        var g = c.createGain();
        var vol = 0.02 + power * 0.05;
        g.gain.setValueAtTime(0.0001, t0);
        g.gain.linearRampToValueAtTime(vol, t0 + 0.008);
        g.gain.exponentialRampToValueAtTime(0.0001, t0 + dur);
        src.connect(bp); bp.connect(g); g.connect(c.destination);
        src.start(t0); src.stop(t0 + dur + 0.02);
        // низкий короткий толчок уплотнения
        var o = c.createOscillator(), og = c.createGain();
        o.type = 'sine';
        o.frequency.setValueAtTime(150 + power * 40, t0);
        o.frequency.exponentialRampToValueAtTime(70, t0 + 0.09);
        og.gain.setValueAtTime(0.025 + power * 0.03, t0);
        og.gain.exponentialRampToValueAtTime(0.001, t0 + 0.1);
        o.connect(og); og.connect(c.destination);
        o.start(t0); o.stop(t0 + 0.13);
      }
      function tick() {
        if (stopped) return;
        var now = c.currentTime;
        if (now >= nextAt) {
          var p = getPower(); p = p < 0 ? 0 : (p > 1 ? 1 : p);
          pack(p);
          nextAt = now + (0.24 - p * 0.13) * (0.85 + Math.random() * 0.3);
        }
        raf = requestAnimationFrame(tick);
      }
      tick();
      return function () { stopped = true; cancelAnimationFrame(raf); };
    },
    throwWhoosh: function (power) { tone({ freq: 500 + power * 300, glideTo: 120, duration: 0.18 + power * 0.1, type: 'triangle', gain: 0.15 + power * 0.1 }); },
    hitPoof: function () { noiseBurst({ duration: 0.12, filterFreq: 1500, gain: 0.22 }); },
    wallThud: function () { tone({ freq: 140, glideTo: 60, duration: 0.12, type: 'sine', gain: 0.2 }); noiseBurst({ duration: 0.08, filterFreq: 400, gain: 0.12 }); },
    explosionBoom: function () { tone({ freq: 120, glideTo: 30, duration: 0.5, type: 'sawtooth', gain: 0.28 }); noiseBurst({ duration: 0.4, filterFreq: 800, gain: 0.28 }); },
    freezeChime: function () { tone({ freq: 1200, duration: 0.25, type: 'sine', gain: 0.16 }); tone({ freq: 1800, duration: 0.3, type: 'sine', gain: 0.1, delay: 0.05 }); },
    shieldThud: function () { tone({ freq: 180, glideTo: 90, duration: 0.2, type: 'square', gain: 0.18 }); },
    stunSound: function () { tone({ freq: 400, glideTo: 150, duration: 0.2, type: 'square', gain: 0.13 }); },
    koSound: function () { tone({ freq: 300, glideTo: 60, duration: 0.5, type: 'sawtooth', gain: 0.22 }); },
    victoryFanfare: function () { [523, 659, 784, 1047].forEach(function (f, i) { tone({ freq: f, duration: 0.2, type: 'triangle', gain: 0.18, delay: i * 0.12 }); }); },
    defeatChord: function () { [220, 196, 164].forEach(function (f, i) { tone({ freq: f, duration: 0.6, type: 'sine', gain: 0.13, delay: i * 0.05 }); }); },
    drawChord: function () { [330, 330].forEach(function (f, i) { tone({ freq: f, duration: 0.4, type: 'triangle', gain: 0.12, delay: i * 0.25 }); }); }
  };
})();
