package hub

import (
	"github.com/coder/websocket"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Роли и баны по IP. Файл на диске правит только админка (internal/moderation), hub его лишь
// читает: запись под h.mu поставила бы fsync поперёк всех матчей. Здесь — последствия:
// выкинуть забаненного, разослать новую роль, стереть чат.

// rank — роль модерации адреса. Вызывать под h.mu (у стора свой мьютекс, дедлока нет).
func (h *Hub) rank(ip string) string { return h.mod.Rank(ip) }

// rankOf — роль игрока: сначала аккаунт, потом адрес. Роль аккаунта сильнее, потому что она
// про человека, а не про сеть, из которой он зашёл; роль по адресу остаётся — для гостей это
// единственный механизм, и уже выданные роли продолжают работать.
func (h *Hub) rankOf(p *session.Player) string {
	if p.AccountID != "" {
		if acc, ok := h.accs.Get(p.AccountID); ok && acc.Rank != "" {
			return acc.Rank
		}
	}
	return h.mod.Rank(p.IP)
}

// banned — заблокирован ли адрес. Вызывать под h.mu.
func (h *Hub) banned(ip string) bool { return h.mod.Banned(ip) }

// accountBanned — заблокирован ли аккаунт игрока. Вызывать под h.mu.
func (h *Hub) accountBanned(p *session.Player) bool {
	if p.AccountID == "" {
		return false
	}
	acc, ok := h.accs.Get(p.AccountID)
	return ok && acc.Banned()
}

// ApplyRankAccount рассылает новую роль аккаунта — зеркало ApplyRank, но по аккаунту, а не по
// адресу. Файл, как и там, пишет админка до вызова и вне h.mu.
func (h *Hub) ApplyRankAccount(accountID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.byAccount[accountID]
	if p != nil {
		p.Send(protocol.MustEncode(protocol.SRank, protocol.RankUpdate{Rank: h.rankOf(p)}))
	}
	// Цвет ника виден всем в истории чата, поэтому её надо перевыслать целиком — ровно как в
	// ApplyRank. Состав идущего матча не трогаем: он зафиксирован при старте.
	for _, other := range h.byID {
		h.sendChatHistory(other)
		if r := h.rooms[other.RoomCode]; r != nil && r.Member(other.ID) != nil {
			h.sendRoomChatHistory(other, r)
		}
	}
	if p != nil && p.RoomCode != "" {
		h.broadcastRoom(h.rooms[p.RoomCode])
	}
	h.log.Info().Str("account", accountID).Msg("moderation: rank applied to account")
}

// ApplyBanAccount выкидывает из игры сессию забаненного аккаунта. Бан адреса остаётся отдельным
// механизмом: он про сеть, этот — про человека, и разлогином от него не спастись.
func (h *Hub) ApplyBanAccount(accountID string) int {
	h.mu.Lock()
	p := h.byAccount[accountID]
	var conn *ws.Conn
	if p != nil {
		p.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: protocol.ErrBanned, Message: "доступ для этого аккаунта заблокирован"}))
		conn, _ = p.Conn.(*ws.Conn)
		h.expirePlayer(p)
		p.ToMenu()
		p.Conn = nil
		h.broadcastOnline(nil)
	}
	h.mu.Unlock()

	if conn != nil {
		conn.Close(websocket.StatusPolicyViolation, "banned")
	}
	h.log.Info().Str("account", accountID).Bool("kicked", conn != nil).Msg("moderation: account ban applied")
	if conn != nil {
		return 1
	}
	return 0
}

// ApplyBan выкидывает из игры всех с этого адреса и закрывает их соединения. Возвращает
// число выкинутых сессий. Сам бан в файл записывает админка до вызова.
func (h *Hub) ApplyBan(ip string) int {
	h.mu.Lock()
	var conns []*ws.Conn
	msg := protocol.MustEncode(protocol.SError, protocol.Error{Code: protocol.ErrBanned, Message: "доступ с этого адреса заблокирован"})
	for _, p := range h.byID {
		if p.IP != ip {
			continue
		}
		p.Send(msg) // очередь отправки писатель дренирует при закрытии — сообщение доедет
		if c, ok := p.Conn.(*ws.Conn); ok {
			conns = append(conns, c)
		}
		h.expirePlayer(p)
		// expirePlayer не сбрасывает место: без этого OnClose полезет в уже покинутый матч.
		p.ToMenu()
		p.Conn = nil
	}
	// Ник забаненного отпускаем: вернуться под ним он всё равно не может, а держать имя
	// заложником до перезапуска сервера незачем.
	h.releaseNicksOfIP(ip)
	if len(conns) > 0 {
		h.broadcastOnline(nil)
	}
	h.mu.Unlock()

	// Закрываем вне мьютекса: OnClose в горутине соединения возьмёт h.mu, и ждать его незачем.
	// c.Session не обнуляем — это гонка с горутиной чтения; безвредно, игрока уже нет в byID.
	for _, c := range conns {
		c.Close(websocket.StatusPolicyViolation, "banned")
	}
	h.log.Info().Str("ip", ip).Int("kicked", len(conns)).Msg("moderation: ban applied")
	return len(conns)
}

// ApplyRank рассылает новую роль адреса: самому игроку — его роль, всем — историю чата
// заново (иначе ники в уже нарисованных сообщениях останутся прежнего цвета), и состояние
// комнат, где он сидит. Состав идущего матча не трогаем: он зафиксирован при старте.
func (h *Hub) ApplyRank(ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rank := h.rank(ip)
	rooms := map[string]bool{}
	for _, p := range h.byID {
		if p.IP != ip {
			continue
		}
		p.Send(protocol.MustEncode(protocol.SRank, protocol.RankUpdate{Rank: rank}))
		if p.RoomCode != "" {
			rooms[p.RoomCode] = true
		}
	}
	for _, p := range h.byID {
		h.sendChatHistory(p)
		// Чат комнаты рисуется теми же строками, значит и его надо перевыслать — иначе
		// в нём ник останется прежнего цвета до выхода из комнаты.
		if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
			h.sendRoomChatHistory(p, r)
		}
	}
	for code := range rooms {
		h.broadcastRoom(h.rooms[code])
	}
	h.log.Info().Str("ip", ip).Str("rank", rank).Msg("moderation: rank applied")
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

// SessionsByIP возвращает ники живых сессий с адреса — админке, чтобы показать, кого затронет
// бан или выдача роли (за одним адресом может сидеть несколько человек).
func (h *Hub) SessionsByIP(ip string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.byID {
		if p.IP == ip {
			out = append(out, p.Nick)
		}
	}
	return out
}
