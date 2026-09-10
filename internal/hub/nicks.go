package hub

import (
	"sort"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// Уникальные ники. Однажды взятый ник занят до перезапуска сервера и освобождается только когда
// игрок сам переименовался: так имя, под которым тебя запомнили в чате, нельзя занять чужому.
//
// Бронь живёт в памяти (на диск не пишем — перезапуск и должен всё отпускать) и привязана сразу к
// двум признакам: адресу и id сессии. Одного адреса мало — у мобильного игрока IP меняется между
// перезагрузками страницы, и он получал бы «ник занят» от собственной брони.
//
// Всё в этом файле вызывается под h.mu.

type nickHold struct {
	nick  string    // как ник выглядел: для логов и админки
	ip    string    // кому отдаём ник обратно
	owner string    // id сессии, взявшей бронь; сессии может уже не быть
	at    time.Time // когда взята: по нему вытесняем самые старые
}

// nickHoldsCap — потолок реестра. Бот, дёргающий hello с новыми никами, иначе растил бы карту
// без предела; при переполнении вытесняем брони, за которыми нет живой сессии.
const nickHoldsCap = 5000

// nickFree — можно ли выдать ник этой сессии. owner пустой у новой сессии (её id ещё нет).
func (h *Hub) nickFree(nick, ip, owner string) bool {
	hold, ok := h.nicks[protocol.NickKey(nick)]
	if !ok {
		return true
	}
	return hold.ip == ip || (owner != "" && hold.owner == owner)
}

// holdNick забирает ник за сессией. Зовётся только после того, как ник ей действительно выдан.
func (h *Hub) holdNick(nick, ip, owner string, now time.Time) {
	if len(h.nicks) >= nickHoldsCap {
		h.evictNickHolds()
	}
	h.nicks[protocol.NickKey(nick)] = nickHold{nick: nick, ip: ip, owner: owner, at: now}
}

// releaseNick отпускает бронь при переименовании. Только свою: иначе двое с одного адреса под
// одним ником отбирали бы бронь друг у друга.
func (h *Hub) releaseNick(key, owner string) {
	if hold, ok := h.nicks[key]; ok && hold.owner == owner {
		delete(h.nicks, key)
	}
}

// evictNickHolds сносит самые старые брони, за которыми нет живой сессии, до 90 % ёмкости.
// Порядок — по времени, а не по обходу карты: иначе вытеснение было бы случайным.
func (h *Hub) evictNickHolds() {
	type cand struct {
		key string
		at  time.Time
	}
	var dead []cand
	for key, hold := range h.nicks {
		if h.byID[hold.owner] == nil {
			dead = append(dead, cand{key, hold.at})
		}
	}
	if len(dead) == 0 {
		h.log.Warn().Int("holds", len(h.nicks)).Msg("nicks: реестр полон, все брони за живыми сессиями")
		return
	}
	sort.Slice(dead, func(i, j int) bool { return dead[i].at.Before(dead[j].at) })
	target := nickHoldsCap * 9 / 10
	for _, c := range dead {
		if len(h.nicks) <= target {
			break
		}
		delete(h.nicks, c.key)
	}
	h.log.Info().Int("holds", len(h.nicks)).Msg("nicks: реестр подрезан")
}

// releaseNicksOfIP отпускает все брони адреса — при бане. Иначе ник забаненного остаётся
// заложником до перезапуска, хотя вернуться под ним он всё равно не может.
func (h *Hub) releaseNicksOfIP(ip string) {
	for key, hold := range h.nicks {
		if hold.ip == ip {
			delete(h.nicks, key)
		}
	}
}
