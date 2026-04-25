package testkit_test

import (
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune/testkit"
)

func TestTestClock_Now(t *testing.T) {
	clk := testkit.NewTestClock()
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := clk.Now(); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
}

func TestTestClock_Advance(t *testing.T) {
	clk := testkit.NewTestClock()
	start := clk.Now()
	clk.Advance(5 * time.Second)
	got := clk.Now()
	want := start.Add(5 * time.Second)
	if !got.Equal(want) {
		t.Errorf("After Advance(5s), Now() = %v, want %v", got, want)
	}
}

func TestTestClock_Timer(t *testing.T) {
	clk := testkit.NewTestClock()
	timer := clk.NewTimer(3 * time.Second)

	// Should not fire before advancing.
	select {
	case <-timer.C():
		t.Fatal("timer fired before Advance")
	default:
	}

	clk.Advance(3 * time.Second)

	select {
	case fired := <-timer.C():
		want := time.Date(2020, 1, 1, 0, 0, 3, 0, time.UTC)
		if !fired.Equal(want) {
			t.Errorf("timer fired with time %v, want %v", fired, want)
		}
	default:
		t.Fatal("timer did not fire after Advance past deadline")
	}
}

func TestTestClock_TimerNotFiredBeforeDeadline(t *testing.T) {
	clk := testkit.NewTestClock()
	timer := clk.NewTimer(5 * time.Second)

	clk.Advance(3 * time.Second) // not past deadline

	select {
	case <-timer.C():
		t.Fatal("timer fired before reaching its deadline")
	default:
		// correct: timer has not fired
	}
}

func TestTestClock_TimerStop(t *testing.T) {
	clk := testkit.NewTestClock()
	timer := clk.NewTimer(1 * time.Second)

	wasActive := timer.Stop()
	if !wasActive {
		t.Error("Stop() returned false for an active timer; want true")
	}

	clk.Advance(2 * time.Second)

	select {
	case <-timer.C():
		t.Fatal("stopped timer fired after Advance")
	default:
		// correct: stopped timer did not fire
	}
}

func TestTestClock_TimerReset(t *testing.T) {
	clk := testkit.NewTestClock()
	timer := clk.NewTimer(5 * time.Second)

	// Advance past the original deadline; timer fires.
	clk.Advance(5 * time.Second)

	select {
	case <-timer.C():
		// consumed
	default:
		t.Fatal("timer did not fire at original deadline")
	}

	// Reset with a new deadline of 3 more seconds.
	timer.Reset(3 * time.Second)

	// Advance only 2s; should not fire.
	clk.Advance(2 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired before new deadline after Reset")
	default:
	}

	// Advance 1 more second; should fire now.
	clk.Advance(1 * time.Second)
	select {
	case <-timer.C():
		// correct
	default:
		t.Fatal("timer did not fire after reaching reset deadline")
	}
}

func TestTestClock_Ticker(t *testing.T) {
	clk := testkit.NewTestClock()
	ticker := clk.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Should not have fired yet.
	select {
	case <-ticker.C():
		t.Fatal("ticker fired before any Advance")
	default:
	}

	// Advance 2s; one tick.
	clk.Advance(2 * time.Second)
	select {
	case <-ticker.C():
		// correct
	default:
		t.Fatal("ticker did not fire after Advance(2s)")
	}

	// Advance another 2s; one more tick.
	clk.Advance(2 * time.Second)
	select {
	case <-ticker.C():
		// correct
	default:
		t.Fatal("ticker did not fire on second Advance(2s)")
	}

	// Advance another 2s; one more tick.
	clk.Advance(2 * time.Second)
	select {
	case <-ticker.C():
		// correct
	default:
		t.Fatal("ticker did not fire on third Advance(2s)")
	}
}

func TestTestClock_TickerStop(t *testing.T) {
	clk := testkit.NewTestClock()
	ticker := clk.NewTicker(1 * time.Second)

	ticker.Stop()

	clk.Advance(5 * time.Second)

	select {
	case <-ticker.C():
		t.Fatal("stopped ticker fired after Advance")
	default:
		// correct
	}
}
