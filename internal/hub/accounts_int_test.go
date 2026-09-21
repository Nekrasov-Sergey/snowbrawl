package hub_test

// Аккаунты в игре: ник и прогресс обучения одни и те же на любом устройстве, гость при этом
// живёт как раньше. Настоящая кука и OAuth проверяются в internal/auth; здесь важно поведение
// игры, когда сервер уже узнал игрока.

import (
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

func TestAccountNickComesFromServer(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")

	c := s.dialAs(t, id)
	w := c.Welcome
	if w.Nick != "Снежок" {
		t.Fatalf("ник пришёл не из аккаунта: %q", w.Nick)
	}
	if w.Account == nil || w.Account.ID != id || w.Account.Provider != accounts.ProviderYandex {
		t.Fatalf("welcome без аккаунта: %+v", w.Account)
	}
	if w.Auth == nil || !w.Auth.Yandex {
		t.Error("клиент не узнает, что вход включён")
	}
}

// Главное, ради чего всё затевалось: телефон и компьютер — один игрок с одним ником.
func TestAccountSameOnSecondDevice(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")

	first := s.dialAs(t, id)
	w1 := first.Welcome

	// Второе устройство: своего токена сессии у него нет вовсе.
	second := s.dialAs(t, id)
	w2 := second.Welcome
	if w2.Nick != "Снежок" {
		t.Fatalf("на втором устройстве ник другой: %q", w2.Nick)
	}
	if w2.PlayerID != w1.PlayerID {
		t.Errorf("аккаунт получил вторую сессию: %s и %s", w1.PlayerID, w2.PlayerID)
	}
	// Прежнее соединение вытесняется — тем же способом, что вторая вкладка у гостя.
	first.expectClosed(t)
}

// Ник, введённый в hello, вошедшему игроку не принадлежит: его имя хранит сервер.
func TestAccountIgnoresHelloNick(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")
	c := s.dialOpts(t, "Самозванец", "", id, false)
	if c.Welcome.Nick != "Снежок" {
		t.Fatalf("ник подменён из hello: %q", c.Welcome.Nick)
	}
}

func TestAccountRename(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")
	c := s.dialAs(t, id)

	c.send(protocol.CNickSet, protocol.NickSet{Nick: "Метель"})
	var st protocol.AccountState
	c.expect(protocol.SAccount, &st)
	if st.Account == nil || st.Account.Nick != "Метель" {
		t.Fatalf("ник не сменился: %+v", st.Account)
	}
	// Имя должно пережить перезаход: оно лежит в аккаунте, а не в сессии.
	c.close()
	again := s.dialAs(t, id)
	if again.Welcome.Nick != "Метель" {
		t.Fatalf("после перезахода ник %q", again.Welcome.Nick)
	}
}

func TestAccountRenameRejected(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")
	c := s.dialAs(t, id)

	// Мат в нике — своя причина отказа, как и у гостя.
	c.send(protocol.CNickSet, protocol.NickSet{Nick: "х"})
	var e protocol.Error
	c.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBadNick {
		t.Errorf("короткий ник: код %q", e.Code)
	}
	// Ник живого гостя аккаунту не отдаём: иначе в лобби будут два одинаковых имени.
	guest := s.connect(t, "Гость", "")
	defer guest.close()
	c.send(protocol.CNickSet, protocol.NickSet{Nick: "Гость"})
	c.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNickTaken {
		t.Errorf("ник гостя: код %q", e.Code)
	}
	// Второе переименование подряд — кулдаун.
	c.send(protocol.CNickSet, protocol.NickSet{Nick: "Метель"})
	var st protocol.AccountState
	c.expect(protocol.SAccount, &st)
	c.send(protocol.CNickSet, protocol.NickSet{Nick: "Вьюга"})
	c.expect(protocol.SError, &e)
	if e.Code != protocol.ErrRenameCooldown {
		t.Errorf("кулдаун: код %q", e.Code)
	}
}

// Гость не может занять ник, закреплённый за аккаунтом, — в этом и смысл постоянной брони.
func TestGuestCannotTakeAccountNick(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	s.account(t, "y-1", "Снежок")

	guest := s.connectRaw(t, "снежок", "", "")
	var e protocol.Error
	guest.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNickTaken {
		t.Fatalf("гость занял ник аккаунта: %q", e.Code)
	}
}

func TestAccountTutorialSyncAndDone(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")
	c := s.dialAs(t, id)
	if len(c.Welcome.Tutorial) != 0 {
		t.Fatalf("у нового аккаунта уже есть прогресс: %v", c.Welcome.Tutorial)
	}

	// Прогресс, накопленный на этом устройстве, вливается в аккаунт.
	c.send(protocol.CTutorialSync, protocol.TutorialSync{IDs: []string{"basics", "sniper"}})
	var st protocol.AccountState
	c.expect(protocol.SAccount, &st)
	if len(st.Tutorial) != 2 {
		t.Fatalf("после слияния: %v", st.Tutorial)
	}
	c.send(protocol.CTutorialDone, protocol.TutorialDone{ID: "bomber"})
	c.expect(protocol.SAccount, &st)
	if len(st.Tutorial) != 3 {
		t.Fatalf("после урока: %v", st.Tutorial)
	}

	// Другое устройство видит тот же прогресс, а своё старое — не теряет.
	c.close()
	other := s.dialAs(t, id)
	if len(other.Welcome.Tutorial) != 3 {
		t.Fatalf("на другом устройстве прогресс %v", other.Welcome.Tutorial)
	}
	other.send(protocol.CTutorialSync, protocol.TutorialSync{IDs: []string{"freezer"}})
	other.expect(protocol.SAccount, &st)
	if len(st.Tutorial) != 4 {
		t.Fatalf("слияние затёрло чужое: %v", st.Tutorial)
	}
}

// Забаненный аккаунт не спасается разлогином — но и гость с того же адреса не страдает.
func TestBannedAccountRejected(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	id := s.account(t, "y-1", "Снежок")
	if err := s.accs.Ban(id, "мат", time.Now()); err != nil {
		t.Fatal(err)
	}
	c := s.dialOpts(t, "", "", id, true)
	var e protocol.Error
	c.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBanned {
		t.Fatalf("забаненный аккаунт пущен: %q", e.Code)
	}
	guest := s.connect(t, "Гость", "")
	defer guest.close()
}

// «Играть гостем»: игрок не вводил имени, сервер обязан выдать своё — и разное разным людям.
func TestGuestGetsGeneratedNick(t *testing.T) {
	t.Parallel()
	// TrustProxy — чтобы клиенты приходили с разных адресов: без него все они 127.0.0.1,
	// и бронь ника считается «своей» для каждого (см. nicks.go).
	s := newServer(t, func(c *config.Config) { c.TrustProxy = true })

	first := s.connect(t, "", "")
	defer first.close()
	if first.Welcome.Nick == "" {
		t.Fatal("гостю без имени не выдали ник")
	}
	if _, err := protocol.NormalizeNick(first.Welcome.Nick); err != nil {
		t.Fatalf("выданный ник %q не проходит проверку: %v", first.Welcome.Nick, err)
	}
	if first.Welcome.Account != nil {
		t.Error("у гостя не должно быть аккаунта")
	}

	second := s.connect(t, "", "")
	defer second.close()
	if second.Welcome.Nick == first.Welcome.Nick {
		t.Fatalf("оба гостя получили одно имя %q", first.Welcome.Nick)
	}

	// Выданное имя забронировано так же, как введённое руками.
	taken := s.connectRaw(t, first.Welcome.Nick, "", "10.0.0.7")
	var e protocol.Error
	taken.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNickTaken {
		t.Fatalf("чужой гостевой ник отдали: %q", e.Code)
	}
}

// Гость без куки должен вести себя ровно как раньше — это главный регрессионный тест затеи.
func TestGuestUnaffected(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	c := s.connect(t, "Гость", "")
	defer c.close()
	if c.Token == "" || c.ID == "" {
		t.Fatal("гость не получил сессию")
	}
	// Смена ника гостю по-прежнему доступна переподключением с новым hello.
	again := s.connect(t, "Гость2", c.Token)
	defer again.close()
	again.expectNone(protocol.SError, 200*time.Millisecond)

	// Сообщения аккаунта от гостя сервер молча игнорирует: клиент не обязан знать в двух
	// местах, вошёл игрок или нет.
	again.send(protocol.CTutorialDone, protocol.TutorialDone{ID: "basics"})
	again.expectNone(protocol.SAccount, 200*time.Millisecond)
	// А вот смена ника аккаунта гостю не положена — тут отказ с внятным кодом.
	again.send(protocol.CNickSet, protocol.NickSet{Nick: "Другой"})
	var e protocol.Error
	again.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNoAccount {
		t.Fatalf("гостю ответили %q", e.Code)
	}
}
