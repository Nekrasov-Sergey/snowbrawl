package admin

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
)

// streamer — одна горутина опроса hub.Stats() на всех подписчиков SSE. Состояние уходит
// только если изменилось: тот же приём, что у списка комнат (internal/hub/browse.go), только
// сравнивается сериализованная сводка.
type streamer struct {
	h *hub.Hub

	mu   sync.Mutex
	subs map[chan []byte]struct{}
	last string // отпечаток последнего разосланного состояния (без поля Now)
	cur  []byte // готовый кадр: его получает новый подписчик, не дожидаясь тика
	stop chan struct{}
	done bool
}

func newStreamer(h *hub.Hub) *streamer {
	return &streamer{h: h, subs: map[chan []byte]struct{}{}, stop: make(chan struct{})}
}

func (s *streamer) run(period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	s.poll()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.poll()
		}
	}
}

func (s *streamer) poll() {
	st := s.h.Stats()
	// Отпечаток считаем без времени снимка: иначе кадр уходил бы каждый тик.
	fp := st
	fp.Now = time.Time{}
	print, err := json.Marshal(fp)
	if err != nil {
		return
	}
	body, err := json.Marshal(st)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = body
	if string(print) == s.last {
		return
	}
	s.last = string(print)
	for ch := range s.subs {
		// Отправка неблокирующая: залипший подписчик не должен останавливать рассылку.
		// Пропущенный кадр не беда — следующий тоже полный.
		select {
		case ch <- body:
		default:
		}
	}
}

// subscribe возвращает канал кадров, текущее состояние и функцию отписки.
func (s *streamer) subscribe() (<-chan []byte, []byte, func()) {
	ch := make(chan []byte, 1)
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		close(ch)
		return ch, nil, func() {}
	}
	s.subs[ch] = struct{}{}
	cur := s.cur
	s.mu.Unlock()
	return ch, cur, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// Close останавливает опрос и выпускает всех подписчиков.
func (s *streamer) Close() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.mu.Unlock()
	close(s.stop)
}

// sseFrame — один кадр потока. JSON от Marshal переводов строки не содержит, поэтому одна
// строка data достаточна.
func sseFrame(body []byte) []byte {
	out := make([]byte, 0, len(body)+8)
	out = append(out, "data: "...)
	out = append(out, body...)
	return append(out, '\n', '\n')
}
