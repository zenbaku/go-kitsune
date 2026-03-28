# Go Pipeline Design — Comprehensive Iteration Summary

## 1) Project Intent

The goal is to design a Go pipeline system inspired in part by ideas like GenStage and Enumerator pipelines in Elixir, but **not constrained by them**.

The system should aim for:

- ergonomic authoring
- strong type safety via Go generics
- concurrency and batching as first-class runtime capabilities
- resilience and observability suitable for production workloads
- a progressive learning curve so users do not need to understand the runtime internals to get value

A central concern throughout the discussion was:

> If users need to learn too many concepts, they will eventually opt out of the system.

That concern became the main design filter.

---

## 2) Early Design Direction: Why a Simple Function Chain Is Not Enough

The initial idea was a pipeline composed of functions:

- each step receives an input
- each step emits an output
- the next step consumes that output

This was considered attractive because it is simple, but it immediately showed limitations.

### Limitations identified

#### A. Begin/end logic
Some logic needs to happen both:

- before processing starts
- after processing finishes

Examples:
- allocate resources before running
- flush buffers after processing
- compute or emit final aggregate output
- begin/commit/rollback a transaction
- build metadata early and use it later

A plain `func(In) Out` chain does not naturally express this.

---

#### B. Branching / forking
Real pipelines often do not stay linear.

A pipeline may need to:

- route different items to different flows
- duplicate items into multiple flows
- merge flows later
- create side outputs such as errors, metrics, audit streams

A plain linear chain cannot express this cleanly.

---

#### C. Long-lived context/state
A later use case made this very explicit:

- build a lookup map early
- use it much later to reconstruct relationships

That means the system needs something beyond per-item transformation.

---

## 3) Internal Model vs User Model

A very important realization:

> The best internal runtime model is not necessarily the best user-facing authoring model.

### Internal/runtime model
A good runtime model can absolutely be:

- a graph of stages
- lifecycle-aware
- concurrent
- observable
- with branching and merging

That model is useful for implementation.

### User-facing model
Users should not need to think in terms of:

- nodes
- ports
- edges
- envelopes
- mailboxes
- emit functions
- runtime scheduling

Those are implementation concepts, not authoring concepts.

### Core conclusion
The right architecture is likely:

- **simple authoring API**
- compiled into a **richer graph/runtime internally**

This was one of the strongest conclusions in the discussion.

---

## 4) Guiding Principle: Intent Over Infrastructure

The user should express:

- what data source exists
- what steps happen
- when data is grouped
- when one item becomes many
- where results go
- what state is needed
- what resilience policies apply

The framework should hide:

- routing machinery
- lifecycle plumbing
- concurrency orchestration
- execution graph details

### Good direction

```go
pipeline.From(fetch).
    Pipe(parse).
    Batch(500).
    FlatMap(expand).
    To(store)
```

### Bad direction

```go
AddNode(...)
Connect(...)
Emit(...)
ConfigurePorts(...)
```

The more the API looks like the second style, the more adoption is likely to suffer.

---

## 5) Progressive Complexity Model

A major design goal is progressive disclosure: a user should be able to grow into the system.

### Level 1 — linear pipeline
User learns only:

- `From`
- `Pipe`
- `To`

Example:

```go
p := pipeline.
    From(fetchUsers).
    Pipe(validateUser).
    Pipe(enrichUser).
    To(storeUser)
```

Mental model:

> data comes in, goes through steps, and exits

---

### Level 2 — branching / filtering
User learns:

- `Filter`
- `Branch` or `If/Else`

Example:

```go
p := pipeline.
    From(fetchUsers).
    Pipe(parseUser).
    Branch(
        pipeline.When(isValid).Then(storeUser),
        pipeline.Otherwise(rejectUser),
    )
```

Mental model:

> some data goes one way, some another

---

### Level 3 — shared run-scoped state
User learns:

- `State` or state injection helpers

Example need:

- build lookup map early
- use it later for correlation or reconstruction

Mental model:

> each run can carry shared state across steps

---

### Level 4 — advanced behavior
Only later do users need to learn:

- custom stage types
- explicit fan-out/fan-in
- tuning concurrency
- retries
- lifecycle hooks
- observability internals
- runtime policies

This progression is important for adoption.

---

## 6) User-Facing Vocabulary

The user-facing API should speak in terms users already understand:

- pipeline
- step
- flow
- source
- sink
- batch
- branch
- filter
- flat-map
- state
- retry
- timeout
- metrics
- naming

The API should avoid exposing low-level vocabulary too early:

