// Package match — цикл одного матча: фиксированный тик, ввод игроков → sim.js,
// снапшоты → подключённым игрокам, боты для отключившихся и бездействующих.
//
// Match потокобезопасен: все публичные методы берут m.mu. Из-под мьютекса наружу
// ничего не вызывается (onEnd вызывается после освобождения), поэтому владелец (hub)
// может звать методы Match под своим мьютексом без риска взаимной блокировки.
package match

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
)

// Причины завершения матча.
const (
	ReasonKO        = "ko"
	ReasonTimeout   = "timeout"
	ReasonAbandoned = "abandoned"
	ReasonShutdown  = "shutdown"
	// PvE (приходят из sim.reason()).
	ReasonCleared   = "cleared"
	ReasonWiped     = "wiped"
	ReasonObjective = "objective"
	ReasonExpired   = "expired"
)

// Result — итог матча.
type Result struct {
	Winner string // "A" | "B" | ""
	Reason string
}

// Options — параметры цикла.
type Options struct {
	TickRate   int
	AFKTimeout time.Duration
	Countdown  time.Duration // отсчёт перед стартом: симуляция стоит, снапшоты идут
	Log        zerolog.Logger
	Now        func() time.Time
	// PvE: пусто/"pvp" — обычный матч; иначе кооперативные волны.
	GameMode   string
	Campaign   bool
	Difficulty int
	Pve        *sim.PveConfig
}

type human struct {
	id        string // id сессии игрока
	simID     string // id бойца в симуляции; отличается от id, если игрок подсел на место бота
	team      string
	conn      session.Sender
	lastInput time.Time
	left      bool // ушёл навсегда: до конца матча играет бот
	bot       bool // сейчас управляется ботом (дисконнект/AFK/left)
}

// Match — живой матч.
type Match struct {
	ID       string
	RoomCode string
	Mode     int
	Arena    int
	GameMode string
	Players  []protocol.MatchPlayer
	Created  time.Time
	StartsAt time.Time // до этого момента идёт отсчёт, симуляция стоит

	opts  Options
	onEnd func(*Match, Result)

	mu      sync.Mutex
	sim     *sim.Match
	humans  map[string]*human
	tick    int
	done    bool
	result  Result
	stopCh  chan struct{}
	stopped sync.Once
}

// New создаёт матч (без запуска цикла).
func New(prog *sim.Program, roomCode string, mode, arena int, players []protocol.MatchPlayer, opts Options, onEnd func(*Match, Result)) (*Match, error) {
	if opts.TickRate <= 0 {
		opts.TickRate = 20
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := sim.MatchConfig{Mode: mode, ArenaIndex: arena, GameMode: opts.GameMode, Difficulty: opts.Difficulty, Pve: opts.Pve}
	if opts.GameMode != "" && opts.GameMode != "pvp" {
		campaign := opts.Campaign
		cfg.Campaign = &campaign
	}
	for _, p := range players {
		pc := sim.PlayerConfig{ID: p.ID, Team: p.Team, Role: p.Role, Bot: p.Bot, Nick: p.Nick, BotLevel: p.BotLevel}
		cfg.Players = append(cfg.Players, pc)
	}
	var seedBytes [4]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		return nil, errors.Wrap(err, "seed")
	}
	s, err := prog.NewMatch(cfg, binary.LittleEndian.Uint32(seedBytes[:]))
	if err != nil {
		return nil, errors.Wrap(err, "create sim match")
	}
	now := opts.Now()
	m := &Match{
		ID: "m" + randomHex(4), RoomCode: roomCode, Mode: mode, Arena: arena, GameMode: opts.GameMode,
		Players: players, Created: now,
		StartsAt: now.Add(opts.Countdown),
		opts:     opts, onEnd: onEnd, sim: s, humans: map[string]*human{}, stopCh: make(chan struct{}),
	}
	for _, p := range players {
		if !p.Bot {
			// bot=true до Attach
			m.humans[p.ID] = &human{id: p.ID, simID: p.ID, team: p.Team, lastInput: now, bot: true}
		}
	}
	return m, nil
}

// Start запускает цикл матча в отдельной горутине.
func (m *Match) Start() { go m.loop() }

// StartMessage — сообщение match.start для конкретного игрока. YourID — это id бойца в
// симуляции: у подсевшего на место бота он не равен id сессии.
func (m *Match) StartMessage(playerID string) []byte {
	m.mu.Lock()
	yourID := playerID
	if h, ok := m.humans[playerID]; ok && h.simID != "" {
		yourID = h.simID
	}
	players := append([]protocol.MatchPlayer(nil), m.Players...)
	m.mu.Unlock()
	return protocol.MustEncode(protocol.SMatchStart, protocol.MatchStart{
		MatchID: m.ID, Mode: m.Mode, Arena: m.Arena, GameMode: m.GameMode, Players: players, YourID: yourID,
		TickRate: m.opts.TickRate, RoomCode: m.RoomCode,
	})
}

