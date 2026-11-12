package schedule

import (
	"testing"
	"time"
)

func TestNextBasic(t *testing.T) {
	after := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC) // 09:00 Shanghai
	n, err := Next("30 9 * * *", "Asia/Shanghai", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 19, 1, 30, 0, 0, time.UTC); !n.Equal(want) {
		t.Fatalf("got %v want %v", n, want)
	}
}

func TestDSTFallBackFiresOnce(t *testing.T) {
	// America/New_York 2026-11-01: 01:30 happens twice.
	loc, _ := time.LoadLocation("America/New_York")
	after := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	first, err := Next("30 1 * * *", "America/New_York", after)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Next("30 1 * * *", "America/New_York", first)
	if second.Sub(first) < 20*time.Hour {
		t.Fatalf("fired twice: %v then %v", first, second)
	}
}

func TestDSTSpringForwardShifts(t *testing.T) {
	// 2026-03-08: 02:30 does not exist in New York.
	loc, _ := time.LoadLocation("America/New_York")
	after := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	n, err := Next("30 2 * * *", "America/New_York", after)
	if err != nil {
		t.Fatal(err)
	}
	if n.In(loc).Day() != 8 {
		t.Fatalf("skipped the day instead of shifting: %v", n.In(loc))
	}
}

func TestCheck(t *testing.T) {
	p := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if Check(p, p.Add(-time.Second)) != NotDue || Check(p, p.Add(time.Minute)) != Due || Check(p, p.Add(time.Hour)) != Missed {
		t.Fatal("check")
	}
}
