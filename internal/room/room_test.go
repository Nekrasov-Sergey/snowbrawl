package room

import (
	"errors"
	"testing"
	"time"
)

func TestJoinLeaveAndHostTransfer(t *testing.T) {
	now := time.Now()
	r := New("SNB-TEST", "h", "1.1.1.1", 2, 0, now)
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
	r := New("SNB-TEST", "h", "1.1.1.1", 3, 0, now)
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
	if err := r.SetConfig("a", 1, 0, 3); !errors.Is(err, ErrNotHost) {
		t.Fatalf("expected ErrNotHost, got %v", err)
	}
	if err := r.SetConfig("h", 1, 5, 3); !errors.Is(err, ErrBadArena) {
		t.Fatalf("expected ErrBadArena, got %v", err)
	}
	// Уменьшаем режим до 1×1: a сидел в A2, должен быть переставлен в свободный слот B0.
	if err := r.SetConfig("h", 1, 1, 3); err != nil {
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

func TestGenerateCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := GenerateCode()
		if len(c) != 8 || c[:4] != "SNB-" {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("codes are not random enough: %d unique of 200", len(seen))
	}
}
