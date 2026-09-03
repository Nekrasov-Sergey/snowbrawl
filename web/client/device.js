/* Определение устройства: сенсорный режим, iOS, вибрация, полный экран. Только клиент. */
window.SBDevice = (function () {
  var Settings = window.SBSettings;

  function mq(q) { try { return !!(window.matchMedia && window.matchMedia(q).matches); } catch (e) { return false; } }
  function hasTouchEvents() { return 'ontouchstart' in window || (navigator.maxTouchPoints || 0) > 0; }
  /** Автоопределение: основной указатель грубый (палец) и есть touch-события. */
  function autoTouch() { return mq('(pointer: coarse)') && hasTouchEvents(); }

  // Значение кэшируется: matchMedia в горячем пути (кадр, pointermove) дорого. Пересчёт в apply().
  var touchCached = null;
  function computeTouch() {
    var s = Settings.get('touch');
    if (s === 'on') return true;
    if (s === 'off') return false;
    return autoTouch();
  }
  function isTouch() {
    if (touchCached === null) touchCached = computeTouch();
    return touchCached;
  }
  function isIOS() {
    var ua = navigator.userAgent || '';
    return /iP(hone|ad|od)/.test(ua) || (navigator.platform === 'MacIntel' && (navigator.maxTouchPoints || 0) > 1);
  }
  function canVibrate() { return typeof navigator.vibrate === 'function' && !isIOS(); }
  function isStandalone() { return mq('(display-mode: standalone)') || navigator.standalone === true; }
  function fullscreenAvailable() { return !!(document.fullscreenEnabled && document.documentElement.requestFullscreen); }
  function isPortrait() { return mq('(orientation: portrait)'); }

  /** Проставить классы на <html>, чтобы CSS знал режим. */
  function apply() {
    touchCached = computeTouch();
    var root = document.documentElement;
    root.classList.toggle('touch', isTouch());
    root.classList.toggle('ios', isIOS());
  }

  return {
    isTouch: isTouch, autoTouch: autoTouch, isIOS: isIOS, canVibrate: canVibrate,
    isStandalone: isStandalone, fullscreenAvailable: fullscreenAvailable, isPortrait: isPortrait, apply: apply
  };
})();
