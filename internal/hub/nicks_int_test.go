package hub_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// TestNickTakenFromAnotherIP — ник, взятый одним игроком, недоступен другому адресу до
// перезапуска сервера, а свой адрес получает его обратно.
func TestNickTakenFromAnotherIP(t *testing.T) {
	t.Parallel()
	s := newServer(t, func(c *config.Config) { c.TrustProxy = true })
	first := s.connectFrom(t, "Мороз", "", "10.1.1.1")
	if first.ID == "" {
		t.Fatal("первый игрок не вошёл")
	}

	// Другой адрес получает отказ с понятным кодом; соединение при этом живо.
	other := s.connectRaw(t, "Мороз", "", "10.2.2.2")
	var e protocol.Error
	other.expect(protocol.SError, &e)
	if e.Code != protocol.ErrNickTaken {
		t.Fatalf("код ошибки %q, ожидался %q", e.Code, protocol.ErrNickTaken)
	}

	// Свободный ник тот же адрес берёт без вопросов.
	third := s.connectFrom(t, "Вьюга", "", "10.2.2.2")
	if third.ID == "" {
		t.Fatal("свободный ник не выдан")
	}

	// Тот же адрес возвращается под своим ником: новая сессия, старая бронь его же.
	again := s.connectFrom(t, "Мороз", "", "10.1.1.1")
	if again.ID == "" || again.ID == first.ID {
		t.Fatalf("свой ник с того же адреса не выдан (id %q)", again.ID)
	}
}

// TestRoomPingPushedOnlyOnChange — задержки лобби едут отдельным сообщением и только при
// изменении: иначе состав рассылался бы заново каждые несколько секунд из-за одной цифры.
func TestRoomPingPushedOnlyOnChange(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	cl := s.connect(t, "Хост", "")
	cl.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var st protocol.RoomState
	cl.expect(protocol.SRoomState, &st)

	// Отвечаем на зонды сервера сами: первый уходит сразу, дальше раз в период зонда.
	probe := s.cfg.PingProbeEvery
	mine := func(rp protocol.RoomPing) bool {
		for _, pp := range rp.Pings {
			if pp.ID == cl.ID && pp.Ping > 0 {
				return true
			}
		}
		return false
	}
	// Собираем кадры до выполнения условия, а срок — только страховка: раньше здесь стояли два
	// фиксированных окна на 8 и 6 секунд, и пакет просто пережидал их. Считать кадры по срокам
	// нельзя в обе стороны: кадр приходит лишь при изменении набора задержек (на стабильной
	// петле их может быть всего один), а самый первый кадр уходит ещё до первого pong и потому
	// пустой.
	collect := func(d time.Duration, done func([]protocol.RoomPing) bool) []protocol.RoomPing {
		var out []protocol.RoomPing
		deadline := time.After(d)
		for {
			if done(out) {
				return out
			}
			select {
			case env, ok := <-cl.inbox:
				if !ok {
					t.Fatal("соединение закрылось")
				}
				switch env.Type {
				case protocol.SPing:
					var ping protocol.Ping
					_ = json.Unmarshal(env.Data, &ping)
					cl.send(protocol.CPong, protocol.Ping{Seq: ping.Seq})
				case protocol.SRoomPing:
					var rp protocol.RoomPing
					if err := json.Unmarshal(env.Data, &rp); err != nil {
						t.Fatal(err)
					}
					out = append(out, rp)
				}
			case <-deadline:
				return out
			}
		}
	}

	got := collect(40*probe, func(out []protocol.RoomPing) bool { return len(out) > 0 && mine(out[len(out)-1]) })
	if len(got) == 0 {
		t.Fatal("задержки лобби не приехали")
	}
	last := got[len(got)-1]
	if last.Code != st.Code {
		t.Fatalf("код комнаты %q, ожидался %q", last.Code, st.Code)
	}
	if !mine(last) {
		t.Fatalf("своей задержки нет в %+v", last.Pings)
	}

	// Рассылка идёт только при изменении: два кадра подряд с одинаковым набором задержек —
	// это сломанный диффинг. Изменение провоцируем сами, заводя в комнату второго игрока:
	// набор задержек меняется составом, а не дрожанием петли. Ждать изменения «само собой»
	// нельзя — на loopback задержка может не меняться вовсе, и проверка сравнивала бы
	// единственный кадр сам с собой, то есть не проверяла бы ничего.
	second := s.connect(t, "Гость", "")
	second.send(protocol.CRoomJoin, protocol.RoomJoin{Code: st.Code})
	second.expect(protocol.SRoomState, nil)
	all := make([]protocol.RoomPing, 0, len(got)+2)
	all = append(all, got...)
	all = append(all, collect(40*probe, func(out []protocol.RoomPing) bool {
		return len(out) > 0 && len(out[len(out)-1].Pings) > 1 // в кадре уже двое
	})...)
	if len(all) < 2 {
		t.Fatalf("после входа второго игрока новых кадров не пришло: %+v", all)
	}
	for i := 1; i < len(all); i++ {
		if fmt.Sprint(all[i].Pings) == fmt.Sprint(all[i-1].Pings) {
			t.Fatalf("задержки разосланы повторно без изменений: %+v", all[i])
		}
	}
}

// TestOnlineSeriesSampled — тик hub кладёт точку в ряд онлайна. Бакетизацию и файл проверяет
// internal/onlinestat; здесь важна только проводка.
func TestOnlineSeriesSampled(t *testing.T) {
	t.Parallel()
	s := newServer(t, nil)
	cl := s.connect(t, "Наблюдатель", "")
	if cl.ID == "" {
		t.Fatal("игрок не вошёл")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, pts := s.hub.OnlineSeries().Points(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
		if len(pts) > 0 && pts[len(pts)-1].N >= 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("точка онлайна не появилась в ряду")
}
