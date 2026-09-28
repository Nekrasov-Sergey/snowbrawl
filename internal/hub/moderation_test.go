package hub_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// dialRaw подключается без харнесса: забаненный получает ошибку вместо welcome, а expect
// в харнессе на ошибке падает.
//
// Чтение заканчивается на первой ошибке протокола, а не по таймауту контекста: забаненному
// сервер сразу рвёт соединение, а вот при матерном нике оно остаётся живым — и раньше хелпер
// висел в Read все пять секунд, хотя нужный кадр уже пришёл.
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
		if env.Type == protocol.SError {
			break
		}
	}
	return out
}

// guest подключается гостем с кукой: id записи — как в куке браузера, запись заведёт хаб.
func (s *testServer) guest(t *testing.T, nick, ip string) (*client, string) {
	t.Helper()
	id := accounts.NewID()
	return s.dialFull(t, nick, "", ip, id, false), id
}

// setRank выдаёт роль записи так же, как админка: сначала база, потом хаб.
func (s *testServer) setRank(t *testing.T, id, rank string) {
	t.Helper()
	if err := s.accs.SetRank(id, rank); err != nil {
		t.Fatal(err)
	}
	s.hub.ApplyRankAccount(id, rank)
}

// Бан — у записи игрока, а не у адреса: сменив IP, забаненный гость не возвращается, а сосед
// по адресу играет дальше.
func TestBannedGuestRejectedAfterIPChange(t *testing.T) {
	t.Parallel()
	s := newServer(t, func(c *config.Config) { c.TrustProxy = true })
	a, id := s.guest(t, "Аня", "10.0.0.1")
	if err := s.accs.Ban(id, "флуд", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := s.hub.ApplyBanAccount(id); n != 1 {
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

	// Та же кука с другого адреса — всё равно бан.
	again := s.dialFull(t, "Аня", "", "10.0.0.2", id, true)
	again.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBanned {
		t.Fatalf("после смены IP код %q, ожидался banned", e.Code)
	}
	again.expectClosed(t)

	// Сосед по адресу бан не наследует.
	s.connectFrom(t, "Боря", "", "10.0.0.1").close()

	// Разбан возвращает доступ.
	if err := s.accs.Unban(id); err != nil {
		t.Fatal(err)
	}
	back := s.dialFull(t, "", "", "10.0.0.3", id, false)
	if back.Welcome.Nick != "Аня" {
		t.Fatalf("после разбана ник %q", back.Welcome.Nick)
	}
	back.close()
}

func TestBanKicksPlayerFromMatch(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	a, aID := s.guest(t, "Аня", "")
	b, _ := s.guest(t, "Боря", "")
	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var st protocol.RoomState
	a.expect(protocol.SRoomState, &st)
	b.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code, Section: "pvp"})
	b.expect(protocol.SRoomState, &st)
	a.send(protocol.CRoomStart, nil)
	a.expect(protocol.SMatchStart, nil)
	b.expect(protocol.SMatchStart, nil)

	if err := s.accs.Ban(aID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Оба клиента с одного адреса, но бан у записи: выкидывается только забаненный.
	if n := s.hub.ApplyBanAccount(aID); n != 1 {
		t.Fatalf("выкинуто %d сессий, ожидалась одна", n)
	}
	var e protocol.Error
	a.expect(protocol.SError, &e)
	if e.Code != protocol.ErrBanned {
		t.Fatalf("код %q", e.Code)
	}
	if got := s.hub.Stats(); got.Players != 1 || got.Sessions[0].Nick != "Боря" {
		t.Fatalf("сессии после бана: %+v", got.Sessions)
	}
}

func TestRankPushedAndSeenInChatAndLobby(t *testing.T) {
	t.Parallel()
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = time.Hour })
	a, aID := s.guest(t, "Аня", "")
	b := s.connect(t, "Боря", "")

	s.setRank(t, aID, protocol.RankModerator)
	var upd protocol.RankUpdate
	a.expect(protocol.SRank, &upd)
	if upd.Rank != protocol.RankModerator {
		t.Fatalf("роль в пуше: %q", upd.Rank)
	}

	a.send(protocol.CChatSend, protocol.ChatSend{Text: "привет"})
	var m protocol.ChatMessage
	b.expect(protocol.SChatMsg, &m)
	if m.Rank != protocol.RankModerator || m.PID != a.ID {
		t.Fatalf("сообщение без роли или автора: %+v", m)
	}

	a.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var st protocol.RoomState
	a.expect(protocol.SRoomState, &st)
	if len(st.Players) != 1 || st.Players[0].Rank != protocol.RankModerator {
		t.Fatalf("роль в лобби: %+v", st.Players)
	}

	// Роль живёт в записи: переподключение с той же кукой её сохраняет.
	a.close()
	back := s.dialFull(t, "", "", "", aID, false)
	if back.Welcome.Rank != protocol.RankModerator {
		t.Fatalf("роль после переподключения: %q", back.Welcome.Rank)
	}

	// Снятие роли доезжает так же, и история чата перекрашивается.
	s.setRank(t, aID, protocol.RankPlayer)
	back.expect(protocol.SRank, &upd)
	if upd.Rank != protocol.RankPlayer {
		t.Fatalf("роль после снятия: %q", upd.Rank)
	}
	var hist protocol.ChatHistory
	b.expect(protocol.SChatHistory, &hist)
	if len(hist.Messages) != 1 || hist.Messages[0].Rank != protocol.RankPlayer {
		t.Fatalf("история чата не перекрашена: %+v", hist.Messages)
	}
}

