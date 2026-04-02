// Package inspector provides a live web UI for observing Kitsune pipeline execution.
//
// Create an [Inspector], pass it to [kitsune.WithHook], and open the URL in a browser.
// The dashboard shows the pipeline graph, per-stage throughput and latency, and a
// scrollable event log — all updated in real time via Server-Sent Events.
//
// Usage:
//
//	insp := inspector.New()
//	defer insp.Close()
//	fmt.Println("Open:", insp.URL())
//	runner.Run(ctx, kitsune.WithHook(insp))
//	fmt.Scanln() // keep server alive for browsing
package inspector

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

//go:embed ui.html
var uiHTML []byte

const (
	tickInterval = 250 * time.Millisecond
	logCapacity  = 200
)

// Inspector collects pipeline events and serves a live dashboard at its HTTP address.
// It implements kitsune.Hook, kitsune.GraphHook, kitsune.OverflowHook,
// kitsune.SupervisionHook, kitsune.SampleHook, and kitsune.BufferHook —
// pass it to kitsune.WithHook.
//
// Use [CancelCh] to receive a stop signal from the UI's Stop button, and
// [RestartCh] to receive a restart signal. Wire them to a context cancel or
// a pipeline loop in user code. Use [PauseCh] and [ResumeCh] to wire the
// UI's Pause/Resume button to a [kitsune.Gate].
type Inspector struct {
	mu      sync.Mutex
	stages  map[string]*stageState
	order   []string // insertion order for deterministic table rendering
	graph   []engine.GraphNode
	logBuf  []logEntry
	clients map[chan sseMsg]struct{}

	bufferQuery func() []engine.BufferStatus // set by OnBuffers; nil until engine calls it

	cancelCh     chan struct{} // swapped on restart; protected by mu
	cancelClosed bool         // prevents double-close; protected by mu
	restartCh    chan struct{} // swapped on each restart; protected by mu
	pauseCh      chan struct{} // swapped on each pause; protected by mu
	resumeCh     chan struct{} // swapped on each resume; protected by mu

	ticker *time.Ticker
	done   chan struct{}

	url string
	srv *http.Server
}

// stageState tracks live metrics for a single named stage.
type stageState struct {
	items    atomic.Int64
	errors   atomic.Int64
	totalNs  atomic.Int64
	drops    atomic.Int64
	restarts atomic.Int64
	status   atomic.Int32 // 0=pending 1=running 2=done

	// written only by the ticker goroutine; safe to read without a lock
	// because rateBits uses atomic ops and prevItems/prevTick have a single writer
	prevItems int64
	prevTick  time.Time
	rateBits  atomic.Uint64 // math.Float64bits(items/sec)
}

func (s *stageState) setRate(r float64) { s.rateBits.Store(math.Float64bits(r)) }
func (s *stageState) getRate() float64  { return math.Float64frombits(s.rateBits.Load()) }

type logEntry struct {
	TS    int64  `json:"ts"`
	Type  string `json:"type"`
	Stage string `json:"stage,omitempty"`
	Msg   string `json:"msg,omitempty"`
}

type sseMsg struct {
	event string
	data  []byte
}

type stageSnapshot struct {
	Items      int64   `json:"items"`
	Errors     int64   `json:"errors"`
	Drops      int64   `json:"drops"`
	Restarts   int64   `json:"restarts"`
	AvgLatNs   int64   `json:"avgLatNs"`
	RatePerSec float64 `json:"ratePerSec"`
	Status     string  `json:"status"`
	BufferLen  int     `json:"bufferLen"`
	BufferCap  int     `json:"bufferCap"`
}

// New creates an Inspector listening on a random available port.
// It panics if the listener cannot be created.
func New() *Inspector {
	insp, err := NewAt("localhost:0")
	if err != nil {
		panic(fmt.Sprintf("inspector: %v", err))
	}
	return insp
}