- node
- edge
- port
- envelope
- emit
- mailbox
- runtime graph

---

## 7) Why Go Syntax Matters

A question came up around operator overloading and whether Go could support a lighter, pipeline-like syntax.

### Conclusion
Go does **not** support operator overloading.

So syntax like:

```go
a >> b >> c
```

or

```go
a | b | c
```

is not feasible as a custom user-defined pipeline operator.

### Implication
The ergonomic ceiling in Go is likely one of:

- method chaining
- builder-style functions
- composition helpers

Examples considered natural in Go:

```go
pipeline.From(source).Pipe(parse).Pipe(validate).To(store)
```

or

```go
pipeline.Build(source, parse, validate, store)
```

This means ergonomics must come from:

- helper naming
- small concepts
- strong defaults
- good adapters

not from clever syntax.

---

## 8) Internal Runtime Model That Still Makes Sense

Even though users should not see it, a strong internal runtime model still emerged.

### Internal stage model
A runtime stage should likely support lifecycle and message handling.

Possible internal shape:

```go
type Stage interface {
    Open(ctx context.Context, emit EmitFunc) error
    Handle(ctx context.Context, env Envelope, emit EmitFunc) error
    Close(ctx context.Context, emit EmitFunc) error
}
```

This supports:

- startup / teardown
- buffering and flush-on-close
- side outputs
- routing
- stateful stages

### Internal graph model
Internally, it is still valuable to model the pipeline as:

- stages/nodes
- edges
- hidden routers/merges/forks
- worker pools
- buffers/queues

This should be compiled from the user-facing DSL.

### Key split
- **Authoring API**: simple, intentful
- **Execution model**: graph-based, concurrent, observable

---

## 9) The State Problem: Why It Matters

A major turning point in the conversation was the realization that pipelines are not just per-item transformations.

### Example pressure from real workflows
A user may need to:

- fetch data early
- build lookup metadata or indices
- use that data much later
- correlate late results back to early items

This cannot be represented well if the only thing that exists is:

- one message
- transformed into another message

### Initial naive solution
Put metadata into the message:

```go
type Message struct {
    Value any
    Meta  map[string]any
}
```

This works for small metadata, but was considered weak because it can become:

- inefficient
- hard to type safely
- unclear in ownership
- awkward for large or shared structures

### Better solution
Introduce shared, scoped state.

But that created another concern:

> If state is a generic shared bag, it becomes invisible plumbing.

This led to a more refined direction.

---

## 10) How State Should Be Modeled

### Core principle
State should feel like **dependency injection**, not like a global mutable map.

### Anti-pattern
This is too plumbing-heavy:

```go
func(ctx context.Context, s pipeline.State, in T) (U, error)
```

if users are forced to do raw key/value access constantly.

Problems:
- feels like hidden global state
- can become stringly typed
- scope/lifecycle unclear
- too much low-level manipulation

### Better design: typed state keys
Use typed keys:

```go
var QueryOrigin = pipeline.Key[map[string]Item]("query-origin")
```

Benefits:
- type safety
- visible intent in code
- easier inspection/documentation

### Better design: explicit declaration
State should be declared at the pipeline level:

```go
pipeline.RunState(QueryOrigin)
```

This makes state:

- visible
- inspectable
- explicitly part of the pipeline contract

### Better design: scoped state
State likely needs scope.

At minimum, discussion identified:

- run-scoped state
- batch-scoped state

Examples:
- `query -> item` map may need run scope
- parent lookup for one batch may only need batch scope

Possible public vocabulary:
- `RunState(...)`
- `BatchState(...)`

### Better design: injection helpers
Rather than exposing raw `State`, inject the typed state into the step.

Examples:

```go
PipeWith(QueryOrigin, fn)
FlatMapWith(QueryOrigin, fn)
```

This lets the user write:

```go
FlatMapWith(QueryOrigin, func(ctx context.Context, origins map[string]Item, item Item) ([]Query, error) {
    ...
})
```

instead of:
- accessing generic bags
- pulling state manually
- performing string-key lookups

### Summary of state direction
State should be:
- typed
- declared
- scoped
- injected
- inspectable

It should **not** primarily be:
- raw
- implicit
- hidden
- string-keyed
- manual

---

## 11) Important Helpers and Ergonomics

A large part of the discussion focused on what helpers should exist so users can express intent without dealing with runtime machinery.

### Core helper categories that emerged

#### A. Source and sink helpers
Examples:

- `From(...)`
- `To(...)`
- `FromSlice(...)`
- `ToSlice(...)`

These lower the barrier to experimentation and testing.

---

