package hub_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// TestNickTakenFromAnotherIP — ник, взятый одним игроком, недоступен другому адресу до
// перезапуска сервера, а свой адрес получает его обратно.
func TestNickTakenFromAnotherIP(t *testing.T) {
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
	s := newServer(t, nil)
	cl := s.connect(t, "Хост", "")
	cl.send(protocol.CRoomCreate, protocol.RoomCreate{Mode: 1, Arena: 0})
	var st protocol.RoomState
	cl.expect(protocol.SRoomState, &st)

	// Отвечаем на зонды сервера сами: первый уходит через pingProbeEvery от начала сессии.
	collect := func(d time.Duration) []protocol.RoomPing {
		var out []protocol.RoomPing
		deadline := time.After(d)
		for {
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

	got := collect(8 * time.Second)
	if len(got) == 0 {
		t.Fatal("задержки лобби не приехали")
	}
	last := got[len(got)-1]
	if last.Code != st.Code {
		t.Fatalf("код комнаты %q, ожидался %q", last.Code, st.Code)
	}
	found := false
	for _, pp := range last.Pings {
		if pp.ID == cl.ID && pp.Ping > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("своей задержки нет в %+v", last.Pings)
	}

	// На loopback задержка не меняется, значит повторных рассылок быть не должно.
	if again := collect(6 * time.Second); len(again) > 0 {
		t.Fatalf("задержки разосланы повторно без изменений: %+v", again)
	}
}

// TestOnlineSeriesSampled — тик hub кладёт точку в ряд онлайна. Бакетизацию и файл проверяет
// internal/onlinestat; здесь важна только проводка.
func TestOnlineSeriesSampled(t *testing.T) {
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
