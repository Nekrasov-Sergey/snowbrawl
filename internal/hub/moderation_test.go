package hub_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// dialRaw подключается без харнесса: забаненный получает ошибку вместо welcome, а expect
// в харнессе на ошибке падает.
func dialRaw(t *testing.T, s *testServer, nick string) []protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(ctx, websocket.MessageText, protocol.MustEncode(protocol.CHello,
		protocol.Hello{Nick: nick, BuildVersion: "dev", ProtocolVersion: protocol.Version}))
	var out []protocol.Envelope
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break // сервер закрыл соединение
		}
		env, _ := protocol.Decode(data)
		out = append(out, env)
	}
	return out
}

func TestBannedIPRejectedOnHello(t *testing.T) {
	s := newServer(t, nil)
	a := s.connect(t, "Аня", "")
	if err := s.mod.Ban("127.0.0.1", "Аня", "флуд", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := s.hub.ApplyBan("127.0.0.1"); n != 1 {
		t.Fatalf("выкинуто сессий: %d, ожидалась одна", n)
	}
	// Забаненному уходит ошибка, соединение закрывается.
	var e protocol.Error
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBanned {
		t.Fatalf("код ошибки %q, ожидался banned", e.Code)
	}
	if st := s.hub.Stats(); st.Players != 0 {
		t.Fatalf("сессия должна быть удалена, осталось %d", st.Players)
	}

	// Повторный вход отклоняется.
	msgs := dialRaw(t, s, "Аня")
	sawBanned := false
	for _, env := range msgs {
		if env.Type == protocol.SWelcome {
			t.Fatal("забаненный не должен получать welcome")
		}
		if env.Type == protocol.SError {
			var er protocol.Error
			_ = json.Unmarshal(env.Data, &er)
			if er.Code == protocol.ErrBanned {
				sawBanned = true
			}
		}
	}
	if !sawBanned {
		t.Fatalf("ожидалась ошибка banned, пришло %v", msgs)
	}

	// Разбан возвращает доступ.
	if err := s.mod.Unban("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	back := s.connect(t, "Аня", "")
	back.close()
}

func TestBanKicksPlayerFromMatch(t *testing.T) {
	s := newServer(t, nil)
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")
	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var st protocol.RoomState
	a.expect(protocol.SRoomState, &st)
	b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pvp"})
	b.expect(protocol.SRoomState, &st)
	a.send(protocol.CRoomStart, nil)
	a.expect(protocol.SMatchStart, nil)
	b.expect(protocol.SMatchStart, nil)

	if err := s.mod.Ban("127.0.0.1", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Оба клиента с одного адреса: бан по IP выкидывает всех, кто с него играет.
	if n := s.hub.ApplyBan("127.0.0.1"); n != 2 {
		t.Fatalf("выкинуто %d сессий, ожидалось 2", n)
	}
	var e protocol.Error
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBanned {
		t.Fatalf("код %q", e.Code)
	}
	if got := s.hub.Stats(); got.Players != 0 {
		t.Fatalf("сессии остались: %+v", got.Sessions)
	}
}

func TestRankPushedAndSeenInChatAndLobby(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = time.Hour })
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")

	if err := s.mod.SetRank("127.0.0.1", protocol.RankAdmin, "Аня", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.hub.ApplyRank("127.0.0.1")
	var upd protocol.RankUpdate
	a.expect(protocol.SRank, &upd)
	if upd.Rank != protocol.RankAdmin {
		t.Fatalf("роль в пуше: %q", upd.Rank)
	}

	a.send(protocol.CChatSend, protocol.ChatSend{Text: "привет"})
	var m protocol.ChatMessage
	b.expect(protocol.SChatMsg, &m)
	if m.Rank != protocol.RankAdmin || m.PID != a.ID {
		t.Fatalf("сообщение без роли или автора: %+v", m)
	}

	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var st protocol.RoomState
	a.expect(protocol.SRoomState, &st)
	if len(st.Players) != 1 || st.Players[0].Rank != protocol.RankAdmin {
		t.Fatalf("роль в лобби: %+v", st.Players)
	}

	// Снятие роли доезжает так же.
	if err := s.mod.SetRank("127.0.0.1", protocol.RankPlayer, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.hub.ApplyRank("127.0.0.1")
	a.expect(protocol.SRank, &upd)
	if upd.Rank != protocol.RankPlayer {
		t.Fatalf("роль после снятия: %q", upd.Rank)
	}
}

func TestChatDeleteRights(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = 0 })
	a := s.connect(t, "Аня", "")  // станет админом
	b := s.connect(t, "Боря", "") // обычный игрок
	c := s.connect(t, "Вика", "") // станет создателем

	// Роль по IP одна на всех в тесте, поэтому проверяем правила по одному, меняя роль.
	send := func(cl *client, text string) protocol.ChatMessage {
		cl.send(protocol.CChatSend, protocol.ChatSend{Text: text})
		var m protocol.ChatMessage
		cl.expect(protocol.SChatMsg, &m)
		return m
	}
	// chat.del рассылается всем, и в очереди клиента лежат удаления из предыдущих шагов —
	// поэтому ждём именно нужный id.
	expectDel := func(cl *client, id uint64) {
		t.Helper()
		for i := 0; i < 10; i++ {
			var d protocol.ChatDel
			cl.expect(protocol.SChatDel, &d)
			if d.ID == id {
				return
			}
		}
		t.Fatalf("%s не получил удаление сообщения %d", cl.name, id)
	}

	// 1. Автор удаляет своё сообщение — у всех.
	own := send(b, "моё сообщение")
	b.send(protocol.CChatDel, protocol.ChatDel{ID: own.ID})
	expectDel(a, own.ID)
	// Повторное удаление того же id не ошибка.
	b.send(protocol.CChatDel, protocol.ChatDel{ID: own.ID})

	// 2. Обычный игрок не может удалить чужое.
	other := send(a, "чужое сообщение")
	b.send(protocol.CChatDel, protocol.ChatDel{ID: other.ID})
	var e protocol.Error
	b.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNotAllowed {
		t.Fatalf("код %q, ожидался not_allowed", e.Code)
	}

	// 3. Админ не может удалить сообщение другого админа. Роль в тесте одна на IP, поэтому
	// «админ удаляет обычного игрока» проверяется отдельно в TestCanDeleteChatRules, где
	// адреса разные.
	if err := s.mod.SetRank("127.0.0.1", protocol.RankAdmin, "Аня", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.hub.ApplyRank("127.0.0.1")
	adminMsg := send(b, "сообщение админа")
	a.send(protocol.CChatDel, protocol.ChatDel{ID: adminMsg.ID})
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNotAllowed {
		t.Fatalf("код %q, ожидался not_allowed на сообщение админа", e.Code)
	}

	// 4. Создатель удаляет сообщение админа.
	if err := s.mod.SetRank("127.0.0.1", protocol.RankCreator, "Вика", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.hub.ApplyRank("127.0.0.1")
	c.send(protocol.CChatDel, protocol.ChatDel{ID: adminMsg.ID})
	expectDel(b, adminMsg.ID)
}

func TestChatClearWipesHistoryForAll(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = 0 })
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")
	a.send(protocol.CChatSend, protocol.ChatSend{Text: "первое"})
	b.expect(protocol.SChatMsg, nil)

	if n := s.hub.ClearChat(); n != 1 {
		t.Fatalf("стёрто %d сообщений", n)
	}
	a.expect(protocol.SChatClear, nil)
	b.expect(protocol.SChatClear, nil)

	// Новый клиент истории не получает (пустая не отправляется), а новое сообщение приходит.
	c := s.connect(t, "Вика", "")
	a.send(protocol.CChatSend, protocol.ChatSend{Text: "после очистки"})
	var m protocol.ChatMessage
	c.expect(protocol.SChatMsg, &m)
	if m.Text != "после очистки" {
		t.Fatalf("сообщение: %+v", m)
	}
	if st := s.hub.Stats(); st.ChatSize != 1 {
		t.Fatalf("в чате %d сообщений", st.ChatSize)
	}
}

func TestChatCensored(t *testing.T) {
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = 0 })
	a := s.connect(t, "Аня", "")
	b := s.connect(t, "Боря", "")
	a.send(protocol.CChatSend, protocol.ChatSend{Text: "ну ты и мудак"})
	var m protocol.ChatMessage
	b.expect(protocol.SChatMsg, &m)
	if strings.Contains(m.Text, "мудак") {
		t.Fatalf("мат не замаскирован: %q", m.Text)
	}
	if !strings.HasPrefix(m.Text, "ну ты и ") || !strings.Contains(m.Text, "*") {
		t.Fatalf("сообщение должно дойти со звёздочками: %q", m.Text)
	}
}

