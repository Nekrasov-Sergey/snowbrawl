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
	Log        zerolog.Logger
	Now        func() time.Time
}

type human struct {
	id        string
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
	Players  []protocol.MatchPlayer
	Created  time.Time

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
	cfg := sim.MatchConfig{Mode: mode, ArenaIndex: arena}
	for _, p := range players {
		cfg.Players = append(cfg.Players, sim.PlayerConfig{ID: p.ID, Team: p.Team, Role: p.Role, Bot: p.Bot, Nick: p.Nick})
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
		ID: "m" + randomHex(4), RoomCode: roomCode, Mode: mode, Arena: arena, Players: players, Created: now,
		opts: opts, onEnd: onEnd, sim: s, humans: map[string]*human{}, stopCh: make(chan struct{}),
	}
	for _, p := range players {
		if !p.Bot {
			m.humans[p.ID] = &human{id: p.ID, team: p.Team, lastInput: now, bot: true} // bot=true до Attach
		}
	}
	return m, nil
}

// Start запускает цикл матча в отдельной горутине.
func (m *Match) Start() { go m.loop() }

// StartMessage — сообщение match.start для конкретного игрока.
func (m *Match) StartMessage(playerID string) []byte {
	return protocol.MustEncode(protocol.SMatchStart, protocol.MatchStart{
		MatchID: m.ID, Mode: m.Mode, Arena: m.Arena, Players: m.Players, YourID: playerID,
		TickRate: m.opts.TickRate, RoomCode: m.RoomCode,
	})
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
	h.lastInput = m.opts.Now()
	if h.bot && h.conn != nil {
		m.setBot(h, false) // вернулся из AFK
	}
	if _, err := m.sim.ApplyInput(playerID, raw); err != nil {
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
}

// Info возвращает сводку.
func (m *Match) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := Info{ID: m.ID, RoomCode: m.RoomCode, Mode: m.Mode, Arena: m.Arena, Tick: m.tick, Created: m.Created}
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
	if err := m.sim.SetBot(h.id, bot); err != nil {
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
	msg := protocol.MustEncode(protocol.SSnapshot, protocol.Snapshot{Tick: m.tick, State: state, Events: events})
	for _, h := range m.humans {
		if h.conn != nil && !h.left {
			h.conn.Send(msg)
		}
	}
	if m.sim.IsOver() {
		m.done = true
		winner := m.sim.Winner()
		reason := ReasonKO
		if winner == "" {
			reason = ReasonTimeout
		}
		m.result = Result{Winner: winner, Reason: reason}
		return true
	}
	return false
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
