package censor

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRootsAreNormalized(t *testing.T) {
	t.Parallel()
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

func TestLatinRootsAreNormalized(t *testing.T) {
	t.Parallel()
	for _, r := range rootsEn {
		if got := normalizeLatin([]rune(r)); got != r {
			t.Errorf("корень %q не в нормализованном виде (стал %q)", r, got)
		}
	}
	for _, e := range exactEn {
		if got := normalizeLatin([]rune(e)); got != e {
			t.Errorf("слово %q не в нормализованном виде (стало %q)", e, got)
		}
	}
	for _, a := range allowEn {
		if got := normalizeLatin([]rune(a)); got != a {
			t.Errorf("исключение %q не в нормализованном виде (стало %q)", a, got)
		}
	}
}

// Английский мат и обход через цифры, растяжку и разрядку.
func TestMaskCatchesEnglishProfanity(t *testing.T) {
	t.Parallel()
	cases := []string{
		"fuck", "FUCK", "f.u.c.k", "fuuuck", "f u c k", "sh1t", "$hit", "bullshit",
		"bitch", "asshole", "motherfucker", "cunt", "slut", "whore", "faggot", "wanker", "twat",
		"cock", "dick", "prick", "fag", "tits",
		// русский мат латиницей — раньше проходил целиком
		"suka", "blyat", "pizdec", "pidoras", "mudak", "nahui", "govno",
	}
	for _, in := range cases {
		if !Bad(in) {
			t.Errorf("%q должно считаться матом", in)
		}
		if out := Mask(in); !strings.Contains(out, "*") {
			t.Errorf("Mask(%q) = %q — без звёздочек", in, out)
		}
	}
}

// Английские слова, которые не должны попасть под фильтр, и русские, которые латинская
// дорожка могла бы испортить: «масса» → «macca», «кот» → «kot», «соска» → «cocka».
func TestMaskKeepsInnocentEnglishAndCyrillic(t *testing.T) {
	t.Parallel()
	cases := []string{
		"as", "class", "pass", "mass", "bass", "massive", "assassin", "grass",
		"cockpit", "peacock", "analysis", "analytics", "cumulative", "document",
		"night", "Nigeria", "shiitake", "Scunthorpe", "debate", "herbal", "verbal",
		"shoe", "hoe", "title", "attitude", "classic", "dickens",
		"good game", "nice shot",
		"масса", "кот", "соска", "рота", "парк", "мастер", "ракета", "сектор", "тема",
		"анализ", "команда", "танк", "снайпер", "хорошо сыграли",
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

func TestMaskCatchesObfuscation(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if Bad("") || Mask("") != "" {
		t.Fatal("пустая строка")
	}
	if Mask("   ") != "   " {
		t.Fatal("пробелы должны сохраняться")
	}
}
