# Inspector

The `inspector` sub-package serves a real-time web dashboard that shows your pipeline graph, per-stage metrics, live throughput and latency, and a scrollable event log, all updated as items flow through the pipeline.

## Install

```
go get github.com/zenbaku/go-kitsune/inspector
```

## Minimal usage

```go
import (
    kitsune  "github.com/zenbaku/go-kitsune"
    "github.com/zenbaku/go-kitsune/inspector"
)

func main() {
    insp := inspector.New()
    defer insp.Close()
    fmt.Println("Inspector:", insp.URL()) // open this in a browser

    // Build your pipeline as usual — name your stages for best results
    records := kitsune.FromSlice(rawRecords)
    parsed  := kitsune.Map(records, parse,    kitsune.WithName("parse"))
    valid   := kitsune.Map(parsed,  validate, kitsune.WithName("validate"), kitsune.Concurrency(4))

    // Pass the inspector as a hook — no other changes needed
    err := valid.ForEach(store, kitsune.WithName("store")).Run(ctx, kitsune.WithHook(insp))
}
```

Open the printed URL in a browser. The dashboard updates in real time as the pipeline runs.

**Tip**: Name every stage with `kitsune.WithName`. Unnamed stages appear as their node kind ("Map", "Filter") with an ID suffix, which makes the graph harder to read.

## Dashboard layout

The dashboard has three panels:

### 1. KPI bar (top)

Two global counters update continuously:
- **Total Items (sink)**: total items that have reached the final stage(s)
- **Throughput**: current items/sec at the sink, averaged over a rolling window

### 2. Pipeline Graph (left)

A live SVG visualization of the DAG. Nodes are stages; directed edges show the data flow direction.

- **Node color** reflects status: pending (neutral), running (highlighted), done (dimmed)
- **Edge color** reflects whether the downstream stage has seen any errors or drops (yellow/orange indicator)
- **Click any node** to open the detail sidebar

Use the **⊡ Fit** button to re-center the graph after resizing the window.

### 3. Stage Metrics table (center)

One row per named stage, with live-updating columns:

| Column | Description |
|---|---|
| Stage | The stage name (`WithName`) or auto-generated label |
| Kind | Internal node kind (`Map`, `FlatMap`, `Batch`, `Filter`, etc.) |
| Status | `pending` / `running` / `done` |
| Items | Total items processed by this stage |
| Errors | Error count (red if > 0) |
| Drops | Items dropped by overflow strategies or Throttle (yellow if > 0) |
| Restarts | Stage restarts triggered by Supervise (yellow if > 0) |
| Throughput | Current items/sec |
| Avg Latency | Mean processing time per item |
| Buffer | Live fill bar showing current / capacity for the output channel |

### 4. Event Log (bottom)

A scrollable log of pipeline lifecycle events: stage starts, completions, errors, restarts, and item samples. Samples appear approximately every 10th item to give you a representative view of what's flowing without overwhelming the log.

## Detail sidebar

Click any graph node to open the sidebar for that stage. The sidebar shows:

- **Status**: current stage status with color indicator
- **Items**: total items processed
- **Throughput**: current items/sec
- **Avg Latency**: mean processing time
- **Errors / Drops / Restarts**: counts, highlighted in red/yellow when non-zero
- **Buffer**: fill bar (current / capacity)
- **Configuration**: concurrency, buffer size, overflow strategy (if non-default)
- **Recent Samples**: the last few item values seen at this stage (~every 10th item), formatted with `%v`

Close the sidebar with the `×` button or by clicking elsewhere.

## Stop and Restart controls

The dashboard has two control buttons:

- **■ Stop**: signals the pipeline to stop (sends to `CancelCh`)
- **↺ Restart**: signals the pipeline to restart (sends to `RestartCh`)

These buttons only send signals, they don't stop or restart the pipeline automatically. You wire them to your application's context in a run loop:

```go
insp := inspector.New()
defer insp.Close()
fmt.Println("Inspector:", insp.URL())

// Build pipeline once — Run can be called multiple times on the same Runner
// ...
sink := pipeline.ForEach(store, kitsune.WithName("store"))

for {
    ctx, cancel := context.WithCancel(context.Background())
    cancelCh  := insp.CancelCh()
    restartCh := insp.RestartCh()

    go func() {
        select {
        case <-cancelCh:  cancel() // Stop button pressed
        case <-restartCh: cancel() // Restart button pressed
        case <-ctx.Done():
        }
    }()

    sink.Run(ctx, kitsune.WithHook(insp))
    cancel()

    // Continue loop on Restart; break on Stop or natural exit
    select {
    case <-restartCh:
        continue
    default:
    }
    break
}
```

See [`examples/inspector`](../examples/inspector) for a complete runnable example with a branching topology (Partition, Broadcast, Merge, supervision, overflow) and the full stop/restart loop.

## Theme

Toggle between dark and light themes with the **☀** button in the top-right corner.

## Production considerations

- The inspector starts an HTTP server on an ephemeral port (`localhost:0`). It is not suitable for exposure to the internet without authentication.
- Each connected browser tab receives all pipeline events via Server-Sent Events. Multiple tabs are supported but each consumes memory proportional to the event log capacity (200 events by default).
- The inspector adds `OnItem` overhead for every item processed, comparable to a structured log write per item. For extremely high-throughput pipelines (>1 million items/sec), consider using a sampling hook (`SampleHook`) instead.
- The buffer fill gauge (`BufferHook`) polls channel fill levels every 250 ms. This overhead is fixed regardless of throughput.