// LobbyStatus — состояние идущего матча для лобби комнаты: сколько осталось времени и что
// с бойцами по слотам. HP берётся из снапшота, поэтому вызов не бесплатный — звать раз в тик.
func (m *Match) LobbyStatus() protocol.RoomMatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := protocol.RoomMatch{Code: m.RoomCode}
	if m.done {
		return st
	}
	hp, koed, timeLeft := m.slotState()
	st.TimeLeftMs = timeLeft / 1000 * 1000 // до секунды: сводка уходит в лобби только при изменении
	for _, p := range m.Players {
		bot := p.Bot
		if h := m.humanBySim(p.ID); h == nil || h.conn == nil || h.left {
			bot = true // слот свободен или его человек ушёл в лобби
		}
		st.Slots = append(st.Slots, protocol.RoomMatchSlot{
			Team: p.Team, Index: p.Index, Nick: p.Nick, Role: p.Role,
			Bot: bot, HP: hp[p.ID], Koed: koed[p.ID],
		})
	}
	return st
}

// FreeSlot сообщает, ведёт ли бойца этого слота бот, то есть можно ли на него сесть.
func (m *Match) FreeSlot(team string, index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.playerAt(team, index)
	if p == nil || m.done {
		return false
	}
	h := m.humanBySim(p.ID)
	return h == nil || h.conn == nil || h.left
}

// Replace сажает живого игрока за бойца слота (team, index).
func (m *Match) Replace(team string, index int, playerID, nick, rank string, conn session.Sender) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return ErrMatchOver
	}
	// Запись игрока, который вышел в лобби или потерял связь, входу не мешает — он возвращается.
	if h, ok := m.humans[playerID]; ok {
		if h.conn != nil && !h.left {
			return ErrAlreadyIn
		}
		delete(m.humans, playerID)
	}
	idx := -1
	for i, p := range m.Players {
		if p.Team == team && p.Index == index {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNoSuchSlot
	}
	simID := m.Players[idx].ID
	if h := m.humanBySim(simID); h != nil {
		if h.conn != nil && !h.left {
			return ErrSlotTaken
		}
		delete(m.humans, h.id) // слот освободил тот, кто вышел в лобби или потерял связь
	}
	now := m.opts.Now()
	h := &human{id: playerID, simID: simID, team: team, conn: conn, lastInput: now, bot: true}
	m.humans[playerID] = h
	m.setBot(h, false)
	m.Players[idx].Nick = nick
	m.Players[idx].Rank = rank // роль модерации: цвет ника в бою
	m.Players[idx].Bot = false
	m.broadcast(m.rosterMessage())
	return nil
}

// Ошибки входа в идущий матч.
var (
	ErrMatchOver  = errors.New("match is over")
	ErrAlreadyIn  = errors.New("already in match")
	ErrNoSuchSlot = errors.New("no such slot")
	ErrSlotTaken  = errors.New("slot taken")
)

// rosterMessage — сообщение match.roster. Вызывать под m.mu.
func (m *Match) rosterMessage() []byte {
	players := append([]protocol.MatchPlayer(nil), m.Players...)
	return protocol.MustEncode(protocol.SMatchRoster, protocol.MatchRoster{Players: players})
}

// humanBySim ищет человека по id бойца в симуляции. Вызывать под m.mu.
func (m *Match) humanBySim(simID string) *human {
	for _, h := range m.humans {
		if h.simID == simID {
			return h
		}
	}
	return nil
}

// playerAt — боец слота (team, index). Вызывать под m.mu.
func (m *Match) playerAt(team string, index int) *protocol.MatchPlayer {
	for i, p := range m.Players {
		if p.Team == team && p.Index == index {
			return &m.Players[i]
		}
	}
	return nil
}

// slotState достаёт из снапшота HP бойцов, признак KO и остаток времени матча. Вызывать под m.mu.
func (m *Match) slotState() (map[string]int, map[string]bool, int) {
	hp, koed := map[string]int{}, map[string]bool{}
	state, err := m.sim.Snapshot()
	if err != nil {
		return hp, koed, 0
	}
	var snap struct {
		TimeLeft int `json:"timeLeft"`
		Players  []struct {
			ID   string `json:"id"`
			HP   int    `json:"hp"`
			Koed bool   `json:"koed"`
		} `json:"players"`
	}
	if err := json.Unmarshal(state, &snap); err != nil {
		return hp, koed, 0
	}
	for _, p := range snap.Players {
		hp[p.ID] = p.HP
		koed[p.ID] = p.Koed
	}
	return hp, koed, snap.TimeLeft
}

