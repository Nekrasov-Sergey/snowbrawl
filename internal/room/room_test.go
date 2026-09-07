package room

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestJoinLeaveAndHostTransfer(t *testing.T) {
	now := time.Now()
	r := New("1234", "h", "1.1.1.1", Config{Mode: 2, GameMode: "pvp"}, now)
	if r.Capacity() != 4 {
		t.Fatalf("capacity = %d", r.Capacity())
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := r.Join(id); err != nil {
			t.Fatalf("join %s: %v", id, err)
		}
	}
	if err := r.Join("d"); !errors.Is(err, ErrFull) {
		t.Fatalf("expected ErrFull, got %v", err)
	}
	// Автораспределение чередует команды: h→A0, a→B0, b→A1, c→B1.
	teams := map[string]int{}
	for _, m := range r.Members {
		teams[m.Team]++
	}
	if teams["A"] != 2 || teams["B"] != 2 {
		t.Fatalf("teams unbalanced: %v", teams)
	}
	if empty := r.Leave("h", now); empty {
		t.Fatal("room must not be empty")
	}
	if r.HostID != "a" {
		t.Fatalf("host must pass to next member, got %s", r.HostID)
	}
	for _, id := range []string{"a", "b"} {
		r.Leave(id, now)
	}
	if !r.Leave("c", now) || !r.IsEmpty() || r.EmptySince.IsZero() {
		t.Fatal("room must become empty with EmptySince set")
	}
}

func TestSlotsAndConfig(t *testing.T) {
	now := time.Now()
	r := New("1234", "h", "1.1.1.1", Config{Mode: 3, GameMode: "pvp"}, now)
	_ = r.Join("a")
	if err := r.SetSlot("a", "A", 0); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("expected ErrSlotTaken, got %v", err)
	}
	if err := r.SetSlot("a", "A", 2); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSlot("a", "C", 0); !errors.Is(err, ErrBadSlot) {
		t.Fatalf("expected ErrBadSlot, got %v", err)
	}
	if err := r.SetConfig("a", Config{Mode: 1, Arena: 0, GameMode: "pvp"}, 3); !errors.Is(err, ErrNotHost) {
		t.Fatalf("expected ErrNotHost, got %v", err)
	}
	if err := r.SetConfig("h", Config{Mode: 1, Arena: 5, GameMode: "pvp"}, 3); !errors.Is(err, ErrBadArena) {
		t.Fatalf("expected ErrBadArena, got %v", err)
	}
	// Уменьшаем режим до 1×1: a сидел в A2, должен быть переставлен в свободный слот B0.
	if err := r.SetConfig("h", Config{Mode: 1, Arena: 1, GameMode: "pvp"}, 3); err != nil {
		t.Fatal(err)
	}
	a := r.Member("a")
	if a.Team != "B" || a.Index != 0 {
		t.Fatalf("a must be moved to B0, got %s%d", a.Team, a.Index)
	}
	_ = r.Join("b")
	if err := r.Join("c"); !errors.Is(err, ErrFull) {
		t.Fatalf("1×1 room must be full after 2 members, got %v", err)
	}
	if err := r.Kick("h", "h", now); !errors.Is(err, ErrNotMember) {
		t.Fatalf("host cannot kick himself, got %v", err)
	}
	if err := r.Kick("h", "a", now); err != nil {
		t.Fatal(err)
	}
	if r.Member("a") != nil {
		t.Fatal("a must be kicked")
	}
}

func TestPveRoomCapacityAndPlacement(t *testing.T) {
	now := time.Now()
	r := New("1234", "h", "1.1.1.1", Config{Mode: 4, GameMode: "survival", Campaign: true, Difficulty: 2}, now)
	if !r.IsPvE() || r.Capacity() != 4 { // пати из 4, только команда A
		t.Fatalf("pve capacity = %d, isPvE = %v", r.Capacity(), r.IsPvE())
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := r.Join(id); err != nil {
			t.Fatalf("join %s: %v", id, err)
		}
	}
	if err := r.Join("d"); !errors.Is(err, ErrFull) {
		t.Fatalf("5th member into party of 4 must fail, got %v", err)
	}
	for _, m := range r.Members {
		if m.Team != "A" {
			t.Fatalf("pve member %s placed on team %q, want A", m.ID, m.Team)
		}
	}
	if err := r.SetSlot("a", "B", 0); !errors.Is(err, ErrBadSlot) {
		t.Fatalf("team B slot must be rejected in pve, got %v", err)
	}
	// Переключение обратно в PvP освобождает лишние слоты и раскидывает по A/B.
	if err := r.SetConfig("h", Config{Mode: 2, Arena: 0, GameMode: "pvp"}, 5); err != nil {
		t.Fatalf("switch to pvp 2x2: %v", err)
	}
	if r.IsPvE() || r.Capacity() != 4 {
		t.Fatalf("after switch: isPvE=%v cap=%d", r.IsPvE(), r.Capacity())
	}
}

func TestGenerateCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := GenerateCode()
		if len(c) != 4 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("codes are not random enough: %d unique of 200", len(seen))
	}
}

func TestReadyAndVisibility(t *testing.T) {
	now := time.Now()
	r := New("1234", "h", "1.1.1.1", Config{Mode: 2, GameMode: "pvp", Visibility: VisibilityClosed}, now)
	if !r.IsClosed() || r.Section() != "pvp" {
		t.Fatalf("visibility=%q section=%q", r.Visibility, r.Section())
	}
	_ = r.Join("a")
	all := func(string) bool { return true }
	if r.AllReady(all) {
		t.Fatal("nobody is ready yet")
	}
	if err := r.SetReady("h", true); err != nil {
		t.Fatal(err)
	}
	if ready, total := r.ReadyCount(all); ready != 1 || total != 2 {
		t.Fatalf("ready=%d total=%d", ready, total)
	}
	if err := r.SetReady("a", true); err != nil {
		t.Fatal(err)
	}
	if !r.AllReady(all) {
		t.Fatal("everyone is ready")
	}
	// Отключённые в расчёт не идут: обрыв связи одного не должен блокировать старт.
	onlyHost := func(id string) bool { return id == "h" }
	_ = r.SetReady("a", false)
	if !r.AllReady(onlyHost) {
		t.Fatal("disconnected member must not block auto start")
	}
	// Смена настроек снимает готовность со всех, смена бойца — только у себя.
	_ = r.SetReady("a", true)
	if err := r.SetConfig("h", Config{Mode: 3, Arena: 1, GameMode: "pvp", Visibility: VisibilityClosed}, 5); err != nil {
		t.Fatal(err)
	}
	if ready, _ := r.ReadyCount(all); ready != 0 {
		t.Fatalf("config change must reset ready, got %d", ready)
	}
	if !r.IsClosed() {
		t.Fatal("visibility must survive config change")
	}
	_ = r.SetReady("h", true)
	_ = r.SetReady("a", true)
	_ = r.SetRole("a", "Танк")
	if r.Member("a").Ready {
		t.Fatal("hero change must drop own ready")
	}
	if !r.Member("h").Ready {
		t.Fatal("hero change must not touch others")
	}
}

func TestSlotsDuringMatch(t *testing.T) {
	now := time.Now()
	r := New("1234", "h", "1.1.1.1", Config{Mode: 2, GameMode: "pvp"}, now)
	// Автоместо ведёт туда, где меньше людей (то есть больше ботов).
	if m := r.Member("h"); m.Team != "A" || m.Index != 0 {
		t.Fatalf("host slot: %+v", m)
	}
	_ = r.Join("a")
	if m := r.Member("a"); m.Team != "B" || m.Index != 0 {
		t.Fatalf("second player must go to the emptier team: %+v", m)
	}
	_ = r.Join("b")
	if m := r.Member("b"); m.Team != "A" || m.Index != 1 {
		t.Fatalf("third player: %+v", m)
	}

	// Во время матча слот комнаты — это боец матча: войти и перейти на свободное место можно.
	r.InMatch = true
	if err := r.Join("c"); err != nil {
		t.Fatalf("join during match: %v", err)
	}
	if m := r.Member("c"); m.Team != "B" || m.Index != 1 {
		t.Fatalf("joined during match: %+v", m)
	}
	if err := r.Join("d"); !errors.Is(err, ErrFull) {
		t.Fatalf("room must be full: %v", err)
	}
	if err := r.SetSlot("c", "A", 0); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("occupied slot must be rejected: %v", err)
	}
	// Место освободилось — его занимает другой участник.
	r.Leave("h", now)
	if err := r.SetSlot("c", "A", 0); err != nil {
		t.Fatalf("free slot during match: %v", err)
	}
	if m := r.Member("c"); m.Team != "A" || m.Index != 0 {
		t.Fatalf("slot change during match: %+v", m)
	}
	// Настройки комнаты в матче менять всё равно нельзя.
	if err := r.SetConfig(r.HostID, Config{Mode: 3, GameMode: "pvp"}, 5); !errors.Is(err, ErrInMatch) {
		t.Fatalf("config during match must be rejected: %v", err)
	}
}