func TestChatDeleteRights(t *testing.T) {
	t.Parallel()
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = 0 })
	admin, adminID := s.guest(t, "Аня", "")
	player := s.connect(t, "Боря", "") // гость без кук: роли у него быть не может
	moder, moderID := s.guest(t, "Вика", "")
	moder2, moder2ID := s.guest(t, "Гена", "")
	s.setRank(t, adminID, protocol.RankAdmin)
	s.setRank(t, moderID, protocol.RankModerator)
	s.setRank(t, moder2ID, protocol.RankModerator)

	send := func(cl *client, text string) protocol.ChatMessage {
		cl.send(protocol.CChatSend, protocol.ChatSend{Text: text})
		for {
			var m protocol.ChatMessage
			cl.expect(protocol.SChatMsg, &m)
			if m.Text == text {
				return m
			}
		}
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
	denied := func(cl *client, id uint64, what string) {
		t.Helper()
		cl.send(protocol.CChatDel, protocol.ChatDel{ID: id})
		var e protocol.Error
		cl.expect(protocol.SError, &e)
		if e.Code != protocol.ErrNotAllowed {
			t.Fatalf("%s: код %q, ожидался not_allowed", what, e.Code)
		}
	}

	// Автор удаляет своё сообщение — у всех; повторное удаление не ошибка.
	own := send(player, "моё сообщение")
	player.send(protocol.CChatDel, protocol.ChatDel{ID: own.ID})
	expectDel(admin, own.ID)
	player.send(protocol.CChatDel, protocol.ChatDel{ID: own.ID})

	denied(player, send(admin, "сообщение админа").ID, "игрок удаляет чужое")
	byModer2 := send(moder2, "сообщение модератора")
	denied(moder, byModer2.ID, "модератор удаляет модератора")
	denied(moder, send(admin, "ещё от админа").ID, "модератор удаляет админа")

	byPlayer := send(player, "от игрока")
	moder.send(protocol.CChatDel, protocol.ChatDel{ID: byPlayer.ID})
	expectDel(player, byPlayer.ID)

	admin.send(protocol.CChatDel, protocol.ChatDel{ID: byModer2.ID})
	expectDel(moder2, byModer2.ID)

	// Роль автора берётся актуальная: снятие роли делает его сообщения удаляемыми модератором.
	again := send(moder2, "до снятия роли")
	s.setRank(t, moder2ID, protocol.RankPlayer)
	moder.send(protocol.CChatDel, protocol.ChatDel{ID: again.ID})
	expectDel(moder2, again.ID)
}

func TestChatClearWipesHistoryForAll(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
