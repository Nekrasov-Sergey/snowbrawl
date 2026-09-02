// Package matchmaking — очереди Quick Match по режимам. Чистая структура данных без
// блокировок: ею владеет hub.
package matchmaking

import "time"

// Entry — игрок в очереди.
type Entry struct {
	PlayerID string
	Role     string
	JoinedAt time.Time
}

// Queue — очередь одного режима.
type Queue struct {
	Mode    int
	Entries []Entry
	// OpenedAt — момент появления первого игрока в пустой очереди; от него считается ожидание.
	OpenedAt time.Time
}

// Queues — все очереди.
type Queues struct {
	Wait   time.Duration
	byMode map[int]*Queue
}

// New создаёт набор очередей с заданным временем ожидания живых игроков.
func New(wait time.Duration) *Queues {
	return &Queues{Wait: wait, byMode: map[int]*Queue{}}
}

// Get возвращает очередь режима (создаёт при необходимости).
func (q *Queues) Get(mode int) *Queue {
	qq, ok := q.byMode[mode]
	if !ok {
		qq = &Queue{Mode: mode}
		q.byMode[mode] = qq
	}
	return qq
}

// All возвращает очереди всех режимов.
func (q *Queues) All() []*Queue {
	res := make([]*Queue, 0, len(q.byMode))
	for _, qq := range q.byMode {
		res = append(res, qq)
	}
	return res
}

// Join ставит игрока в очередь. Возвращает очередь.
func (q *Queues) Join(mode int, playerID, role string, now time.Time) *Queue {
	qq := q.Get(mode)
	for _, e := range qq.Entries {
		if e.PlayerID == playerID {
			return qq
		}
	}
	if len(qq.Entries) == 0 {
		qq.OpenedAt = now
	}
	qq.Entries = append(qq.Entries, Entry{PlayerID: playerID, Role: role, JoinedAt: now})
	return qq
}

// Leave убирает игрока из очереди режима.
func (q *Queues) Leave(mode int, playerID string) {
	qq, ok := q.byMode[mode]
	if !ok {
		return
	}
	for i, e := range qq.Entries {
		if e.PlayerID == playerID {
			qq.Entries = append(qq.Entries[:i], qq.Entries[i+1:]...)
			break
		}
	}
}

// Capacity — сколько мест в матче этого режима.
func (qq *Queue) Capacity() int { return 2 * qq.Mode }

// Full сообщает, что живых игроков хватает на полный матч.
func (qq *Queue) Full() bool { return len(qq.Entries) >= qq.Capacity() }

// WaitLeft — сколько осталось ждать до добора ботами.
func (qq *Queue) WaitLeft(now time.Time, wait time.Duration) time.Duration {
	if len(qq.Entries) == 0 {
		return wait
	}
	left := wait - now.Sub(qq.OpenedAt)
	if left < 0 {
		return 0
	}
	return left
}

// Ready сообщает, что пора стартовать матч: очередь полна или истекло ожидание.
func (qq *Queue) Ready(now time.Time, wait time.Duration) bool {
	if len(qq.Entries) == 0 {
		return false
	}
	return qq.Full() || qq.WaitLeft(now, wait) == 0
}

// Take забирает из очереди до capacity игроков для матча и переоткрывает очередь для оставшихся.
func (qq *Queue) Take(now time.Time) []Entry {
	n := qq.Capacity()
	if n > len(qq.Entries) {
		n = len(qq.Entries)
	}
	taken := append([]Entry(nil), qq.Entries[:n]...)
	qq.Entries = append([]Entry(nil), qq.Entries[n:]...)
	if len(qq.Entries) > 0 {
		qq.OpenedAt = now
	}
	return taken
}
