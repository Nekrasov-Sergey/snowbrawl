package snowbrawl_test

// Сторож словаря переводов: каждая строка, которую клиент покажет игроку, обязана иметь перевод
// в web/client/i18n/en.js, и наоборот — в словаре не должно быть ключей, которых уже нет в коде.
// Ключ перевода — сама русская строка (см. web/client/i18n.js), поэтому список ключей собирается
// из исходников теми же правилами, по которым клиент их ищет.
//
// Тест ходит по файлам с диска, а не через embed: он про исходники, а не про собранный бинарник.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// t('…') и t("…") — но не chart(…), toast(…) и прочие имена, кончающиеся на t.
	reTCall = regexp.MustCompile(`(^|[^\pL\pN_$])t\(\s*('((?:\\.|[^'\\])*)'|"((?:\\.|[^"\\])*)")`)
	// I18n.plural(n, ['шаг', 'шага', 'шагов']) → ключ «шаг|шага|шагов»
	rePlural  = regexp.MustCompile(`I18n\.plural\([^,]+,\s*\[([^\]]+)\]`)
	reQuoted  = regexp.MustCompile(`'((?:\\.|[^'\\])*)'`)
	reMapVal  = regexp.MustCompile(`:\s*'((?:\\.|[^'\\])*)'`)
	reArena   = regexp.MustCompile(`\{ name: '([^']+)'`)
	reHTMLTag = regexp.MustCompile(`<[^>]*>`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reScript  = regexp.MustCompile(`(?s)<script.*?</script>`)
	reAttr    = regexp.MustCompile(`(placeholder|title|aria-label)="([^"]*)"`)
	reCyr     = regexp.MustCompile(`[\p{Cyrillic}]`)
	reLineCom = regexp.MustCompile(`(?m)//.*$`)
)

// SIM_ROLES и SIM_NICKS в sim.js — идентификаторы, но клиент их показывает, поэтому перевод нужен.
var simExtras = []string{
	"Раннер", "Танк", "Снайпер", "Бомбер", "Фризер", "Щит",
	"Рой", "Йети", "Ком", "Снежколёт", "Вьюга", "Йети-вожак", "Снеговик-голем",
	"Соперник", "Вы",
	"Бот {n}", "Союзник {n}", "Игрок {n}", // имена, которые придумывает сервер, см. I18n.nick
	"Бот", "Союзник", // тот же ник без номера — слот, освобождённый пересевшим игроком (match.go)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("читаем %s: %v", path, err)
	}
	return string(b)
}

type keySet struct {
	order []string
	seen  map[string]bool
}

func (k *keySet) add(s string) {
	s = strings.TrimSpace(s)
	if s == "" || k.seen[s] {
		return
	}
	k.seen[s] = true
	k.order = append(k.order, s)
}

