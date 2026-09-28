package hub

import (
	"github.com/coder/websocket"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Роли и баны. Они принадлежат записи игрока (internal/accounts), а не адресу: у гостя запись
// тоже есть, и бан следует за ним при смене IP. Запись в базу делает админка до вызова и вне
// h.mu; здесь — последствия: выкинуть забаненного, разослать новую роль, стереть чат.
//
// У браузера без кук записи нет, и роль с баном ему не выдать — это цена отказа от IP-модерации:
// сосед по NAT или мобильному оператору больше не получает чужого модератора или чужой бан.

// rankOf — роль игрока, снятая с записи при подключении. Вызывать под h.mu.
func (h *Hub) rankOf(p *session.Player) string { return p.Rank }

// ApplyRankAccount рассылает новую роль записи: самому игроку — его роль, всем — историю чата
// заново (иначе ники в уже нарисованных сообщениях останутся прежнего цвета), и состояние
// комнаты, где он сидит. Состав идущего матча не трогаем: он зафиксирован при старте.
func (h *Hub) ApplyRankAccount(accountID, rank string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.byAccount[accountID]
	if p != nil {
		p.Rank = rank
		p.Send(protocol.MustEncode(protocol.SRank, protocol.RankUpdate{Rank: rank}))
	}
	// Роль автора в чате — актуальная, а не на момент отправки: иначе снятие роли с модератора
	// не давало бы другому модератору удалить его старые сообщения.
	setRank := func(buf []chatEntry) {
		for i := range buf {
			if buf[i].authorAccount == accountID {
				buf[i].authorRank = rank
			}
		}
	}
	setRank(h.chat)
	for _, buf := range h.roomChat {
		setRank(buf)
	}
	for _, other := range h.byID {
		h.sendChatHistory(other)
		// Чат комнаты рисуется теми же строками, значит и его надо перевыслать.
		if r := h.rooms[other.RoomCode]; r != nil && r.Member(other.ID) != nil {
			h.sendRoomChatHistory(other, r)
		}
	}
	if p != nil && p.RoomCode != "" {
		h.broadcastRoom(h.rooms[p.RoomCode])
	}
	h.log.Info().Str("account", accountID).Str("rank", rank).Msg("moderation: rank applied")
}

// ApplyBanAccount выкидывает из игры сессию забаненной записи. Возвращает число выкинутых.
func (h *Hub) ApplyBanAccount(accountID string) int {
	h.mu.Lock()
	p := h.byAccount[accountID]
	var conn *ws.Conn
	if p != nil {
		// Очередь отправки писатель дренирует при закрытии — сообщение доедет.
		p.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: protocol.ErrBanned, Message: "доступ заблокирован"}))
		conn, _ = p.Conn.(*ws.Conn)
		h.expirePlayer(p)
		// expirePlayer не сбрасывает место: без этого OnClose полезет в уже покинутый матч.
		p.ToMenu()
		p.Conn = nil
		h.broadcastOnline(nil)
	}
	h.mu.Unlock()

	// Закрываем вне мьютекса: OnClose в горутине соединения возьмёт h.mu, и ждать его незачем.
	if conn != nil {
		conn.Close(websocket.StatusPolicyViolation, "banned")
	}
	h.log.Info().Str("account", accountID).Bool("kicked", conn != nil).Msg("moderation: ban applied")
	if conn != nil {
		return 1
	}
	return 0
}

// ClearChat стирает историю общего чата у всех. Чаты комнат не трогает: они живут не дольше
// своей комнаты и никому, кроме её участников, не видны. Возвращает, сколько сообщений стёрто.
// chatSeq не сбрасываем: новые id столкнулись бы с теми, что клиенты держат в разметке.
func (h *Hub) ClearChat() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.chat)
	h.chat = nil
	msg := protocol.MustEncode(protocol.SChatClear, nil)
	for _, p := range h.byID {
		p.Send(msg)
	}
	h.log.Info().Int("messages", n).Msg("moderation: chat cleared")
	return n
}
