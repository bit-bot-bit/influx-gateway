package flux

import (
	"strings"
	"testing"
	"time"
)

func TestSplitNowQuery_WithNowStop(t *testing.T) {
	cutoff := time.Date(2026, 2, 20, 6, 10, 0, 0, time.UTC)
	q := `from(bucket: "metrics") |> range(start: -30m, stop: now()) |> filter(fn: (r) => r._measurement == "http_requests")`
	cached, live, ok := SplitNowQuery(q, cutoff)
	if !ok {
		t.Fatalf("expected split")
	}
	if !strings.Contains(cached, `stop: time(v: "2026-02-20T06:10:00Z")`) {
		t.Fatalf("cached query not rewritten: %s", cached)
	}
	if !strings.Contains(live, `range(start: time(v: "2026-02-20T06:10:00Z"), stop: now())`) {
		t.Fatalf("live query not rewritten: %s", live)
	}
}

func TestSplitNowQuery_WithGrafanaTimeRangeStop(t *testing.T) {
	cutoff := time.Date(2026, 2, 20, 6, 10, 0, 0, time.UTC)
	q := `from(bucket: "metrics") |> range(start: v.timeRangeStart, stop: v.timeRangeStop) |> filter(fn: (r) => r._measurement == "cpu")`
	cached, live, ok := SplitNowQuery(q, cutoff)
	if !ok {
		t.Fatalf("expected split")
	}
	if !strings.Contains(cached, `stop: time(v: "2026-02-20T06:10:00Z")`) {
		t.Fatalf("cached query not rewritten: %s", cached)
	}
	if !strings.Contains(live, `stop: v.timeRangeStop`) {
		t.Fatalf("live query should keep v.timeRangeStop: %s", live)
	}
}

func TestSplitNowQuery_WithAbsoluteTimes(t *testing.T) {
	cutoff := time.Date(2026, 2, 20, 6, 10, 0, 0, time.UTC)
	q := `from(bucket: "metrics") |> range(start: 2026-02-20T05:40:29Z, stop: 2026-02-20T06:10:29Z) |> count()`
	cached, live, ok := SplitNowQuery(q, cutoff)
	if !ok {
		t.Fatalf("expected split")
	}
	if !strings.Contains(cached, `range(start: time(v: "2026-02-20T05:40:00Z"), stop: time(v: "2026-02-20T06:10:00Z"))`) {
		t.Fatalf("cached query not normalized as expected: %s", cached)
	}
	if !strings.Contains(live, `range(start: time(v: "2026-02-20T06:10:00Z"), stop: 2026-02-20T06:10:29Z)`) {
		t.Fatalf("live query not rewritten as expected: %s", live)
	}
}

