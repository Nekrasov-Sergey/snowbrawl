package matchmaking

import (
	"testing"
	"time"
)

func TestQueueFillsAndTimesOut(t *testing.T) {
	qs := New(15 * time.Second)
	t0 := time.Now()
	q := qs.Join(1, "a", "Танк", t0)
	if q.Ready(t0, qs.Wait) {
		t.Fatal("one player must not be ready immediately")
	}
	if left := q.WaitLeft(t0.Add(5*time.Second), qs.Wait); left != 10*time.Second {
		t.Fatalf("waitLeft = %v", left)
	}
	if !q.Ready(t0.Add(15*time.Second), qs.Wait) {
		t.Fatal("must be ready after wait expires")
	}
	qs.Join(1, "b", "Танк", t0.Add(time.Second))
	if !q.Full() || !q.Ready(t0.Add(2*time.Second), qs.Wait) {
		t.Fatal("two players fill a 1×1 queue")
	}
	qs.Join(1, "c", "Танк", t0.Add(2*time.Second))
	taken := q.Take(t0.Add(2 * time.Second))
	if len(taken) != 2 || taken[0].PlayerID != "a" || taken[1].PlayerID != "b" {
		t.Fatalf("take = %+v", taken)
	}
	if len(q.Entries) != 1 || q.Entries[0].PlayerID != "c" || !q.OpenedAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("remaining queue wrong: %+v opened %v", q.Entries, q.OpenedAt)
	}
	qs.Leave(1, "c")
	if len(q.Entries) != 0 {
		t.Fatal("leave must remove the player")
	}
	qs.Join(1, "a", "Танк", t0)
	qs.Join(1, "a", "Танк", t0)
	if len(q.Entries) != 1 {
		t.Fatal("double join must be idempotent")
	}
}
