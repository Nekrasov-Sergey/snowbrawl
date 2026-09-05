// Package hub — центр сервера: сессии игроков, комнаты, очереди Quick Match, реестр
// матчей, дренаж. Все структуры данных под одним мьютексом; матчи живут в своих
// горутинах и общаются с hub через потокобезопасные методы и колбэк onEnd.
package hub

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/match"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/matchmaking"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/room"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Hub — состояние сервера.
type Hub struct {
	cfg  config.Config
	prog *sim.Program
	log  zerolog.Logger
	now  func() time.Time

	mu       sync.Mutex
	byToken  map[string]*session.Player
	byID     map[string]*session.Player
	rooms    map[string]*room.Room
	queues   *matchmaking.Queues
	matches  map[string]*match.Match
	draining bool
	drainAt  time.Time
	rng      *rand.Rand

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New создаёт hub.
func New(cfg config.Config, prog *sim.Program, log zerolog.Logger) *Hub {
	return &Hub{
		cfg: cfg, prog: prog, log: log, now: time.Now,
		byToken: map[string]*session.Player{}, byID: map[string]*session.Player{},
		rooms: map[string]*room.Room{}, queues: matchmaking.New(cfg.QueueWait),
		matches: map[string]*match.Match{},
		rng:     rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xDEADBEEF)),
		stopCh:  make(chan struct{}),
	}
}

// Run запускает фоновый цикл таймаутов (очереди, TTL сессий и комнат).
func (h *Hub) Run() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-t.C:
				h.tick()
			}
		}
	}()
}

