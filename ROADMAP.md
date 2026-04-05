# go-kitsune v1 Roadmap

v1 is the stable, production engine. Most active development is now happening in v2. This file tracks work that belongs on the v1 side.

---

## Hooks module migration

The shared `github.com/zenbaku/go-kitsune/hooks` module was introduced in v2 and backported to v1 (`engine/compile.go` now aliases all hook interfaces and types from it). The `replace` directive in v1's `go.mod` points at `./hooks`.

The goal is that any tail implementing `hooks.Hook` (or any extension interface) works with both engines without code changes — swapping the engine is a one-line `go.mod` change for the user.

### Tail migration

Three tails implement hook interfaces and reference hook-owned types (`GraphNode`, `BufferStatus`) — these can be made fully engine-agnostic:

- [ ] **`tails/kotel`** — implements `GraphHook` (uses `GraphNode`) and `BufferHook` (uses `BufferStatus`); replace `kitsune.GraphNode` / `kitsune.BufferStatus` with `hooks.GraphNode` / `hooks.BufferStatus`; add `require github.com/zenbaku/go-kitsune/hooks` to its `go.mod` and remove the engine `replace` if no engine types remain
- [ ] **`tails/kdatadog`** — implements `Hook`, `OverflowHook`, `SupervisionHook`; replace `kitsune.Hook` / `kitsune.OverflowHook` / `kitsune.SupervisionHook` with their `hooks` equivalents
- [ ] **`tails/kprometheus`** — same as `kdatadog`

The remaining 22 tails (`kazeh`, `kazsb`, `kclickhouse`, `kdynamo`, `kes`, `kfile`, `kgcs`, `kgrpc`, `khttp`, `kkafka`, `kkinesis`, `kmongo`, `kmqtt`, `knats`, `kpostgres`, `kpubsub`, `kpulsar`, `kredis`, `ks3`, `ksqlite`, `ksqs`, `kwebsocket`) use pipeline operators (`FromSlice`, `Map`, `Pipeline`, etc.) and must depend on the engine regardless. Full engine-independence is not achievable here: v1 operators work on `chan any` while v2 operators are generic (`Pipeline[T]`), so there is no shared interface both can satisfy.

The practical path to dual-engine support for these tails is **shared logic + thin wrappers**:

```
tails/kkafka/
  internal/   ← connection, consumer loop, config — no engine types
  v1/         ← wraps internal, builds a v1 pipeline
  v2/         ← wraps internal, builds a v2 pipeline
```

The engine-specific wrapper is usually tiny (a `Channel[T]` source and a `ForEach` sink around the shared logic). This mirrors the `database/sql` driver pattern in the Go ecosystem. No migration work is planned at this time.

### Migration pattern for hook-only tails

```go
// Before
import kitsune "github.com/zenbaku/go-kitsune"

var _ kitsune.Hook = (*MyTail)(nil)

// After
import "github.com/zenbaku/go-kitsune/hooks"

var _ hooks.Hook = (*MyTail)(nil)
```

`go.mod` before:
```
require github.com/zenbaku/go-kitsune v0.0.0-...
replace github.com/zenbaku/go-kitsune => ../..
```

`go.mod` after (if tail no longer needs the engine at all):
```
require github.com/zenbaku/go-kitsune/hooks v0.1.0
```
