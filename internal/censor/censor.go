// Package censor прячет мат в тексте, который игроки видят друг у друга: сообщения общего чата
// и ники. Готовой библиотеки под русский язык нет (популярные — англоязычные списки), поэтому
// здесь свой список корней плюс нормализация, снимающая типовые способы обхода: латиница вместо
// похожих кириллических букв, цифры вместо букв, точки и дефисы внутри слова, растянутые буквы,
// разрядка пробелами.
//
// Ноль ложных срабатываний не гарантируется: список исключений (allow) правится по мере находок.
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

// span — границы слова в рунах исходной строки.
type span struct{ from, to int }

type word struct {
	span
	norm string
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

func bad(norm string) bool {
	if norm == "" {
		return false
	}
	for _, a := range allow {
		if strings.Contains(norm, a) {
			return false
		}
	}
	for _, r := range roots {
		if strings.Contains(norm, r) {
			return true
		}
	}
	for _, e := range exact {
		if norm == e {
			return true
		}
	}
	return false
}

func split(rs []rune) []word {
	var out []word
	start := -1
	for i, r := range rs {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, word{span{start, i}, normalize(rs[start:i])})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, word{span{start, len(rs)}, normalize(rs[start:])})
	}
	return out
}

const glueMax = 2 // склеиваем только огрызки: «х у й». Группы длиннее дали бы «на хате»

// scan возвращает границы матерных слов в рунах исходной строки.
func scan(rs []rune) []span {
	ws := split(rs)
	hit := make([]bool, len(ws))
	for i := range ws {
		if bad(ws[i].norm) {
			hit[i] = true
		}
	}
	// Разрядка пробелами: склеиваем подряд идущие короткие огрызки и проверяем целиком.
	for i := 0; i < len(ws); i++ {
		n := len([]rune(ws[i].norm))
		if n == 0 || n > glueMax {
			continue
		}
		acc := ws[i].norm
		for j := i + 1; j < len(ws) && j-i < 8; j++ {
			m := len([]rune(ws[j].norm))
			if m == 0 || m > glueMax {
				break
			}
			acc += ws[j].norm
			if bad(acc) {
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
