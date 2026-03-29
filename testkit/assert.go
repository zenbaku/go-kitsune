package testkit

import (
	"testing"
)

// AssertNoErrors fails the test if the hook recorded any item errors.
func AssertNoErrors(t testing.TB, hook *RecordingHook) {
	t.Helper()
	if errs := hook.Errors(); len(errs) != 0 {
		t.Errorf("testkit.AssertNoErrors: got %d error(s), want 0", len(errs))
	}
}

// AssertErrorCount fails the test if the total number of item errors does not
// equal want.
func AssertErrorCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Errors())
	if got != want {
		t.Errorf("testkit.AssertErrorCount: got %d, want %d", got, want)
	}
}

// AssertDropCount fails the test if the total number of overflow drops does not
// equal want.
func AssertDropCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Drops())
	if got != want {
		t.Errorf("testkit.AssertDropCount: got %d, want %d", got, want)
	}
}

// AssertRestartCount fails the test if the total number of supervision restarts
// does not equal want.
func AssertRestartCount(t testing.TB, hook *RecordingHook, want int) {
	t.Helper()
	got := len(hook.Restarts())
	if got != want {
		t.Errorf("testkit.AssertRestartCount: got %d, want %d", got, want)
	}
}

// AssertStageProcessed fails the test if the named stage's processed count
// (from its OnStageDone event) does not equal want.
// It also fails if no done event is found for the stage.
func AssertStageProcessed(t testing.TB, hook *RecordingHook, stage string, want int64) {
	t.Helper()
	for _, d := range hook.Dones() {
		if d.Stage == stage {
			if d.Processed != want {
				t.Errorf("testkit.AssertStageProcessed(%q): got %d, want %d", stage, d.Processed, want)
			}
			return
		}
	}
	t.Errorf("testkit.AssertStageProcessed: no done event for stage %q", stage)
}

// AssertStageErrors fails the test if the named stage's error count
// (from its OnStageDone event) does not equal want.
// It also fails if no done event is found for the stage.
func AssertStageErrors(t testing.TB, hook *RecordingHook, stage string, want int64) {
	t.Helper()
	for _, d := range hook.Dones() {
		if d.Stage == stage {
			if d.Errors != want {
				t.Errorf("testkit.AssertStageErrors(%q): got %d, want %d", stage, d.Errors, want)
			}
			return
		}
	}
	t.Errorf("testkit.AssertStageErrors: no done event for stage %q", stage)
}
