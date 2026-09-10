package protocol

import "testing"

func TestNormalizeNick(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"  Сергей  ", "Сергей", true},
		{"Ivan   Petrov", "Ivan Petrov", true},
		{"a", "", false},
		{"слишком_длинный_ник_1234", "", false},
		{"bad<script>", "", false},
		{"ok-nick_1", "ok-nick_1", true},
	}
	for _, c := range cases {
		got, err := NormalizeNick(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizeNick(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestNormalizeRoomCode(t *testing.T) {
	for _, in := range []string{"1234", " 1234 ", "snb-1234", "SNB-1234"} {
		got, err := NormalizeRoomCode(in)
		if err != nil || got != "1234" {
			t.Errorf("NormalizeRoomCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"12a4", "123", "12345", "", "abc-1234", "SNB-96DJ"} {
		if _, err := NormalizeRoomCode(in); err == nil {
			t.Errorf("NormalizeRoomCode(%q) must fail", in)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	b := MustEncode(SWelcome, Welcome{Token: "t", PlayerID: "p"})
	env, err := Decode(b)
	if err != nil || env.Type != SWelcome || len(env.Data) == 0 {
		t.Fatalf("decode: %+v %v", env, err)
	}
	if _, err := Decode([]byte(`{"d":1}`)); err == nil {
		t.Fatal("empty type must fail")
	}
}

func TestNormalizeNickRejectsProfanity(t *testing.T) {
	for _, bad := range []string{"хуй", "ХуЙ", "п1здец", "мудак"} {
		if _, err := NormalizeNick(bad); err == nil {
			t.Errorf("ник %q должен быть отклонён", bad)
		}
	}
	for _, ok := range []string{"Аня", "Команда 1", "Хутор"} {
		if _, err := NormalizeNick(ok); err != nil {
			t.Errorf("ник %q должен приниматься: %v", ok, err)
		}
	}
}

func TestNickKey(t *testing.T) {
	// Одно и то же имя: регистр, латиница-двойники, разделители.
	same := [][2]string{
		{"Вася", "вася"},
		{"Вася", "ВАСЯ"},
		{"Вася", "Ba_cя"},
		{"Snow Brawl", "snow-brawl"},
		{"Игрок", "Игрок"},
	}
	for _, pair := range same {
		if NickKey(pair[0]) != NickKey(pair[1]) {
			t.Errorf("%q и %q должны давать один ключ (%q vs %q)", pair[0], pair[1], NickKey(pair[0]), NickKey(pair[1]))
		}
	}
	// Разные имена: цифры и повторы букв значимы, иначе игрок получает отказ без объяснения.
	diff := [][2]string{
		{"Игрок2", "Игрок5"},
		{"Анна", "Ана"},
		{"Вася", "Вася2"},
	}
	for _, pair := range diff {
		if NickKey(pair[0]) == NickKey(pair[1]) {
			t.Errorf("%q и %q не должны совпадать (оба %q)", pair[0], pair[1], NickKey(pair[0]))
		}
	}
}
