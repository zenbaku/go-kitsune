package internal

// ReceiveBatchSize is the maximum number of items drained from an input channel
// per fast-path iteration: one blocking receive followed by up to
// (ReceiveBatchSize - 1) non-blocking receives. Using a fixed-size array keeps
// the buffer stack-allocated for small element types.
const ReceiveBatchSize = 16

// IsNoopHook reports whether h is the no-op hook sentinel.
// Fast-path stage runners are only activated when the hook is NoopHook —
// any real hook must receive OnStageStart / OnItem / OnStageDone calls, which
// the fast paths skip entirely.
func IsNoopHook(h Hook) bool {
	_, ok := h.(NoopHook)
	return ok
}

// IsDefaultHandler reports whether h is the default halt-on-error handler.
// A nil handler is treated as DefaultHandler — it means "not explicitly set",
// which the engine resolves to DefaultHandler at run time.
// Fast paths assume DefaultHandler semantics: return the error immediately,
// no retry, no skip, no return-value substitution.
func IsDefaultHandler(h ErrorHandler) bool {
	if h == nil {
		return true
	}
	_, ok := h.(DefaultHandler)
	return ok
}