// Shutdown останавливает матчи и закрывает соединения.
func (h *Hub) Shutdown() {
	close(h.stopCh)
	h.wg.Wait()
	h.mu.Lock()
	matches := make([]*match.Match, 0, len(h.matches))
	for _, m := range h.matches {
		matches = append(matches, m)
	}
	conns := make([]*ws.Conn, 0, len(h.byID))
	for _, p := range h.byID {
		if c, ok := p.Conn.(*ws.Conn); ok {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	for _, m := range matches {
		m.Stop(match.ReasonShutdown)
	}
	time.Sleep(200 * time.Millisecond) // дать match.end уйти в сокеты
	for _, c := range conns {
		c.Close(websocket.StatusGoingAway, "server restart")
	}
}

// ---- ws.Handler ----

// OnMessage обрабатывает сообщение клиента.
func (h *Hub) OnMessage(c *ws.Conn, env protocol.Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, _ := c.Session.(*session.Player)
	if p == nil {
		if env.Type != protocol.CHello {
			h.sendErr(c, protocol.ErrNotAllowed, "send hello first")
			return
		}
		h.handleHello(c, env.Data)
		return
	}
	if p.Conn != c {
		// Старое соединение, которое уже заменено новым.
		c.Close(websocket.StatusPolicyViolation, "replaced")
		return
	}
	switch env.Type {
	case protocol.CHello:
		h.sendErr(c, protocol.ErrNotAllowed, "already said hello")
	case protocol.CPing:
		c.Send(protocol.MustEncode(protocol.SPong, nil))
	case protocol.CQueueJoin:
		h.handleQueueJoin(p, env.Data)
	case protocol.CQueueLeave:
		h.leaveQueue(p)
		h.sendQueueStatus(p)
	case protocol.CRoomCreate:
		h.handleRoomCreate(p, env.Data)
	case protocol.CRoomJoin:
		h.handleRoomJoin(p, env.Data)
	case protocol.CRoomSlot:
		h.handleRoomSlot(p, env.Data)
	case protocol.CRoomRole:
		h.handleRoomRole(p, env.Data)
	case protocol.CRoomConfig:
		h.handleRoomConfig(p, env.Data)
	case protocol.CRoomKick:
		h.handleRoomKick(p, env.Data)
	case protocol.CRoomStart:
		h.handleRoomStart(p)
	case protocol.CRoomLeave:
		h.leaveRoom(p, true)
	case protocol.CMatchLeave:
		h.leaveMatch(p)
	case protocol.CInput:
		h.handleInput(p, env.Data)
	default:
		h.sendErr(c, protocol.ErrBadMessage, "unknown type "+env.Type)
	}
}

// OnClose — соединение закрылось.
func (h *Hub) OnClose(c *ws.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, _ := c.Session.(*session.Player)
	if p == nil || p.Conn != c {
		return
	}
	p.Conn = nil
	p.DisconnectedAt = h.now()
	switch p.Place {
	case session.InQueue:
		h.leaveQueue(p)
	case session.InRoom:
		h.broadcastRoom(h.rooms[p.RoomCode])
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			m.Detach(p.ID)
		}
	}
	h.log.Debug().Str("player", p.ID).Str("place", string(p.Place)).Msg("player disconnected")
}

// ---- hello / сессии ----

func (h *Hub) handleHello(c *ws.Conn, data json.RawMessage) {
	var hello protocol.Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		h.sendErr(c, protocol.ErrBadMessage, "bad hello")
		c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	if hello.ProtocolVersion != protocol.Version {
		h.sendErr(c, protocol.ErrBadVersion, fmt.Sprintf("protocol %d required", protocol.Version))
		c.Send(protocol.MustEncode(protocol.SReload, nil))
		c.Close(websocket.StatusPolicyViolation, "bad protocol version")
		return
	}
	if h.cfg.BuildVersion != "dev" && hello.BuildVersion != "" && hello.BuildVersion != h.cfg.BuildVersion {
		c.Send(protocol.MustEncode(protocol.SReload, nil))
		c.Close(websocket.StatusPolicyViolation, "stale client")
		return
	}
	now := h.now()
	var p *session.Player
	if hello.Token != "" {
		p = h.byToken[hello.Token]
	}
	if p == nil {
		nick, err := protocol.NormalizeNick(hello.Nick)
		if err != nil {
			h.sendErr(c, protocol.ErrBadNick, err.Error())
			return
		}
		p = session.New(nick, c.IP(), now)
		h.byToken[p.Token] = p
		h.byID[p.ID] = p
		h.log.Info().Str("player", p.ID).Str("nick", nick).Str("ip", p.IP).Msg("new player")
	} else {
		if hello.Nick != "" && p.Place == session.InMenu {
			if nick, err := protocol.NormalizeNick(hello.Nick); err == nil {
				p.Nick = nick
			}
		}
		if old, ok := p.Conn.(*ws.Conn); ok && old != c {
			old.Session = nil
			old.Close(websocket.StatusPolicyViolation, "replaced by new connection")
		}
		p.IP = c.IP()
	}
	p.Conn = c
	c.Session = p

	c.Send(protocol.MustEncode(protocol.SWelcome, protocol.Welcome{
		Token: p.Token, PlayerID: p.ID, Nick: p.Nick, Build: h.cfg.BuildVersion, SimVersion: h.prog.Version(),
		Proto: protocol.Version, Draining: h.draining, Resume: string(p.Place),
	}))
	if h.draining {
		c.Send(h.drainMessage())
	}
	// Восстановление места.
	switch p.Place {
	case session.InQueue:
		h.sendQueueStatus(p)
	case session.InRoom:
		if r := h.rooms[p.RoomCode]; r != nil {
			h.broadcastRoom(r)
		} else {
			p.ToMenu()
		}
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			c.Send(m.StartMessage(p.ID))
			m.Attach(p.ID, c)
		} else {
			p.ToMenu()
			if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
				p.Place, p.RoomCode = session.InRoom, r.Code
				h.broadcastRoom(r)
			}
		}
	}
}

// ---- Quick Match ----

func (h *Hub) handleQueueJoin(p *session.Player, data json.RawMessage) {
	var req protocol.QueueJoin
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad queue.join")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	if h.draining {
		h.sendErrP(p, protocol.ErrDraining, "server is restarting soon")
		return
	}
	if req.Mode < 1 || req.Mode > 4 {
		h.sendErrP(p, protocol.ErrBadMode, "mode must be 1..4")
		return
	}
	if !h.prog.HasRole(req.Role) {
		h.sendErrP(p, protocol.ErrBadRole, "unknown role")
		return
	}
	q := h.queues.Join(req.Mode, p.ID, req.Role, h.now())
	p.Place, p.QueueMode = session.InQueue, req.Mode
	if q.Full() {
		h.startQuickMatch(q)
		return
	}
	h.broadcastQueue(q)
}

func (h *Hub) leaveQueue(p *session.Player) {
	if p.Place != session.InQueue {
		return
	}
	h.queues.Leave(p.QueueMode, p.ID)
	q := h.queues.Get(p.QueueMode)
	p.ToMenu()
	h.broadcastQueue(q)
}

