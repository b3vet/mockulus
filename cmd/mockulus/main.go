// SPDX-License-Identifier: Apache-2.0

// Command mockulus is a cloud-native mock server that is wire-compatible with a
// defined subset of the WireMock 3.x admin API.
//
// This file is wiring and lifecycle only; every behavior lives in internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/b3vet/mockulus/internal/admin"
	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/journal"
	"github.com/b3vet/mockulus/internal/jsonpath"
	"github.com/b3vet/mockulus/internal/match"
	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/regexx"
	"github.com/b3vet/mockulus/internal/response"
	"github.com/b3vet/mockulus/internal/scenario"
	"github.com/b3vet/mockulus/internal/server"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/couchbase"
	"github.com/b3vet/mockulus/internal/store/file"
	"github.com/b3vet/mockulus/internal/store/memory"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/template"
	"github.com/b3vet/mockulus/internal/tracing"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// maxTraceFlush caps how long shutdown waits for the last spans to be exported.
// See the call site for why this is not shutdown_timeout.
const maxTraceFlush = 5 * time.Second

// traceFlushBudget keeps the flush inside a deployment's own shutdown budget: a
// pod configured to stop quickly must not be held open by telemetry.
func traceFlushBudget(cfg config.Config) time.Duration {
	if d := cfg.ShutdownTimeout.D(); d < maxTraceFlush {
		return d
	}
	return maxTraceFlush
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mockulus: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to a YAML configuration file")
		showVersion = flag.Bool("version", false, "print the version and exit")
		healthcheck = flag.Bool("healthcheck", false,
			"probe this pod's own /healthz and exit 0 if it answers")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mockulus " + version + " (" + runtime.Version() + ")")
		return nil
	}

	cfg, err := config.Load(*configPath, os.LookupEnv)
	if err != nil {
		return err
	}

	if *healthcheck {
		return selfCheck(cfg)
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	for _, line := range cfg.Dump() {
		log.Debug("config", "setting", line)
	}

	m := metrics.New(version, runtime.Version(), cfg.MetricsEnabled)

	// Tracing is off unless a collector was configured. When it is off nothing
	// below is constructed and no component is handed a tracer, so serving takes
	// the same path it always has (SPEC §14.3).
	var tracer *tracing.Tracer
	var traceProvider *tracing.Provider
	if cfg.Tracing.Enabled {
		headers, headerErr := cfg.Tracing.ParsedHeaders()
		if headerErr != nil {
			// Validation has already rejected this; the check is here so the
			// error cannot become a silent empty header set if it ever moves.
			return fmt.Errorf("tracing.headers: %w", headerErr)
		}
		// Background rather than the signal context: the exporter outlives the
		// signal that begins the drain, because the last spans are flushed
		// after the listeners have closed (SPEC §4.5).
		traceProvider, err = tracing.New(context.Background(), tracing.Options{
			Endpoint:    cfg.Tracing.Endpoint,
			Insecure:    cfg.Tracing.Insecure,
			Headers:     headers,
			SampleRatio: cfg.Tracing.SampleRatio,
			ServiceName: cfg.Tracing.ServiceName,
			Version:     version,
			InstanceID:  podName(),
		}, log, m.TraceExportFailures.Inc)
		if err != nil {
			return err
		}
		tracer = traceProvider.Tracer()
		log.Info("tracing enabled",
			"endpoint", cfg.Tracing.Endpoint,
			"sample_ratio", cfg.Tracing.SampleRatio,
			"service_name", cfg.Tracing.ServiceName)
	}

	// Templating: `wm-compat` mirrors the pinned WireMock, which requires the
	// per-stub transformer declaration — verified directly, a stub without it
	// serves {{request.path}} literally. So wm-compat and off differ only in
	// whether the engine exists at all, and `on` forces it globally.
	var renderer response.Renderer
	var templateEngine *template.Engine
	if cfg.TemplatingEnabled != config.TemplatingOff {
		templateEngine = template.NewEngine(int(cfg.TemplateMaxOutputBytes.B()),
			jsonpath.TemplateHelper)
		renderer = templateEngine
	}

	engine := match.NewEngine(cfg, m, log, renderer)
	if tracer != nil {
		engine.SetTracer(tracer)
	}

	srvCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv := server.New(server.Options{
		Config:  cfg,
		Logger:  log,
		Metrics: m,
		Mock:    engine,
	})

	// Step 2 of SPEC §4.4, and it has to come before step 3: connecting to
	// Couchbase can take minutes when a whole cluster restarts at once, and a
	// pod that is not answering /healthz during that window is a pod Kubernetes
	// restarts — over and over, into a backoff that outlasts the outage it was
	// reacting to. Binding first means the pod can say "alive, not ready" for
	// as long as it takes. The admin API is installed once it has a store.
	if err = srv.StartAdmin(); err != nil {
		return err
	}

	st, err := openStore(srvCtx, cfg, log)
	if err != nil {
		if !cfg.StartWithoutStore {
			return err
		}
		// Ready with an empty snapshot, serving mock traffic while admin writes
		// return 503 until the store appears (SPEC §4.4).
		st = memory.New(cfg.EphemeralStubTTL.D())
	}

	// One regex policy for the whole process: admin writes and snapshot
	// rebuilds must compile a stub identically, or a stub could register on one
	// path and be quarantined on the other.
	stubOpts := stub.Options{
		CompileRegex: regexCompiler(cfg, m, log),
		// One JSONPath implementation for matching and for templating: a stub
		// must not be able to disagree with itself about what a path selects.
		CompileJSONPath: func(expr string) (matchers.JSONPathEvaluator, error) {
			return jsonpath.NewEvaluator(expr)
		},
		GlobalTemplating: cfg.TemplatingEnabled == config.TemplatingOn,
	}
	if templateEngine != nil {
		stubOpts.CompileTemplate = templateEngine.Compile
	}

	// Every store call made through the StubStore surface is timed and its
	// failures counted (§14.1). The driver itself stays in `st` because the
	// optional interfaces below are reached by type assertion, which a wrapper
	// cannot carry — see internal/store/instrument.go.
	timed := store.Instrumented(st, m)

	builder := match.NewBuilder(timed, engine, log, m, stubOpts)

	// Scenarios: only stubs that are in one ever read state, so a deployment
	// that uses no scenarios pays nothing for this (P2).
	var scenarios *scenario.Client
	if scenarioStore, ok := st.(store.ScenarioStore); ok {
		scenarios = scenario.NewClient(scenarioStore, log, m, cfg.ScenarioKVTimeout.D())
		engine.SetScenarioGate(scenarioGate(scenarios, m, log))
		engine.SetTransitioner(scenarios)
	}

	// The journal: off by default, because always-on journaling at 50k RPS is
	// 50k writes/s — the collapse this project exists to avoid (D3).
	var journalStore store.JournalStore
	var journalWriter *journal.Writer
	if cfg.JournalEnabled {
		js, ok := st.(store.JournalStore)
		if !ok {
			return fmt.Errorf("journal_enabled is set but the %s store cannot record a journal",
				cfg.EffectiveStore())
		}
		journalStore = js
		journalWriter = journal.NewWriter(journal.Config{
			Enabled:       true,
			MaxBody:       int(cfg.JournalMaxBody.B()),
			BufferEntries: cfg.JournalBuffer,
			BufferBytes:   cfg.JournalBufferBytes.B(),
			FlushWorkers:  cfg.JournalFlushWorkers,
			BatchSize:     cfg.JournalBatchSize,
			FlushInterval: cfg.JournalFlushInterval.D(),
			Pod:           podName(),
		}, js, log, m)
		journalWriter.Start()
		engine.SetRecorder(journalWriter)
	}

	// adminShutdown carries a stop requested through the admin API. It is
	// buffered and sent to at most once, so a second request cannot block on a
	// drain already under way.
	adminShutdown := make(chan struct{}, 1)
	adminAPI := admin.New(admin.Options{
		Config:      cfg,
		Logger:      log,
		Metrics:     m,
		Version:     version,
		Store:       timed,
		Engine:      engine,
		Builder:     builder,
		Scenarios:   scenarios,
		Journal:     journalStore,
		StubOptions: stubOpts,
		Tracer:      tracer,
		Shutdown: func() {
			select {
			case adminShutdown <- struct{}{}:
			default:
			}
		},
	})

	srv.SetAdmin(adminAPI)

	ctx := srvCtx

	loadStart := time.Now()
	if err := builder.Rebuild(ctx, metrics.TriggerResync); err != nil {
		if !cfg.StartWithoutStore {
			return fmt.Errorf("initial snapshot load: %w", err)
		}
		log.Warn("starting with an empty snapshot; the store was unreachable",
			"error", err)
	}
	loadTook := time.Since(loadStart)

	// Convergence: one cheap epoch read per interval, plus an unconditional
	// resync that sweeps expired documents and self-heals a missed signal.
	if signal, ok := st.(match.ChangeSignal); ok {
		poller := match.NewPoller(signal, builder, engine, log, m,
			cfg.SyncInterval.D(), cfg.ResyncInterval.D())
		go poller.Run(ctx)
	}

	if err := srv.StartMock(); err != nil {
		return err
	}
	srv.SetReady(true)

	log.Info("mockulus started",
		"version", version,
		"store", cfg.EffectiveStore(),
		"stubs", engine.Snapshot().Len(),
		"load_ms", loadTook.Milliseconds(),
		"mock_addr", srv.MockAddr(),
		"admin_addr", srv.AdminAddr(),
		"admin_on_mock_port", cfg.AdminOnMockPort)

	select {
	case <-ctx.Done():
	case <-adminShutdown:
		// An admin-initiated stop takes the same path as a signal: readiness
		// drops, the drain window elapses, then the listeners close.
	}
	stop() // a second signal from here on kills the process outright
	log.Info("shutting down")

	shutdownCtx := context.Background()
	var errs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, err)
	}
	if journalWriter != nil {
		// Flush before closing the store: a verification made just before
		// SIGTERM should still find its entry (SPEC §4.5).
		journalWriter.Stop()
	}
	if traceProvider != nil {
		// After the listeners have drained, so the spans of the last requests
		// served are exported rather than dropped on the way out.
		//
		// The budget is deliberately its own small number rather than another
		// shutdown_timeout. A collector that has gone away would otherwise hold
		// the drain for a second full timeout, pushing the worst case past the
		// default terminationGracePeriodSeconds — and what waits past the grace
		// period is not a slower shutdown but a SIGKILL, which cuts off
		// in-flight responses and leaves the journal batch unflushed. A
		// telemetry backend must not be able to turn a rolling restart into a
		// mock-server incident, so the last spans get a few seconds and no more.
		//
		// A failure here is logged rather than returned. Exiting non-zero
		// because the last spans could not be delivered would report a pod that
		// drained cleanly as a pod that crashed — on every replica of a rolling
		// restart, for the whole of a collector outage. Losing telemetry is not
		// the process failing at its job.
		flushCtx, cancel := context.WithTimeout(shutdownCtx, traceFlushBudget(cfg))
		if err := traceProvider.Shutdown(flushCtx); err != nil {
			log.Warn("the last spans could not be exported", "error", err)
		}
		cancel()
	}
	if err := st.Close(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("close store: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

// selfCheck probes this pod's own liveness endpoint and reports whether it
// answered. It backs the image's HEALTHCHECK (SPEC §15.1): the runtime base has
// no shell and no curl, so the only thing in the image that can make an HTTP
// request is mockulus itself.
//
// It reads the same configuration the server does, so it probes the port this
// deployment actually bound rather than the default. It probes /healthz and not
// the mock port, because an unmatched mock request is a 404 by design — a
// health check aimed there would report a working pod as broken the moment a
// suite reset its stubs.
func selfCheck(cfg config.Config) error {
	if cfg.AdminPort == 0 {
		return errors.New("healthcheck: admin_port is ephemeral, so there is no fixed address to probe")
	}
	url := "http://127.0.0.1:" + strconv.Itoa(cfg.AdminPort) + "/healthz"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	// No Authorization header: /healthz is deliberately outside admin_auth_token
	// (SPEC §17), and a check that needed the token would put it in the image's
	// process list on every probe.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %d", url, resp.StatusCode)
	}
	return nil
}

// regexCompiler builds the process-wide regex policy: WireMock's matchers
// require a full match, the fallback engine is bounded by regex_timeout, and a
// timeout is counted and named so an operator can find the offending stub.
func regexCompiler(cfg config.Config, m *metrics.Metrics, log *slog.Logger) matchers.RegexCompiler {
	opts := regexx.Options{
		Timeout:  cfg.RegexTimeout.D(),
		Anchored: true,
		OnTimeout: func(source string) {
			m.RegexTimeouts.Inc()
			log.Warn("regex match timed out; treating it as a non-match",
				"pattern", source, "timeout", cfg.RegexTimeout.String())
		},
	}
	return func(pattern string) (matchers.PatternMatcher, error) {
		return regexx.Compile(pattern, opts)
	}
}

// podName identifies the replica that recorded a journal entry, so a
// cross-pod verification result can be attributed.
func podName() string {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name
	}
	if host, err := os.Hostname(); err == nil {
		return host
	}
	return "unknown"
}