// releaseSlot помечает слот как ведомый ботом и рассылает новый состав. Вызывать под m.mu.
func (m *Match) releaseSlot(simID string) {
	for i, p := range m.Players {
		if p.ID == simID && !p.Bot {
			m.Players[i].Bot = true
			m.broadcast(m.rosterMessage())
			return
		}
	}
}

// Attach подключает (или переподключает) игрока: снапшоты пойдут в conn, бот отдаёт управление.
func (m *Match) Attach(playerID string, conn session.Sender) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.humans[playerID]
	if !ok || h.left || m.done {
		return
	}
	h.conn = conn
	h.lastInput = m.opts.Now()
	m.setBot(h, false)
}

// Detach — игрок отключился: место сохраняется, бойца ведёт бот.
func (m *Match) Detach(playerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.humans[playerID]; ok {
		h.conn = nil
		m.setBot(h, true)
		m.releaseSlot(h.simID)
	}
}

// Leave — игрок ушёл навсегда.
func (m *Match) Leave(playerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.humans[playerID]; ok {
		h.conn = nil
		h.left = true
		m.setBot(h, true)
		m.releaseSlot(h.simID)
	}
}

// Input передаёт ввод игрока в симуляцию.
func (m *Match) Input(playerID string, raw json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.humans[playerID]
	if !ok || h.left || m.done {
		return
	}
	now := m.opts.Now()
	h.lastInput = now
	if now.Before(m.StartsAt) {
		return // идёт отсчёт: команды до старта не применяем, иначе боец рванёт с первого тика
	}
	if h.bot && h.conn != nil {
		m.setBot(h, false) // вернулся из AFK
	}
	if _, err := m.sim.ApplyInput(h.simID, raw); err != nil {
		m.opts.Log.Warn().Err(err).Str("match", m.ID).Str("player", playerID).Msg("input rejected by sim")
	}
}

// Stop принудительно завершает матч (перезапуск сервера).
func (m *Match) Stop(reason string) {
	m.stopped.Do(func() {
		m.mu.Lock()
		if !m.done {
			m.done = true
			m.result = Result{Reason: reason}
		}
		m.mu.Unlock()
		close(m.stopCh)
	})
}

// Info — сводка для админки.
type Info struct {
	ID       string    `json:"id"`
	RoomCode string    `json:"roomCode,omitempty"`
	Mode     int       `json:"mode"`
	Arena    int       `json:"arena"`
	Tick     int       `json:"tick"`
	Humans   int       `json:"humans"`
	Online   int       `json:"online"`
	Created  time.Time `json:"created"`
	// StartsAt — конец отсчёта: до него симуляция стоит. Админке нужно, чтобы отличить
	// «идёт отсчёт» от «идёт бой» и показать честную длительность боя.
	StartsAt time.Time    `json:"startsAt"`
	Players  []PlayerInfo `json:"players"`
}

// PlayerInfo — участник матча для админки: кто это и кто им сейчас управляет.
type PlayerInfo struct {
	ID     string `json:"id"`
	Nick   string `json:"nick"`
	Team   string `json:"team"`
	Role   string `json:"role"`
	Bot    bool   `json:"bot"`              // бот с самого начала (добор до полного состава)
	BotNow bool   `json:"botNow,omitempty"` // за человека сейчас играет бот: обрыв, AFK или уход
	Online bool   `json:"online,omitempty"`
	Left   bool   `json:"left,omitempty"` // вышел насовсем
}

// Info возвращает сводку.
func (m *Match) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := Info{ID: m.ID, RoomCode: m.RoomCode, Mode: m.Mode, Arena: m.Arena, Tick: m.tick,
		Created: m.Created, StartsAt: m.StartsAt}
	for _, p := range m.Players {
		pi := PlayerInfo{ID: p.ID, Nick: p.Nick, Team: p.Team, Role: p.Role, Bot: p.Bot}
		if h := m.humanBySim(p.ID); h != nil {
			pi.BotNow = h.bot
			pi.Left = h.left
			pi.Online = h.conn != nil && !h.conn.Closed()
		}
		info.Players = append(info.Players, pi)
	}
	for _, h := range m.humans {
		if !h.left {
			info.Humans++
		}
		if h.conn != nil && !h.conn.Closed() {
			info.Online++
		}
	}
	return info
}