func (h *Hub) sendQueueStatus(p *session.Player) {
	if p.Place != session.InQueue {
		p.Send(protocol.MustEncode(protocol.SQueueStatus, protocol.QueueStatus{InQueue: false}))
		return
	}
	q := h.queues.Get(p.QueueMode)
	p.Send(protocol.MustEncode(protocol.SQueueStatus, protocol.QueueStatus{
		InQueue: true, Mode: q.Mode, Players: len(q.Entries), Needed: q.Capacity(),
		WaitLeft: int(q.WaitLeft(h.now(), h.cfg.QueueWait) / time.Millisecond),
	}))
}

func (h *Hub) broadcastQueue(q *matchmaking.Queue) {
	for _, e := range q.Entries {
		if p := h.byID[e.PlayerID]; p != nil {
			h.sendQueueStatus(p)
		}
	}
}

func (h *Hub) startQuickMatch(q *matchmaking.Queue) {
	entries := q.Take(h.now())
	h.rng.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	var players []protocol.MatchPlayer
	for i, e := range entries {
		p := h.byID[e.PlayerID]
		if p == nil {
			continue
		}
		team := "A"
		if i%2 == 1 {
			team = "B"
		}
		players = append(players, protocol.MatchPlayer{ID: p.ID, Nick: p.Nick, Team: team, Role: e.Role})
	}
	arena := h.rng.IntN(h.prog.ArenaCount())
	h.launchMatch("", q.Mode, arena, players)
}

// ---- Комнаты ----

func (h *Hub) handleRoomCreate(p *session.Player, data json.RawMessage) {
	var req protocol.RoomCreate
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.create")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	if req.Mode < 1 || req.Mode > 4 {
		h.sendErrP(p, protocol.ErrBadMode, "mode must be 1..4")
		return
	}
	if req.Arena < 0 || req.Arena >= h.prog.ArenaCount() {
		h.sendErrP(p, protocol.ErrBadArena, "unknown arena")
		return
	}
	live := 0
	for _, r := range h.rooms {
		if r.HostIP == p.IP && !r.IsEmpty() {
			live++
		}
	}
	if live >= h.cfg.RoomsPerIP {
		h.sendErrP(p, protocol.ErrRoomLimit, "too many rooms from your address")
		return
	}
	code := room.GenerateCode()
	for h.rooms[code] != nil {
		code = room.GenerateCode()
	}
	r := room.New(code, p.ID, p.IP, req.Mode, req.Arena, h.now())
	h.rooms[code] = r
	p.Place, p.RoomCode = session.InRoom, code
	h.log.Info().Str("room", code).Str("host", p.ID).Int("mode", req.Mode).Msg("room created")
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomJoin(p *session.Player, data json.RawMessage) {
	var req protocol.RoomJoin
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.join")
		return
	}
	if p.Place != session.InMenu {
		h.sendErrP(p, protocol.ErrBusy, "leave current room/match first")
		return
	}
	code, err := protocol.NormalizeRoomCode(req.Code)
	if err != nil {
		h.sendErrP(p, protocol.ErrRoomNotFound, "bad code")
		return
	}
	r := h.rooms[code]
	if r == nil {
		h.sendErrP(p, protocol.ErrRoomNotFound, "room not found")
		return
	}
	if r.InMatch {
		h.sendErrP(p, protocol.ErrBusy, "match in progress, try later")
		return
	}
	if err := r.Join(p.ID); err != nil {
		h.sendErrP(p, protocol.ErrRoomFull, "room is full")
		return
	}
	p.Place, p.RoomCode = session.InRoom, code
	h.broadcastRoom(r)
}

func (h *Hub) roomOf(p *session.Player) *room.Room {
	if p.Place != session.InRoom {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a room")
		return nil
	}
	r := h.rooms[p.RoomCode]
	if r == nil {
		p.ToMenu()
		h.sendErrP(p, protocol.ErrRoomNotFound, "room is gone")
		return nil
	}
	return r
}

