/* Локализация интерфейса. Только клиент.
 *
 * Ключ перевода — сама русская строка: русский текст остаётся в коде как есть и работает
 * без словаря, а промах в словаре даёт не пустой экран, а русскую фразу. Из-за этого
 * web/sim/sim.js локализации не касается вовсе: имена ролей, арен и сложностей ботов там —
 * идентификаторы, они ходят по сети и проверяются сервером, переводится только показ.
 *
 * Язык меняется с перезагрузкой страницы (см. set): так applyDom отрабатывает ровно один раз
 * и не может перевести уже переведённую строку второй раз.
 */
window.SBI18n = (function () {
  var COOKIE = 'sb_lang';          // читает сервер: страницы /auth/* отвечают на языке игрока
  var DICTS = { en: window.SBI18nEN || {} };

  // Язык: явный выбор игрока сильнее всего, иначе смотрим на браузер. Русский — и дефолт,
  // и запасной вариант для непонятного navigator.language.
  var cur = (function () {
    var saved = window.SBSettings && window.SBSettings.get('lang');
    if (saved === 'ru' || saved === 'en') return saved;
    var nav = (navigator.language || navigator.userLanguage || '');
    return /^ru/i.test(nav) ? 'ru' : 'en';
  })();

  function dict() { return DICTS[cur] || null; }

  // params подставляются по {имя}. Отдельный проход, а не replace по каждому ключу: так
  // подставленное значение не может само стать плейсхолдером.
  function fill(s, params) {
    if (!params) return s;
    return s.replace(/\{(\w+)\}/g, function (m, k) {
      return params[k] === undefined ? m : String(params[k]);
    });
  }

  function t(key, params) {
    if (key === undefined || key === null) return key;
    var d = dict();
    var s = (d && d[key] !== undefined) ? d[key] : key;
    return fill(s, params);
  }

  // Числительные. forms — русские формы, они же ключ словаря: 'игрок|игрока|игроков'.
  // В английском форм две, выбор по единице.
  function plural(n, forms) {
    if (cur === 'ru') return pluralRu(n, forms);
    var d = dict(), key = forms.join('|');
    var en = d && d[key] !== undefined ? String(d[key]).split('|') : null;
    if (!en) return pluralRu(n, forms); // словарь не знает — лучше русская форма, чем пустота
    return n === 1 ? en[0] : (en[1] !== undefined ? en[1] : en[0]);
  }
  function pluralRu(n, forms) {
    var m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return forms[0];
    if (m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20)) return forms[1];
    return forms[2];
  }

  // Имена ботов: ник бойца — «Бот Сугроб» в PvP и «Союзник Сугроб» в PvE. Тот же список —
  // match.BotNames на сервере (internal/match/botnames.go), совпадение сверяет i18n_test.go.
  var BOT_NAMES = [
    'Снежок', 'Сугроб', 'Сосулька', 'Метель', 'Пурга', 'Буран', 'Иней', 'Льдинка', 'Снежинка', 'Морозко',
    'Позёмка', 'Наст', 'Айсберг', 'Пломбир', 'Валенок', 'Варежка', 'Санки', 'Ледник', 'Сквозняк', 'Холодок'
  ];
  // Разбор ника бота: { prefix, name, n } или null. n — номер, если пул имён кончился.
  // Живой игрок с ником «Бот Вася» ботом не считается: имени нет в пуле.
  var BOT_NICK_RE = /^(Бот|Союзник) (\S+)(?: (\d+))?$/;
  function botNick(s) {
    var m = BOT_NICK_RE.exec(s || '');
    return m && BOT_NAMES.indexOf(m[2]) >= 0 ? { prefix: m[1], name: m[2], n: m[3] } : null;
  }
  function isBotNick(s) { return !!botNick(s); }
  // Ники, которые придумал сервер или оффлайновый режим: только они и переводятся. Имя живого
  // игрока — его собственность, оно одинаково видно всем.
  function nick(s) {
    if (cur === 'ru' || !s) return s;
    var b = botNick(s);
    if (b) {
      var name = t(b.name) + (b.n ? ' ' + b.n : '');
      return b.prefix === 'Бот' ? t('Бот {name}', { name: name }) : t('Союзник {name}', { name: name });
    }
    var m = /^Игрок (\d+)$/.exec(s);
    if (m) return t('Игрок {n}', { n: m[1] });
    return s === 'Вы' || s === 'Соперник' ? t(s) : s;
  }

  // Статическая разметка index.html переводится обходом, а не разметкой ключей в HTML:
  // текст там и так на русском, дублировать его в data-атрибуты незачем.
  var ATTRS = ['placeholder', 'title', 'aria-label'];
  var SKIP = { SCRIPT: 1, STYLE: 1, CANVAS: 1 };
  function applyDom(root) {
    if (cur === 'ru' || !root) return;
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, null, false);
    var node;
    while ((node = walker.nextNode())) {
      if (node.nodeType === 1) {
        if (SKIP[node.tagName]) continue;
        for (var i = 0; i < ATTRS.length; i++) {
          var v = node.getAttribute(ATTRS[i]);
          if (v) node.setAttribute(ATTRS[i], t(v));
        }
        continue;
      }
      if (node.parentNode && SKIP[node.parentNode.tagName]) continue;
      // Пробелы по краям сохраняем: «Вы: <b>ник</b>» — отдельный текстовый узел «Вы: ».
      var raw = node.nodeValue, body = raw.replace(/^\s+|\s+$/g, '');
      if (!body) continue;
      var out = t(body);
      if (out === body) continue;
      node.nodeValue = raw.slice(0, raw.indexOf(body)) + out + raw.slice(raw.indexOf(body) + body.length);
    }
  }

  function set(lang) {
    if (lang !== 'ru' && lang !== 'en') return;
    if (window.SBSettings) window.SBSettings.set('lang', lang);
    try {
      document.cookie = COOKIE + '=' + lang + '; path=/; max-age=31536000; SameSite=Lax';
    } catch (e) { /* приватный режим */ }
    if (lang === cur) return;
    location.reload();
  }

  if (document.documentElement) document.documentElement.lang = cur;
  applyDom(document.body);

  return { lang: function () { return cur; }, t: t, plural: plural, nick: nick, isBotNick: isBotNick, BOT_NAMES: BOT_NAMES, applyDom: applyDom, set: set };
})();
