package hub

import (
	"encoding/json"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/censor"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Общий чат главного меню. Сообщения лежат в памяти hub под общим мьютексом, живут
// h.cfg.ChatTTL (по умолчанию час) и теряются при перезапуске сервера — БД в проекте нет.
// chatCap ограничивает буфер и при флуде внутри часа.
const chatCap = 300

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

// handleChatSend принимает сообщение игрока и рассылает его всем подключённым.
// Вызывать под h.mu.
func (h *Hub) handleChatSend(p *session.Player, data json.RawMessage) {
	var req protocol.ChatSend
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad chat.send")
		return
	}
	text, err := protocol.NormalizeChat(req.Text)
	if err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "empty message")
		return
	}
	now := h.now()
	if !p.LastChatAt.IsZero() && now.Sub(p.LastChatAt) < h.cfg.ChatCooldown {
		h.sendErrP(p, protocol.ErrChatFlood, "slow down")
		return
	}
	p.LastChatAt = now

	text = censor.Mask(text) // мат под звёздочками, само сообщение доходит
	h.chatSeq++
	e := chatEntry{msg: protocol.ChatMessage{ID: h.chatSeq, PID: p.ID, Nick: p.Nick, Text: text, TS: now.UnixMilli()}, authorIP: p.IP}
	h.chat = append(h.chat, e)
	h.pruneChat(now)

	out := protocol.MustEncode(protocol.SChatMsg, h.chatOut(e))
	for _, q := range h.byID {
		q.Send(out)
	}
	h.log.Debug().Str("player", p.ID).Str("nick", p.Nick).Msg("chat message")
}

// sendChatHistory отправляет игроку последние сообщения чата. Вызывать под h.mu.
func (h *Hub) sendChatHistory(p *session.Player) {
	h.pruneChat(h.now())
	if len(h.chat) == 0 {
		return
	}
	hist := make([]protocol.ChatMessage, 0, len(h.chat))
	for _, e := range h.chat {
		hist = append(hist, h.chatOut(e))
	}
	p.Send(protocol.MustEncode(protocol.SChatHistory, protocol.ChatHistory{Messages: hist}))
}

// handleChatDel удаляет сообщение у всех. Права: автор — своё; «Создатель» — любое;
// «Админ» — только сообщения обычных игроков (ни создателя, ни других админов).
// Вызывать под h.mu.
func (h *Hub) handleChatDel(p *session.Player, data json.RawMessage) {
	var req protocol.ChatDel
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad chat.del")
		return
	}
	idx := -1
	for i := range h.chat {
		if h.chat[i].msg.ID == req.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return // уже удалено или выпало из буфера по TTL: операция идемпотентна
	}
	if !h.canDeleteChat(p, h.chat[idx]) {
		h.sendErrP(p, protocol.ErrNotAllowed, "нельзя удалить это сообщение")
		return
	}
	h.chat = append(h.chat[:idx], h.chat[idx+1:]...)
	msg := protocol.MustEncode(protocol.SChatDel, protocol.ChatDel{ID: req.ID})
	for _, q := range h.byID {
		q.Send(msg)
	}
	h.log.Info().Str("player", p.ID).Str("nick", p.Nick).Str("rank", h.rank(p.IP)).
		Uint64("message", req.ID).Msg("chat message deleted")
}

// canDeleteChat — права на удаление сообщения. Вызывать под h.mu.
func (h *Hub) canDeleteChat(p *session.Player, e chatEntry) bool {
	if e.msg.PID == p.ID {
		return true // своё сообщение может удалить любой
	}
	switch h.rank(p.IP) {
	case protocol.RankCreator:
		return true
	case protocol.RankAdmin:
		return h.rank(e.authorIP) == protocol.RankPlayer
	default:
		return false
	}
}

// pruneChat выбрасывает сообщения старше ChatTTL и обрезает буфер до chatCap.
// Вызывать под h.mu.
func (h *Hub) pruneChat(now time.Time) {
	cut := now.Add(-h.cfg.ChatTTL).UnixMilli()
	i := 0
	for i < len(h.chat) && h.chat[i].msg.TS < cut {
		i++
	}
	if i > 0 {
		h.chat = append(h.chat[:0], h.chat[i:]...)
	}
	if len(h.chat) > chatCap {
		h.chat = append(h.chat[:0], h.chat[len(h.chat)-chatCap:]...)
	}
}