func (h *Hub) handleRoomSlot(p *session.Player, data json.RawMessage) {
	var req protocol.RoomSlot
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.slot")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if err := r.SetSlot(p.ID, req.Team, req.Index); err != nil {
		h.sendErrP(p, protocol.ErrBadSlot, err.Error())
		return
	}
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomRole(p *session.Player, data json.RawMessage) {
	var req protocol.RoomRole
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.role")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if !h.prog.HasRole(req.Role) {
		h.sendErrP(p, protocol.ErrBadRole, "unknown role")
		return
	}
	_ = r.SetRole(p.ID, req.Role)
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomConfig(p *session.Player, data json.RawMessage) {
	var req protocol.RoomConfig
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.config")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if err := r.SetConfig(p.ID, req.Mode, req.Arena, h.prog.ArenaCount()); err != nil {
		h.sendErrP(p, protocol.ErrNotAllowed, err.Error())
		return
	}
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomKick(p *session.Player, data json.RawMessage) {
	var req protocol.RoomKick
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad room.kick")
		return
	}
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if err := r.Kick(p.ID, req.PlayerID, h.now()); err != nil {
		h.sendErrP(p, protocol.ErrNotAllowed, err.Error())
		return
	}
	if target := h.byID[req.PlayerID]; target != nil && target.RoomCode == r.Code {
		target.ToMenu()
		target.Send(protocol.MustEncode(protocol.SRoomLeft, protocol.Error{Code: "kicked", Message: "вас выгнали из комнаты"}))
	}
	h.broadcastRoom(r)
}

// leaveRoom выводит игрока из комнаты; notify — послать ему room.left.
func (h *Hub) leaveRoom(p *session.Player, notify bool) {
	r := h.rooms[p.RoomCode]
	if r == nil {
		p.ToMenu()
		return
	}
	empty := r.Leave(p.ID, h.now())
	p.ToMenu()
	if notify {
		p.Send(protocol.MustEncode(protocol.SRoomLeft, nil))
	}
	if empty {
		if r.InMatch {
			return // удалим, когда матч закончится
		}
		delete(h.rooms, r.Code)
		return
	}
	h.broadcastRoom(r)
}

func (h *Hub) handleRoomStart(p *session.Player) {
	r := h.roomOf(p)
	if r == nil {
		return
	}
	if r.HostID != p.ID {
		h.sendErrP(p, protocol.ErrNotAllowed, "only host can start")
		return
	}
	if r.InMatch {
		h.sendErrP(p, protocol.ErrBusy, "match already running")
		return
	}
	if h.draining {
		h.sendErrP(p, protocol.ErrDraining, "server is restarting soon")
		return
	}
	r.PlaceAll()
	var players []protocol.MatchPlayer
	for _, m := range r.Members {
		mp := h.byID[m.ID]
		if mp == nil {
			continue
		}
		players = append(players, protocol.MatchPlayer{ID: m.ID, Nick: mp.Nick, Team: m.Team, Role: m.Role})
	}
	m := h.launchMatch(r.Code, r.Mode, r.Arena, players)
	if m == nil {
		return
	}
	r.InMatch, r.MatchID = true, m.ID
	h.broadcastRoom(r)
}

func (h *Hub) roomState(r *room.Room) protocol.RoomState {
	st := protocol.RoomState{Code: r.Code, HostID: r.HostID, Mode: r.Mode, Arena: r.Arena, InMatch: r.InMatch, LastWinner: r.LastWinner}
	for _, m := range r.Members {
		p := h.byID[m.ID]
		rp := protocol.RoomPlayer{ID: m.ID, Team: m.Team, Index: m.Index, Role: m.Role, Host: m.ID == r.HostID}
		if p != nil {
			rp.Nick, rp.Connected = p.Nick, p.Connected()
		}
		st.Players = append(st.Players, rp)
	}
	return st
}

func (h *Hub) broadcastRoom(r *room.Room) {
	if r == nil {
		return
	}
	msg := protocol.MustEncode(protocol.SRoomState, h.roomState(r))
	for _, m := range r.Members {
		if p := h.byID[m.ID]; p != nil && p.Place == session.InRoom {
			p.Send(msg)
		}
	}
}

// ---- Матчи ----

