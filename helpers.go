package kitsune

import "context"

// orDefault returns s if non-empty, otherwise def.
func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// itemContext returns a context for a single item, applying per-item timeout
// if configured. The returned cancel must always be called.
func itemContext(ctx context.Context, cfg stageConfig) (context.Context, context.CancelFunc) {
	if cfg.timeout > 0 {
		return context.WithTimeout(ctx, cfg.timeout)
	}
	return ctx, func() {}
}

// reportErr delivers err to errCh without blocking (first error wins).
func reportErr(errCh chan error, err error) {
	select {
	case errCh <- err:
	default:
	}
}
