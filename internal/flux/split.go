package flux

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// Handles the common Flux form: range(start: <expr>, stop: now())
	// and avoids naive matching that breaks on nested parentheses in now().
	rangeNowPattern = regexp.MustCompile(`range\s*\(\s*start\s*:\s*([^,]+?)\s*,\s*stop\s*:\s*now\s*\(\s*\)\s*\)`)
	// Grafana Flux typically uses v.timeRangeStop.
	rangeVarStopPattern = regexp.MustCompile(`range\s*\(\s*start\s*:\s*([^,]+?)\s*,\s*stop\s*:\s*v\.timeRangeStop\s*\)`)
	// Handles absolute stop instants with either bare RFC3339 or time(v: "...").
	rangeAbsStopPattern = regexp.MustCompile(`range\s*\(\s*start\s*:\s*([^,]+?)\s*,\s*stop\s*:\s*((?:time\s*\(\s*v\s*:\s*"[^"]+"\s*\))|(?:time\s*\(\s*v\s*:\s*'[^']+'\s*\))|(?:"?\d{4}-\d{2}-\d{2}T[0-9:\.\+\-]+Z"?))\s*\)`)
	timeWithStringArg = regexp.MustCompile(`^time\s*\(\s*v\s*:\s*("([^"]+)"|'([^']+)')\s*\)$`)
)

func SplitNowQuery(flux string, cutoff time.Time) (cached string, live string, ok bool) {
	if m := rangeNowPattern.FindStringSubmatch(flux); len(m) >= 2 {
		cached, live = buildSplitQueries(flux, m[0], m[1], cutoffExpr(cutoff), "now()")
		return cached, live, true
	}

	if m := rangeVarStopPattern.FindStringSubmatch(flux); len(m) >= 2 {
		cached, live = buildSplitQueries(flux, m[0], m[1], cutoffExpr(cutoff), "v.timeRangeStop")
		return cached, live, true
	}

	// Support Grafana/influx plugin payloads where stop is already expanded to an absolute time.
	if m := rangeAbsStopPattern.FindStringSubmatch(flux); len(m) >= 3 {
		stop, err := parseFluxTimeExpr(m[2])
		if err != nil || !cutoff.Before(stop) {
			return "", "", false
		}
		startExpr := strings.TrimSpace(m[1])
		cachedStart := startExpr
		if start, err := parseFluxTimeExpr(startExpr); err == nil && start.Before(stop) {
			window := stop.Sub(start)
			if window > 0 {
				cachedStart = cutoffExpr(cutoff.Add(-window))
			}
		}
		cached, live = buildSplitQueries(flux, m[0], cachedStart, cutoffExpr(cutoff), strings.TrimSpace(m[2]))
		return cached, live, true
	}

	return "", "", false
}

func buildSplitQueries(flux, fullRangeMatch, startExpr, cachedStopExpr, liveStopExpr string) (string, string) {
	cachedRange := fmt.Sprintf("range(start: %s, stop: %s)", strings.TrimSpace(startExpr), cachedStopExpr)
	liveRange := fmt.Sprintf("range(start: %s, stop: %s)", cachedStopExpr, liveStopExpr)
	return strings.Replace(flux, fullRangeMatch, cachedRange, 1), strings.Replace(flux, fullRangeMatch, liveRange, 1)
}

func cutoffExpr(cutoff time.Time) string {
	return fmt.Sprintf("time(v: %q)", cutoff.UTC().Format(time.RFC3339Nano))
}

func parseFluxTimeExpr(expr string) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	if m := timeWithStringArg.FindStringSubmatch(expr); len(m) > 0 {
		ts := m[2]
		if ts == "" {
			ts = m[3]
		}
		return time.Parse(time.RFC3339Nano, ts)
	}
	expr = strings.Trim(expr, `"`)
	return time.Parse(time.RFC3339Nano, expr)
}
