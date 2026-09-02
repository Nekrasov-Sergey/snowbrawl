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
	for _, in := range []string{"snb-96dj", "96DJ", " SNB-96DJ "} {
		got, err := NormalizeRoomCode(in)
		if err != nil || got != "SNB-96DJ" {
			t.Errorf("NormalizeRoomCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"SNB-96IJ", "SNB-9", "", "abc-1234"} {
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
