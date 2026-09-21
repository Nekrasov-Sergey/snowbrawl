package auth

import (
	"sync"
	"time"
)

// limiter — сколько раз с одного адреса можно дёрнуть ручку входа. Без него /auth/yandex
// становится генератором мусорных кук и запросов к Яндексу от любого желающего.
//
// Устроен проще токен-бакета из internal/ws: здесь события редкие (вход человека), точность
// не нужна, нужен потолок. Окно фиксированное, чистка — при обращении, чтобы не заводить
// отдельную горутину ради карты на десяток записей.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*window
}

type window struct {
	count int
	until time.Time
}

func newLimiter(limit int, win time.Duration) *limiter {
	return &limiter{limit: limit, window: win, seen: map[string]*window{}}
}

// allow — можно ли обслужить обращение с этого адреса.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 10000 {
		l.sweep(now)
	}
	w, ok := l.seen[key]
	if !ok || now.After(w.until) {
		l.seen[key] = &window{count: 1, until: now.Add(l.window)}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// sweep выбрасывает истёкшие окна. Вызывать под l.mu.
func (l *limiter) sweep(now time.Time) {
	for k, w := range l.seen {
		if now.After(w.until) {
			delete(l.seen, k)
		}
	}
}
