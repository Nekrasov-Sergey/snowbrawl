package hub_test

// Чат комнаты: адресация (только участники), история для вошедшего, удаление, общий с
// основным чатом антифлуд и смерть буфера вместе с комнатой.

import (
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// waitHistory ждёт историю нужного чата: при входе в комнату клиенту приходят обе.
func (cl *client) waitHistory(scope string) protocol.ChatHistory {
	cl.t.Helper()
	for i := 0; i < 10; i++ {
		var h protocol.ChatHistory
		cl.expect(protocol.SChatHistory, &h)
		if h.Scope == scope {
			return h
		}
	}
	cl.t.Fatalf("%s: история чата %q не пришла", cl.name, scope)
	return protocol.ChatHistory{}
}

func TestRoomChat(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	host := s.connect(t, "Хост", "")
	guest := s.connect(t, "Гость", "")
	outsider := s.connect(t, "Прохожий", "")

	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)
	guest.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	guest.waitRoom("двое в комнате", func(st protocol.RoomState) bool { return len(st.Players) == 2 })

	// Сообщение в чат комнаты видят оба её участника — и только они.
	host.send(protocol.CChatSend, protocol.ChatSend{Text: "идём по правому краю", Scope: protocol.ChatScopeRoom})
	var m protocol.ChatMessage
	host.expect(protocol.SChatMsg, &m)
	if m.Scope != protocol.ChatScopeRoom || m.Text != "идём по правому краю" {
		t.Fatalf("автор получил %+v", m)
	}
	var got protocol.ChatMessage
	guest.expect(protocol.SChatMsg, &got)
	if got.Scope != protocol.ChatScopeRoom || got.Nick != "Хост" {
		t.Fatalf("сосед по комнате получил %+v", got)
	}
	roomMsgID := got.ID

	// Прохожему чат комнаты не достаётся: первым его chat.msg будет сообщение общего чата.
	// Структура обязательно свежая: json.Unmarshal не обнуляет поля с omitempty, и Scope
	// от предыдущего сообщения остался бы прежним.
	guest.send(protocol.CChatSend, protocol.ChatSend{Text: "всем привет"})
	var open protocol.ChatMessage
	outsider.expect(protocol.SChatMsg, &open)
	if open.Scope != protocol.ChatScopeGlobal || open.Text != "всем привет" {
		t.Fatalf("в меню пришло сообщение комнаты: %+v", open)
	}

	// Вошедший позже видит, о чём говорили в комнате.
	late := s.connect(t, "Поздний", "")
	late.waitHistory(protocol.ChatScopeGlobal)
	late.send(protocol.CRoomJoin, protocol.RoomJoin{Code: rs.Code})
	hist := late.waitHistory(protocol.ChatScopeRoom)
	if len(hist.Messages) != 1 || hist.Messages[0].Text != "идём по правому краю" {
		t.Fatalf("история комнаты = %+v", hist.Messages)
	}

	// Автор удаляет своё сообщение — оно пропадает у всех участников комнаты.
	host.send(protocol.CChatDel, protocol.ChatDel{ID: roomMsgID, Scope: protocol.ChatScopeRoom})
	var del protocol.ChatDel
	guest.expect(protocol.SChatDel, &del)
	if del.ID != roomMsgID || del.Scope != protocol.ChatScopeRoom {
		t.Fatalf("chat.del = %+v", del)
	}

	// Вне комнаты писать в чат комнаты нельзя.
	outsider.send(protocol.CChatSend, protocol.ChatSend{Text: "подслушаю", Scope: protocol.ChatScopeRoom})
	var e protocol.Error
	outsider.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNotAllowed {
		t.Fatalf("ожидался not_allowed, got %s", e.Code)
	}

	// Комната опустела — её чат исчез вместе с ней.
	host.send(protocol.CRoomLeave, nil)
	host.expect(protocol.SRoomLeft, nil)
	guest.send(protocol.CRoomLeave, nil)
	guest.expect(protocol.SRoomLeft, nil)
	late.send(protocol.CRoomLeave, nil)
	late.expect(protocol.SRoomLeft, nil)
	if n := s.hub.Stats().RoomChat; n != 0 {
		t.Fatalf("после ухода всех в чатах комнат осталось %d сообщений", n)
	}
}

// Антифлуд один на оба чата: второй чат не должен становиться обходом паузы.
func TestRoomChatSharesCooldown(t *testing.T) {
	t.Parallel()
	s := newServer(t, func(c *config.Config) { c.ChatCooldown = time.Hour })
	host := s.connect(t, "Хост", "")
	host.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 2, Arena: 0})
	var rs protocol.RoomState
	host.expect(protocol.SRoomState, &rs)

	host.send(protocol.CChatSend, protocol.ChatSend{Text: "первое", Scope: protocol.ChatScopeRoom})
	var m protocol.ChatMessage
	host.expect(protocol.SChatMsg, &m)

	host.send(protocol.CChatSend, protocol.ChatSend{Text: "второе"})
	var e protocol.Error
	host.expect(protocol.SError, &e)
	if e.Code != protocol.ErrChatFlood {
		t.Fatalf("ожидался chat_flood, got %s", e.Code)
	}
}