#### B. Core transformation helper
A single main transform verb should exist, likely:

- `Pipe(...)`

This should cover the common case of 1 -> 1 transformation.

---

#### C. Filter helper
Users often need to continue only when a condition holds.

Examples:
- `Filter(pred)`
- possibly later `KeepIf` / `DropIf`

---

#### D. Branch helpers
Branching should be declarative.

Possible shapes:

```go
Branch(
    pipeline.When(cond).Then(flowA),
    pipeline.Otherwise(flowB),
)
```

or maybe:

```go
If(cond, flowA).Else(flowB)
```

The user should not need to think about ports or routing tables.

---

#### E. Fan-out / side-output helpers
The discussion highlighted helpers like:

- `FlatMap(...)`
- `Fork(...)`
- `Tee(...)` (future possibility)

`FlatMap` in particular emerged as essential because many real workflows require 1 -> N expansion.

---

#### F. Batch helpers
Batching was considered a first-class requirement, not a niche feature.

Example:
- `Batch(500)`

Use cases:
- performance
- API efficiency
- enrichment patterns
- fan-out control

---

#### G. State-aware helpers
State should be encoded via:
- typed keys
- declaration helpers
- injection helpers

Examples:
- `RunState(key)`
- `PipeWith(key, fn)`
- `FlatMapWith(key, fn)`

---

#### H. Error/resilience helpers
The user should not need to implement error machinery manually for common strategies.

Desired helpers:
- `Retry(n)`
- `OnError(handler)`
- possibly later `Timeout`, `Recover`, etc.

---

#### I. Naming helpers
Naming is important for both readability and inspection.

Example:
- `Named("parse-orders")`

This matters especially if a live inspector is added later.

---

#### J. Modifiers / decorators
A promising pattern is to allow stage modifiers such as:
- retry
- timeout
- naming
- metrics
- tracing

Possible feel:

```go
pipeline.Step(parse).Named("parse").Retry(3)
```

This keeps advanced behaviors attached to intentful steps, instead of forcing new top-level concepts.

---

## 12) FlatMap Emerged as Essential

One of the clearest insights from the conversation was:

> `FlatMap` is not optional for real-world usefulness.

Why?

Because many realistic workflows involve:
- one item becoming many queries
- one query expanding into many results
- paginated sub-requests
- branching from one input into multiple downstream units

The complex example later in the conversation depends on it heavily.

So `FlatMap` is likely core v1 material.

---

## 13) Real-World Use Case Discussed in Detail

A concrete use case was explored to pressure-test the API.

### Use case summary

1. Fetch all records from a paginated API
2. Batch the results
3. For each batch, compute unique parent IDs
4. Fetch parent details (also paginated)
5. Enrich original rows with parent data
6. For each enriched item, fan out multiple queries
7. Build a global map of `{query -> item}`
8. Run external API requests for each query
9. Each external response returns many results
10. Correlate each result back to the original item using the earlier map
11. Send each final correlated result to SQS

### Important realization
This workflow is not a single homogeneous stream. It contains several data shape changes.

The flowing units are really different at different points:
- records/items
- batches of records
- enriched items
- queries
- external results
- correlated final outputs

This validated the need for:
- batching
- flat-map
- state
- type-safe stage chaining across different input/output types

### User-facing modeling direction
The desirable user-facing expression looked roughly like:

```go
p := pipeline.
    RunState(QueryOrigin).
    From(FetchAllRecords()).
    Batch(500).
    Pipe(EnrichWithParents()).
    FlatMapWith(QueryOrigin, BuildQueries).
    FlatMap(RunExternalSearch()).
    PipeWith(QueryOrigin, CorrelateResults).
    To(SendToSQS())
```

### Why this is good
It reads close to business intent:

- fetch
- batch
- enrich
- explode into queries
- remember origins
- execute
- correlate
- sink

### Why it is important
This example became the main pressure test for the API.

A conclusion drawn from it:

If the API cannot express this kind of flow cleanly with a small number of concepts, then the user-facing model is still too low-level.

---

## 14) Live Inspection and Browsable Execution View

Another major direction was the possibility of supporting live inspection of pipeline execution, such as a browsable page showing what the pipeline is doing.

### Strong conclusion
This is likely not just a nice-to-have. It could become one of the strongest reasons to use the system.

### Why it matters
If users can see:
- what is running
- where things are blocked
- which step is failing
- how many items are flowing
- where branching occurred
- what state exists

then they do not need to mentally model as much hidden machinery.

### Design conclusion
Inspection should not be bolted on later. The runtime should emit execution events by design.

