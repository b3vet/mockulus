// SPDX-License-Identifier: Apache-2.0

package match

import (
	"crypto/sha256"
	"sync"

	"github.com/b3vet/mockulus/internal/stub"
)

// compileCache makes a rebuild cost what *changed* rather than what exists.
//
// Convergence is level-triggered: any epoch change reloads the whole store
// state (SPEC §8). That is deliberately simple and has no missed-event class of
// bug, but it would mean recompiling every stub on every change — at 10k stubs,
// for one edit.
//
// The cache closes that gap. A document whose content is byte-identical to one
// already compiled reuses the existing CompiledStub, pointer and all, including
// its inlined response body. Two consecutive snapshots therefore share memory
// for every unchanged stub, and both rebuild CPU and transient memory growth
// scale with the number of changed documents (SPEC §6.2, gate S7).
type compileCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	// hits and misses are reported after each rebuild, so the cache's value is
	// observable rather than assumed.
	hits, misses int
}

type cacheEntry struct {
	// hash is the digest of the document the stub was compiled from.
	hash [sha256.Size]byte
	stub *stub.CompiledStub
}

func newCompileCache() *compileCache {
	return &compileCache{entries: map[string]cacheEntry{}}
}

// get returns the compiled form of a document if it is unchanged since the last
// build.
//
// The key is the stub id and the value is checked against the document's hash,
// so an edit under the same id is a miss rather than a stale hit.
func (c *compileCache) get(id string, doc []byte) (*stub.CompiledStub, bool) {
	hash := sha256.Sum256(doc)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[id]
	if !ok || entry.hash != hash {
		c.misses++
		return nil, false
	}
	c.hits++
	return entry.stub, true
}

// put records a freshly compiled stub.
func (c *compileCache) put(id string, doc []byte, cs *stub.CompiledStub) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = cacheEntry{hash: sha256.Sum256(doc), stub: cs}
}

// retain drops every entry not in the given set, so deleted stubs do not hold
// their compiled form — and their response bodies — alive forever.
func (c *compileCache) retain(live map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.entries {
		if !live[id] {
			delete(c.entries, id)
		}
	}
}

// stats returns and resets the hit and miss counts for one rebuild.
func (c *compileCache) stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hits, misses = c.hits, c.misses
	c.hits, c.misses = 0, 0
	return hits, misses
}

// size reports how many compiled stubs are being held.
func (c *compileCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
