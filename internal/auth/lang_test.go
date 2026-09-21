package auth

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func request(lang string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/auth/yandex/callback", nil)
	if lang != "" {
		r.AddCookie(&http.Cookie{Name: CookieLang, Value: lang})
	}
	return r
}

func TestMsgRussianByDefault(t *testing.T) {
	const ru = "вход не подтверждён, попробуйте ещё раз"
	for _, lang := range []string{"", "ru", "de", "EN", "en-US"} {
		if got := msg(request(lang), ru); got != ru {
			t.Errorf("язык %q: ожидали русский текст, получили %q", lang, got)
		}
	}
}

func TestMsgEnglishByCookie(t *testing.T) {
	got := msg(request("en"), "вход не подтверждён, попробуйте ещё раз")
	if got != "the sign-in was not confirmed, try again" {
		t.Errorf("не перевели на английский: %q", got)
	}
}

// Незнакомая строка не должна пропадать: лучше показать русскую, чем пустую страницу.
func TestMsgUnknownStringStaysAsIs(t *testing.T) {
	const ru = "какой-то новый текст"
	if got := msg(request("en"), ru); got != ru {
		t.Errorf("ожидали исходный текст, получили %q", got)
	}
}

// Каждая строка таблицы обязана встречаться в коде ручек: иначе перевод мёртвый и вводит
// в заблуждение того, кто будет править тексты.
func TestEveryTranslatedMessageIsUsed(t *testing.T) {
	b, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatalf("читаем auth.go: %v", err)
	}
	for ru := range messagesEN {
		if !strings.Contains(string(b), `"`+ru+`"`) {
			t.Errorf("перевод для %q больше нигде не используется — уберите его", ru)
		}
	}
}
