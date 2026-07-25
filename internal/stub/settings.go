// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/b3vet/mockulus/internal/wmcompat"
)

// supportedSettingsFields lists the keys a settings write may carry. WireMock's
// other settings — `proxyPassThrough` and its extended block — are deliberately
// absent: accepting a knob and then ignoring it is exactly the
// accept-and-behave-differently failure the error catalog exists to prevent, so
// they are refused by name (SPEC §5.1, P3).
var supportedSettingsFields = map[string]bool{
	"fixedDelay": true, "delayDistribution": true,
}

// Settings is the compiled global settings document (SPEC §5.1). The global
// response delay is the whole of it, which is why there is nothing else here.
type Settings struct {
	// FixedDelay is what a matched response waits out when its own stub did not
	// declare a fixed delay.
	FixedDelay time.Duration
	// Delay is the distribution sampled per matched response, again only when
	// the stub declared none of its own.
	Delay DelayDistribution
}

// IsZero reports whether these settings ask for nothing at all. It is what lets
// a deployment nobody has configured carry no settings on its snapshot, so the
// serve path skips the composition entirely (P2, P4).
func (s Settings) IsZero() bool { return s.FixedDelay == 0 && s.Delay.Kind == DelayNone }

// CompileSettings validates a settings document and compiles the global delay
// it asks for. Like every other 422 in the product it collects every problem
// rather than stopping at the first, so a caller fixes one document once.
func CompileSettings(raw []byte) (Settings, *wmcompat.ErrorList) {
	var out Settings

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs := &wmcompat.ErrorList{}
		errs.Add(wmcompat.NewError(wmcompat.CodeMalformed, "settings must be a JSON object"))
		return out, errs
	}

	errs := &wmcompat.ErrorList{}
	// Sorted, so a document with several unknown keys reports them in an order
	// that does not depend on map iteration.
	for _, field := range sortedKeys(doc) {
		if !supportedSettingsFields[field] {
			errs.Addf(wmcompat.CodeUnknownSetting, "/"+field,
				"unknown setting "+field+"; mockulus supports fixedDelay and delayDistribution")
		}
	}

	if v, ok := doc["fixedDelay"]; ok && !isNull(v) {
		var ms int64
		if err := json.Unmarshal(v, &ms); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/fixedDelay",
				"fixedDelay must be a whole number of milliseconds")
		} else if ms < 0 {
			// WireMock accepts a negative global delay and then never waits;
			// refusing it names the mistake instead of silently doing nothing.
			errs.Addf(wmcompat.CodeMalformed, "/fixedDelay", "fixedDelay must not be negative")
		} else {
			out.FixedDelay = time.Duration(ms) * time.Millisecond
		}
	}

	if v, ok := doc["delayDistribution"]; ok && !isNull(v) {
		out.Delay = parseDelayDistribution(errs, v, "/delayDistribution")
	}

	if !errs.Empty() {
		return Settings{}, errs
	}
	return out, nil
}

// isNull reports whether a raw value is JSON null. An explicit null is how a
// client clears one setting while leaving the other alone, and WireMock reads
// it as absence rather than as a malformed value.
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
