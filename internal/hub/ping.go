package hub

import (
	"encoding/json"
	"math"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Задержка игроков. Измерение одно на всех: зондирует сервер (`ping{seq}` → `pong{seq}`), а
// измеренное число рассылается отдельным кадром `self.ping`. Своего зонда у клиента нет: два
// независимых замера давали два разных числа в углу экрана и в лобби, а верить числу от
// клиента нельзя — его видят все участники лобби и админ.
//
// Почему `self.ping`, а не поле в зонде: зонд несёт прошлое измерение, поэтому угол экрана
// отставал от лобби на целый цикл (до 2 с) — ровно то расхождение, которое видел игрок.
// Отдельный кадр уходит на том же тике хаба, что и room.ping, поэтому индикатор связи и своя
// строка в составе лобби меняются одновременно. Слать его нужно всем подключённым, а не
// только сидящим в лобби: угол виден и в меню, и в бою, и в оффлайн-тренировке.
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
	p.Send(protocol.MustEncode(protocol.SPing, protocol.Ping{Seq: p.PingSeq}))
}

// pushSelfPing отдаёт игроку его собственную задержку, если она изменилась с прошлой отправки.
// Ноль («неизвестна») отправляется наравне с остальными значениями — иначе после потери зондов
// в углу экрана осталось бы висеть число от мёртвого канала.
func (h *Hub) pushSelfPing(p *session.Player) {
	if p.PingSent == p.PingMs {
		return
	}
	p.PingSent = p.PingMs
	p.Send(protocol.MustEncode(protocol.SSelfPing, protocol.SelfPing{Ms: p.PingMs}))
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
	// Новому соединению число не отправляли ни разу: без сброса первый же ноль сочли бы
	// уже доехавшим и угол экрана остался бы с прежней цифрой.
	p.PingSent = -1
}
