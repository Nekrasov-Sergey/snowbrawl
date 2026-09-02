// Package session — игрок (анонимная сессия по токену) и его текущее место в игре.
// Структуры не потокобезопасны: ими владеет hub и правит под своим мьютексом.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Place — где сейчас находится игрок.
type Place string

// Возможные места игрока.
const (
	InMenu  Place = "menu"
	InQueue Place = "queue"
	InRoom  Place = "room"
	InMatch Place = "match"
)

// Sender — то, куда можно отправить сообщение игроку (WebSocket-соединение).
type Sender interface {
	Send(msg []byte)
	Closed() bool
}

// Player — сессия игрока.
type Player struct {
	ID    string
	Token string
	Nick  string
	IP    string

	Conn           Sender    // nil, если игрок отключён
	DisconnectedAt time.Time // когда пропало соединение (если Conn == nil)
	CreatedAt      time.Time

	Place     Place
	RoomCode  string // если Place == InRoom или матч из комнаты
	QueueMode int    // если Place == InQueue
	MatchID   string // если Place == InMatch
}

// New создаёт игрока с новым токеном и идентификатором.
func New(nick, ip string, now time.Time) *Player {
	return &Player{
		ID:        "p" + randomHex(4),
		Token:     randomHex(16),
		Nick:      nick,
		IP:        ip,
		CreatedAt: now,
		Place:     InMenu,
	}
}

// Connected сообщает, есть ли у игрока живое соединение.
func (p *Player) Connected() bool { return p.Conn != nil && !p.Conn.Closed() }

// Send отправляет сообщение, если игрок подключён.
func (p *Player) Send(msg []byte) {
	if p.Connected() {
		p.Conn.Send(msg)
	}
}

// ToMenu сбрасывает место игрока.
func (p *Player) ToMenu() {
	p.Place = InMenu
	p.RoomCode = ""
	p.QueueMode = 0
	p.MatchID = ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