### Runtime should produce events like:
- run started / finished
- step started / completed / failed
- item entered / emitted
- branch taken
- item dropped
- state updated
- retry happened
- queue depth changed

### Two outputs of a run
A pipeline run effectively produces:
- business/output data
- execution trace / telemetry

That trace can power:
- logs
- metrics
- debugging
- live UI
- later replay/postmortem

### A practical first UI
The initial UI does not need full per-item tracing.

A useful first UI could show:
- pipeline run status
- each step and whether it is idle / active / blocked / failed
- counts per step
- error counts
- average duration
- queue depth
- branch counts
- recent errors

### Visibility levels suggested
Possible observation modes:
- Basic: counts/status/timings/errors
- Debug: sample payloads, state changes, branch decisions
- Trace: full per-item lineage

### Important UX rule
Even if the runtime internally thinks in ports/edges/nodes, the UI should speak the user’s language:
- step
- branch
- state
- run

not:
- edge 27
- out port 2
- node 14

---

## 15) Project Goals Explicitly Reviewed

A goals list was introduced:

- high-performance parallel processing with fine-grained concurrency control
- fan-out/fan-in patterns
- type-safe pipeline construction using Go generics
- robust error handling with custom strategies
- context-aware cancellation
- retries with configurable backoff
- batch processing
- metrics collection
- OpenTelemetry tracing
- circuit breaker
- rate limiting
- memory pooling
- thorough testing/examples
- chain stages with different input/output types

### Evaluation
The goals were considered strong, but mixed different categories:

- core user-facing value
- runtime capabilities
- operational features
- performance techniques
- quality goals

### Important conclusion
Not all of these should be equally central in v1.

They should be layered.

---

## 16) Refined Project Pillars

The goals were reorganized conceptually into a clearer structure.

### Pillar 1 — ergonomic typed composition
Users should be able to compose typed steps naturally, including steps with different input/output types.

### Pillar 2 — concurrency-aware execution
The runtime should support:
- configurable parallelism
- fan-out/fan-in
- batching
- likely bounded buffering/backpressure

### Pillar 3 — production resilience
The system should support:
- context cancellation
- retries
- configurable error strategies
- later rate limiting / circuit breakers

### Pillar 4 — built-in observability
The system should support:
- metrics
- tracing
- runtime event emission
- live inspection eventually

### Pillar 5 — performance-conscious runtime
The runtime should aim for:
- efficient batching
- low allocation overhead
- perhaps memory pooling where beneficial
- concurrency controls

### Missing goals that were identified
Two important goals were considered missing from the original list:

#### A. Ergonomic authoring / progressive disclosure
This should be a first-class goal, not an afterthought.

#### B. Inspectability
Built-in runtime visibility should also be an explicit goal.

---

## 17) Important Missing Runtime Concerns

Two runtime concerns were called out as needing explicit thought:

### A. Backpressure
Given the goals around concurrency and throughput, the system should define what happens when downstream is slower than upstream.

Questions include:
- bounded vs unbounded buffers
- push vs pull vs hybrid demand model
- how blocking or dropping behaves

Backpressure was considered a likely missing explicit goal.

### B. Ordering semantics
Given parallelism and fan-out/fan-in, the system should define:
- whether order is preserved by default
- whether it is not preserved by default
- whether it is configurable per step

This was called out as a design decision that affects trust and runtime complexity.

---

## 18) Public API Direction for v1

A concrete v1 direction emerged.

### Core composition
Must feel great and small:
- `From`
- `Pipe`
- `To`

### Shape-changing primitives
Needed early:
- `Batch(n)`
- `FlatMap(fn)`
- possibly `Filter(fn)`

### State
Should be ergonomic, not raw:
- `Key[T]`
- `RunState(key)`
- `PipeWith(key, fn)`
- `FlatMapWith(key, fn)`

### Concurrency
Keep minimal but useful:
- `Parallelism(n)`
- optional `Ordered()` later or if necessary

### Errors & resilience
Core helpers:
- `Retry(n)`
- `OnError(handler)`

### Context/cancellation
Should simply be built in

### Observability
At least:
- names
- basic metrics
- internal runtime event emission

### Important design discipline
Try to keep the number of top-level user-visible concepts small.
A rough target proposed was on the order of 10–15 major exported symbols for the initial public API.

---

## 19) Why Generics Matter

Type safety using Go generics was one of the strongest goals.

### Why it matters
The use case involves steps that change types across the pipeline:
- `Record`
- `[]Record`
- `EnrichedRecord`
- `Query`
- `ExternalResult`
- `FinalResult`