func TestProfaneNickRejected(t *testing.T) {
	s := newServer(t, nil)
	msgs := dialRaw(t, s, "мудак")
	for _, env := range msgs {
		if env.Type == protocol.SWelcome {
			t.Fatal("матерный ник не должен приниматься")
		}
	}
	// Код отдельный от bad_nick: клиент должен назвать игроку настоящую причину.
	sawProfanity := false
	for _, env := range msgs {
		if env.Type == protocol.SError {
			var e protocol.Error
			_ = json.Unmarshal(env.Data, &e)
			if e.Code == protocol.ErrNickProfanity {
				sawProfanity = true
			}
		}
	}
	if !sawProfanity {
		t.Fatalf("ожидалась ошибка nick_profanity, пришло %v", msgs)
	}
}

func TestJoinWrongSectionRejected(t *testing.T) {
	s := newServer(t, nil)
	a := s.connect(t, "Аня", "")
	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0, GameMode: "survival"})
	var st protocol.RoomState
	a.expect(protocol.SRoomState, &st)

	b := s.connect(t, "Боря", "")
	b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pvp"})
	var e protocol.Error
	b.expect(protocol.SError, &e)
	if e.Code != protocol.ErrWrongSection {
		t.Fatalf("код %q, ожидался wrong_section", e.Code)
	}

	// Тот же код из своего раздела работает.
	b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pve"})
	b.expect(protocol.SRoomState, &st)

	// Попытка «не туда» тратит лимит кода: иначе ответ был бы оракулом «код существует».
	b.send(protocol.CRoomLeave, nil)
	b.expect(protocol.SRoomLeft, nil)
	for i := 0; i < 4; i++ {
		b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pvp"})
		b.expect(protocol.SError, &e)
		if e.Code != protocol.ErrWrongSection {
			t.Fatalf("попытка %d: код %q", i, e.Code)
		}
	}
	b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pvp"})
	b.expect(protocol.SError, &e)
	if e.Code != protocol.ErrTooManyTries {
		t.Fatalf("после пяти неудач ожидался too_many_tries, got %q", e.Code)
	}
}

