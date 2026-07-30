// SPDX-License-Identifier: Apache-2.0

// Package server owns mockulus' two listeners, the routing between them, and
// the startup and shutdown sequences of SPEC §4.4 and §4.5.
//
// The mock listener carries all stub traffic and — unless `admin_on_mock_port`
// is turned off — also the admin API, because WireMock clients default to
// same-port `/__admin` (SPEC D10). The admin listener additionally carries the
// operational surface: health, readiness, metrics and pprof, none of which are
// ever exposed on the mock port.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/b3vet/mockulus/internal/adminui"
	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// adminPrefix is the path prefix the WireMock admin API lives under.
const adminPrefix = "/__admin"

// Options carries everything a Server needs; every handler is supplied by the
// caller so this package stays transport-only.
type Options struct {
	Config  config.Config
	Logger  *slog.Logger
	Metrics *metrics.Metrics

	// Mock handles stub traffic on the mock port.
	Mock http.Handler
	// Admin handles /__admin/** on both ports. It may be nil at construction
	// and installed later with SetAdmin, which is what lets the admin listener
	// bind before the store has connected (SPEC §4.4 step 2).
	Admin http.Handler
}

// Server runs the mock and admin listeners.
type Server struct {
	cfg     config.Config
	log     *slog.Logger
	metrics *metrics.Metrics

	mock  *http.Server
	admin *http.Server

	// adminAPI is swapped in once the store is connected. Until then the
	// listener is already serving, and /__admin/** answers 503 rather than 404
	// — the endpoints exist, the deployment just cannot reach its store yet.
	adminAPI atomic.Pointer[http.Handler]

	ready atomic.Bool

	mockLn  net.Listener
	adminLn net.Listener
}

// New wires the listeners and their routing. It binds nothing; call Start.
func New(opts Options) *Server {
	s := &Server{
		cfg:     opts.Config,
		log:     opts.Logger,
		metrics: opts.Metrics,
	}

	if opts.Admin != nil {
		s.adminAPI.Store(&opts.Admin)
	}

	s.mock = &http.Server{
		Handler:           s.mockHandler(s.mockRouter(opts.Mock, s.pendingAdmin())),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// No WriteTimeout on the mock port: stub delays are legitimate, and the
		// per-response deadline is applied by the renderer (SPEC §12.1).
		ErrorLog: slog.NewLogLogger(opts.Logger.With("listener", "mock").Handler(), slog.LevelDebug),
		// The floor is stated rather than inherited. crypto/tls has moved its
		// default server minimum twice, and it is still steerable from outside
		// the binary by GODEBUG — so left unset, what a mockulus deployment
		// accepts would be a property of the toolchain it happened to be built
		// with and the environment it happened to start in (SPEC §12.1).
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	s.admin = &http.Server{
		Handler:           s.adminRouter(s.pendingAdmin()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    1 << 20,
		WriteTimeout:      60 * time.Second,
		ErrorLog:          slog.NewLogLogger(opts.Logger.With("listener", "admin").Handler(), slog.LevelDebug),
	}
	return s
}

// SetAdmin installs the admin API once its dependencies exist.
//
// Startup binds the admin listener before it connects the store, so that a pod
// waiting on a slow or absent Couchbase is still answering /healthz and /readyz
// (SPEC §4.4 step 2). That ordering is the difference between a pod Kubernetes
// leaves alone to retry and one it restarts into CrashLoopBackOff — but the
// admin API itself needs the store, so the two cannot come up together.
func (s *Server) SetAdmin(h http.Handler) { s.adminAPI.Store(&h) }

// pendingAdmin dispatches to the installed admin API, or reports the store as
// unavailable while there is none.
//
// The indirection costs one atomic load per admin request and none at all on
// the mock path, since the mock router only reaches it for a /__admin prefix.
func (s *Server) pendingAdmin() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := s.adminAPI.Load(); h != nil {
			(*h).ServeHTTP(w, r)
			return
		}
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeStoreUnavailable,
			"the store is not connected yet; this pod is still starting"))
	})
}

