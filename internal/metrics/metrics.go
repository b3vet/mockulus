// SPDX-License-Identifier: Apache-2.0

// Package metrics owns mockulus' Prometheus registry and every collector it
// exposes. The set mirrors SPEC §14.1 exactly: collectors are pre-registered
// once at construction so the hot path only ever increments an existing series
// (SPEC §16.3 rule 4), and no metric carries a per-stub label — a 10k-stub
// deployment must not mint 10k series.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Trigger values for the snapshot reload counter (SPEC §8).
const (
	TriggerAdmin  = "admin"
	TriggerEpoch  = "epoch"
	TriggerResync = "resync"
)

// Metrics holds every collector mockulus exports.
type Metrics struct {
	registry *prometheus.Registry

	BuildInfo *prometheus.GaugeVec

	HTTPRequests        *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	AdminRequests       *prometheus.CounterVec

	SnapshotStubs           prometheus.Gauge
	SnapshotEpoch           prometheus.Gauge
	SnapshotReloads         *prometheus.CounterVec
	SnapshotReloadDuration  prometheus.Histogram
	SnapshotReloadFailures  prometheus.Counter
	SnapshotQuarantined     *prometheus.CounterVec
	StoreOperationDuration  *prometheus.HistogramVec
	StoreErrors             *prometheus.CounterVec
	ScenarioReads           prometheus.Counter
	ScenarioCASRetries      prometheus.Counter
	ScenarioTransitionConfl prometheus.Counter
	JournalEnqueued         prometheus.Counter
	JournalDropped          prometheus.Counter
	JournalFlushDuration    prometheus.Histogram
	TemplateRenderErrors    prometheus.Counter
	RegexTimeouts           prometheus.Counter
	MatchCandidates         prometheus.Histogram
	TraceExportFailures     prometheus.Counter
}

// requestDurationBuckets span 100µs to 10s log-spaced, per SPEC §14.1 — wide
// enough to hold both a sub-millisecond static hit and a delayed stub.
var requestDurationBuckets = []float64{
	0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
	0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// New builds the registry and every collector in SPEC §14.1. Passing enabled
// false yields a fully usable Metrics whose collectors are not registered, so
// call sites never need a nil check (SPEC §13 `metrics_enabled`).
func New(version, goVersion string, enabled bool) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "mockulus_build_info",
			Help: "Build information about this mockulus binary.",
		}, []string{"version", "go_version"}),

		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockulus_http_requests_total",
			Help: "Mock-port requests served, by match outcome and status code.",
		}, []string{"matched", "code"}),

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mockulus_http_request_duration_seconds",
			Help:    "Mock-port request duration in seconds.",
			Buckets: requestDurationBuckets,
		}, []string{"matched"}),

		AdminRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockulus_admin_requests_total",
			Help: "Admin API requests, by endpoint group and status code.",
		}, []string{"endpoint_group", "code"}),

		SnapshotStubs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mockulus_snapshot_stubs",
			Help: "Stubs in the currently served snapshot.",
		}),
		SnapshotEpoch: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mockulus_snapshot_epoch",
			Help: "Epoch of the currently served snapshot.",
		}),
		SnapshotReloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockulus_snapshot_reloads_total",
			Help: "Snapshot rebuilds, by what triggered them.",
		}, []string{"trigger"}),
		SnapshotReloadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mockulus_snapshot_reload_duration_seconds",
			Help:    "Time taken to rebuild and swap a snapshot.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}),
		SnapshotReloadFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_snapshot_reload_failures_total",
			Help: "Snapshot rebuilds abandoned because the store read failed.",
		}),
		SnapshotQuarantined: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockulus_snapshot_quarantined_total",
			Help: "Documents excluded from a snapshot build, by reason.",
		}, []string{"reason"}),

		StoreOperationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mockulus_store_operation_duration_seconds",
			Help:    "Store operation duration in seconds, by operation.",
			Buckets: []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10},
		}, []string{"op"}),
		StoreErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockulus_store_errors_total",
			Help: "Failed store operations, by operation.",
		}, []string{"op"}),

		ScenarioReads: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_scenario_reads_total",
			Help: "Scenario state reads performed on the request path.",
		}),
		ScenarioCASRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_scenario_cas_retries_total",
			Help: "Scenario transition attempts retried after a CAS conflict.",
		}),
		ScenarioTransitionConfl: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_scenario_transition_conflicts_total",
			Help: "Scenario transitions skipped because another replica moved the state first.",
		}),

		JournalEnqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_journal_enqueued_total",
			Help: "Journal entries accepted onto the write queue.",
		}),
		JournalDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_journal_dropped_total",
			Help: "Journal entries dropped because the queue was full.",
		}),
		JournalFlushDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mockulus_journal_flush_duration_seconds",
			Help:    "Time taken to flush a batch of journal entries.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}),

		TemplateRenderErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_template_render_errors_total",
			Help: "Response templates that failed to render at serve time.",
		}),
		RegexTimeouts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_regex_timeouts_total",
			Help: "Regex evaluations abandoned after hitting the match timeout.",
		}),
		MatchCandidates: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mockulus_match_candidates",
			Help:    "Candidate stubs evaluated per mock request.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
		}),
		// A deployment whose collector is unreachable must not read as a
		// deployment with nothing to report: the spans are dropped either way,
		// and this is the difference between knowing that and not (SPEC §14.3).
		TraceExportFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mockulus_trace_export_failures_total",
			Help: "Trace export batches the collector did not accept.",
		}),
	}

	m.BuildInfo.WithLabelValues(version, goVersion).Set(1)

	if enabled {
		m.registry.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			m.BuildInfo,
			m.HTTPRequests,
			m.HTTPRequestDuration,
			m.AdminRequests,
			m.SnapshotStubs,
			m.SnapshotEpoch,
			m.SnapshotReloads,
			m.SnapshotReloadDuration,
			m.SnapshotReloadFailures,
			m.SnapshotQuarantined,
			m.StoreOperationDuration,
			m.StoreErrors,
			m.ScenarioReads,
			m.ScenarioCASRetries,
			m.ScenarioTransitionConfl,
			m.JournalEnqueued,
			m.JournalDropped,
			m.JournalFlushDuration,
			m.TemplateRenderErrors,
			m.RegexTimeouts,
			m.MatchCandidates,
			m.TraceExportFailures,
		)
	}
	return m
}

// Registry exposes the registry backing the /metrics endpoint.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ObserveStoreOperation records one store call's duration and whether it
// failed. It is the store package's Recorder, kept here so that package depends
// on a one-method interface rather than on the whole registry.
func (m *Metrics) ObserveStoreOperation(op string, d time.Duration, err error) {
	m.StoreOperationDuration.WithLabelValues(op).Observe(d.Seconds())
	if err != nil {
		m.StoreErrors.WithLabelValues(op).Inc()
	}
}
