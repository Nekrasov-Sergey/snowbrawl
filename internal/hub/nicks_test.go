package hub

import (
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Брони ников проверяем внутри пакета: правило различает адреса, а в интеграционных тестах все
// клиенты приходят с 127.0.0.1 — та же причина, что у прав на удаление сообщений.
func newNickHub() *Hub {
	return &Hub{
		log:   zerolog.New(io.Discard),
		byID:  map[string]*session.Player{},
		nicks: map[string]nickHold{},
	}
}

func TestNickHoldRules(t *testing.T) {
	h := newNickHub()
	now := time.Now()
	h.holdNick("Вася", "10.0.0.1", "p1", now)

	if h.nickFree("Вася", "10.0.0.2", "") {
		t.Fatal("занятый ник обязан быть недоступен с другого адреса")
	}
	if !h.nickFree("Вася", "10.0.0.1", "") {
		t.Fatal("тот же адрес должен получать свой ник обратно: иначе F5 отнимает имя")
	}
	if !h.nickFree("Вася", "10.9.9.9", "p1") {
		t.Fatal("та же сессия должна получать свой ник и с нового адреса (мобильная сеть)")
	}
	if !h.nickFree("Петя", "10.0.0.2", "") {
		t.Fatal("свободный ник отказали")
	}

	// Регистр, латиница-двойники и разделители — то же имя.
	for _, same := range []string{"вася", "ВАСЯ", "Ba_cя", "Ва-ся"} {
		if h.nickFree(same, "10.0.0.2", "") {
			t.Fatalf("%q должно считаться тем же ником, что «Вася»", same)
		}
	}
	// А цифры и повторы букв — разные имена: иначе игрок получает отказ, на который не может
	// ничего ответить.
	for _, other := range []string{"Вася2", "Вася5", "Вааася"} {
		if !h.nickFree(other, "10.0.0.2", "") {
			t.Fatalf("%q не должно совпадать с «Вася»", other)
		}
	}
}

func TestNickReleaseOnlyOwn(t *testing.T) {
	h := newNickHub()
	now := time.Now()
	h.holdNick("Вася", "10.0.0.1", "p1", now)

	h.releaseNick(protocol.NickKey("Вася"), "p2")
	if h.nickFree("Вася", "10.0.0.2", "") {
		t.Fatal("чужой releaseNick не должен снимать бронь")
	}
	h.releaseNick(protocol.NickKey("Вася"), "p1")
	if !h.nickFree("Вася", "10.0.0.2", "") {
		t.Fatal("после переименования ник обязан освободиться")
	}
}

func TestReleaseNicksOfIP(t *testing.T) {
	h := newNickHub()
	now := time.Now()
	h.holdNick("Вася", "10.0.0.1", "p1", now)
	h.holdNick("Петя", "10.0.0.1", "p2", now)
	h.holdNick("Маша", "10.0.0.2", "p3", now)

	h.releaseNicksOfIP("10.0.0.1")
	if !h.nickFree("Вася", "10.0.0.9", "") || !h.nickFree("Петя", "10.0.0.9", "") {
		t.Fatal("бан обязан отпускать ники адреса")
	}
	if h.nickFree("Маша", "10.0.0.9", "") {
		t.Fatal("ники других адресов бан не трогает")
	}
}

func TestEvictNickHoldsKeepsLiveSessions(t *testing.T) {
	h := newNickHub()
	now := time.Now()
	// Живая сессия с самой старой бронью — её вытеснять нельзя.
	h.byID["alive"] = &session.Player{ID: "alive", IP: "10.0.0.1"}
	h.holdNick("Живой", "10.0.0.1", "alive", now)
	for i := 0; i < nickHoldsCap+50; i++ {
		h.holdNick("Мертвый"+string(rune('a'+i%26))+string(rune('a'+i/26)), "10.1.0.1", "dead", now.Add(time.Duration(i)*time.Second))
	}
	if len(h.nicks) > nickHoldsCap {
		t.Fatalf("реестр вырос до %d при потолке %d", len(h.nicks), nickHoldsCap)
	}
	if h.nickFree("Живой", "10.9.9.9", "") {
		t.Fatal("бронь живой сессии вытеснена")
	}
}
