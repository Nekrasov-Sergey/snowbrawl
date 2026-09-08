package hub

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/room"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// roomsPerPage — сколько комнат отдаётся одной страницей списка.
const roomsPerPage = 25

// Защита от перебора кодов закрытых комнат: не больше maxCodeTries неудачных попыток
// с одного адреса за codeTryWindow.
const (
	maxCodeTries  = 5
	codeTryWindow = time.Minute
)

// listSub — подписка игрока на страницу списка комнат. last — что ему уже отправлено:
// в тике страница пересобирается и уходит только при изменении.
type listSub struct {
	section string
	page    int
	last    string
}

// codeTry — счётчик неудачных попыток войти по коду с одного адреса.
type codeTry struct {
	n       int
	resetAt time.Time
}

// codeTryAllowed сообщает, можно ли ещё пробовать код с этого адреса. Вызывать под h.mu.
func (h *Hub) codeTryAllowed(ip string) bool {
	t := h.codeTries[ip]
	if t == nil || h.now().After(t.resetAt) {
		return true
	}
	return t.n < maxCodeTries
}

// codeTryFailed отмечает неудачную попытку кода. Вызывать под h.mu.
func (h *Hub) codeTryFailed(ip string) {
	now := h.now()
	t := h.codeTries[ip]
	if t == nil || now.After(t.resetAt) {
		h.codeTries[ip] = &codeTry{n: 1, resetAt: now.Add(codeTryWindow)}
		return
	}
	t.n++
}

// handleRoomList отдаёт страницу списка комнат и подписывает игрока на её обновления.
func (h *Hub) handleRoomList(p *session.Player, data json.RawMessage) {
	var req protocol.RoomList
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.list")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	section := normSection(req.Section)
	page := req.Page
	if page < 0 {
		page = 0
	}
	sub := &listSub{section: section, page: page}
	h.listSubs[p.ID] = sub
	msg, body := h.roomListMessage(section, page)
	sub.last = body
	p.Send(msg)
}

// pushRoomLists рассылает подписчикам изменившиеся страницы. Вызывать под h.mu.
func (h *Hub) pushRoomLists() {
	if len(h.listSubs) == 0 {
		return
	}
	// Страницы кэшируем: несколько игроков обычно смотрят одну и ту же.
	type key struct {
		section string
		page    int
	}
	cache := map[key]struct {
		msg  []byte
		body string
	}{}
	for id, sub := range h.listSubs {
		p := h.byID[id]
		if p == nil || !p.Connected() || p.Place != session.InMenu {
			delete(h.listSubs, id)
			continue
		}
		k := key{sub.section, sub.page}
		c, ok := cache[k]
		if !ok {
			c.msg, c.body = h.roomListMessage(sub.section, sub.page)
			cache[k] = c
		}
		if c.body == sub.last {
			continue
		}
		sub.last = c.body
		p.Send(c.msg)
	}
}

// pushRoomMatches рассылает сводку идущего матча тем, кто сидит в лобби этих комнат: состав по
// слотам, HP и таймер. Как и список комнат, уходит только при изменении содержимого.
// Вызывать под h.mu.
func (h *Hub) pushRoomMatches() {
	for code, r := range h.rooms {
		m := h.matches[r.MatchID]
		if !r.InMatch || m == nil {
			delete(h.matchPush, code)
			continue
		}
		watchers := false
		for _, mem := range r.Members {
			if p := h.byID[mem.ID]; p != nil && p.Place == session.InRoom && p.Connected() {
				watchers = true
				break
			}
		}
		if !watchers {
			delete(h.matchPush, code)
			continue
		}
		st := m.LobbyStatus()
		body, err := json.Marshal(st)
		if err != nil || h.matchPush[code] == string(body) {
			continue
		}
		h.matchPush[code] = string(body)
		msg := protocol.MustEncode(protocol.SRoomMatch, st)
		for _, mem := range r.Members {
			if p := h.byID[mem.ID]; p != nil && p.Place == session.InRoom {
				p.Send(msg)
			}
		}
	}
}

// sendRoomMatch отправляет сводку матча одному игроку. Вызывать под h.mu.
func (h *Hub) sendRoomMatch(p *session.Player, r *room.Room) {
	if r == nil || !r.InMatch {
		return
	}
	if m := h.matches[r.MatchID]; m != nil {
		p.Send(protocol.MustEncode(protocol.SRoomMatch, m.LobbyStatus()))
	}
}

