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
    chargeLoopStart: function (getPower) {
      if (!enabled) return function () {};
      var c = ensureCtx();
      var osc = c.createOscillator(), g = c.createGain();
      osc.type = 'sawtooth'; osc.frequency.value = 220; g.gain.value = 0.0001;
      osc.connect(g); g.connect(c.destination); osc.start();
      var raf;
      function update() {
        var p = getPower();
        osc.frequency.setTargetAtTime(220 + p * 380, c.currentTime, 0.05);
        g.gain.setTargetAtTime(0.04 + p * 0.05, c.currentTime, 0.05);
        raf = requestAnimationFrame(update);
      }
      update();
      return function () { cancelAnimationFrame(raf); try { g.gain.setTargetAtTime(0.0001, c.currentTime, 0.05); osc.stop(c.currentTime + 0.2); } catch (e) { /* уже остановлен */ } };
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
