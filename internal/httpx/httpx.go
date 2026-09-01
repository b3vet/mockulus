// SPDX-License-Identifier: Apache-2.0

// Package httpx derives the connection-level facts about a request — the host
// it was addressed to, the port it arrived on, and whether it came over TLS.
//
// It exists so those three answers have exactly one definition. They are read
// in two places that must never disagree: the `host`, `port` and `scheme`
// request matchers (SPEC §5.2) and the `request.host`, `request.port` and
// `request.scheme` template variables (§10.3). A stub that matched on a host and
// then rendered a different one into its own response would be a bug nobody
// would think to look for, and duplicating six lines is how that happens.
//
// The same reasoning produced internal/javatime during the date/time work.
package httpx

import (
	"net/http"
	"strings"
)

// Host is the host a request was addressed to, without the port.
//
// It is read from the Host header — which is what the client asked for — rather
// than from the listener, because one deployment fronting several virtual hosts
// is the case the matcher exists for and the listener is the same for all of
// them.
//
// An IPv6 literal carries colons inside brackets, so the port is only the part
// after a colon that is not inside them.
func Host(r *http.Request) string {
	host, _ := SplitHostPort(r.Host)
	return host
}

// SplitHostPort separates a Host header into its host and port halves in one
// scan, returning an empty port when the header named none.
//
// The rule is not net.SplitHostPort's: that one errors on a header with no port
// at all, which is the ordinary case, and a criterion must not depend on
// whether the client bothered to spell out `:80`. An IPv6 literal keeps its
// brackets, and the colon inside one is not a port separator — which is what
// the bracket test is for.
func SplitHostPort(h string) (host, port string) {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		return h[:i], h[i+1:]
	}
	return h, ""
}

// Port is the port from the Host header, or empty when the header carries none.
//
// Empty rather than the scheme's default: the header said nothing, and
// answering "80" would be inventing a fact the request did not carry. A stub
// that wants "no port was named" can match the empty string; one that wants the
// default has to say so.
func Port(r *http.Request) string {
	_, port := SplitHostPort(r.Host)
	return port
}

// Scheme is "https" when the request arrived over TLS and "http" otherwise.
//
// It reports what *this process* terminated, which is not always what the
// client used. Behind an ingress or a load balancer that terminates TLS, a
// request the caller sent as https arrives here as plain http and is reported
// as such. Forwarding headers are deliberately not consulted: X-Forwarded-Proto
// is whatever the caller claimed unless every hop is trusted, and a matcher
// that can be steered by a request header is a matcher that decides nothing.
//
// SPEC §12.1 puts TLS on the mock listener only, so a deployment that terminates
// TLS in mockulus itself gets the answer it expects; one that terminates it
// earlier should match on a header its own ingress sets.
func Scheme(r *http.Request) string { return SchemeOf(r.TLS != nil) }

// SchemeOf is Scheme for a caller that has already reduced the request to
// whether this process terminated TLS, so a pooled request need not hold the
// whole *http.Request to answer it later.
func SchemeOf(tls bool) string {
	if tls {
		return "https"
	}
	return "http"
}
