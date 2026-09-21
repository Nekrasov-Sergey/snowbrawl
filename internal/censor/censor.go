// Package censor прячет мат в тексте, который игроки видят друг у друга: сообщения общего чата
// и ники. Готовой библиотеки под русский язык нет (популярные — англоязычные списки), поэтому
// здесь свой список корней плюс нормализация, снимающая типовые способы обхода: латиница вместо
// похожих кириллических букв, цифры вместо букв, точки и дефисы внутри слова, растянутые буквы,
// разрядка пробелами.
//
// Дорожек нормализации две, и каждое слово проходит обе. Кириллическая сводит текст к русским
// буквам (латиница и цифры — к похожим кириллическим), латинская — наоборот, к английским.
// Одной не хватает по устройству: после кириллической «fuck» превращается в «иск», а после
// латинской «хуй» — в пустоту. Английский мат и русский мат в транслите ловит вторая, русский —
// первая, и списки у них раздельные.
//
// Язык интерфейса игрока на цензуру не влияет: ник и сообщение в общем чате видят все сразу,
// и то, что для одного мат, не может быть безобидным для другого.
//
// Ноль ложных срабатываний не гарантируется: списки исключений (allow, allowEn) правятся
// по мере находок.
package censor

import (
	"strings"
	"unicode"
)

// homoglyphs — похожие на кириллицу символы: латиница, цифры, знаки.
var homoglyphs = map[rune]rune{
	'a': 'а', 'b': 'в', 'c': 'с', 'd': 'д', 'e': 'е', 'g': 'г', 'h': 'н', 'i': 'и', 'j': 'и',
	'k': 'к', 'l': 'л', 'm': 'м', 'n': 'п', 'o': 'о', 'p': 'р', 'r': 'г', 's': 'с', 't': 'т',
	'u': 'и', 'x': 'х', 'y': 'у', 'z': 'з',
	'0': 'о', '1': 'и', '3': 'з', '4': 'ч', '6': 'б', '9': 'я',
	'@': 'а', '$': 'с', '№': 'н',
}

// latinLookalikes — кириллица, которой пишут латинские буквы: только бесспорные начертания.
// Спорных (п→n, г→r, и→u) здесь намеренно нет: они превращают безобидные русские слова
// в английские корни и дают ложные срабатывания.
var latinLookalikes = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'к': 'k', 'м': 'm', 'н': 'h',
	'о': 'o', 'р': 'p', 'с': 'c', 'т': 't', 'х': 'x', 'у': 'y',
}

// leet — цифры и знаки вместо латинских букв.
var leet = map[rune]rune{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's',
}

// span — границы слова в рунах исходной строки.
type span struct{ from, to int }

type word struct {
	span
	norm    string // кириллическая нормаль: для roots/allow/exact
	normLat string // латинская нормаль: для rootsEn/allowEn/exactEn
}

// Fold сводит символ к кириллическому «двойнику»: регистр вниз, латиница и цифры-похожие — к
// букве, «ё» и «й» — к «е» и «и». Экспортировано, чтобы сравнение ников (protocol.NickKey)
// пользовалось той же картой обхода, что и цензура, и они не разъезжались.
func Fold(r rune) rune {
	r = unicode.ToLower(r)
	if m, ok := homoglyphs[r]; ok {
		r = m
	}
	switch r {
	case 'ё':
		return 'е'
	case 'й':
		return 'и'
	}
	return r
}

// normalize приводит слово к виду, в котором его можно сверять с корнями.
func normalize(rs []rune) string {
	var b strings.Builder
	var prev rune
	for _, r := range rs {
		r = Fold(r)
		if !unicode.Is(unicode.Cyrillic, r) {
			continue // точки, дефисы, звёздочки и прочий мусор внутри слова
		}
		if r == prev {
			continue // растянутые буквы: «хуууй»
		}
		prev = r
		b.WriteRune(r)
	}
	return b.String()
}

