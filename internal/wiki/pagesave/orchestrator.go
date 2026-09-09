package pagesave

import (
	"context"
	"github.com/perber/wiki/internal/core/tree"
	"time"

	httpmetrics "github.com/perber/wiki/internal/http/metrics"
)

// PageSideEffect is implemented by any component that reacts to a page mutation.
// Apply is always called synchronously and errors are handled internally (best-effort).
type PageSideEffect interface {
	Apply(event PageSaveEvent)
}

type namedPageSideEffect interface {
	Name() string
}

// PageSaveOrchestrator fans out a PageSaveEvent to all registered side effects.
type PageSaveOrchestrator struct {
	metrics       *httpmetrics.HTTPMetrics
	sideEffects   []PageSideEffect
	transactional bool
	err           error
	deferred      []func()
}

// NewPageSaveOrchestrator creates an orchestrator with the given side effects.
func NewPageSaveOrchestrator(metrics *httpmetrics.HTTPMetrics, effects ...PageSideEffect) *PageSaveOrchestrator {
	return &PageSaveOrchestrator{metrics: metrics, sideEffects: effects}
}

// Run delivers the event to each side effect in registration order.
func (o *PageSaveOrchestrator) Run(event PageSaveEvent) {
	for _, se := range o.sideEffects {
		if o.transactional {
			if _, ok := se.(*RevisionSideEffect); !ok {
				o.deferred = append(o.deferred, func() { se.Apply(event) })
				continue
			}
		}
		started := time.Now()
		se.Apply(event)
		o.metrics.ObservePageSaveSideEffect(string(event.Operation), sideEffectName(se), started)
	}
}

// Transact commits Phase 3 data first. Existing SQLite-derived side effects run
// afterwards, unchanged. Revision failures propagate instead of being best-effort.
func (o *PageSaveOrchestrator) Transact(ctx context.Context, t *tree.TreeService, fn func(*tree.TreeService, *PageSaveOrchestrator) error) error {
	var bound *PageSaveOrchestrator
	err := t.Transact(ctx, func(local *tree.TreeService) error {
		bound = &PageSaveOrchestrator{metrics: o.metrics, transactional: true}
		for _, effect := range o.sideEffects {
			if r, ok := effect.(*RevisionSideEffect); ok {
				copy := *r
				copy.svc = r.svc.Bind(local)
				copy.onError = func(err error) {
					if bound.err == nil {
						bound.err = err
					}
				}
				bound.sideEffects = append(bound.sideEffects, &copy)
			} else {
				bound.sideEffects = append(bound.sideEffects, effect)
			}
		}
		if err := fn(local, bound); err != nil {
			return err
		}
		return bound.err
	})
	if err != nil {
		return err
	}
	for _, work := range bound.deferred {
		work()
	}
	return nil
}

// AfterCommit keeps attachment/favorites cleanup outside the page transaction.
func (o *PageSaveOrchestrator) AfterCommit(work func()) {
	if o.transactional {
		o.deferred = append(o.deferred, work)
	} else {
		work()
	}
}

func sideEffectName(effect PageSideEffect) string {
	if named, ok := effect.(namedPageSideEffect); ok {
		return named.Name()
	}
	return "unknown"
}
