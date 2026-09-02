// Package room — комната с кодом SNB-XXXX и её лобби. Чистая структура данных без
// блокировок: ею владеет hub.
package room

import (
	"crypto/rand"
	"math/big"
	"time"

	"github.com/pkg/errors"
)

// Member — участник лобби.
type Member struct {
	ID    string
	Team  string // "A" | "B" | ""
	Index int    // слот в команде
	Role  string // "" — не выбрал
}

// Room — комната.
type Room struct {
	Code       string
	HostID     string
	HostIP     string
	Mode       int
	Arena      int
	Members    []*Member
	InMatch    bool
	MatchID    string
	LastWinner string
	CreatedAt  time.Time
	EmptySince time.Time // с какого момента комната пуста (нулевое — не пуста)
}

// Ошибки операций с комнатой.
var (
	ErrFull      = errors.New("room is full")
	ErrNotMember = errors.New("not a member")
	ErrNotHost   = errors.New("not a host")
	ErrBadSlot   = errors.New("bad slot")
	ErrSlotTaken = errors.New("slot taken")
	ErrInMatch   = errors.New("room is in match")
	ErrTooMany   = errors.New("too many members for this mode")
	ErrBadMode   = errors.New("bad mode")
	ErrBadArena  = errors.New("bad arena")
)

// New создаёт комнату с хостом внутри.
func New(code, hostID, hostIP string, mode, arena int, now time.Time) *Room {
	r := &Room{Code: code, HostID: hostID, HostIP: hostIP, Mode: mode, Arena: arena, CreatedAt: now}
	r.Members = append(r.Members, &Member{ID: hostID, Team: "A", Index: 0})
	return r
}

// Capacity — число мест в комнате.
func (r *Room) Capacity() int { return 2 * r.Mode }

// Member возвращает участника по id.
func (r *Room) Member(id string) *Member {
	for _, m := range r.Members {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// Join добавляет игрока в первый свободный слот.
func (r *Room) Join(id string) error {
	if r.Member(id) != nil {
		return nil
	}
	if len(r.Members) >= r.Capacity() {
		return ErrFull
	}
	m := &Member{ID: id}
	r.Members = append(r.Members, m)
	r.autoPlace(m)
	r.EmptySince = time.Time{}
	return nil
}

// Leave убирает игрока. Если ушёл хост — хостом становится следующий. Возвращает true,
// если комната опустела.
func (r *Room) Leave(id string, now time.Time) bool {
	for i, m := range r.Members {
		if m.ID == id {
			r.Members = append(r.Members[:i], r.Members[i+1:]...)
			break
		}
	}
	if len(r.Members) == 0 {
		r.EmptySince = now
		return true
	}
	if r.HostID == id {
		r.HostID = r.Members[0].ID
	}
	return false
}

// SetSlot ставит игрока в слот команды.
func (r *Room) SetSlot(id, team string, index int) error {
	if r.InMatch {
		return ErrInMatch
	}
	m := r.Member(id)
	if m == nil {
		return ErrNotMember
	}
	if (team != "A" && team != "B") || index < 0 || index >= r.Mode {
		return ErrBadSlot
	}
	if o := r.slotOwner(team, index); o != nil && o != m {
		return ErrSlotTaken
	}
	m.Team, m.Index = team, index
	return nil
}

// SetRole выбирает бойца.
func (r *Room) SetRole(id, role string) error {
	m := r.Member(id)
	if m == nil {
		return ErrNotMember
	}
	m.Role = role
	return nil
}

// SetConfig меняет режим и арену (только хост, не в матче).
func (r *Room) SetConfig(hostID string, mode, arena, arenaCount int) error {
	if hostID != r.HostID {
		return ErrNotHost
	}
	if r.InMatch {
		return ErrInMatch
	}
	if mode < 1 || mode > 4 {
		return ErrBadMode
	}
	if arena < 0 || arena >= arenaCount {
		return ErrBadArena
	}
	if len(r.Members) > 2*mode {
		return ErrTooMany
	}
	r.Mode, r.Arena = mode, arena
	// Слоты за пределами нового режима освобождаем и расставляем заново.
	for _, m := range r.Members {
		if m.Index >= mode {
			m.Team, m.Index = "", 0
		}
	}
	for _, m := range r.Members {
		if m.Team == "" {
			r.autoPlace(m)
		}
	}
	return nil
}

// Kick выгоняет игрока (только хост, не себя).
func (r *Room) Kick(hostID, targetID string, now time.Time) error {
	if hostID != r.HostID {
		return ErrNotHost
	}
	if targetID == hostID || r.Member(targetID) == nil {
		return ErrNotMember
	}
	r.Leave(targetID, now)
	return nil
}

// PlaceAll расставляет по слотам всех, кто не выбрал команду (перед стартом).
func (r *Room) PlaceAll() {
	for _, m := range r.Members {
		if m.Team == "" {
			r.autoPlace(m)
		}
	}
}

// IsEmpty сообщает, что в комнате никого.
func (r *Room) IsEmpty() bool { return len(r.Members) == 0 }

func (r *Room) slotOwner(team string, index int) *Member {
	for _, m := range r.Members {
		if m.Team == team && m.Index == index {
			return m
		}
	}
	return nil
}

// autoPlace ставит в первый свободный слот, чередуя команды для баланса.
func (r *Room) autoPlace(m *Member) {
	countA, countB := 0, 0
	for _, o := range r.Members {
		if o == m {
			continue
		}
		switch o.Team {
		case "A":
			countA++
		case "B":
			countB++
		}
	}
	order := []string{"A", "B"}
	if countB < countA {
		order = []string{"B", "A"}
	}
	for _, team := range order {
		for i := 0; i < r.Mode; i++ {
			if r.slotOwner(team, i) == nil {
				m.Team, m.Index = team, i
				return
			}
		}
	}
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ0123456789"

// GenerateCode возвращает код вида SNB-XXXX (без похожих символов I и O).
func GenerateCode() string {
	b := make([]byte, 4)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return "SNB-" + string(b)
}
