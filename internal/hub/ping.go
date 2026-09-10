package hub

import (
	"encoding/json"
	"math"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Задержка игроков. Сервер зондирует сам (`ping{seq}` → `pong{seq}`), а не верит числу от
// клиента: значение показывается в лобби всем участникам и в админке, и подделывать его
// незачем. Клиентский зонд (CPing → SPong) при этом остаётся: им живёт индикатор связи.
//
// Публикуется не сырое измерение, а округлённое до pingStep с зоной нечувствительности. Причина
// не в красоте: сводка админки обязана не меняться сама по себе между вызовами (см. Stats), на
// этом стоит диффинг SSE. С живым RTT кадр уходил бы каждую секунду, а страница перерисовывает
// таблицы целиком — она бы мигала.
//
// Всё здесь вызывается под h.mu.

const (
	pingProbeEvery = 4 * time.Second  // реже клиентских 2 с: это для лобби и админки
	pingLost       = 15 * time.Second // зонд без ответа: задержка снова неизвестна
	pingStep       = 10               // мс, шаг публикации
	pingDeadband   = 12               // мс, дрожание вокруг текущего значения не публикуем
)

// probePing отправляет зонд, если пора. Живость соединения этим не проверяется — за неё отвечает
// heartbeat самого WebSocket (internal/ws/conn.go), и смешивать их нельзя.
func (h *Hub) probePing(p *session.Player, now time.Time) {
	// Первый зонд не раньше pingProbeEvery от начала сессии: иначе задержка появлялась бы в
	// первые же миллисекунды и ломала тесты стабильности сводки.
	if now.Sub(p.CreatedAt) < pingProbeEvery {
		return
	}
	if !p.PingSentAt.IsZero() {
		if now.Sub(p.PingSentAt) > pingLost {
			p.PingSentAt = time.Time{}
			p.RTT, p.PingMs = 0, 0
		}
		return // зонд в полёте: второй не шлём, иначе seq перестанет что-то значить
	}
	if !p.RTTAt.IsZero() && now.Sub(p.RTTAt) < pingProbeEvery {
		return
	}
	p.PingSeq++
	p.PingSentAt = now
	p.Send(protocol.MustEncode(protocol.SPing, protocol.Ping{Seq: p.PingSeq}))
}

// handlePong считает RTT по ответу. Молчит на битом теле и на чужом номере: pong — не повод для
// ошибки, иначе клиент после реконнекта получал бы её на каждый запоздавший ответ.
func (h *Hub) handlePong(p *session.Player, data json.RawMessage) {
	var pong protocol.Ping
	if len(data) > 0 {
		if err := json.Unmarshal(data, &pong); err != nil {
			return
		}
	}
	if p.PingSentAt.IsZero() || pong.Seq != p.PingSeq {
		return
	}
	now := h.now()
	sample := now.Sub(p.PingSentAt)
	p.PingSentAt = time.Time{}
	p.RTTAt = now
	if p.RTT == 0 {
		p.RTT = sample
	} else {
		p.RTT = (p.RTT + sample) / 2 // сглаживание: одиночный выброс не дёргает цифру
	}
	publishPing(p)
}

// publishPing переносит сырой RTT в показываемое значение. Возвращает true, если оно изменилось.
func publishPing(p *session.Player) bool {
	ms := float64(p.RTT) / float64(time.Millisecond)
	q := int(math.Round(ms/pingStep)) * pingStep
	if q < pingStep {
		q = pingStep // ноль означает «неизвестно», поэтому на loopback показываем 10 мс
	}
	if p.PingMs != 0 && math.Abs(float64(q-p.PingMs)) <= pingDeadband {
		return false
	}
	if p.PingMs == q {
		return false
	}
	p.PingMs = q
	return true
}

// resetPing забывает задержку: после реконнекта и обрыва прежнее число относится к мёртвому
// каналу, и лобби рисовало бы задержку у того, кто уже не на связи.
func resetPing(p *session.Player) {
	p.RTT, p.PingMs = 0, 0
	p.RTTAt, p.PingSentAt = time.Time{}, time.Time{}
	p.PingSeq = 0
}
