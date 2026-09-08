package censor

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRootsAreNormalized(t *testing.T) {
	for _, r := range roots {
		if got := normalize([]rune(r)); got != r {
			t.Errorf("корень %q не в нормализованном виде (стал %q)", r, got)
		}
	}
	for _, a := range allow {
		if got := normalize([]rune(a)); got != a {
			t.Errorf("исключение %q не в нормализованном виде (стало %q)", a, got)
		}
	}
}

func TestMaskCatchesObfuscation(t *testing.T) {
	cases := []string{
		"хуй", "ХуЙ", "х.у.й", "хуууй", "xyй", "х у й", "х-у-й",
		"пиздец", "иди на хуй", "ебать", "ёбнул", "заебал", "п1здец",
		"мудак", "гандон", "сука", "бля", "блять", "б л я т ь", "шлюха", "дрочить",
	}
	for _, in := range cases {
		if !Bad(in) {
			t.Errorf("%q должно считаться матом", in)
		}
		out := Mask(in)
		if !strings.Contains(out, "*") {
			t.Errorf("Mask(%q) = %q — без звёздочек", in, out)
		}
	}
}

func TestMaskKeepsInnocentWords(t *testing.T) {
	cases := []string{
		"команда", "мандарин", "мандат", "мандолина", "мандраж", "скипидар",
		"требовать", "употреблять", "лебеда", "хлеб", "себе", "тебе", "тебя", "себя",
		"ребёнок", "ребята", "серебро", "погреб", "гребец", "страхование", "бляха",
		"сукно", "хутор", "худой", "хуже", "красный", "мусор", "щебетать",
		"рубля", "корабля", "оскорблять", "ослаблять", "потреблять", "грабли",
		"привет всем", "кидай снежок в танка",
	}
	for _, in := range cases {
		if Bad(in) {
			t.Errorf("%q не мат, но помечено", in)
		}
		if out := Mask(in); out != in {
			t.Errorf("Mask(%q) = %q — текст изменился", in, out)
		}
	}
}

func TestMaskPreservesRestOfMessage(t *testing.T) {
	in := "ну ты и мудак конечно"
	out := Mask(in)
	if out == in {
		t.Fatal("сообщение должно измениться")
	}
	if utf8.RuneCountInString(out) != utf8.RuneCountInString(in) {
		t.Fatalf("длина изменилась: %q → %q", in, out)
	}
	if !strings.HasPrefix(out, "ну ты и ") || !strings.HasSuffix(out, " конечно") {
		t.Fatalf("остальной текст не сохранился: %q", out)
	}
	if strings.Contains(out, "мудак") {
		t.Fatalf("слово не замаскировано: %q", out)
	}
}

func TestMaskEmptyAndPlain(t *testing.T) {
	if Bad("") || Mask("") != "" {
		t.Fatal("пустая строка")
	}
	if Mask("   ") != "   " {
		t.Fatal("пробелы должны сохраняться")
	}
}
