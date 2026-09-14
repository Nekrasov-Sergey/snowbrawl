package hub

import (
	"encoding/json"
	"math"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Задержка игроков. Измерение одно на всех: зондирует сервер (`ping{seq}` → `pong{seq}`), и то
// же самое число едет обратно в теле следующего зонда — его клиент показывает в индикаторе
// связи. Своего зонда у клиента нет: два независимых замера давали два разных числа в углу
// экрана и в лобби, а верить числу от клиента нельзя — его видят все участники лобби и админ.
//
// Публикуется живое значение с точностью до миллисекунды. Раньше здесь были шаг 10 мс и зона
// нечувствительности 12 мс: сводка админки не должна меняться сама по себе, на этом стоит
// диффинг SSE (см. Stats), а страница перерисовывала таблицы целиком и мигала бы. Но зона
// оказалась шире шага, и переход на одну ступень не публиковался никогда — число застревало
// и врало. Мигание вылечено на своём месте, в internal/admin/admin.html: таблица обновляет
// изменившиеся ячейки, а не пересобирается целиком.
//
// Всё здесь вызывается под h.mu.

const (
	pingProbeEvery = 2 * time.Second  // от этого зонда живёт индикатор связи, реже нельзя
	pingLost       = 15 * time.Second // зонд без ответа: задержка снова неизвестна
)

// probeEvery — период зонда: из конфигурации, если она его задаёт (тесты), иначе боевой.
func (h *Hub) probeEvery() time.Duration {
	if h.cfg.PingProbeEvery > 0 {
		return h.cfg.PingProbeEvery
	}
	return pingProbeEvery
}

// probePing отправляет зонд, если пора. Живость соединения этим не проверяется — за неё отвечает
// heartbeat самого WebSocket (internal/ws/conn.go), и смешивать их нельзя.
func (h *Hub) probePing(p *session.Player, now time.Time) {
	// Первый зонд уходит сразу: индикатор связи ждёт числа, и держать его пустым лишние
	// секунды незачем.
	if !p.PingSentAt.IsZero() {
		if now.Sub(p.PingSentAt) > pingLost {
			p.PingSentAt = time.Time{}
			p.RTT, p.PingMs = 0, 0
		}
		return // зонд в полёте: второй не шлём, иначе seq перестанет что-то значить
	}
	if !p.RTTAt.IsZero() && now.Sub(p.RTTAt) < h.probeEvery() {
		return
	}
	p.PingSeq++
	p.PingSentAt = now
	// В зонде едет прошлое измерение: отдельное сообщение ради одной цифры заводить незачем.
	p.Send(protocol.MustEncode(protocol.SPing, protocol.Ping{Seq: p.PingSeq, Ms: p.PingMs}))
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
	q := int(math.Round(float64(p.RTT) / float64(time.Millisecond)))
	if q < 1 {
		q = 1 // ноль означает «неизвестно», поэтому на loopback показываем 1 мс
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
