# Design: RunHandle correctness fixes

Date: 2026-04-11
Status: Approved

## Problem

Two correctness bugs in `kitsune.go`:

### 1. `runWithDrain` timer goroutine leak

`runWithDrain` uses `time.After(drainTimeout)` inside a goroutine to enforce the drain
timeout. When the drain completes before the timeout fires, `drainCtx.Done()` exits
the select — but the allocated timer goroutine keeps running until the duration elapses.
Every `Run` call with `WithDrainTimeout` leaks a goroutine temporarily.

### 2. `RunHandle.errCh` single-consumer footgun

`Wait()` and `Err()` both read from the same buffered-1 `chan error`. A caller that uses
both (or two goroutines that both call `Wait()`) will have one block forever after the
other consumes the single value.

## Design

### Fix 1: timer goroutine leak

Replace `time.After(drainTimeout)` with `time.NewTimer(drainTimeout)` and call
`timer.Stop()` in the branch where `drainCtx.Done()` fires first. The `timer.C` branch
is unchanged.

### Fix 2: RunHandle redesign

**Remove** the `errCh <-chan error` field from `RunHandle`.

**Add** a plain `err error` field. The goroutine in `RunAsync` writes `h.err = err`
*before* calling `close(h.done)`. The channel close provides a happens-before guarantee;
reading `h.err` after `<-h.done` returns is safe without a mutex or atomic.

**`Wait() error`** — unchanged signature, now implemented as:
```go
func (h *RunHandle) Wait() error {
    <-h.done
    return h.err
}
```
Any number of concurrent callers are safe: they all block on `h.done` and all read the
same `h.err` after it closes.

**`Done() <-chan struct{}`** — unchanged.

**`Err() error`** — signature changes from `<-chan error` to `error`. Non-blocking:
returns `h.err` if `done` is already closed, `nil` otherwise. Callers migrate the select
pattern from:
```go
select { case err := <-handle.Err(): ... }
```
to:
```go
select { case <-handle.Done(): err := handle.Err(); ... }
```
This is the same pattern as `context.Context` and is safe for any number of concurrent
callers.

**Pause / Resume / Paused** — unchanged.

## Testing

- Add a regression test: two concurrent `Wait()` callers must both receive the correct
  result (not one blocking forever).
- Add a test: calling `Err()` before the pipeline finishes returns nil; after Done()
  closes it returns the correct error.
- Existing drain tests cover the timer leak indirectly; run `task test:race` to confirm
  no races.

## Files changed

- `kitsune.go`: both fixes
- `runasync_test.go` or `drain_test.go`: new regression tests
