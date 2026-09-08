package hub

import (
	"encoding/json"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Общий чат главного меню. Сообщения лежат в памяти hub под общим мьютексом, живут
// h.cfg.ChatTTL (по умолчанию час) и теряются при перезапуске сервера — БД в проекте нет.
// chatCap ограничивает буфер и при флуде внутри часа.
const chatCap = 300

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

	h.chatSeq++
	msg := protocol.ChatMessage{ID: h.chatSeq, Nick: p.Nick, Text: text, TS: now.UnixMilli()}
	h.chat = append(h.chat, msg)
	h.pruneChat(now)

	out := protocol.MustEncode(protocol.SChatMsg, msg)
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
	hist := make([]protocol.ChatMessage, len(h.chat))
	copy(hist, h.chat)
	p.Send(protocol.MustEncode(protocol.SChatHistory, protocol.ChatHistory{Messages: hist}))
}

// pruneChat выбрасывает сообщения старше ChatTTL и обрезает буфер до chatCap.
// Вызывать под h.mu.
func (h *Hub) pruneChat(now time.Time) {
	cut := now.Add(-h.cfg.ChatTTL).UnixMilli()
	i := 0
	for i < len(h.chat) && h.chat[i].TS < cut {
		i++
	}
	if i > 0 {
		h.chat = append(h.chat[:0], h.chat[i:]...)
	}
	if len(h.chat) > chatCap {
		h.chat = append(h.chat[:0], h.chat[len(h.chat)-chatCap:]...)
	}
}
