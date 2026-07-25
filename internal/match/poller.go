// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"log/slog"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
)

// ChangeSignal tells a pod that stub state may have changed.
//
// It exists as an interface because the mechanism is expected to be replaced.
// v1 polls a counter document, which needs no extra infrastructure and is
// trivially robust; the roadmap swaps in a Couchbase DCP stream for near-zero
// propagation latency (SPEC §8, ROADMAP 2.4). Nothing above this interface has
// to change when that happens.
type ChangeSignal interface {
	// Epoch reports the store's current change counter.
	Epoch(ctx context.Context) (uint64, error)
}

// Poller drives snapshot convergence on one pod.
//
// Two timers, doing different jobs. The epoch poll is the fast path: one cheap
// counter read per interval, and a rebuild only when the counter moved. The
// resync is the backstop — an unconditional reload that sweeps documents whose
// TTL expired without bumping the epoch, and self-heals a signal that was
// somehow missed (SPEC §7.4, §8).
type Poller struct {
	signal  ChangeSignal
	builder *Builder
	engine  *Engine
	log     *slog.Logger
	metrics *metrics.Metrics

	syncInterval   time.Duration
	resyncInterval time.Duration
}

// NewPoller wires a poller to its signal and builder.
func NewPoller(signal ChangeSignal, builder *Builder, engine *Engine, log *slog.Logger,
	m *metrics.Metrics, syncInterval, resyncInterval time.Duration) *Poller {
	return &Poller{
		signal:         signal,
		builder:        builder,
		engine:         engine,
		log:            log,
		metrics:        m,
		syncInterval:   syncInterval,
		resyncInterval: resyncInterval,
	}
}

// Run polls until the context is cancelled. It is expected to be run in its own
// goroutine and never returns an error: a failed poll is a transient condition
// to retry, not a reason to stop converging.
func (p *Poller) Run(ctx context.Context) {
	sync := time.NewTicker(p.syncInterval)
	defer sync.Stop()

	resync := time.NewTicker(p.resyncInterval)
	defer resync.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-sync.C:
			p.pollOnce(ctx)

		case <-resync.C:
			// Unconditional: this is what sweeps naturally expired documents,
			// whose disappearance bumps no counter.
			if err := p.builder.Rebuild(ctx, metrics.TriggerResync); err != nil {
				p.log.Warn("resync rebuild failed; keeping the current snapshot", "error", err)
			}
		}
	}
}

// pollOnce reads the epoch and rebuilds if it moved.
func (p *Poller) pollOnce(ctx context.Context) {
	// The read is bounded by the poll interval: a slow store must not let polls
	// pile up behind each other.
	pollCtx, cancel := context.WithTimeout(ctx, p.syncInterval)
	defer cancel()

	epoch, err := p.signal.Epoch(pollCtx)
	if err != nil {
		// A store outage is a degraded mode, not a failure: the loaded snapshot
		// keeps serving and the next tick tries again (SPEC §4.6).
		p.log.Debug("epoch poll failed; will retry", "error", err)
		p.metrics.StoreErrors.WithLabelValues("epoch").Inc()
		return
	}

	if epoch == p.engine.Snapshot().Epoch {
		return
	}

	p.log.Debug("epoch changed; rebuilding",
		"from", p.engine.Snapshot().Epoch, "to", epoch)
	if err := p.builder.Rebuild(ctx, metrics.TriggerEpoch); err != nil {
		p.log.Warn("epoch-triggered rebuild failed; keeping the current snapshot", "error", err)
	}
}