// foldLatin — зеркало Fold: сводит символ к латинскому «двойнику». Не экспортируется:
// сравнение ников (protocol.NickKey) стоит на Fold, и вторая карта ему не нужна.
func foldLatin(r rune) rune {
	r = unicode.ToLower(r)
	if m, ok := leet[r]; ok {
		return m
	}
	if m, ok := latinLookalikes[r]; ok {
		return m
	}
	return r
}

// normalizeLatin приводит слово к виду, в котором его можно сверять с английскими корнями.
func normalizeLatin(rs []rune) string {
	var b strings.Builder
	var prev rune
	for _, r := range rs {
		r = foldLatin(r)
		if r < 'a' || r > 'z' {
			continue
		}
		if r == prev {
			continue
		}
		prev = r
		b.WriteRune(r)
	}
	return b.String()
}

// match — общая проверка одной нормали по тройке списков «исключения → корни → целые слова».
func match(norm string, allowList, rootList, exactList []string) bool {
	if norm == "" {
		return false
	}
	for _, a := range allowList {
		if strings.Contains(norm, a) {
			return false
		}
	}
	for _, r := range rootList {
		if strings.Contains(norm, r) {
			return true
		}
	}
	for _, e := range exactList {
		if norm == e {
			return true
		}
	}
	return false
}

// bad — слово матерное, если его поймала любая из двух дорожек.
func bad(norm, normLat string) bool {
	return match(norm, allow, roots, exact) || match(normLat, allowEn, rootsEn, exactEn)
}

func split(rs []rune) []word {
	var out []word
	start := -1
	for i, r := range rs {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, makeWord(rs, start, i))
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, makeWord(rs, start, len(rs)))
	}
	return out
}

func makeWord(rs []rune, from, to int) word {
	return word{span{from, to}, normalize(rs[from:to]), normalizeLatin(rs[from:to])}
}

const glueMax = 2 // склеиваем только огрызки: «х у й». Группы длиннее дали бы «на хате»

// gluable отвечает, годится ли слово в склейку разрядки. Пустое слово (одни знаки препинания)
// и кусок длиннее огрызка цепочку разрывают.
func gluable(w word) bool {
	nRu, nLat := len([]rune(w.norm)), len([]rune(w.normLat))
	return (nRu > 0 || nLat > 0) && nRu <= glueMax && nLat <= glueMax
}

// scan возвращает границы матерных слов в рунах исходной строки.
func scan(rs []rune) []span {
	ws := split(rs)
	hit := make([]bool, len(ws))
	for i := range ws {
		if bad(ws[i].norm, ws[i].normLat) {
			hit[i] = true
		}
	}
	// Разрядка пробелами: склеиваем подряд идущие короткие огрызки и проверяем целиком.
	// Копим обе нормали сразу — «х у й» и «f u c k» ломаются одинаково.
	for i := 0; i < len(ws); i++ {
		if !gluable(ws[i]) {
			continue
		}
		acc, accLat := ws[i].norm, ws[i].normLat
		for j := i + 1; j < len(ws) && j-i < 8; j++ {
			if !gluable(ws[j]) {
				break
			}
			acc, accLat = acc+ws[j].norm, accLat+ws[j].normLat
			if bad(acc, accLat) {
				for k := i; k <= j; k++ {
					hit[k] = true
				}
				break
			}
		}
	}
	var out []span
	for i := range ws {
		if hit[i] {
			out = append(out, ws[i].span)
		}
	}
	return out
}

// Mask заменяет матерные слова звёздочками, остальной текст оставляет как есть.
func Mask(text string) string {
	rs := []rune(text)
	spans := scan(rs)
	if len(spans) == 0 {
		return text
	}
	for _, s := range spans {
		for i := s.from; i < s.to; i++ {
			rs[i] = '*'
		}
	}
	return string(rs)
}

// Bad сообщает, есть ли в тексте мат. Для ников: их не маскируют, а отклоняют.
func Bad(text string) bool { return len(scan([]rune(text))) > 0 }