// mockHandler wraps the mock router in cleartext HTTP/2 when h2c is enabled.
//
// h2c is off by default, and the reason is fault fidelity rather than caution
// (SPEC §12.5, deviation #15). Faults are injected by hijacking the connection
// and writing bytes — a truncated body, a malformed chunk, an RST. Over HTTP/2
// there is no connection to hijack: the best available answer is a stream
// reset, which is a different thing from the byte-level breakage a stub asked
// for. A team that turns h2c on is choosing protocol coverage over that
// fidelity, so the choice is theirs to make explicitly.
//
// When it is off, the returned handler is the router itself — an h2c wrapper
// that nobody asked for would put a type assertion on every request of the hot
// path for a feature that is not in use (P2).
//
// staticcheck reports this package and NewHandler as deprecated, and the
// replacement it names — http.Server.Protocols with SetUnencryptedHTTP2 — is
// not one. net/http implements cleartext HTTP/2 by prior knowledge only; the
// HTTP/1.1 `Upgrade: h2c` handshake has no equivalent there. Migrating was
// tried and TestH2CEnabledGatesCleartextHTTP2 caught it: the upgrade case
// answered 200 where it must answer 101 Switching Protocols. So the
// deprecation is acknowledged and not acted on, because acting on it would
// quietly drop a capability this package's own tests assert. The exclusion in
// .golangci.yml points back here.
func (s *Server) mockHandler(router http.Handler) http.Handler {
	if !s.cfg.H2CEnabled {
		return router
	}
	// TLS is negotiated by ALPN and needs no upgrade path, so h2c applies only
	// to the cleartext listener.
	if s.cfg.TLSEnabled() {
		return router
	}
	return h2c.NewHandler(router, &http2.Server{IdleTimeout: 75 * time.Second})
}

// mockRouter is the mock port's single catch-all handler. It does one prefix
// comparison before dispatching, with no router framework on the hot path
// (SPEC §12.2).
func (s *Server) mockRouter(mock, admin http.Handler) http.Handler {
	if !s.cfg.AdminOnMockPort {
		return mock
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= len(adminPrefix) && r.URL.Path[:len(adminPrefix)] == adminPrefix {
			admin.ServeHTTP(w, r)
			return
		}
		mock.ServeHTTP(w, r)
	})
}

// adminRouter serves the admin API alongside the operational endpoints. Go
// 1.22 ServeMux pattern routing is used here; this is not the hot path.
func (s *Server) adminRouter(admin http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(adminPrefix, admin)
	mux.Handle(adminPrefix+"/", admin)

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// The admin port's root sends a browser to the UI (§5.7). Somebody who
	// port-forwards a pod and opens it in a browser is looking for the UI, and
	// the alternative is a 404 that says nothing about where it lives.
	//
	// Admin listener only. The mock port's root belongs to the stubs — a
	// redirect there would answer a request a stub was entitled to match, which
	// is the one thing the mock port must never do. The admin API reached
	// through the mock port keeps its /__admin prefix and is unaffected.
	if s.cfg.UIEnabled {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, adminui.Prefix, http.StatusFound)
		})
	}

	if s.cfg.MetricsEnabled {
		mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.Registry(), promhttp.HandlerOpts{
			ErrorLog: slog.NewLogLogger(s.log.With("component", "metrics").Handler(), slog.LevelWarn),
		}))
	}

	// pprof lives on the admin port only, never on the mock port (SPEC §14.3).
	profile := s.withAdminToken(pprofHandler())
	mux.Handle("GET /debug/pprof/", profile)
	mux.Handle("GET /debug/pprof/cmdline", profile)
	mux.Handle("GET /debug/pprof/profile", profile)
	mux.Handle("GET /debug/pprof/symbol", profile)
	mux.Handle("GET /debug/pprof/trace", profile)

	return recoverMiddleware(s.log, mux)
}