// scenarioGate builds the request-path state check.
//
// A state read that fails is a 500 rather than a guess: serving the wrong side
// of a state machine because the store hiccuped is worse than saying so. Plain
// stubs are unaffected, which is what keeps the failure contained to the
// deployments actually using scenarios (SPEC §4.6, §9.2).
func scenarioGate(client *scenario.Client, m *metrics.Metrics, log *slog.Logger) match.ScenarioGate {
	return func(ref *stub.ScenarioRef, req *match.ParsedRequest) bool {
		if ref.RequiredState == "" {
			return true
		}

		// Memoized per request: a request touching several stubs in one
		// scenario reads its state once (SPEC §9.2).
		if state, ok := req.ScenarioState(ref.Name); ok {
			return state == ref.RequiredState
		}

		state, err := client.State(context.Background(), ref.Name)
		if err != nil {
			m.StoreErrors.WithLabelValues("scenario_read").Inc()
			log.Warn("scenario state unavailable; failing the request",
				"scenario", ref.Name, "error", err)
			// Recorded on the request, not swallowed here: returning a bare
			// false would be indistinguishable from "the state is not the one
			// this stub wants", and the request would be answered as though the
			// flow were somewhere it may well not be. The engine turns this
			// into the 500 of Appendix B code 1021.
			req.FailScenarioRead(err)
			return false
		}
		req.MemoizeScenarioState(ref.Name, state)
		return state == ref.RequiredState
	}
}

