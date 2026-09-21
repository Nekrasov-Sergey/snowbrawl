package protocol

import (
	"strings"
	"testing"
)

// Каждая пара из списков обязана быть годным ником: длину и мат проверяем на всех комбинациях,
// а не на выборке — списки маленькие, а ошибка в одном слове иначе всплыла бы у случайного
// игрока и в самый неподходящий момент.
func TestRandomNickWordsAreValid(t *testing.T) {
	t.Parallel()
	for _, adj := range nickAdjectives {
		for _, noun := range nickNouns {
			nick := adj + " " + noun
			if n := nickRunes(nick); n > MaxNickRunes {
				t.Errorf("%q: %d рун, предел %d", nick, n, MaxNickRunes)
			}
			got, err := NormalizeNick(nick)
			if err != nil {
				t.Errorf("%q не проходит проверку ника: %v", nick, err)
				continue
			}
			if got != nick {
				t.Errorf("%q нормализуется в %q — значит в слове лишние пробелы", nick, got)
			}
		}
	}
}

// Списки не должны содержать дублей: иначе одни имена выпадают чаще других без причины.
func TestRandomNickWordsAreUnique(t *testing.T) {
	t.Parallel()
	for _, list := range [][]string{nickAdjectives, nickNouns} {
		seen := map[string]bool{}
		for _, w := range list {
			if seen[w] {
				t.Errorf("слово %q встречается дважды", w)
			}
			seen[w] = true
		}
	}
}

func TestRandomNickVaries(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		nick := RandomNick()
		if _, err := NormalizeNick(nick); err != nil {
			t.Fatalf("%q не проходит проверку ника: %v", nick, err)
		}
		if len(strings.Fields(nick)) != 2 {
			t.Fatalf("ожидались два слова, получено %q", nick)
		}
		seen[nick] = true
	}
	// Двести бросков из полутора тысяч комбинаций почти наверняка дадут больше полусотни
	// разных имён; меньше означало бы, что случайность сломана.
	if len(seen) < 50 {
		t.Fatalf("слишком мало разных имён: %d из 200 бросков", len(seen))
	}
	if RandomNickTotal() != len(nickAdjectives)*len(nickNouns) {
		t.Error("RandomNickTotal не совпадает с размером списков")
	}
}

func TestFallbackNickIsValid(t *testing.T) {
	t.Parallel()
	for i := 0; i < 100; i++ {
		nick := FallbackNick()
		if _, err := NormalizeNick(nick); err != nil {
			t.Fatalf("%q не проходит проверку ника: %v", nick, err)
		}
	}
}