func TestStatsSortsSessionsByJoinTime(t *testing.T) {
	s := newServer(t, nil)
	// Ники в обратном алфавитном порядке: сортировка должна быть по времени входа.
	first := s.connect(t, "Яна", "")
	time.Sleep(5 * time.Millisecond)
	second := s.connect(t, "Боря", "")
	time.Sleep(5 * time.Millisecond)
	third := s.connect(t, "Аня", "")

	st := s.hub.Stats()
	if len(st.Sessions) != 3 {
		t.Fatalf("сессий %d", len(st.Sessions))
	}
	want := []string{first.ID, second.ID, third.ID}
	for i, id := range want {
		if st.Sessions[i].ID != id {
			t.Fatalf("порядок сессий: %d-я %s, ожидался %s (%+v)", i, st.Sessions[i].ID, id, st.Sessions)
		}
		if st.Sessions[i].Since.IsZero() {
			t.Fatalf("у сессии %s нет времени входа", id)
		}
	}
}

// TestStatsIsStableBetweenCalls — предусловие SSE-потока админки: при неизменном состоянии
// сводка должна быть побайтово той же. Ловит и «мс назад» в полях, и несортированные map.
func TestStatsIsStableBetweenCalls(t *testing.T) {
	s := newServer(t, nil)
	a := s.connect(t, "Аня", "")
	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	a.expect(protocol.SRoomState, nil)
	b := s.connect(t, "Боря", "")
	b.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 3, Arena: 1})
	b.expect(protocol.SRoomState, nil)

	dump := func() string {
		st := s.hub.Stats()
		st.Now = time.Time{} // время снимка меняется всегда, оно и не входит в отпечаток
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	first := dump()
	time.Sleep(30 * time.Millisecond)
	if second := dump(); first != second {
		t.Fatalf("сводка меняется сама по себе:\n%s\n%s", first, second)
	}
}
