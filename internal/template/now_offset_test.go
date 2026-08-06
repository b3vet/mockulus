// SPDX-License-Identifier: Apache-2.0

package template

import (
	"testing"
	"time"
)

// The helper end to end, so a parsed offset that never reaches the instant is
// caught: twelve months on is the same date one year later, whatever today is.
// A 365-day year renders two or three days short of it, and an offset parsed
// and dropped renders today.
func TestNowHelperShiftsTwelveMonthsByAWholeYear(t *testing.T) {
	today, err := nowHelper(nil, map[string]any{"format": "yyyy-MM-dd"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := time.Parse("2006-01-02", today.(string))
	if err != nil {
		t.Fatal(err)
	}
	// 29 February is the one date in the year that has no anniversary, and the
	// clamp that handles it is asserted above rather than here.
	if base.Month() == time.February && base.Day() == 29 {
		t.Skip("a leap day has no same-date anniversary")
	}

	out, err := nowHelper(nil, map[string]any{"offset": "12 months", "format": "yyyy-MM-dd"})
	if err != nil {
		t.Fatal(err)
	}

	want := base.AddDate(1, 0, 0).Format("2006-01-02")
	if out != want {
		t.Errorf("{{now offset='12 months'}} rendered %v, want %s", out, want)
	}
}
