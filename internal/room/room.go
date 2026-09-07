// Package room — комната с кодом из четырёх цифр и её лобби. Чистая структура данных без
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
	Ready bool   // нажал «Готов»
}

// Room — комната.
type Room struct {
	Code       string
	HostID     string
	HostIP     string
	Mode       int
	Arena      int
	GameMode   string // "" | "pvp" | "survival" | "defense"
	Campaign   bool   // PvE: кампания (иначе эндлесс)
	Difficulty int    // PvE: 0..2
	Visibility string // "open" — видна в списке и открыта; "closed" — нужен код
	Members    []*Member
	InMatch    bool
	MatchID    string
	LastWinner string
	CreatedAt  time.Time
	EmptySince time.Time // с какого момента комната пуста (нулевое — не пуста)
}

// IsPvE — комната играет кооперативный PvE (не PvP).
func (r *Room) IsPvE() bool { return r.GameMode != "" && r.GameMode != "pvp" }

// Section — раздел меню, к которому относится комната: "pvp" или "pve".
func (r *Room) Section() string {
	if r.IsPvE() {
		return "pve"
	}
	return "pvp"
}

// IsClosed — в комнату можно войти только по коду.
func (r *Room) IsClosed() bool { return r.Visibility == VisibilityClosed }

// Видимость комнаты.
const (
	VisibilityOpen   = "open"
	VisibilityClosed = "closed"
)

// NormalizeVisibility приводит видимость к известному значению; всё неизвестное — открытая комната.
func NormalizeVisibility(v string) string {
	if v == VisibilityClosed {
		return VisibilityClosed
	}
	return VisibilityOpen
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
func New(code, hostID, hostIP string, cfg Config, now time.Time) *Room {
	r := &Room{Code: code, HostID: hostID, HostIP: hostIP, Mode: cfg.Mode, Arena: cfg.Arena,
		GameMode: cfg.GameMode, Campaign: cfg.Campaign, Difficulty: cfg.Difficulty,
		Visibility: NormalizeVisibility(cfg.Visibility), CreatedAt: now}
	r.Members = append(r.Members, &Member{ID: hostID, Team: "A", Index: 0})
	return r
}

// Capacity — число мест в комнате: PvE — пати из Mode игроков (только команда A),
// PvP — две команды по Mode.
func (r *Room) Capacity() int {
	if r.IsPvE() {
		return r.Mode
	}
	return 2 * r.Mode
}

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

// SetSlot ставит игрока в слот команды. Работает и во время матча: слот комнаты — это боец
// матча, и сидящий в лобби участник может перейти на свободное место. Того, кто прямо сейчас
// играет, не пускает hub (у него другое место в сессии).
func (r *Room) SetSlot(id, team string, index int) error {
	m := r.Member(id)
	if m == nil {
		return ErrNotMember
	}
	if team != "A" && team != "B" {
		return ErrBadSlot
	}
	if r.IsPvE() && team != "A" {
		return ErrBadSlot
	}
	if index < 0 || index >= r.Mode {
		return ErrBadSlot
	}
	if o := r.slotOwner(team, index); o != nil && o != m {
		return ErrSlotTaken
	}
	if m.Team != team || m.Index != index {
		m.Ready = false
	}
	m.Team, m.Index = team, index
	return nil
}

// SetRole выбирает бойца. Смена бойца снимает готовность только у этого игрока.
func (r *Room) SetRole(id, role string) error {
	m := r.Member(id)
	if m == nil {
		return ErrNotMember
	}
	if m.Role != role {
		m.Ready = false
	}
	m.Role = role
	return nil
}

// SetReady отмечает готовность игрока.
func (r *Room) SetReady(id string, ready bool) error {
	m := r.Member(id)
	if m == nil {
		return ErrNotMember
	}
	m.Ready = ready
	return nil
}

// ResetReady снимает готовность со всех: настройки комнаты изменились.
func (r *Room) ResetReady() {
	for _, m := range r.Members {
		m.Ready = false
	}
}

// ReadyCount — сколько игроков с живым соединением готовы и сколько их всего.
// Отключённые (в окне реконнекта) не считаются: иначе обрыв связи одного блокирует старт.
func (r *Room) ReadyCount(connected func(id string) bool) (ready, total int) {
	for _, m := range r.Members {
		if connected != nil && !connected(m.ID) {
			continue
		}
		total++
		if m.Ready {
			ready++
		}
	}
	return ready, total
}

// AllReady — в комнате есть хотя бы один игрок на связи и все такие игроки готовы.
func (r *Room) AllReady(connected func(id string) bool) bool {
	ready, total := r.ReadyCount(connected)
	return total > 0 && ready == total
}

// SetConfig меняет режим игры, размер, арену и настройки PvE (только хост, не в матче).
func (r *Room) SetConfig(hostID string, cfg Config, arenaCount int) error {
	if hostID != r.HostID {
		return ErrNotHost
	}
	if r.InMatch {
		return ErrInMatch
	}
	if cfg.Mode < 1 || cfg.Mode > 4 {
		return ErrBadMode
	}
	if cfg.Arena < 0 || cfg.Arena >= arenaCount {
		return ErrBadArena
	}
	pve := cfg.GameMode != "" && cfg.GameMode != "pvp"
	maxN := 2 * cfg.Mode
	if pve {
		maxN = cfg.Mode
	}
	if len(r.Members) > maxN {
		return ErrTooMany
	}
	vis := NormalizeVisibility(cfg.Visibility)
	changed := r.Mode != cfg.Mode || r.Arena != cfg.Arena || r.GameMode != cfg.GameMode ||
		r.Campaign != cfg.Campaign || r.Difficulty != cfg.Difficulty || r.Visibility != vis
	r.Mode, r.Arena = cfg.Mode, cfg.Arena
	r.GameMode, r.Campaign, r.Difficulty = cfg.GameMode, cfg.Campaign, cfg.Difficulty
	r.Visibility = vis
	// Игрок соглашался играть в другие правила — готовность снимается со всех.
	if changed {
		r.ResetReady()
	}
	// Слоты за пределами нового режима (или команда B в PvE) освобождаем и расставляем заново.
	for _, m := range r.Members {
		if m.Index >= cfg.Mode || (pve && m.Team == "B") {
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

// Config — параметры комнаты, задаваемые хостом.
type Config struct {
	Mode       int
	Arena      int
	GameMode   string
	Campaign   bool
	Difficulty int
	Visibility string
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

// autoPlace ставит в первый свободный слот команды, где больше ботов (то есть меньше людей).
// Во время матча «больше ботов» и означает «больше свободных мест». В PvE вся команда — A.
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
	if r.IsPvE() {
		order = []string{"A"}
	} else if countB < countA {
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

const codeAlphabet = "0123456789"

// GenerateCode возвращает код из четырёх цифр (0000–9999); коллизии перегенерирует hub.
func GenerateCode() string {
	b := make([]byte, 4)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return string(b)
}