// NewAt creates an Inspector listening on addr (e.g. ":8080").
func NewAt(addr string) (*Inspector, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	insp := &Inspector{
		stages:    make(map[string]*stageState),
		clients:   make(map[chan sseMsg]struct{}),
		ticker:    time.NewTicker(tickInterval),
		done:      make(chan struct{}),
		cancelCh:  make(chan struct{}),
		restartCh: make(chan struct{}),
		pauseCh:   make(chan struct{}),
		resumeCh:  make(chan struct{}),
		url:       "http://" + ln.Addr().String(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", insp.handleSSE)
	mux.HandleFunc("GET /state", insp.handleState)
	mux.HandleFunc("POST /control", insp.handleControl)
	mux.HandleFunc("GET /", insp.handleUI)
	insp.srv = &http.Server{Handler: mux}

	go func() { _ = insp.srv.Serve(ln) }()
	go insp.broadcastLoop()

	return insp, nil
}

// URL returns the inspector's HTTP address (e.g. "http://localhost:54321").
func (i *Inspector) URL() string { return i.url }

// CancelCh returns the current cancel channel, which is closed when the user
// clicks Stop in the UI. Call again after each restart to get a fresh channel:
//
//	cancelCh := insp.CancelCh()
//	go func() { <-cancelCh; cancel() }()
func (i *Inspector) CancelCh() <-chan struct{} {
	i.mu.Lock()
	ch := i.cancelCh
	i.mu.Unlock()
	return ch
}

// RestartCh returns the current restart channel. It is closed when the user
// clicks Restart in the UI. Call again after each restart to get the next one:
//
//	for {
//	    ctx, cancel := context.WithCancel(context.Background())
//	    go func() { <-insp.CancelCh(); cancel() }()
//	    runner.Run(ctx, kitsune.WithHook(insp))
//	    if _, open := <-insp.RestartCh(); !open { break } // wait for restart signal
//	}
func (i *Inspector) RestartCh() <-chan struct{} {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.restartCh
}

// PauseCh returns a channel that is closed when the user clicks Pause in the UI.
// Call again after each cycle to get a fresh channel. Typical usage:
//
//	gate := kitsune.NewGate()
//	go func() {
//	    for {
//	        select {
//	        case <-insp.PauseCh():  gate.Pause()
//	        case <-insp.ResumeCh(): gate.Resume()
//	        case <-ctx.Done():      return
//	        }
//	    }
//	}()
//	runner.Run(ctx, kitsune.WithHook(insp), kitsune.WithPauseGate(gate))
func (i *Inspector) PauseCh() <-chan struct{} {
	i.mu.Lock()
	ch := i.pauseCh
	i.mu.Unlock()
	return ch
}

// ResumeCh returns a channel that is closed when the user clicks Resume in the UI.
// Call again after each cycle to get a fresh channel. See [PauseCh] for usage.
func (i *Inspector) ResumeCh() <-chan struct{} {
	i.mu.Lock()
	ch := i.resumeCh
	i.mu.Unlock()
	return ch
}

// Close sends a final stats update, stops the ticker, and shuts down the HTTP server.
func (i *Inspector) Close() error {
	// Broadcast final state before closing so browsers see terminal stats.
	if snap := i.buildSnapshot(); snap != nil {
		if data, err := json.Marshal(snap); err == nil {
			i.broadcast(sseMsg{"stats", data})
		}
	}
	i.ticker.Stop()
	close(i.done)
	return i.srv.Shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// Hook interfaces
// ---------------------------------------------------------------------------

// OnGraph implements engine.GraphHook.
func (i *Inspector) OnGraph(nodes []engine.GraphNode) {
	i.mu.Lock()
	i.graph = nodes
	i.mu.Unlock()

	data, _ := json.Marshal(nodes)
	i.broadcast(sseMsg{"graph", data})
}

// OnBuffers implements engine.BufferHook.
func (i *Inspector) OnBuffers(query func() []engine.BufferStatus) {
	i.mu.Lock()
	i.bufferQuery = query
	i.mu.Unlock()
}

// OnStageStart implements engine.Hook.
func (i *Inspector) OnStageStart(_ context.Context, stage string) {
	i.mu.Lock()
	if _, ok := i.stages[stage]; !ok {
		i.stages[stage] = &stageState{}
		i.order = append(i.order, stage)
	}
	s := i.stages[stage]
	i.mu.Unlock()
	s.status.Store(1)

	i.emitLog(logEntry{TS: nowMs(), Type: "start", Stage: stage})
}

// OnItem implements engine.Hook.
func (i *Inspector) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	i.mu.Lock()
	s := i.stages[stage]
	i.mu.Unlock()
	if s == nil {
		return
	}
	s.totalNs.Add(dur.Nanoseconds())
	if err != nil {
		s.errors.Add(1)
	} else {
		s.items.Add(1)
	}
}

// OnStageDone implements engine.Hook.
func (i *Inspector) OnStageDone(_ context.Context, stage string, processed int64, _ int64) {
	i.mu.Lock()
	s := i.stages[stage]
	i.mu.Unlock()
	if s == nil {
		return
	}
	s.status.Store(2)
	i.emitLog(logEntry{TS: nowMs(), Type: "done", Stage: stage})
}

// OnDrop implements engine.OverflowHook.
func (i *Inspector) OnDrop(_ context.Context, stage string, _ any) {
	i.mu.Lock()
	s := i.stages[stage]
	i.mu.Unlock()
	if s == nil {
		return
	}
	s.drops.Add(1)
	i.emitLog(logEntry{TS: nowMs(), Type: "drop", Stage: stage})
}

// OnStageRestart implements engine.SupervisionHook.
func (i *Inspector) OnStageRestart(_ context.Context, stage string, attempt int, cause error) {
	i.mu.Lock()
	s := i.stages[stage]
	i.mu.Unlock()
	if s == nil {
		return
	}
	s.restarts.Add(1)
	s.status.Store(1)

	msg := fmt.Sprintf("attempt %d", attempt)
	if cause != nil {
		msg += ": " + cause.Error()
	}
	i.emitLog(logEntry{TS: nowMs(), Type: "restart", Stage: stage, Msg: msg})
}

// OnItemSample implements engine.SampleHook.
func (i *Inspector) OnItemSample(_ context.Context, stage string, item any) {
	val := fmt.Sprintf("%v", item)
	if len(val) > 80 {
		val = val[:79] + "…"
	}
	type sampleMsg struct {
		Stage string `json:"stage"`
		Value string `json:"value"`
		TS    int64  `json:"ts"`
	}
	if data, err := json.Marshal(sampleMsg{Stage: stage, Value: val, TS: nowMs()}); err == nil {
		i.broadcast(sseMsg{"sample", data})
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (i *Inspector) handleControl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "cancel":
		i.mu.Lock()
		if !i.cancelClosed {
			i.cancelClosed = true
			close(i.cancelCh)
		}
		i.mu.Unlock()
	case "restart":
		// Swap in fresh channels so Stop works again after restart.
		i.mu.Lock()
		oldRestart := i.restartCh
		i.restartCh = make(chan struct{})
		i.cancelCh = make(chan struct{})
		i.cancelClosed = false
		i.mu.Unlock()
		close(oldRestart)
	case "pause":
		i.mu.Lock()
		old := i.pauseCh
		i.pauseCh = make(chan struct{})
		i.mu.Unlock()
		close(old)
		if data, err := json.Marshal(map[string]bool{"paused": true}); err == nil {
			i.broadcast(sseMsg{"paused", data})
		}
	case "resume":
		i.mu.Lock()
		old := i.resumeCh
		i.resumeCh = make(chan struct{})
		i.mu.Unlock()
		close(old)
		if data, err := json.Marshal(map[string]bool{"paused": false}); err == nil {
			i.broadcast(sseMsg{"paused", data})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (i *Inspector) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(uiHTML)
}

func (i *Inspector) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe before replaying so we don't miss events during the replay window.
	ch := i.subscribe()
	defer i.unsubscribe(ch)

	// Replay current graph and log buffer.
	i.mu.Lock()
	graph := i.graph
	logSnapshot := make([]logEntry, len(i.logBuf))
	copy(logSnapshot, i.logBuf)
	i.mu.Unlock()

	if graph != nil {
		if data, err := json.Marshal(graph); err == nil {
			writeSSE(w, "graph", data)
		}
	}
	for _, entry := range logSnapshot {
		if data, err := json.Marshal(entry); err == nil {
			writeSSE(w, "log", data)
		}
	}
	if snap := i.buildSnapshot(); snap != nil {
		if data, err := json.Marshal(snap); err == nil {
			writeSSE(w, "stats", data)
		}
	}
	flusher.Flush()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, msg.event, msg.data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (i *Inspector) handleState(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	i.mu.Lock()
	graph := i.graph
	i.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"graph": graph,
		"stats": i.buildSnapshot(),
	})
}

// ---------------------------------------------------------------------------
// Broadcast loop — computes per-stage rates and pushes stats every tick
// ---------------------------------------------------------------------------

func (i *Inspector) broadcastLoop() {
	for {
		select {
		case <-i.done:
			return
		case now := <-i.ticker.C:
			i.mu.Lock()
			order := make([]string, len(i.order))
			copy(order, i.order)
			stages := make(map[string]*stageState, len(i.stages))
			for k, v := range i.stages {
				stages[k] = v
			}
			query := i.bufferQuery
			i.mu.Unlock()

			if len(order) == 0 {
				continue
			}

			// Query buffer fill levels (nil before engine calls OnBuffers).
			buffers := make(map[string]engine.BufferStatus)
			if query != nil {
				for _, bs := range query() {
					buffers[bs.Stage] = bs
				}
			}

			snap := make(map[string]stageSnapshot, len(order))
			for _, name := range order {
				s := stages[name]
				items := s.items.Load()
				var rate float64
				if !s.prevTick.IsZero() {
					if elapsed := now.Sub(s.prevTick).Seconds(); elapsed > 0 {
						rate = float64(items-s.prevItems) / elapsed
					}
				}
				s.prevItems = items
				s.prevTick = now
				s.setRate(rate)
				snap[name] = snapStageWithBuffer(s, buffers[name])
			}

			if data, err := json.Marshal(snap); err == nil {
				i.broadcast(sseMsg{"stats", data})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (i *Inspector) subscribe() chan sseMsg {
	ch := make(chan sseMsg, 64)
	i.mu.Lock()
	i.clients[ch] = struct{}{}
	i.mu.Unlock()
	return ch
}

func (i *Inspector) unsubscribe(ch chan sseMsg) {
	i.mu.Lock()
	delete(i.clients, ch)
	i.mu.Unlock()
}

func (i *Inspector) broadcast(msg sseMsg) {
	i.mu.Lock()
	for ch := range i.clients {
		select {
		case ch <- msg:
		default: // drop for slow clients; they'll catch up on the next tick
		}
	}
	i.mu.Unlock()
}

func (i *Inspector) emitLog(entry logEntry) {
	i.mu.Lock()
	if len(i.logBuf) >= logCapacity {
		i.logBuf = i.logBuf[1:]
	}
	i.logBuf = append(i.logBuf, entry)
	i.mu.Unlock()

	if data, err := json.Marshal(entry); err == nil {
		i.broadcast(sseMsg{"log", data})
	}
}

func (i *Inspector) buildSnapshot() map[string]stageSnapshot {
	i.mu.Lock()
	order := make([]string, len(i.order))
	copy(order, i.order)
	stages := make(map[string]*stageState, len(i.stages))
	for k, v := range i.stages {
		stages[k] = v
	}
	query := i.bufferQuery
	i.mu.Unlock()

	if len(order) == 0 {
		return nil
	}

	buffers := make(map[string]engine.BufferStatus)
	if query != nil {
		for _, bs := range query() {
			buffers[bs.Stage] = bs
		}
	}

	snap := make(map[string]stageSnapshot, len(order))
	for _, name := range order {
		snap[name] = snapStageWithBuffer(stages[name], buffers[name])
	}
	return snap
}

func snapStageWithBuffer(s *stageState, buf engine.BufferStatus) stageSnapshot {
	items := s.items.Load()
	errs := s.errors.Load()
	ns := s.totalNs.Load()
	total := items + errs
	var avgNs int64
	if total > 0 {
		avgNs = ns / total
	}
	status := "pending"
	switch s.status.Load() {
	case 1:
		status = "running"
	case 2:
		status = "done"
	}
	return stageSnapshot{
		Items:      items,
		Errors:     errs,
		Drops:      s.drops.Load(),
		Restarts:   s.restarts.Load(),
		AvgLatNs:   avgNs,
		RatePerSec: s.getRate(),
		Status:     status,
		BufferLen:  buf.Length,
		BufferCap:  buf.Capacity,
	}
}

func writeSSE(w http.ResponseWriter, event string, data []byte) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func nowMs() int64 { return time.Now().UnixMilli() }
