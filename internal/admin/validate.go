// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"net/http"

	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// ValidationReport is the answer to POST /__admin/mockulus/validate (SPEC
// §5.7.2): what an import of this batch would have refused, with nothing
// written.
type ValidationReport struct {
	// Valid is the conjunction over Results.
	Valid bool `json:"valid"`
	// WouldImport is the batch verdict, and is false whenever any mapping is
	// invalid. It is stated separately from Valid rather than inferred from it
	// because the two answer different questions, and only one of them is the
	// one a caller about to run an import is asking.
	WouldImport bool `json:"wouldImport"`
	Summary     struct {
		Total   int `json:"total"`
		Valid   int `json:"valid"`
		Invalid int `json:"invalid"`
	} `json:"summary"`
	Results []ValidationResult `json:"results"`
}

// ValidationResult is one mapping's verdict.
type ValidationResult struct {
	// Index is the position in the submitted array. It is how a caller joins a
	// result back onto its input: Id may be absent, and content is not a key.
	Index int `json:"index"`
	// ID is present only for a mapping that carried one.
	ID    string `json:"id,omitempty"`
	Valid bool   `json:"valid"`
	// Errors is exactly what a real registration would have answered for this
	// mapping — same codes, same titles, same pointers, produced by the same
	// validation. Absent when Valid.
	Errors []wmcompat.Error `json:"errors,omitempty"`
}

// validateMappings answers what an import would do, and does none of it.
//
// The endpoint exists because the cost of asking today is a deployment: a team
// holding a WireMock mappings directory can either register the stubs somewhere
// and read the refusals, or read the compatibility matrix by hand. The first
// mutates a deployment that is usually shared.
//
// Three properties are load-bearing and each is a decision rather than a
// consequence.
//
// It answers 200 even when every mapping is invalid. The call was asked what
// would happen and it said, which is a success; a 422 would put an ordinary
// result behind an error path in every client and make a partial report
// unreadable. Non-2xx is reserved for the reasons any endpoint has — an
// unreadable body, a missing envelope, a failed token check.
//
// It reports two verdicts. Import is atomic (deviation #21), so one bad mapping
// in fifty writes nothing; a per-mapping list alone would let a reader count
// forty-nine passes and conclude the file was mostly fine.
//
// It calls stub.Compile with the handler's own options — the same call
// importMappings makes — rather than reimplementing validation. A validator that
// can disagree with the registrar is worse than no validator, because it is
// believed.
//
// Nothing here touches the store, so it answers normally while the deployment is
// degraded. That is deliberate: assessing a mappings file is exactly the sort of
// thing somebody does during an outage.
func (h *Handler) validateMappings(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req importRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed,
			"validate body must be an object with a mappings array: "+err.Error()))
		return
	}
	if req.Mappings == nil {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeMalformed, "/mappings",
			"validate needs a mappings array"))
		return
	}

	report := ValidationReport{
		Valid:   true,
		Results: make([]ValidationResult, 0, len(req.Mappings)),
	}
	for i, mapping := range req.Mappings {
		result := ValidationResult{Index: i, Valid: true}

		compiled, errs := stub.Compile(mapping, 0, h.stubOpts)
		switch {
		case errs != nil:
			result.Valid = false
			// The pointers stay rooted at the mapping rather than being
			// rewritten to /mappings/<i>/… the way import rewrites them. Import
			// has to, because its errors arrive in one flat envelope with
			// nothing else to say which mapping they belong to. Here the result
			// carries the index, so a pointer rooted at the mapping is the one
			// that can be handed straight to an editor showing that document.
			result.Errors = errs.Errors()
		default:
			result.ID = compiled.ID
		}

		if result.Valid {
			report.Summary.Valid++
		} else {
			report.Summary.Invalid++
			report.Valid = false
		}
		report.Results = append(report.Results, result)
	}
	report.Summary.Total = len(report.Results)
	// Atomic import: anything invalid means nothing is written.
	report.WouldImport = report.Valid

	wmcompat.WriteJSON(w, http.StatusOK, report)
}