// launchMatch дополняет состав ботами, создаёт матч и переводит игроков в него.
func (h *Hub) launchMatch(roomCode string, mode, arena int, humans []protocol.MatchPlayer) *match.Match {
	players := h.fillTeams(mode, humans)
	m, err := match.New(h.prog, roomCode, mode, arena, players, match.Options{
		TickRate: h.cfg.TickRate, AFKTimeout: h.cfg.AFKTimeout, Log: h.log, Now: h.now,
	}, h.onMatchEnd)
	if err != nil {
		h.log.Error().Err(err).Msg("create match")
		for _, hp := range humans {
			if p := h.byID[hp.ID]; p != nil {
				h.sendErrP(p, protocol.ErrInternal, "failed to create match")
				p.ToMenu()
			}
		}
		return nil
	}
	h.matches[m.ID] = m
	for _, hp := range humans {
		p := h.byID[hp.ID]
		if p == nil {
			continue
		}
		p.Place, p.MatchID, p.RoomCode, p.QueueMode = session.InMatch, m.ID, roomCode, 0
		if p.Connected() {
			p.Send(m.StartMessage(p.ID))
			m.Attach(p.ID, p.Conn)
		}
	}
	m.Start()
	h.log.Info().Str("match", m.ID).Str("room", roomCode).Int("mode", mode).Int("humans", len(humans)).Msg("match started")
	return m
}

// fillTeams назначает роли не выбравшим, дополняет команды ботами и убирает дубли ников.
func (h *Hub) fillTeams(mode int, humans []protocol.MatchPlayer) []protocol.MatchPlayer {
	roles := h.prog.Roles()
	players := make([]protocol.MatchPlayer, 0, 2*mode)
	count := map[string]int{"A": 0, "B": 0}
	nicks := map[string]int{}
	for _, hp := range humans {
		if hp.Role == "" || !h.prog.HasRole(hp.Role) {
			hp.Role = roles[h.rng.IntN(len(roles))]
		}
		if n := nicks[hp.Nick]; n > 0 {
			hp.Nick = fmt.Sprintf("%s (%d)", hp.Nick, n+1)
		}
		nicks[hp.Nick]++
		count[hp.Team]++
		players = append(players, hp)
	}
	botN := 0
	for _, team := range []string{"A", "B"} {
		for count[team] < mode {
			botN++
			players = append(players, protocol.MatchPlayer{
				ID: fmt.Sprintf("bot%d", botN), Nick: fmt.Sprintf("Бот %d", botN), Team: team,
				Role: roles[h.rng.IntN(len(roles))], Bot: true,
			})
			count[team]++
		}
	}
	return players
}

func (h *Hub) onMatchEnd(m *match.Match, res match.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.matches, m.ID)
	r := h.rooms[m.RoomCode]
	for _, mp := range m.Players {
		if mp.Bot {
			continue
		}
		p := h.byID[mp.ID]
		if p == nil || p.MatchID != m.ID {
			continue
		}
		p.ToMenu()
		if r != nil && r.Member(p.ID) != nil {
			p.Place, p.RoomCode = session.InRoom, r.Code
		}
	}
	if r != nil {
		r.InMatch, r.MatchID = false, ""
		if res.Winner != "" {
			r.LastWinner = res.Winner
		} else {
			r.LastWinner = "draw"
		}
		if r.IsEmpty() {
			delete(h.rooms, r.Code)
		} else {
			h.broadcastRoom(r)
		}
	}
}

func (h *Hub) leaveMatch(p *session.Player) {
	if p.Place != session.InMatch {
		h.sendErrP(p, protocol.ErrNotAllowed, "not in a match")
		return
	}
	if m := h.matches[p.MatchID]; m != nil {
		m.Leave(p.ID)
	}
	if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
		empty := r.Leave(p.ID, h.now())
		if !empty {
			h.broadcastRoom(r)
		}
	}
	p.ToMenu()
	p.Send(protocol.MustEncode(protocol.SRoomLeft, nil))
}

func (h *Hub) handleInput(p *session.Player, data json.RawMessage) {
	if p.Place != session.InMatch {
		return
	}
	var in protocol.Input
	if err := json.Unmarshal(data, &in); err != nil || in.Kind == "" {
		return
	}
	if m := h.matches[p.MatchID]; m != nil {
		m.Input(p.ID, data)
	}
}

// ---- Фоновые таймауты ----

func (h *Hub) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()

	for _, q := range h.queues.All() {
		if h.draining {
			continue
		}
		if q.Ready(now, h.cfg.QueueWait) {
			h.startQuickMatch(q)
		} else if len(q.Entries) > 0 {
			h.broadcastQueue(q)
		}
	}

	for code, r := range h.rooms {
		if r.IsEmpty() && !r.InMatch && now.Sub(r.EmptySince) > h.cfg.RoomTTL {
			delete(h.rooms, code)
		}
	}

	for _, p := range h.byID {
		if p.Connected() {
			continue
		}
		if now.Sub(p.DisconnectedAt) < h.cfg.ReconnectTTL {
			continue
		}
		h.expirePlayer(p)
	}
}