// openStore builds the configured store driver (SPEC §7.1, D9).
//
// A Couchbase that is not there yet is not a startup failure. SPEC §4.4 has the
// pod stay alive and not-ready, retrying forever, because a store outage during
// a rollout must not become a crash loop — Kubernetes will simply not route to
// a pod that never reports ready.
func openStore(ctx context.Context, cfg config.Config, log *slog.Logger) (store.StubStore, error) {
	switch driver := cfg.EffectiveStore(); driver {
	case config.StoreMemory:
		return memory.New(cfg.EphemeralStubTTL.D()), nil

	case config.StoreCouchbase:
		return connectCouchbase(ctx, cfg, log)

	case config.StoreFile:
		return file.Open(cfg, log)

	default:
		return nil, fmt.Errorf("store driver %q is not available in this build", driver)
	}
}

// connectCouchbase retries with backoff until it connects or the process is
// asked to stop.
func connectCouchbase(ctx context.Context, cfg config.Config, log *slog.Logger) (store.StubStore, error) {
	const (
		initialBackoff = 500 * time.Millisecond
		maxBackoff     = 15 * time.Second
	)

	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		st, err := couchbase.Open(ctx, cfg, log)
		if err == nil {
			log.Info("connected to couchbase",
				"connstr", cfg.Couchbase.ConnStr,
				"bucket", cfg.Couchbase.Bucket,
				"scope", cfg.Couchbase.Scope,
				"attempts", attempt)
			return st, nil
		}

		if cfg.StartWithoutStore {
			// The escape hatch of SPEC §4.4: become ready with an empty
			// snapshot so mock traffic can be served. Admin writes still fail
			// until the store connects, and nothing is buffered, so there is
			// nothing to reconcile when it does.
			log.Warn("couchbase is unreachable and start_without_store is set; serving an empty snapshot",
				"error", err)
			return nil, err
		}

		log.Warn("couchbase is unreachable; retrying",
			"attempt", attempt, "retry_in", backoff.String(), "error", err)

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// newLogger builds the structured logger described in SPEC §14.2: JSON to
// stdout by default, with a text handler available for local development.
func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Log.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.Log.Format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
