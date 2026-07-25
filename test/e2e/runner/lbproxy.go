// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

// LBProxy is topology T3's stand-in for a ClusterIP Service: one address in
// front of every replica, handing each request to the next one (SPEC §19.4).
//
// It is per-request round-robin rather than per-connection, and that is the
// whole point. "Any replica serves any request and any admin call" is the claim
// the distributed design rests on, and a proxy that pinned a client to a
// replica would let a broken replica pass the suite for as long as no case
// happened to land on it. Here every step of every unpinned case lands
// somewhere else, so the claim is under test continuously instead of being
// asserted once by a case written for it.
type LBProxy struct {
	// Addr is the proxy's own base URL, which is what cases talk to.
	Addr string

	listener net.Listener
	server   *http.Server
	next     atomic.Uint64
}

// StartLBProxy fronts the given backend base URLs on an ephemeral local port.
// The transport is the replicas' own, so a proxied request reaches them exactly
// as a direct one would.
func StartLBProxy(backends []string, transport http.RoundTripper) (*LBProxy, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("a load balancer needs at least one replica behind it")
	}

	targets := make([]*url.URL, 0, len(backends))
	for _, base := range backends {
		u, err := url.Parse(base)
		if err != nil {
			return nil, fmt.Errorf("replica address %q: %w", base, err)
		}
		targets = append(targets, u)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	p := &LBProxy{Addr: "http://" + listener.Addr().String(), listener: listener}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			// Add(1)-1 rather than a plain increment so the first request lands
			// on replica 0: a case that pins nothing still starts where a reader
			// of the transcript expects it to.
			r.SetURL(targets[(p.next.Add(1)-1)%uint64(len(targets))])
		},
		// A replica that cannot be reached must surface as an error the case can
		// read, not as the proxy's generic 502 with the reason on the runner's
		// stderr where nothing collects it.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "the harness load balancer could not reach a replica: %v", err)
		},
	}

	p.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

// Stop closes the proxy.
func (p *LBProxy) Stop() error {
	if p.server == nil {
		return nil
	}
	return p.server.Close()
}
