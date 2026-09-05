/* Настройки игрока на устройстве (localStorage). Только клиент. */
window.SBSettings = (function () {
  var KEY = 'sb.settings';
  var DEFAULTS = {
    haptics: true,     // вибрация при попадании (где поддерживается)
    touch: 'auto',     // сенсорное управление: auto | on | off
    pcControls: 'wasd' // ПК: wasd (WASD + бросок удержанием ЛКМ) | classic (мышь: клик — идти, ЛКМ на бойце — замах)
  };
  var cur = load();

  function load() {
    var out = {};
    for (var k in DEFAULTS) out[k] = DEFAULTS[k];
    try {
      var saved = JSON.parse(localStorage.getItem(KEY) || '{}');
      for (var k2 in DEFAULTS) if (saved[k2] !== undefined) out[k2] = saved[k2];
    } catch (e) { /* приватный режим или мусор в хранилище */ }
    return out;
  }
  function save() { try { localStorage.setItem(KEY, JSON.stringify(cur)); } catch (e) { /* игнор */ } }

  return {
    get: function (k) { return cur[k]; },
    set: function (k, v) { cur[k] = v; save(); },
    all: function () { return cur; }
  };
})();
