package hub

import (
	"encoding/json"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/censor"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/room"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Чаты игры. Общий чат главного меню виден всем подключённым, чат комнаты — только её
// участникам (включая тех, кто сейчас в матче). Сообщения лежат в памяти hub под общим
// мьютексом, живут h.cfg.ChatTTL (по умолчанию час) и теряются при перезапуске сервера —
// БД в проекте нет. Чат комнаты живёт не дольше самой комнаты: буфер удаляется вместе с ней.
// chatCap и roomChatCap ограничивают буферы и при флуде внутри часа.
const (
	chatCap     = 300
	roomChatCap = 100
)

// chatEntry — сообщение и адрес автора. Адрес нужен для прав модерации: роль автора берётся
// АКТУАЛЬНАЯ, а не та, что была на момент отправки. Иначе снятие роли с админа не давало бы
// другому админу удалить его старые сообщения (и наоборот).
type chatEntry struct {
	msg      protocol.ChatMessage
	authorIP string
}

// chatOut достраивает сообщение для отправки: роль автора считается здесь и только здесь.
// Вызывать под h.mu.
func (h *Hub) chatOut(e chatEntry) protocol.ChatMessage {
	out := e.msg
	out.Rank = h.rank(e.authorIP)
	return out
}

// chatRoom разбирает область чата из сообщения клиента. Для общего чата возвращает nil,
// для чата комнаты — комнату игрока. Второй результат false означает отказ (ошибка уже
// отправлена). Комната годится и когда игрок в матче: слот в комнате за ним сохраняется.
// Вызывать под h.mu.
func (h *Hub) chatRoom(p *session.Player, scope string) (*room.Room, bool) {
	if scope != protocol.ChatScopeRoom {
		return nil, true
	}
	r := h.rooms[p.RoomCode]
	if r == nil || r.Member(p.ID) == nil {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a room")
		return nil, false
	}
	return r, true
}

// chatBuf возвращает буфер чата: общий (r == nil) или комнаты. Вызывать под h.mu.
func (h *Hub) chatBuf(r *room.Room) []chatEntry {
	if r == nil {
		return h.chat
	}
	return h.roomChat[r.Code]
}

// setChatBuf записывает буфер обратно. Вызывать под h.mu.
func (h *Hub) setChatBuf(r *room.Room, list []chatEntry) {
	if r == nil {
		h.chat = list
		return
	}
	if len(list) == 0 {
		delete(h.roomChat, r.Code)
		return
	}
	h.roomChat[r.Code] = list
}

// chatSend рассылает сообщение аудитории чата: общий — всем подключённым, чат комнаты —
// её участникам. Вызывать под h.mu.
func (h *Hub) chatSend(r *room.Room, msg []byte) {
	if r != nil {
		h.sendToRoom(r, msg)
		return
	}
	for _, q := range h.byID {
		q.Send(msg)
	}
}

// handleChatSend принимает сообщение игрока и рассылает его аудитории выбранного чата.
// Вызывать под h.mu.
func (h *Hub) handleChatSend(p *session.Player, data json.RawMessage) {
	var req protocol.ChatSend
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad chat.send")
		return
	}
	r, ok := h.chatRoom(p, req.Scope)
	if !ok {
		return
	}
	text, err := protocol.NormalizeChat(req.Text)
	if err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "empty message")
		return
	}
	now := h.now()
	// Кулдаун один на оба чата: второй чат не должен становиться обходом антифлуда.
	if !p.LastChatAt.IsZero() && now.Sub(p.LastChatAt) < h.cfg.ChatCooldown {
		h.sendErrP(p, protocol.ErrChatFlood, "slow down")
		return
	}
	p.LastChatAt = now

	text = censor.Mask(text) // мат под звёздочками, само сообщение доходит
	h.chatSeq++              // нумерация общая для всех чатов: id сообщения уникален
	scope := protocol.ChatScopeGlobal
	if r != nil {
		scope = protocol.ChatScopeRoom
	}
	e := chatEntry{msg: protocol.ChatMessage{ID: h.chatSeq, PID: p.ID, Nick: p.Nick, Text: text,
		TS: now.UnixMilli(), Scope: scope}, authorIP: p.IP}
	h.setChatBuf(r, append(h.chatBuf(r), e))
	h.pruneChat(now)

	h.chatSend(r, protocol.MustEncode(protocol.SChatMsg, h.chatOut(e)))
	h.log.Debug().Str("player", p.ID).Str("nick", p.Nick).Str("scope", scope).Msg("chat message")
}

