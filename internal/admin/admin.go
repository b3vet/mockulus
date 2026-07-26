// SPDX-License-Identifier: Apache-2.0

// Package admin implements the WireMock-compatible `/__admin` API. Handlers
// here are transport only: they decode, delegate to the stub, store and match
// packages, and encode a WireMock-shaped response.
//
// Every endpoint outside the supported matrix of SPEC §5.1 answers 404 with a
// catalog error pointing at the roadmap, so a team migrating from WireMock
// learns exactly where they stand instead of getting silence.
package admin

import (
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/match"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/scenario"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// guessedWireMockVersion is reported alongside our own version so WireMock
// clients that probe for a version see a value they can reason about.
const guessedWireMockVersion = "3.x-subset"

// Options carries the collaborators the admin API needs.
type Options struct {
	Config  config.Config
	Logger  *slog.Logger
	Metrics *metrics.Metrics
	Version string

	Store   store.StubStore
	Engine  *match.Engine
	Builder *match.Builder
	// Scenarios is nil when no scenario store is configured, which is the case
	// only in degraded startup.
	Scenarios *scenario.Client
	// Journal is nil when journaling is off, which is the default. Every
	// journal-dependent endpoint then reports the disabled error.
	Journal store.JournalStore
	// StubOptions carries the regex policy, so admin writes compile a stub
	// exactly as a snapshot rebuild would.
	StubOptions stub.Options
	// Shutdown begins the ordinary drain sequence, as SIGTERM would. It backs
	// POST /__admin/shutdown, which is registered only when the operator has
	// enabled it.
	Shutdown func()
}

// Handler serves the admin API.
type Handler struct {
	cfg     config.Config
	log     *slog.Logger
	metrics *metrics.Metrics
	version string

	store     store.StubStore
	engine    *match.Engine
	builder   *match.Builder
	scenarios *scenario.Client
	journal   store.JournalStore
	stubOpts  stub.Options
	shutdown  func()
	// started backs the uptime the health endpoint reports.
	started time.Time

	mux http.Handler
}

// New builds the admin handler and its routing table.
func New(opts Options) *Handler {
	h := &Handler{
		cfg:       opts.Config,
		log:       opts.Logger,
		metrics:   opts.Metrics,
		version:   opts.Version,
		store:     opts.Store,
		engine:    opts.Engine,
		builder:   opts.Builder,
		scenarios: opts.Scenarios,
		journal:   opts.Journal,
		stubOpts:  opts.StubOptions,
		shutdown:  opts.Shutdown,
		started:   time.Now(),
	}

	mux := http.NewServeMux()

	// Stub mappings. The sub-resource routes are registered before the {id}
	// routes they would otherwise be captured by.
	mux.HandleFunc("POST /__admin/mappings", h.createMapping)
	mux.HandleFunc("GET /__admin/mappings", h.listMappings)
	mux.HandleFunc("DELETE /__admin/mappings", h.deleteAllMappings)
	mux.HandleFunc("POST /__admin/mappings/reset", h.resetMappings)
	mux.HandleFunc("POST /__admin/mappings/save", h.saveMappings)
	mux.HandleFunc("POST /__admin/mappings/import", h.importMappings)
	mux.HandleFunc("POST /__admin/mappings/find-by-metadata", h.findByMetadata)
	mux.HandleFunc("POST /__admin/mappings/remove-by-metadata", h.removeByMetadata)
	mux.HandleFunc("GET /__admin/mappings/{id}", h.getMapping)
	mux.HandleFunc("PUT /__admin/mappings/{id}", h.updateMapping)
	mux.HandleFunc("DELETE /__admin/mappings/{id}", h.deleteMapping)

	// Response body files, which back bodyFileName.
	mux.HandleFunc("GET /__admin/files", h.listFiles)
	mux.HandleFunc("GET /__admin/files/{name...}", h.getFile)
	mux.HandleFunc("PUT /__admin/files/{name...}", h.putFile)
	mux.HandleFunc("DELETE /__admin/files/{name...}", h.deleteFile)

	// The request journal and verification.
	mux.HandleFunc("GET /__admin/requests", h.listRequests)
	mux.HandleFunc("DELETE /__admin/requests", h.clearRequests)
	mux.HandleFunc("POST /__admin/requests/reset", h.clearRequests)
	mux.HandleFunc("POST /__admin/requests/count", h.countRequests)
	mux.HandleFunc("POST /__admin/requests/find", h.findRequests)
	mux.HandleFunc("POST /__admin/requests/remove", h.removeRequests)
	mux.HandleFunc("GET /__admin/requests/unmatched", h.unmatchedRequests)
	mux.HandleFunc("GET /__admin/requests/unmatched/near-misses", h.unmatchedNearMisses)
	mux.HandleFunc("POST /__admin/near-misses/request", h.nearMissesForRequest)
	mux.HandleFunc("POST /__admin/near-misses/request-pattern", h.nearMissesForPattern)
	mux.HandleFunc("GET /__admin/requests/{id}", h.getRequest)
	mux.HandleFunc("DELETE /__admin/requests/{id}", h.deleteRequest)

	// The deployment-wide reset, which is all three of the above at once.
	mux.HandleFunc("POST /__admin/reset", h.resetAll)

	// Scenarios.
	mux.HandleFunc("GET /__admin/scenarios", h.listScenarios)
	mux.HandleFunc("POST /__admin/scenarios/reset", h.resetScenarios)
	mux.HandleFunc("PUT /__admin/scenarios/{name}/state", h.setScenarioState)

	// Global settings: the deployment-wide response delay (SPEC §5.1).
	mux.HandleFunc("GET /__admin/settings", h.getSettings)
	mux.HandleFunc("POST /__admin/settings", h.putSettings)

	mux.HandleFunc("GET /__admin/health", h.health)
	mux.HandleFunc("GET /__admin/version", h.versionInfo)

	// The shutdown route is registered only when it is enabled, so when it is
	// not, the request falls through to the unsupported-endpoint 404 that
	// deviation #8 specifies — the endpoint does not exist rather than existing
	// and refusing. Kubernetes owns the pod lifecycle in the deployment this is
	// built for, and an unauthenticated caller that can stop a replica is a
	// denial of service with an HTTP interface.
	if h.cfg.AdminShutdownEnabled && h.shutdown != nil {
		mux.HandleFunc("POST /__admin/shutdown", h.shutdownServer)
	}

	// Anything else under /__admin is outside the supported matrix.
	mux.HandleFunc("/__admin/", h.notFound)
	mux.HandleFunc("/__admin", h.notFound)

	// Metrics wrap authentication rather than the other way round, so a
	// refusal is counted under the group it was aimed at. A deployment whose
	// token is being guessed otherwise looks idle: the requests never reach the
	// counter, and the one signal an operator has that someone is knocking is
	// the one the middleware order throws away.
	h.mux = h.withMetrics(h.withAuth(mux))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// withAuth enforces the optional static admin token on every admin route,
// including the ones registered after this was written: it wraps the mux rather
// than each handler, so a new route is guarded by existing.
//
// Comparison is constant-time so the endpoint cannot be used as an oracle
// (SPEC §17).
func (h *Handler) withAuth(next http.Handler) http.Handler {
	if h.cfg.AdminAuthToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.AdminTokenAccepted(r.Header.Get("Authorization")) {
			wmcompat.WriteErrors(w, http.StatusUnauthorized,
				wmcompat.NewError(wmcompat.CodeMalformed, "admin API requires a valid Authorization token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withMetrics records `mockulus_admin_requests_total`. Labels are the endpoint
// group rather than the raw path, so per-stub paths cannot inflate cardinality
// (SPEC §14.1).
func (h *Handler) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		h.metrics.AdminRequests.
			WithLabelValues(endpointGroup(r.URL.Path), strconv.Itoa(rec.status)).
			Inc()
	})
}

// endpointGroup reduces an admin path to its low-cardinality group.
func endpointGroup(path string) string {
	rest := strings.TrimPrefix(path, "/__admin")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "root"
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		head, tail := rest[:i], rest[i+1:]
		// Keep a known sub-resource ("mappings/reset"), drop identifiers.
		if !strings.ContainsAny(tail, "/") && isWord(tail) {
			return head + "/" + tail
		}
		return head
	}
	return rest
}

func isWord(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && r != '-' {
			return false
		}
	}
	return s != ""
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// notFound answers any admin path outside the supported matrix.
// shutdownServer begins the drain and answers before it starts.
//
// The response is written first and the drain runs after, because a caller that
// asked the process to stop still deserves to be told it will: draining takes
// shutdown_drain seconds by design, and a client that has to wait it out cannot
// distinguish a graceful stop from a hang.
func (h *Handler) shutdownServer(w http.ResponseWriter, _ *http.Request) {
	h.log.Info("shutdown requested through the admin API")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go h.shutdown()
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	wmcompat.WriteError(w, wmcompat.UnsupportedEndpoint(r.URL.Path))
}

// resetAll is the deployment-wide reset: non-persistent mappings swept, journal
// emptied, scenarios back to Started, in one call (SPEC §5.1). It is what a
// suite presses between runs — and, being deployment-wide, the call SPEC §1
// tells runners sharing an instance never to press.
//
// The journal and the scenario states are swept only where they exist, rather
// than failing when they do not. Journaling is off by default and a degraded
// start has no scenario store, so the alternative would make the
// deployment-wide reset the one call that does not work on the deployment
// mockulus ships with.
func (h *Handler) resetAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := h.store.DeleteEphemeralStubs(ctx); err != nil {
		h.storeError(w, "delete_ephemeral_stubs", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	// Rebuilt before the other two so the mock port stops serving the swept
	// stubs on this pod immediately, rather than at the next poll.
	h.rebuild(r, "reset all")

	if h.journal != nil {
		if err := h.journal.ClearJournal(ctx); err != nil {
			h.storeError(w, "clear_journal", err)
			return
		}
	}
	if h.scenarios != nil {
		if err := h.scenarios.ResetAll(ctx); err != nil {
			h.storeError(w, "reset_scenarios", err)
			return
		}
	}

	h.log.Info("deployment reset")
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
}

// health reports the WireMock health shape plus mockulus detail. The extra
// fields are a catalogued extension, not a compatibility diff (SPEC §5.6); the
// WireMock-defined ones are all present, because a client reading any of them
// must keep working.
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	snap := h.engine.Snapshot()
	now := time.Now().UTC()
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"status":          "healthy",
		"message":         "mockulus is ok",
		"version":         h.version,
		"uptimeInSeconds": int64(now.Sub(h.started).Seconds()),
		"timestamp":       now.Format(time.RFC3339Nano),
		"store": map[string]any{
			"driver": h.cfg.EffectiveStore(),
		},
		"stubs": snap.Len(),
		"epoch": snap.Epoch,
	})
}

// versionInfo reports our version and the WireMock surface we mirror.
func (h *Handler) versionInfo(w http.ResponseWriter, _ *http.Request) {
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"version":                h.version,
		"guessedWireMockVersion": guessedWireMockVersion,
		"goVersion":              runtime.Version(),
	})
}
