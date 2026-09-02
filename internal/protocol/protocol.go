// Package protocol описывает сообщения WebSocket между клиентом и сервером.
// Каждое сообщение — JSON-конверт {"t": "<тип>", "d": <данные>}.
// Подробное описание — docs/PROTOCOL.md. При несовместимом изменении увеличивайте Version.
package protocol

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/pkg/errors"
)

// Version — версия протокола. Клиент присылает её в hello.
const Version = 1

// Envelope — конверт любого сообщения.
type Envelope struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

// Типы сообщений клиент → сервер.
const (
	CHello      = "hello"
	CQueueJoin  = "queue.join"
	CQueueLeave = "queue.leave"
	CRoomCreate = "room.create"
	CRoomJoin   = "room.join"
	CRoomSlot   = "room.slot"
	CRoomRole   = "room.role"
	CRoomConfig = "room.config"
	CRoomKick   = "room.kick"
	CRoomStart  = "room.start"
	CRoomLeave  = "room.leave"
	CMatchLeave = "match.leave"
	CInput      = "input"
	CPing       = "ping"
)

// Типы сообщений сервер → клиент.
const (
	SWelcome     = "welcome"
	SError       = "error"
	SQueueStatus = "queue.status"
	SRoomState   = "room.state"
	SRoomLeft    = "room.left"
	SMatchStart  = "match.start"
	SSnapshot    = "snapshot"
	SMatchEnd    = "match.end"
	SDrain       = "drain"
	SReload      = "reload"
	SPong        = "pong"
)

// Коды ошибок в SError.
const (
	ErrBadMessage   = "bad_message"
	ErrBadVersion   = "bad_version"
	ErrNotAllowed   = "not_allowed"
	ErrBadNick      = "bad_nick"
	ErrRoomNotFound = "room_not_found"
	ErrRoomFull     = "room_full"
	ErrRoomLimit    = "room_limit"
	ErrBusy         = "busy"
	ErrDraining     = "draining"
	ErrBadMode      = "bad_mode"
	ErrBadArena     = "bad_arena"
	ErrBadRole      = "bad_role"
	ErrBadSlot      = "bad_slot"
	ErrServerFull   = "server_full"
	ErrInternal     = "internal"
)

// Hello — первое сообщение клиента.
type Hello struct {
	Token           string `json:"token,omitempty"` // токен сессии из localStorage
	Nick            string `json:"nick"`
	BuildVersion    string `json:"build"`
	ProtocolVersion int    `json:"proto"`
}

// Welcome — ответ на hello.
type Welcome struct {
	Token      string `json:"token"`
	PlayerID   string `json:"playerId"`
	Nick       string `json:"nick"`
	Build      string `json:"build"`
	SimVersion string `json:"sim"`
	Proto      int    `json:"proto"`
	Draining   bool   `json:"draining,omitempty"`
	// Куда клиент должен вернуться после реконнекта: "menu" | "queue" | "room" | "match".
	Resume string `json:"resume"`
}

// Error — ошибка обработки сообщения.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"msg,omitempty"`
}

// QueueJoin — встать в очередь Quick Match.
type QueueJoin struct {
	Mode int    `json:"mode"`
	Role string `json:"role"`
}

// QueueStatus — состояние очереди для игрока.
type QueueStatus struct {
	InQueue  bool `json:"inQueue"`
	Mode     int  `json:"mode,omitempty"`
	Players  int  `json:"players,omitempty"`  // живых игроков в очереди
	Needed   int  `json:"needed,omitempty"`   // всего мест
	WaitLeft int  `json:"waitLeft,omitempty"` // мс до добора ботами
}

// RoomCreate — создать комнату.
type RoomCreate struct {
	Mode  int `json:"mode"`
	Arena int `json:"arena"`
}

// RoomJoin — войти по коду.
type RoomJoin struct {
	Code string `json:"code"`
}

// RoomSlot — занять слот команды.
type RoomSlot struct {
	Team  string `json:"team"` // "A" | "B"
	Index int    `json:"index"`
}

// RoomRole — выбрать бойца в лобби.
type RoomRole struct {
	Role string `json:"role"`
}

// RoomConfig — хост меняет режим/арену.
type RoomConfig struct {
	Mode  int `json:"mode"`
	Arena int `json:"arena"`
}

// RoomKick — хост выгоняет игрока.
type RoomKick struct {
	PlayerID string `json:"playerId"`
}

