// SPDX-License-Identifier: Apache-2.0

// Package scenario implements stateful mocks: the state machine a stub can be
// gated on and advanced by (SPEC §9).
//
// The design follows from one fact: state that lives in a pod gives wrong
// answers behind a load balancer. Two replicas would each believe the scenario
// was somewhere different, and which answer a test got would depend on which
// pod it reached. So state lives in the store, transitions use compare-and-swap,
// and the cost is one key-value read — paid only by requests that actually
// match a stub in a scenario (D6, P2).
package scenario

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
)

// Started is the initial state of every scenario (SPEC §9.1). A scenario with
// no stored document reads as this rather than as an error, so a scenario comes
// into being simply by a stub mentioning it.
const Started = "Started"

// casAttempts bounds a transition's retries. A scenario is a functional-test
// construct, not a contended counter: if three attempts lose the race, another
// pod has moved the state and this transition no longer applies.
const casAttempts = 3

// Client reads and advances scenario state.
type Client struct {
	store   store.ScenarioStore
	log     *slog.Logger
	metrics *metrics.Metrics
	timeout time.Duration
}

// NewClient wires a client to the scenario store.
func NewClient(st store.ScenarioStore, log *slog.Logger, m *metrics.Metrics, timeout time.Duration) *Client {
	return &Client{store: st, log: log, metrics: m, timeout: timeout}
}

// State returns a scenario's current state, treating an absent document as
// Started.
func (c *Client) State(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	c.metrics.ScenarioReads.Inc()

	state, _, err := c.store.GetScenario(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return Started, nil
	}
	if err != nil {
		return "", err
	}
	if state.State == "" {
		return Started, nil
	}
	return state.State, nil
}

// Transition advances a scenario after a stub has served.
//
// requiredState is the gate the matching stub declared, or empty for an
// unconditional transition. The gate is re-checked under CAS because matching
// and transitioning are not atomic across pods: another replica may have moved
// the state in between, in which case this transition no longer applies and is
// skipped rather than forced (SPEC §9.3).
//
// A failed transition never fails the response. WireMock's transition is
// fire-and-forget within one process; the distributed equivalent is to serve
// what was matched and record that the move did not happen.
func (c *Client) Transition(ctx context.Context, name, requiredState, newState string) error {
	if newState == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	for attempt := range casAttempts {
		if attempt > 0 {
			c.metrics.ScenarioCASRetries.Inc()
		}

		current, cas, err := c.store.GetScenario(ctx, name)
		missing := errors.Is(err, store.ErrNotFound)
		if err != nil && !missing {
			return err
		}

		state := Started
		if !missing && current.State != "" {
			state = current.State
		}

		if requiredState != "" && state != requiredState {
			// Another pod transitioned after this request's match-time gate
			// passed. WireMock has no cross-node race to have semantics for;
			// ours is to skip the transition and count it.
			c.metrics.ScenarioTransitionConfl.Inc()
			c.log.Debug("scenario transition skipped: another replica moved the state first",
				"scenario", name, "expected", requiredState, "found", state, "target", newState)
			return nil
		}

		next := store.ScenarioState{State: newState, UpdatedAt: time.Now().UTC()}

		if missing {
			err = c.store.InsertScenario(ctx, name, next)
		} else {
			err = c.store.ReplaceScenario(ctx, name, next, cas)
		}
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		// Lost the race; re-read and try again.
	}

	c.metrics.ScenarioTransitionConfl.Inc()
	c.log.Warn("scenario transition abandoned after repeated conflicts",
		"scenario", name, "target", newState, "attempts", casAttempts)
	return nil
}

// isRetryable reports whether a write failed because another writer won, as
// opposed to the store being broken.
//
// The check is on the error text rather than a typed error because the sentinel
// belongs to the driver and this package is written against the interface. The
// memory driver and the Couchbase driver both report a lost race this way.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "cas mismatch") ||
		contains(msg, "already exists") ||
		contains(msg, "document exists")
}

func contains(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// ResetAll clears every stored state, so every scenario reads as Started.
func (c *Client) ResetAll(ctx context.Context) error {
	return c.store.DeleteAllScenarios(ctx)
}

// States returns every stored state by scenario name.
func (c *Client) States(ctx context.Context) (map[string]store.ScenarioState, error) {
	return c.store.ListScenarioStates(ctx)
}

// SetState writes a scenario's state directly, backing the admin endpoint.
func (c *Client) SetState(ctx context.Context, name, state string) error {
	return c.store.UpsertScenario(ctx, name, store.ScenarioState{
		State:     state,
		UpdatedAt: time.Now().UTC(),
	})
}
