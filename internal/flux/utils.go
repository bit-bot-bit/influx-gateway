package flux

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

func ParseFluxJSONBody(body []byte) (string, func(string) ([]byte, error), bool) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, false
	}
	rawQuery, ok := payload["query"]
	if !ok {
		return "", nil, false
	}
	var flux string
	if err := json.Unmarshal(rawQuery, &flux); err != nil || strings.TrimSpace(flux) == "" {
		return "", nil, false
	}
	builder := func(nextFlux string) ([]byte, error) {
		clone := make(map[string]json.RawMessage, len(payload))
		for k, v := range payload {
			clone[k] = v
		}
		b, err := json.Marshal(nextFlux)
		if err != nil {
			return nil, err
		}
		clone["query"] = b
		return json.Marshal(clone)
	}
	return flux, builder, true
}

func QuerySignature(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:8])
}

func QueryPreview(query string, max int) string {
	query = strings.TrimSpace(strings.Join(strings.Fields(query), " "))
	if max <= 0 || len(query) <= max {
		return query
	}
	return query[:max] + "..."
}

func SplitCutoff(now time.Time, freshnessWindow time.Duration) time.Time {
	if freshnessWindow <= 0 {
		return now
	}
	// Quantize to window boundaries so repeated queries share the same cached segment key.
	return now.UTC().Truncate(freshnessWindow)
}