// pprofHandler routes the profiling endpoints. They are registered on their own
// mux so one token check in front covers all of them, including any the stdlib
// grows later under /debug/pprof/.
func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// withAdminToken puts `admin_auth_token` in front of the profiling endpoints.
//
// A heap profile is a copy of everything the process is holding, which on this
// process means stub bodies — the same bytes SPEC §17 keeps out of the logs
// because teams put real credentials in their mocks. Leaving pprof open on a
// port whose only credential is this token would let anyone who can reach :9090
// read those bodies without ever presenting it, and /debug/pprof/cmdline hands
// over the process' arguments on the way past.
//
// /healthz, /readyz and /metrics stay open even when a token is set: the
// kubelet and Prometheus cannot present one, and neither endpoint carries stub
// content. Profiling is the one that does.
func (s *Server) withAdminToken(next http.Handler) http.Handler {
	if s.cfg.AdminAuthToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.AdminTokenAccepted(r.Header.Get("Authorization")) {
			wmcompat.WriteErrors(w, http.StatusUnauthorized,
				wmcompat.NewError(wmcompat.CodeMalformed,
					"profiling requires the same Authorization token as the admin API"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealthz reports process liveness. It never consults the store: a
// Couchbase outage must not restart pods (SPEC §15.2).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz reports whether this pod can serve mock traffic: it stays 200
// through a store outage, because the loaded snapshot is still servable
// (SPEC §4.6, §15.2).
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// SetReady flips readiness. Startup calls it once the first snapshot is loaded;
// shutdown clears it before draining (SPEC §4.4, §4.5).
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Ready reports the current readiness state.
func (s *Server) Ready() bool { return s.ready.Load() }

// StartAdmin binds and serves the admin listener. It is called before the store
// connects so that health and readiness are observable during a slow boot
// (SPEC §4.4 step 2).
func (s *Server) StartAdmin() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":"+strconv.Itoa(s.cfg.AdminPort))
	if err != nil {
		return fmt.Errorf("bind admin port %d: %w", s.cfg.AdminPort, err)
	}
	s.adminLn = ln
	go s.serve(s.admin, ln, "admin")
	return nil
}

// StartMock binds and serves the mock listener.
func (s *Server) StartMock() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":"+strconv.Itoa(s.cfg.Port))
	if err != nil {
		return fmt.Errorf("bind mock port %d: %w", s.cfg.Port, err)
	}
	s.mockLn = ln
	go func() {
		if s.cfg.TLSEnabled() {
			s.serveTLS(s.mock, ln)
			return
		}
		s.serve(s.mock, ln, "mock")
	}()
	return nil
}

func (s *Server) serve(srv *http.Server, ln net.Listener, name string) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.Error("listener stopped", "listener", name, "error", err)
	}
}

func (s *Server) serveTLS(srv *http.Server, ln net.Listener) {
	err := srv.ServeTLS(ln, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.Error("listener stopped", "listener", "mock", "error", err)
	}
}

// MockAddr reports the address the mock listener bound to, which lets tests and
// the E2E harness run on port 0.
func (s *Server) MockAddr() string { return addrOf(s.mockLn) }

// AdminAddr reports the address the admin listener bound to.
func (s *Server) AdminAddr() string { return addrOf(s.adminLn) }

func addrOf(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}

// Shutdown performs the drain sequence of SPEC §4.5: readiness is dropped
// first, the drain window lets endpoint removal propagate through Kubernetes,
// and only then are the listeners closed.
func (s *Server) Shutdown(ctx context.Context) error {
	s.SetReady(false)

	if drain := s.cfg.ShutdownDrain.D(); drain > 0 {
		s.log.Info("draining", "duration", drain.String())
		timer := time.NewTimer(drain)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout.D())
	defer cancel()

	var errs []error
	if err := s.mock.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("mock listener: %w", err))
	}
	if err := s.admin.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("admin listener: %w", err))
	}
	return errors.Join(errs...)
}

// recoverMiddleware turns a panic in an admin handler into a 500 rather than
// taking the process down with it.
func recoverMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic serving admin request", "path", r.URL.Path, "panic", v)
				http.Error(w, `{"errors":[{"code":10,"title":"Internal error"}]}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