func (h *Hub) expirePlayer(p *session.Player) {
	switch p.Place {
	case session.InQueue:
		h.leaveQueue(p)
	case session.InRoom:
		h.leaveRoom(p, false)
	case session.InMatch:
		if m := h.matches[p.MatchID]; m != nil {
			m.Leave(p.ID)
		}
		if r := h.rooms[p.RoomCode]; r != nil {
			if !r.Leave(p.ID, h.now()) {
				h.broadcastRoom(r)
			}
		}
	}
	delete(h.byID, p.ID)
	delete(h.byToken, p.Token)
	h.log.Debug().Str("player", p.ID).Msg("session expired")
}

// ---- Дренаж и админка ----

// SetDrain включает/выключает режим дренажа: новые матчи не стартуют, всем показан баннер.
func (h *Hub) SetDrain(active bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = active
	if active {
		h.drainAt = h.now()
	}
	msg := h.drainMessage()
	for _, p := range h.byID {
		p.Send(msg)
	}
	// Игроков в очередях возвращаем в меню: матчи всё равно не стартуют.
	if active {
		for _, q := range h.queues.All() {
			for _, e := range append([]matchmaking.Entry(nil), q.Entries...) {
				if p := h.byID[e.PlayerID]; p != nil {
					h.leaveQueue(p)
				}
			}
		}
	}
}

func (h *Hub) drainMessage() []byte {
	d := protocol.Drain{Active: h.draining}
	if h.draining {
		left := h.cfg.DrainTimeout - h.now().Sub(h.drainAt)
		if left < 0 {
			left = 0
		}
		d.InSeconds = int(left / time.Second)
	}
	return protocol.MustEncode(protocol.SDrain, d)
}

// Stats — сводка для админки.
type Stats struct {
	Build       string       `json:"build"`
	SimVersion  string       `json:"sim"`
	Proto       int          `json:"proto"`
	Now         time.Time    `json:"now"`
	Draining    bool         `json:"draining"`
	DrainSince  *time.Time   `json:"drainSince,omitempty"`
	Players     int          `json:"players"`
	Online      int          `json:"online"`
	Rooms       []RoomStat   `json:"rooms"`
	Queues      []QueueStat  `json:"queues"`
	Matches     []match.Info `json:"matches"`
	MatchesLive int          `json:"matchesLive"`
}

// RoomStat — комната в сводке.
type RoomStat struct {
	Code    string `json:"code"`
	Mode    int    `json:"mode"`
	Arena   int    `json:"arena"`
	Members int    `json:"members"`
	InMatch bool   `json:"inMatch"`
}

// QueueStat — очередь в сводке.
type QueueStat struct {
	Mode     int `json:"mode"`
	Players  int `json:"players"`
	WaitLeft int `json:"waitLeftMs"`
}

// Online возвращает число подключённых игроков (для публичного /api/online).
func (h *Hub) Online() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.byID {
		if p.Connected() {
			n++
		}
	}
	return n
}

// Stats возвращает сводку.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	st := Stats{Build: h.cfg.BuildVersion, SimVersion: h.prog.Version(), Proto: protocol.Version, Now: now, Draining: h.draining}
	if h.draining {
		t := h.drainAt
		st.DrainSince = &t
	}
	st.Players = len(h.byID)
	for _, p := range h.byID {
		if p.Connected() {
			st.Online++
		}
	}
	for _, r := range h.rooms {
		st.Rooms = append(st.Rooms, RoomStat{Code: r.Code, Mode: r.Mode, Arena: r.Arena, Members: len(r.Members), InMatch: r.InMatch})
	}
	for _, q := range h.queues.All() {
		if len(q.Entries) > 0 {
			st.Queues = append(st.Queues, QueueStat{Mode: q.Mode, Players: len(q.Entries), WaitLeft: int(q.WaitLeft(now, h.cfg.QueueWait) / time.Millisecond)})
		}
	}
	for _, m := range h.matches {
		st.Matches = append(st.Matches, m.Info())
	}
	st.MatchesLive = len(h.matches)
	return st
}

// ---- утилиты ----

func (h *Hub) sendErr(c *ws.Conn, code, msg string) {
	c.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}

func (h *Hub) sendErrP(p *session.Player, code, msg string) {
	p.Send(protocol.MustEncode(protocol.SError, protocol.Error{Code: code, Message: msg}))
}
