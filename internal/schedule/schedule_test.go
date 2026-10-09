package schedule

import (
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Ljubljana")
	// Friday 2026-10-09 07:00 in Ljubljana (05:00 UTC)
	now := time.Date(2026, 10, 9, 7, 0, 30, 0, loc)
	r := Rule{Enabled: true, Time: "07:00", TZ: "Europe/Ljubljana", Action: "unload-all"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if !r.Due(now.UTC()) {
		t.Error("should be due (time zone conversion)")
	}
	r.Days = []int{1, 2, 3, 4} // Mon–Thu
	if r.Due(now) {
		t.Error("not due on Friday")
	}
	r.Days = []int{5}
	r.LastRun = now.Add(-30 * time.Second)
	if r.Due(now) {
		t.Error("already ran this minute")
	}
	if (&Rule{Time: "7am", TZ: "UTC", Action: "unload-all"}).Validate() == nil {
		t.Error("bad time accepted")
	}
}