// RoomPlayer — участник лобби.
type RoomPlayer struct {
	ID        string `json:"id"`
	Nick      string `json:"nick"`
	Team      string `json:"team,omitempty"` // "" — ещё не выбрал
	Index     int    `json:"index"`
	Role      string `json:"role,omitempty"`
	Host      bool   `json:"host,omitempty"`
	Connected bool   `json:"connected"`
}

// RoomState — полное состояние лобби, рассылается всем при любом изменении.
type RoomState struct {
	Code    string       `json:"code"`
	HostID  string       `json:"hostId"`
	Mode    int          `json:"mode"`
	Arena   int          `json:"arena"`
	Players []RoomPlayer `json:"players"`
	InMatch bool         `json:"inMatch"`
	// Результат последнего матча комнаты (для экрана лобби после боя).
	LastWinner string `json:"lastWinner,omitempty"`
}

// MatchPlayer — участник матча.
type MatchPlayer struct {
	ID   string `json:"id"`
	Nick string `json:"nick"`
	Team string `json:"team"`
	Role string `json:"role"`
	Bot  bool   `json:"bot"`
}

// MatchStart — матч начался (или переподключение к идущему матчу).
type MatchStart struct {
	MatchID  string        `json:"matchId"`
	Mode     int           `json:"mode"`
	Arena    int           `json:"arena"`
	Players  []MatchPlayer `json:"players"`
	YourID   string        `json:"yourId"`
	TickRate int           `json:"tickRate"`
	RoomCode string        `json:"roomCode,omitempty"` // если матч из комнаты
}

// Snapshot — состояние симуляции за тик. State — снапшот sim.js как есть, Events — события шага.
type Snapshot struct {
	Tick   int             `json:"tick"`
	State  json.RawMessage `json:"s"`
	Events json.RawMessage `json:"e,omitempty"`
}

// MatchEnd — матч завершён.
type MatchEnd struct {
	Winner   string `json:"winner"` // "A" | "B" | "" (ничья)
	YourTeam string `json:"yourTeam,omitempty"`
	Reason   string `json:"reason"` // "ko" | "timeout" | "abandoned" | "shutdown"
	RoomCode string `json:"roomCode,omitempty"`
}

// Drain — сервер готовится к перезапуску.
type Drain struct {
	Active    bool `json:"active"`
	InSeconds int  `json:"inSeconds,omitempty"`
}

// Input — ввод игрока. Пробрасывается в sim.js как есть, поэтому здесь только валидация формы.
type Input struct {
	Kind  string   `json:"kind"`
	X     float64  `json:"x"`
	Y     float64  `json:"y"`
	Power *float64 `json:"power,omitempty"`
}

// Encode упаковывает сообщение в конверт.
func Encode(typ string, data any) ([]byte, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, errors.Wrapf(err, "marshal %s", typ)
		}
		raw = b
	}
	b, err := json.Marshal(Envelope{Type: typ, Data: raw})
	if err != nil {
		return nil, errors.Wrapf(err, "marshal envelope %s", typ)
	}
	return b, nil
}

// MustEncode — Encode без ошибки для типов, которые заведомо сериализуются.
func MustEncode(typ string, data any) []byte {
	b, err := Encode(typ, data)
	if err != nil {
		panic(err)
	}
	return b
}

// Decode разбирает конверт.
func Decode(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return env, errors.Wrap(err, "unmarshal envelope")
	}
	if env.Type == "" {
		return env, errors.New("empty message type")
	}
	return env, nil
}

// MaxMessageSize — максимальный размер входящего сообщения в байтах.
const MaxMessageSize = 4 * 1024

var nickRe = regexp.MustCompile(`^[\p{L}\p{N} _\-]+$`)

// NormalizeNick приводит ник к допустимому виду или возвращает ошибку.
// Правила: 2–16 символов, буквы, цифры, пробел, дефис, подчёркивание; пробелы схлопываются.
func NormalizeNick(nick string) (string, error) {
	nick = strings.Join(strings.Fields(nick), " ")
	n := utf8.RuneCountInString(nick)
	if n < 2 || n > 16 {
		return "", errors.New("nick must be 2..16 characters")
	}
	if !nickRe.MatchString(nick) {
		return "", errors.New("nick has forbidden characters")
	}
	return nick, nil
}

// ValidRoomCode проверяет формат кода комнаты SNB-XXXX.
var roomCodeRe = regexp.MustCompile(`^SNB-[A-HJ-NP-Z0-9]{4}$`)

// NormalizeRoomCode приводит код к каноническому виду (верхний регистр, префикс).
func NormalizeRoomCode(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) == 4 {
		code = "SNB-" + code
	}
	if !roomCodeRe.MatchString(code) {
		return "", errors.New("bad room code")
	}
	return code, nil
}
