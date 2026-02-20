package gateway

import (
	"sort"
	"strings"
	"time"
)

type csvRow struct {
	time time.Time
	raw  string
}

func MergeCSV(cachedBody, liveBody []byte) []byte {
	cMeta, cHeader, cRows := parseCSVResponse(string(cachedBody))
	_, lHeader, lRows := parseCSVResponse(string(liveBody))

	header := cHeader
	if header == "" {
		header = lHeader
	}
	if header == "" {
		return liveBody
	}

	seen := map[string]struct{}{}
	rows := make([]csvRow, 0, len(cRows)+len(lRows))

	for _, row := range append(cRows, lRows...) {
		if _, ok := seen[row.raw]; ok {
			continue
		}
		seen[row.raw] = struct{}{}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].time.Before(rows[j].time) })

	out := make([]string, 0, 3+len(rows))
	out = append(out, cMeta...)
	out = append(out, header)
	for _, row := range rows {
		out = append(out, row.raw)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func parseCSVResponse(body string) (meta []string, header string, rows []csvRow) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 {
		return nil, "", nil
	}

	timeCol := -1
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			meta = append(meta, line)
			continue
		}
		if header == "" {
			header = line
			cols := strings.Split(line, ",")
			for i, c := range cols {
				if strings.TrimSpace(c) == "_time" {
					timeCol = i
					break
				}
			}
			continue
		}
		parts := strings.Split(line, ",")
		t := time.Unix(0, 0).UTC()
		if timeCol >= 0 && timeCol < len(parts) {
			parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[timeCol]))
			if err == nil {
				t = parsed
			}
		}
		rows = append(rows, csvRow{time: t, raw: line})
	}

	return meta, header, rows
}