// block вырезает тело объявления `var NAME = {` … `\n  };`.
func block(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)var ` + name + ` = \{(.*?)\n  \};`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("в исходнике не найден блок %s — тест устарел, поправьте регулярку", name)
	}
	return m[1]
}

func wantedKeys(t *testing.T) *keySet {
	t.Helper()
	keys := &keySet{seen: map[string]bool{}}

	for _, f := range []string{"web/client/app.js", "web/client/tutorial.js"} {
		src := read(t, f)
		for _, m := range reTCall.FindAllStringSubmatch(src, -1) {
			if m[3] != "" || strings.HasPrefix(m[2], "'") {
				keys.add(strings.ReplaceAll(m[3], `\'`, `'`))
			} else {
				keys.add(m[4])
			}
		}
		for _, m := range rePlural.FindAllStringSubmatch(src, -1) {
			var forms []string
			for _, q := range reQuoted.FindAllStringSubmatch(m[1], -1) {
				forms = append(forms, q[1])
			}
			if len(forms) == 3 {
				keys.add(strings.Join(forms, "|"))
			}
		}
	}

	// Тексты ошибок сервера лежат таблицей по коду, переводятся в точке показа.
	for _, m := range reMapVal.FindAllStringSubmatch(block(t, read(t, "web/client/app.js"), "ERR_TEXT"), -1) {
		keys.add(strings.ReplaceAll(m[1], `\'`, `'`))
	}

	sim := read(t, "web/sim/sim.js")
	for _, m := range reArena.FindAllStringSubmatch(sim, -1) {
		keys.add(m[1])
	}
	for _, name := range []string{"HERO_DESCRIPTIONS", "ABILITY_HINT_TEXT"} {
		for _, m := range reMapVal.FindAllStringSubmatch(block(t, sim, name), -1) {
			keys.add(strings.ReplaceAll(m[1], `\'`, `'`))
		}
	}
	levels := regexp.MustCompile(`BOT_LEVEL_NAMES:\s*\[([^\]]+)\]`).FindStringSubmatch(sim)
	if levels == nil {
		t.Fatal("в sim.js не найден BOT_LEVEL_NAMES — тест устарел")
	}
	for _, q := range reQuoted.FindAllStringSubmatch(levels[1], -1) {
		keys.add(q[1])
	}
	for _, s := range simExtras {
		keys.add(s)
	}

	// Статическую разметку переводит обход DOM (I18n.applyDom) — тем же набором мест.
	page := read(t, "web/index.html")
	if i := strings.Index(page, "<body>"); i >= 0 {
		page = page[i:]
	}
	page = reScript.ReplaceAllString(reComment.ReplaceAllString(page, ""), "")
	for _, m := range reAttr.FindAllStringSubmatch(page, -1) {
		if reCyr.MatchString(m[2]) {
			keys.add(unescapeHTML(m[2]))
		}
	}
	for _, chunk := range reHTMLTag.Split(page, -1) {
		if c := strings.TrimSpace(chunk); reCyr.MatchString(c) {
			keys.add(unescapeHTML(c))
		}
	}
	return keys
}

func unescapeHTML(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return r.Replace(s)
}

// dictKeys читает ключи словаря: строки вида `'ключ': 'value',` на верхнем уровне объекта.
func dictKeys(t *testing.T) map[string]string {
	t.Helper()
	src := reLineCom.ReplaceAllString(read(t, "web/client/i18n/en.js"), "")
	re := regexp.MustCompile(`(?m)^\s*'((?:\\.|[^'\\])*)':\s*'((?:\\.|[^'\\])*)'`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[strings.ReplaceAll(m[1], `\'`, `'`)] = strings.ReplaceAll(m[2], `\'`, `'`)
	}
	if len(out) == 0 {
		t.Fatal("словарь en.js разобран пустым — тест устарел, поправьте регулярку")
	}
	return out
}

func TestEnglishDictionaryCoversEveryKey(t *testing.T) {
	want, dict := wantedKeys(t), dictKeys(t)
	var missing []string
	for _, k := range want.order {
		if _, ok := dict[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("нет перевода для %d строк — добавьте их в web/client/i18n/en.js:", len(missing))
		for _, k := range missing {
			t.Errorf("  %q", k)
		}
	}
}

func TestEnglishDictionaryHasNoStaleKeys(t *testing.T) {
	want, dict := wantedKeys(t), dictKeys(t)
	var stale []string
	for k := range dict {
		if !want.seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("в словаре %d ключей, которых больше нет в коде — уберите их:", len(stale))
		for _, k := range stale {
			t.Errorf("  %q", k)
		}
	}
}

// Плейсхолдеры обязаны дожить до перевода: без {n} подстановка потеряет число.
func TestEnglishDictionaryKeepsPlaceholders(t *testing.T) {
	ph := regexp.MustCompile(`\{(\w+)\}`)
	for key, val := range dictKeys(t) {
		in, out := ph.FindAllString(key, -1), ph.FindAllString(val, -1)
		sort.Strings(in)
		sort.Strings(out)
		if strings.Join(in, ",") != strings.Join(out, ",") {
			t.Errorf("плейсхолдеры разошлись:\n  ключ:    %q → %v\n  перевод: %q → %v", key, in, val, out)
		}
	}
}

// Числительные: русский ключ — три формы, английский перевод — две.
func TestEnglishPluralFormsCount(t *testing.T) {
	for key, val := range dictKeys(t) {
		if !strings.Contains(key, "|") {
			continue
		}
		if n := len(strings.Split(key, "|")); n != 3 {
			t.Errorf("ключ-числительное %q: ожидались три русские формы, найдено %d", key, n)
		}
		if n := len(strings.Split(val, "|")); n != 2 {
			t.Errorf("перевод числительного %q → %q: ожидались две английские формы, найдено %d", key, val, n)
		}
	}
}

// Страховка от «тест ничего не проверил»: если регулярки перестанут находить строки,
// остальные тесты пройдут на пустом множестве и пропустят настоящую пропажу перевода.
func TestKeyExtractionIsNotEmpty(t *testing.T) {
	if n := len(wantedKeys(t).order); n < 300 {
		t.Fatalf("из исходников извлечено всего %d строк — разбор сломался", n)
	}
}