// HumanIDs возвращает игроков-людей, не ушедших навсегда.
func (m *Match) HumanIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, h := range m.humans {
		if !h.left {
			ids = append(ids, h.id)
		}
	}
	return ids
}

func (m *Match) setBot(h *human, bot bool) {
	if h.bot == bot {
		return
	}
	h.bot = bot
	if err := m.sim.SetBot(h.simID, bot); err != nil {
		m.opts.Log.Warn().Err(err).Str("match", m.ID).Msg("setBot")
	}
}

func (m *Match) loop() {
	interval := time.Second / time.Duration(m.opts.TickRate)
	dt := 1.0 / float64(m.opts.TickRate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			m.finish()
			return
		case <-ticker.C:
			if m.step(dt) {
				m.finish()
				return
			}
		}
	}
}

// step выполняет один тик; возвращает true, если матч завершён.
func (m *Match) step(dt float64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return true
	}
	now := m.opts.Now()

	// AFK и «все ушли».
	active := 0
	for _, h := range m.humans {
		if h.left {
			continue
		}
		active++
		if h.conn != nil && !h.conn.Closed() && !h.bot && m.opts.AFKTimeout > 0 && now.Sub(h.lastInput) > m.opts.AFKTimeout {
			m.setBot(h, true)
		}
	}
	if active == 0 {
		m.done = true
		m.result = Result{Reason: ReasonAbandoned}
		return true
	}

	// Отсчёт перед стартом: симуляцию не двигаем, но снапшоты шлём — клиент рисует арену,
	// бойцов на стартовых местах и крупные «3, 2, 1».
	if left := m.StartsAt.Sub(now); left > 0 {
		state, err := m.sim.Snapshot()
		if err != nil {
			m.opts.Log.Error().Err(err).Str("match", m.ID).Msg("sim snapshot failed, aborting match")
			m.done = true
			m.result = Result{Reason: ReasonShutdown}
			return true
		}
		m.tick++
		m.broadcast(protocol.MustEncode(protocol.SSnapshot, protocol.Snapshot{
			Tick: m.tick, State: state, Countdown: int(left.Milliseconds()),
		}))
		return false
	}

	events, err := m.sim.Step(dt)
	if err != nil {
		m.opts.Log.Error().Err(err).Str("match", m.ID).Msg("sim step failed, aborting match")
		m.done = true
		m.result = Result{Reason: ReasonShutdown}
		return true
	}
	m.tick++
	state, err := m.sim.Snapshot()
	if err != nil {
		m.opts.Log.Error().Err(err).Str("match", m.ID).Msg("sim snapshot failed, aborting match")
		m.done = true
		m.result = Result{Reason: ReasonShutdown}
		return true
	}
	if len(events) <= 2 { // "[]"
		events = nil
	}
	m.broadcast(protocol.MustEncode(protocol.SSnapshot, protocol.Snapshot{Tick: m.tick, State: state, Events: events}))
	if m.sim.IsOver() {
		m.done = true
		winner := m.sim.Winner()
		// Причину даёт сама симуляция (PvP: ko/timeout; PvE: cleared/wiped/objective/expired).
		reason := m.sim.Reason()
		if reason == "" {
			reason = ReasonKO
			if winner == "" {
				reason = ReasonTimeout
			}
		}
		m.result = Result{Winner: winner, Reason: reason}
		return true
	}
	return false
}

// broadcast шлёт сообщение всем подключённым игрокам матча. Вызывать под m.mu.
func (m *Match) broadcast(msg []byte) {
	for _, h := range m.humans {
		if h.conn != nil && !h.left {
			h.conn.Send(msg)
		}
	}
}

// finish рассылает match.end и уведомляет владельца. Вызывается ровно один раз.
func (m *Match) finish() {
	m.mu.Lock()
	res := m.result
	type target struct {
		conn session.Sender
		team string
	}
	var targets []target
	for _, h := range m.humans {
		if h.conn != nil && !h.left {
			targets = append(targets, target{h.conn, h.team})
		}
	}
	m.mu.Unlock()

	for _, t := range targets {
		t.conn.Send(protocol.MustEncode(protocol.SMatchEnd, protocol.MatchEnd{
			Winner: res.Winner, YourTeam: t.team, Reason: res.Reason, RoomCode: m.RoomCode,
		}))
	}
	m.opts.Log.Info().Str("match", m.ID).Str("winner", res.Winner).Str("reason", res.Reason).Int("ticks", m.tick).Msg("match finished")
	if m.onEnd != nil {
		m.onEnd(m, res)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
