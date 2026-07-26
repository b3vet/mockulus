// SPDX-License-Identifier: Apache-2.0

package couchbase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"

	"github.com/b3vet/mockulus/internal/store"
)

// Scenario state is the one piece of mutable state a mock request may read
// (SPEC §9, D6). It lives in Couchbase rather than in each pod because the
// alternative — per-pod state behind a load balancer — gives wrong answers as
// soon as there is more than one replica.
//
// Transitions use compare-and-swap so two pods serving the same scenario
// concurrently cannot both advance it. The CAS token is the SDK's, passed
// through the store interface as an opaque number.

// GetScenario returns a scenario's stored state and its CAS token.
func (s *Store) GetScenario(ctx context.Context, name string) (store.ScenarioState, store.CAS, error) {
	res, err := s.scenarios.Get(keyPrefixScenario+name, &gocb.GetOptions{
		Context: ctx,
		// Scenario reads are on the request path, so they get a tighter budget
		// than other store operations: a sick node must not stall a response
		// beyond scenario_kv_timeout (SPEC §7.2).
		Timeout: s.scenarioTimeout(ctx),
	})
	if err != nil {
		return store.ScenarioState{}, 0, wrap(err)
	}

	var state store.ScenarioState
	if err := res.Content(&state); err != nil {
		return store.ScenarioState{}, 0, err
	}
	return state, store.CAS(res.Cas()), nil
}

// InsertScenario creates a state document, failing if one already exists.
func (s *Store) InsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	_, err := s.scenarios.Insert(keyPrefixScenario+name, state, &gocb.InsertOptions{
		Context: ctx, Timeout: s.scenarioTimeout(ctx), DurabilityLevel: s.durability,
	})
	return wrap(err)
}

// ReplaceScenario overwrites a state document only if its CAS still matches,
// which is what makes a transition safe when several pods serve the same
// scenario at once.
func (s *Store) ReplaceScenario(ctx context.Context, name string, state store.ScenarioState, cas store.CAS) error {
	_, err := s.scenarios.Replace(keyPrefixScenario+name, state, &gocb.ReplaceOptions{
		Context: ctx, Timeout: s.scenarioTimeout(ctx),
		DurabilityLevel: s.durability,
		Cas:             gocb.Cas(cas),
	})
	return wrap(err)
}

// UpsertScenario writes unconditionally, for a transition with no gate.
func (s *Store) UpsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	_, err := s.scenarios.Upsert(keyPrefixScenario+name, state, &gocb.UpsertOptions{
		Context: ctx, Timeout: s.scenarioTimeout(ctx), DurabilityLevel: s.durability,
	})
	return wrap(err)
}

// DeleteAllScenarios clears every state document, so every scenario reads back
// as Started (SPEC §9.4).
func (s *Store) DeleteAllScenarios(ctx context.Context) error {
	return s.deleteWhere(ctx, collScenarios)
}

// ListScenarioStates returns every stored state by scenario name.
func (s *Store) ListScenarioStates(ctx context.Context) (map[string]store.ScenarioState, error) {
	raw, err := s.loadCollection(ctx, s.scenarios, collScenarios)
	if err != nil {
		return nil, err
	}

	out := make(map[string]store.ScenarioState, len(raw))
	for id, content := range raw {
		var state store.ScenarioState
		if err := decodeInto(content, &state); err != nil {
			s.log.Warn("stored scenario state does not decode; skipping", "key", id, "error", err)
			continue
		}
		out[strings.TrimPrefix(id, keyPrefixScenario)] = state
	}
	return out, nil
}

// scenarioTimeout returns the request-path budget for a scenario operation,
// honouring a caller deadline that is already tighter.
func (s *Store) scenarioTimeout(ctx context.Context) time.Duration {
	budget := s.scenarioKVTimeout
	if budget <= 0 {
		budget = s.kvTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < budget {
			return remaining
		}
	}
	return budget
}

// IsCASMismatch reports whether an error is a lost CAS race, which the
// transition logic retries rather than treating as a failure.
func IsCASMismatch(err error) bool {
	return errors.Is(err, gocb.ErrCasMismatch) || errors.Is(err, gocb.ErrDocumentExists)
}