// roomListMessage собирает страницу списка: готовое сообщение и его тело для сравнения.
// Вызывать под h.mu.
func (h *Hub) roomListMessage(section string, page int) ([]byte, string) {
	briefs := h.roomBriefs(section)
	total := len(briefs)
	pages := (total + roomsPerPage - 1) / roomsPerPage
	if page > 0 && page*roomsPerPage >= total {
		page = 0
		if pages > 0 {
			page = pages - 1
		}
	}
	from := page * roomsPerPage
	to := from + roomsPerPage
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	res := protocol.RoomListPage{Section: section, Page: page, Pages: pages, Total: total, Rooms: briefs[from:to]}
	body, err := json.Marshal(res)
	if err != nil {
		return nil, ""
	}
	return protocol.MustEncode(protocol.SRoomList, res), string(body)
}

// roomBriefs — комнаты раздела, старые первыми. Код закрытой комнаты не раскрываем:
// иначе список сам выдаёт то, что защищает код. Вызывать под h.mu.
func (h *Hub) roomBriefs(section string) []protocol.RoomBrief {
	now := h.now()
	out := make([]protocol.RoomBrief, 0, len(h.rooms))
	rooms := make([]*room.Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		if r.IsEmpty() || r.Section() != section {
			continue
		}
		rooms = append(rooms, r)
	}
	// Порядок: открытые со свободными местами → открытые заполненные → закрытые со свободными
	// → закрытые заполненные. Дата создания — последний по важности признак.
	rank := func(r *room.Room) int {
		n := 0
		if r.IsClosed() {
			n += 2
		}
		if len(r.Members) >= r.Capacity() {
			n++
		}
		return n
	}
	sort.Slice(rooms, func(i, j int) bool {
		if ri, rj := rank(rooms[i]), rank(rooms[j]); ri != rj {
			return ri < rj
		}
		if rooms[i].CreatedAt.Equal(rooms[j].CreatedAt) {
			return rooms[i].Code < rooms[j].Code
		}
		return rooms[i].CreatedAt.Before(rooms[j].CreatedAt)
	})
	for _, r := range rooms {
		humans := len(r.Members)
		capacity := r.Capacity()
		b := protocol.RoomBrief{
			Code: r.Code, Section: section, GameMode: r.GameMode, Campaign: r.Campaign,
			Mode: r.Mode, Arena: r.Arena, Humans: humans, Capacity: capacity,
			InMatch: r.InMatch, Visibility: r.Visibility,
			AgeMs: now.Sub(r.CreatedAt).Milliseconds(),
		}
		if b.Bots = capacity - humans; b.Bots < 0 {
			b.Bots = 0
		}
		if p := h.byID[r.HostID]; p != nil {
			b.HostNick = p.Nick
		}
		// Место есть, пока людей меньше, чем слотов: во время матча вошедший садится за
		// бойца своего слота, которого до этого вёл бот.
		b.Joinable = humans < capacity
		if r.IsClosed() {
			b.Code = ""
			b.NeedCode = true
		}
		out = append(out, b)
	}
	return out
}

// handleMatchJoin вводит игрока в идущий матч за бойца его слота в комнате.
func (h *Hub) handleMatchJoin(p *session.Player) {
	if p.Place != session.InRoom {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a room")
		return
	}
	if h.draining {
		h.sendErrP(p, protocol.ErrDraining, "server is restarting soon")
		return
	}
	r := h.rooms[p.RoomCode]
	if r == nil || !r.InMatch {
		h.sendErrP(p, protocol.ErrBusy, "no match in this room")
		return
	}
	m := h.matches[r.MatchID]
	mem := r.Member(p.ID)
	if m == nil || mem == nil {
		h.sendErrP(p, protocol.ErrBusy, "match is over")
		return
	}
	if err := m.Replace(mem.Team, mem.Index, p.ID, p.Nick, h.rank(p.IP), p.Conn); err != nil {
		h.sendErrP(p, protocol.ErrSlotTaken, err.Error())
		return
	}
	p.Training = false
	p.Place, p.MatchID = session.InMatch, m.ID
	p.Send(m.StartMessage(p.ID))
	h.broadcastRoom(r)
	h.log.Info().Str("match", m.ID).Str("player", p.ID).
		Str("slot", mem.Team+strconv.Itoa(mem.Index)).Msg("player joined running match")
}

// normSection приводит раздел к "pvp" или "pve".
func normSection(s string) string {
	if s == "pve" {
		return "pve"
	}
	return "pvp"
}
