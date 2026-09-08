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
const Version = 2

// Envelope — конверт любого сообщения.
type Envelope struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

// Типы сообщений клиент → сервер.
const (
	CHello      = "hello"
	CRoomCreate = "room.create"
	CRoomJoin   = "room.join"
	CRoomSlot   = "room.slot"
	CRoomRole   = "room.role"
	CRoomConfig = "room.config"
	CRoomReady  = "room.ready"
	CRoomKick   = "room.kick"
	CRoomStart  = "room.start"
	CRoomLeave  = "room.leave"
	CRoomList   = "room.list"   // он же подписка на обновления списка
	CRoomUnlist = "room.unlist" // уход с экрана списка
	CMatchJoin  = "match.join"  // войти в идущий матч за бойца своего слота
	CMatchLeave = "match.leave"
	CInput      = "input"
	CTraining   = "training"
	CChatSend   = "chat.send" // сообщение в общий чат главного меню
	CPing       = "ping"
)

// Типы сообщений сервер → клиент.
const (
	SWelcome     = "welcome"
	SError       = "error"
	SRoomState   = "room.state"
	SRoomLeft    = "room.left"
	SRoomList    = "room.list"
	SRoomMatch   = "room.match"
	SMatchStart  = "match.start"
	SMatchRoster = "match.roster"
	SSnapshot    = "snapshot"
	SMatchEnd    = "match.end"
	SDrain       = "drain"
	SOnline      = "online"
	SReload      = "reload"
	SChatMsg     = "chat.msg"     // одно сообщение чата (рассылка всем)
	SChatHistory = "chat.history" // последние сообщения чата (после welcome)
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
	ErrBadGameMode  = "bad_gamemode"
	ErrBadSlot      = "bad_slot"
	ErrCodeRequired = "code_required"
	ErrBadCode      = "bad_code"
	ErrTooManyTries = "too_many_tries"
	ErrSlotTaken    = "slot_taken"
	ErrNoSlots      = "no_slots"
	ErrChatFlood    = "chat_flood" // слишком часто пишете в чат
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
	Online     int    `json:"online"` // сколько игроков сейчас на сервере, включая этого
	// Куда клиент должен вернуться после реконнекта: "menu" | "room" | "match".
	Resume string `json:"resume"`
}

// Online — число игроков на сервере. Приходит в welcome и дальше при каждом изменении:
// счётчик привязан к тому же соединению, что и игра, поэтому не врёт при обрыве.
type Online struct {
	N int `json:"n"`
}

// Error — ошибка обработки сообщения.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"msg,omitempty"`
}

// Training — клиент сообщает, что играет тренировку с ботами. Тренировка целиком в браузере,
// сервер в ней не участвует и знает о ней только отсюда — чтобы админка показывала, чем занят игрок.
type Training struct {
	On    bool   `json:"on"`
	Mode  int    `json:"mode,omitempty"`
	Arena int    `json:"arena,omitempty"`
	Role  string `json:"role,omitempty"`
}