A generics-based design can make those transitions explicit and safe.

### Caution raised
Generics are valuable, but the API should avoid becoming a type-level puzzle. The generic surface should remain readable and practical.

So the direction is:
- strong type safety
- small generic vocabulary
- no needlessly elaborate type DSL

---

## 20) Strong User-Experience Rule

A key rule emerged:

> Most users should be able to build most pipelines by composing ordinary functions.

This implies:
- plain functions should be first-class authoring units
- users should not need to implement framework interfaces for common cases
- helpers should lift ordinary functions into the runtime model

This was considered a central adoption principle.

---

## 21) Recommended Identity of the Project

The project appears to be converging on this identity:

> A small, type-safe Go pipeline library with a serious runtime underneath.

Expanded form:

- simple authoring API
- strong typed composition
- batching and flat-map as first-class concepts
- state modeled as injected, typed dependency
- concurrency and resilience built into the runtime
- observability built in
- advanced power available progressively, not upfront

This framing was considered much more promising than:
- a visible graph orchestration engine
- or a fully explicit low-level stage graph API

---

## 22) Main Risks Identified

### Risk 1 — exposing too much runtime too early
If users need to understand:
- ports
- lifecycle
- emitters
- routing machinery
- envelopes
- node/edge graphs

too early, adoption will suffer.

### Risk 2 — state becoming invisible plumbing
If state is too implicit, users will not trust it.
If it is too explicit and raw, it becomes annoying.

Typed, declared, injected state was the proposed compromise.

### Risk 3 — capability overload
Trying to make every goal first-class in v1 could produce:
- too many public concepts
- a bloated API
- poor onboarding

### Risk 4 — inadequate pressure-testing
The design needs to be validated against multiple realistic workflows, not only theoretical elegance.

---

## 23) Practical Next-Step Direction That Emerged

The strongest practical direction proposed was:

1. lock a small v1 surface
2. pressure-test it against real examples
3. adjust the authoring API before adding more advanced runtime features

### Good test examples
- the complex paginated + enrichment + correlation + SQS example already discussed
- a simpler ETL flow
- perhaps a stream-ish or batch aggregation example

### Guiding evaluation question
Can realistic workflows be expressed with only:
- `From`
- `Pipe`
- `To`
- `Batch`
- `FlatMap`
- typed state
- basic error/concurrency controls

If yes, the public API is probably on the right track.

---

## 24) Condensed v1 Shape

The strongest v1 proposal that emerged is approximately:

### Public concepts
- Pipeline
- Step
- State (or state injection)
- Source
- Sink

### Core helpers
- `From`
- `Pipe`
- `To`
- `Batch`
- `FlatMap`
- maybe `Filter`

### State
- `Key[T]`
- `RunState`
- `PipeWith`
- `FlatMapWith`

### Modifiers / policies
- `Retry`
- `Parallelism`
- `Named`
- `OnError`

### Built-in runtime concerns
- context support
- cancellation
- metrics/event emission
- eventual tracing support
- later live inspector

---

## 25) One-Line Summary of the Direction

A concise summary of the direction reached:

> Build a type-safe Go pipeline library where users compose ordinary functions with a small set of intentful helpers, while a richer concurrent, observable runtime handles batching, fan-out, state, resilience, and execution details underneath.

---

## 26) Most Important Takeaways

1. **The system should optimize for ergonomics first, not visible power first.**
2. **The runtime can be graph-based, but the user should not need to author graphs.**
3. **`FlatMap` and `Batch` are likely foundational, not optional.**
4. **State is necessary, but it should be typed, declared, scoped, injected, and inspectable.**
5. **Observability is part of usability, not just operations.**
6. **Generics are a major advantage, but the type surface must stay disciplined.**
7. **Backpressure and ordering semantics need explicit design decisions.**
8. **v1 should stay small and be pressure-tested against real workflows before adding more power.**

---

## 27) Proposed Next Iteration Focus

The next useful design iteration would likely be one of these:

### Option A — concrete public API draft
Define actual Go signatures and core types for:
- `From`
- `Pipe`
- `FlatMap`
- `Batch`
- `Key[T]`
- `RunState`
- `PipeWith`
- `FlatMapWith`
- `Retry`
- `Parallelism`

### Option B — package layout draft
Sketch:
- public package API
- runtime/internal packages
- observability hooks
- state abstraction
- extension points

### Option C — end-to-end typed example
Implement the complex paginated-enrichment-correlation workflow using the proposed v1 API to see whether it really feels ergonomic.

All three would be good next steps.