// sendChatHistory отправляет игроку последние сообщения общего чата. Вызывать под h.mu.
func (h *Hub) sendChatHistory(p *session.Player) {
	h.pruneChat(h.now())
	h.sendHistory(p, nil)
}

// sendRoomChatHistory отправляет игроку историю чата его комнаты. Вызывать под h.mu.
func (h *Hub) sendRoomChatHistory(p *session.Player, r *room.Room) {
	if r == nil {
		return
	}
	h.pruneChat(h.now())
	h.sendHistory(p, r)
}

// sendHistory отправляет одну историю: общую (r == nil) или комнаты. Вызывать под h.mu.
func (h *Hub) sendHistory(p *session.Player, r *room.Room) {
	buf := h.chatBuf(r)
	if len(buf) == 0 {
		return
	}
	hist := make([]protocol.ChatMessage, 0, len(buf))
	for _, e := range buf {
		hist = append(hist, h.chatOut(e))
	}
	scope := protocol.ChatScopeGlobal
	if r != nil {
		scope = protocol.ChatScopeRoom
	}
	p.Send(protocol.MustEncode(protocol.SChatHistory, protocol.ChatHistory{Messages: hist, Scope: scope}))
}

// handleChatDel удаляет сообщение у всей аудитории его чата. Права: автор — своё;
// «Админ» — любое; «Модератор» — только сообщения обычных игроков (ни админов, ни других
// модераторов). Вызывать под h.mu.
func (h *Hub) handleChatDel(p *session.Player, data json.RawMessage) {
	var req protocol.ChatDel
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad chat.del")
		return
	}
	r, ok := h.chatRoom(p, req.Scope)
	if !ok {
		return
	}
	buf := h.chatBuf(r)
	idx := -1
	for i := range buf {
		if buf[i].msg.ID == req.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return // уже удалено или выпало из буфера по TTL: операция идемпотентна
	}
	if !h.canDeleteChat(p, buf[idx]) {
		h.sendErrP(p, protocol.ErrNotAllowed, "нельзя удалить это сообщение")
		return
	}
	scope := buf[idx].msg.Scope
	h.setChatBuf(r, append(buf[:idx], buf[idx+1:]...))
	h.chatSend(r, protocol.MustEncode(protocol.SChatDel, protocol.ChatDel{ID: req.ID, Scope: scope}))
	h.log.Info().Str("player", p.ID).Str("nick", p.Nick).Str("rank", h.rank(p.IP)).
		Str("scope", scope).Uint64("message", req.ID).Msg("chat message deleted")
}

// canDeleteChat — права на удаление сообщения. Вызывать под h.mu.
func (h *Hub) canDeleteChat(p *session.Player, e chatEntry) bool {
	if e.msg.PID == p.ID {
		return true // своё сообщение может удалить любой
	}
	switch h.rank(p.IP) {
	case protocol.RankAdmin:
		return true
	case protocol.RankModerator:
		return h.rank(e.authorIP) == protocol.RankPlayer
	default:
		return false
	}
}

// pruneChat выбрасывает сообщения старше ChatTTL и обрезает буферы всех чатов.
// Вызывать под h.mu.
func (h *Hub) pruneChat(now time.Time) {
	cut := now.Add(-h.cfg.ChatTTL).UnixMilli()
	h.chat = pruneChatBuf(h.chat, cut, chatCap)
	for code, buf := range h.roomChat {
		buf = pruneChatBuf(buf, cut, roomChatCap)
		if len(buf) == 0 {
			delete(h.roomChat, code)
			continue
		}
		h.roomChat[code] = buf
	}
}

// pruneChatBuf чистит один буфер: сначала по времени, потом по размеру.
func pruneChatBuf(buf []chatEntry, cut int64, limit int) []chatEntry {
	i := 0
	for i < len(buf) && buf[i].msg.TS < cut {
		i++
	}
	if i > 0 {
		buf = append(buf[:0], buf[i:]...)
	}
	if len(buf) > limit {
		buf = append(buf[:0], buf[len(buf)-limit:]...)
	}
	return buf
}
