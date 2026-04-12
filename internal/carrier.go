package internal

import "context"

// ContextCarrier is implemented by item types that carry a context.Context
// with an attached trace span (or any other per-item context values).
// When an item implements ContextCarrier, the engine threads its context
// through stage functions so that per-item child spans can be created with
// a normal tracer.Start(ctx, ...) call — no stage signature changes needed.
//
// Cancellation still comes from the stage context; the item's context only
// contributes its values (e.g. the trace span). This keeps pipeline shutdown
// and per-item timeout semantics intact.
type ContextCarrier interface {
	Context() context.Context
}

// carrierCtx is a context that inherits cancellation/deadline from stage but
// values (e.g. trace span) from item. Value looks up item context first, then
// falls back to stage context, so item-level values shadow stage-level ones.
type carrierCtx struct {
	context.Context                 // cancellation and deadline from the stage
	values          context.Context // per-item values (trace span, etc.)
}

func (c carrierCtx) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

// ItemCtx returns a context for processing item. If item implements
// ContextCarrier, it returns a merged context: cancellation from stageCtx,
// values from item.Context(). Otherwise stageCtx is returned unchanged,
// so there is zero overhead for items that don't carry a context.
func ItemCtx[T any](stageCtx context.Context, item T) context.Context {
	if cc, ok := any(item).(ContextCarrier); ok {
		return carrierCtx{Context: stageCtx, values: cc.Context()}
	}
	return stageCtx
}

// ItemCtxWithMapper returns a merged context using mapper to extract per-item
// values from item. The mapper takes precedence over ContextCarrier: if item
// also implements ContextCarrier, only the mapper is used. If mapper returns
// nil, stageCtx is returned unchanged.
func ItemCtxWithMapper[T any](stageCtx context.Context, item T, mapper func(T) context.Context) context.Context {
	if itemValues := mapper(item); itemValues != nil {
		return carrierCtx{Context: stageCtx, values: itemValues}
	}
	return stageCtx
}