// RoomCreate — создать комнату.
type RoomCreate struct {
	Mode  int `json:"mode"`
	Arena int `json:"arena"`
	// PvE: gameMode "" | "pvp" | "survival" | "defense"; campaign — кампания (иначе эндлесс).
	// Difficulty 0..2 — сложность ботов (в PvP они занимают пустые слоты, в PvE это ещё и
	// сдвиг уровня врагов). Указатель, чтобы отличить «не прислали» от «Лёгкий»: без него
	// комната по умолчанию получала бы самых слабых ботов.
	GameMode   string `json:"gameMode,omitempty"`
	Campaign   bool   `json:"campaign,omitempty"`
	Difficulty *int   `json:"difficulty,omitempty"`
	// Visibility "open" — комната видна в списке и открыта для входа; "closed" — видна со
	// замочком, войти можно только по коду. Пустое значение считается "open".
	Visibility string `json:"visibility,omitempty"`
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

// RoomConfig — хост меняет настройки комнаты. Любое изменение сбрасывает готовность всех.
type RoomConfig struct {
	Mode       int    `json:"mode"`
	Arena      int    `json:"arena"`
	GameMode   string `json:"gameMode,omitempty"`
	Campaign   bool   `json:"campaign,omitempty"`
	Difficulty *int   `json:"difficulty,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// RoomReady — игрок отмечает готовность. Когда готовы все люди в комнате, включая хоста,
// матч стартует сам.
type RoomReady struct {
	Ready bool `json:"ready"`
}

// RoomList — запросить страницу списка комнат и подписаться на её обновления.
// Section: "pvp" | "pve". Страницы нумеруются с нуля.
type RoomList struct {
	Section string `json:"section"`
	Page    int    `json:"page"`
}

// RoomBrief — строка списка комнат.
type RoomBrief struct {
	Code       string `json:"code"`
	Section    string `json:"section"`
	GameMode   string `json:"gameMode,omitempty"`
	Campaign   bool   `json:"campaign,omitempty"`
	Mode       int    `json:"mode"`
	Arena      int    `json:"arena"`
	Humans     int    `json:"humans"`   // людей в комнате
	Bots       int    `json:"bots"`     // слотов, которые занимают боты
	Capacity   int    `json:"capacity"` // всего мест
	InMatch    bool   `json:"inMatch"`
	Visibility string `json:"visibility"`
	HostNick   string `json:"hostNick,omitempty"`
	AgeMs      int64  `json:"ageMs"`              // сколько комната существует
	Joinable   bool   `json:"joinable"`           // есть куда сесть
	NeedCode   bool   `json:"needCode,omitempty"` // закрытая: нужен код
}

// RoomListPage — страница списка комнат. Сортировка по времени создания, старые первыми.
type RoomListPage struct {
	Section string      `json:"section"`
	Page    int         `json:"page"`
	Pages   int         `json:"pages"`
	Total   int         `json:"total"`
	Rooms   []RoomBrief `json:"rooms"`
}

// RoomMatchSlot — боец идущего матча в разрезе слота комнаты.
type RoomMatchSlot struct {
	Team  string `json:"team"`
	Index int    `json:"index"`
	Nick  string `json:"nick"`
	Role  string `json:"role"`
	Bot   bool   `json:"bot"` // сейчас ведётся ботом: слот свободен или его человек в лобби
	HP    int    `json:"hp"`
	Koed  bool   `json:"koed,omitempty"`
}

// RoomMatch — состояние идущего матча для тех, кто сидит в лобби этой комнаты.
// Рассылается, пока содержимое меняется (раз в секунду по таймеру матча).
type RoomMatch struct {
	Code       string          `json:"code"`
	TimeLeftMs int             `json:"timeLeftMs"`
	Slots      []RoomMatchSlot `json:"slots"`
}

// MatchRoster — состав матча изменился: кто-то подсел на место бота или вышел.
type MatchRoster struct {
	Players []MatchPlayer `json:"players"`
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
	Ready     bool   `json:"ready,omitempty"`
	InMatch   bool   `json:"inMatch,omitempty"` // играет в идущем матче (иначе сидит в лобби)
}

// RoomState — полное состояние лобби, рассылается всем при любом изменении.
type RoomState struct {
	Code       string       `json:"code"`
	HostID     string       `json:"hostId"`
	Mode       int          `json:"mode"`
	Arena      int          `json:"arena"`
	GameMode   string       `json:"gameMode,omitempty"`
	Campaign   bool         `json:"campaign,omitempty"`
	Difficulty int          `json:"difficulty,omitempty"`
	Visibility string       `json:"visibility"`
	Players    []RoomPlayer `json:"players"`
	InMatch    bool         `json:"inMatch"`
	ReadyCount int          `json:"readyCount"` // сколько людей нажали «Готов»
	// Результат последнего матча комнаты (для экрана лобби после боя).
	LastWinner string `json:"lastWinner,omitempty"`
}

// MatchPlayer — участник матча.
type MatchPlayer struct {
	ID       string `json:"id"`
	Nick     string `json:"nick"`
	Team     string `json:"team"`
	Index    int    `json:"index"` // слот комнаты, за которым закреплён этот боец
	Role     string `json:"role"`
	Bot      bool   `json:"bot"`
	BotLevel *int   `json:"botLevel,omitempty"` // уровень бота 0..2; nil — уровень по умолчанию
}

// MatchStart — матч начался (или переподключение к идущему матчу).
type MatchStart struct {
	MatchID  string        `json:"matchId"`
	Mode     int           `json:"mode"`
	Arena    int           `json:"arena"`
	GameMode string        `json:"gameMode,omitempty"` // "" == "pvp"
	Players  []MatchPlayer `json:"players"`
	YourID   string        `json:"yourId"`
	TickRate int           `json:"tickRate"`
	RoomCode string        `json:"roomCode,omitempty"` // если матч из комнаты
}

// Snapshot — состояние симуляции за тик. State — снапшот sim.js как есть, Events — события шага.
// Countdown > 0 — идёт отсчёт перед стартом: симуляция стоит, ввод сервером не принимается.
type Snapshot struct {
	Tick      int             `json:"tick"`
	State     json.RawMessage `json:"s"`
	Events    json.RawMessage `json:"e,omitempty"`
	Countdown int             `json:"cd,omitempty"` // мс до старта матча
}

// MatchEnd — матч завершён.
type MatchEnd struct {
	Winner   string `json:"winner"` // "A" | "B" | "" (ничья)
	YourTeam string `json:"yourTeam,omitempty"`
	Reason   string `json:"reason"` // PvP: "ko"|"timeout"|"abandoned"|"shutdown"; PvE: "cleared"|"wiped"|"objective"|"expired"
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

// ChatSend — сообщение игрока в общий чат.
type ChatSend struct {
	Text string `json:"text"`
}

// ChatMessage — одно сообщение чата.
type ChatMessage struct {
	ID   uint64 `json:"id"`
	Nick string `json:"nick"`
	Text string `json:"text"`
	TS   int64  `json:"ts"` // unix-время в мс
}

// ChatHistory — пачка последних сообщений (после welcome).
type ChatHistory struct {
	Messages []ChatMessage `json:"messages"`
}

// MaxChatRunes — предел длины сообщения чата.
const MaxChatRunes = 300

// NormalizeChat чистит текст сообщения чата: схлопывает пробелы, убирает управляющие
// символы, ограничивает длину. Пустой результат — сообщение отклоняется.
func NormalizeChat(text string) (string, error) {
	var b strings.Builder
	for _, r := range text {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if out == "" {
		return "", errors.New("empty message")
	}
	if utf8.RuneCountInString(out) > MaxChatRunes {
		out = string([]rune(out)[:MaxChatRunes])
	}
	return out, nil
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

// roomCodeRe — формат кода комнаты: четыре цифры.
var roomCodeRe = regexp.MustCompile(`^[0-9]{4}$`)

// NormalizeRoomCode приводит код к каноническому виду: обрезает пробелы и
// устаревший префикс SNB- (до 0.2.1 коды были вида SNB-XXXX).
func NormalizeRoomCode(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.TrimPrefix(code, "SNB-")
	if !roomCodeRe.MatchString(code) {
		return "", errors.New("bad room code")
	}
	return code, nil
}
