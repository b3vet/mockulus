// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/b3vet/mockulus/internal/scenario"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Scenario existence is derived from the stubs, not stored: a scenario exists
// because some stub mentions it, and its possible states are the ones those
// stubs name. Only the *current* state is persisted (SPEC §9.1, §9.4). That is
// what makes a scenario disappear when the last stub referencing it is deleted,
// with no separate lifecycle to keep in step.

// scenarioView is the shape the admin API reports.
type scenarioView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	State          string   `json:"state"`
	PossibleStates []string `json:"possibleStates"`
}

// listScenarios merges the snapshot-derived definitions with the stored states.
func (h *Handler) listScenarios(w http.ResponseWriter, r *http.Request) {
	defined := h.engine.Snapshot().Scenarios()

	stored := map[string]string{}
	if h.scenarios != nil {
		states, err := h.scenarios.States(r.Context())
		if err != nil {
			h.storeError(w, "list_scenarios", err)
			return
		}
		for name, state := range states {
			stored[name] = state.State
		}
	}

	names := make([]string, 0, len(defined))
	for name := range defined {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]scenarioView, 0, len(names))
	for _, name := range names {
		state := scenario.Started
		if s, ok := stored[name]; ok && s != "" {
			state = s
		}
		views = append(views, scenarioView{
			// A scenario has no identity beyond its name, so the id is the name.
			ID:             name,
			Name:           name,
			State:          state,
			PossibleStates: defined[name],
		})
	}

	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"scenarios": views})
}

// resetScenarios clears every stored state, so all scenarios read as Started.
func (h *Handler) resetScenarios(w http.ResponseWriter, r *http.Request) {
	if h.scenarios == nil {
		wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	if err := h.scenarios.ResetAll(r.Context()); err != nil {
		h.storeError(w, "reset_scenarios", err)
		return
	}
	h.log.Info("all scenarios reset to Started")
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
}

// setScenarioState puts a scenario into a named state.
//
// Both the scenario and the state are validated against what the stubs define.
// Accepting an arbitrary state would let a test drive a scenario somewhere no
// stub can match, which looks like a mockulus bug rather than a typo.
func (h *Handler) setScenarioState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	defined := h.engine.Snapshot().Scenarios()
	states, known := defined[name]
	if !known {
		wmcompat.WriteErrors(w, http.StatusNotFound,
			wmcompat.NewError(wmcompat.CodeScenarioInvalid,
				"no stub defines a scenario named "+name))
		return
	}

	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.State == "" {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeScenarioInvalid, "/state",
			`the body must be {"state": "..."}`))
		return
	}

	if !slicesContains(states, body.State) {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeScenarioInvalid, "/state",
			"no stub in scenario "+name+" uses the state "+body.State))
		return
	}

	if h.scenarios == nil {
		wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	if err := h.scenarios.SetState(r.Context(), name, body.State); err != nil {
		h.storeError(w, "set_scenario_state", err)
		return
	}

	h.log.Info("scenario state set", "scenario", name, "state", body.State)
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
