package engine

import (
	"encoding/json"

	"github.com/Diudiuseven/user-timing-analytics-go/aggregate"
)

func dump(ws []*aggregate.WindowResult) string {
	data, _ := json.MarshalIndent(ws, "", "  ")
	return string(data)
}

// normalizedStarts re-runs the engine's group normalization for a session
// and returns corrected start timestamps in client-input order, enabling
// direct assertions on rollback clamping.
func normalizedStarts(e *Engine, sessionID string, clientStarts []int64) []int64 {
	norm := newTestNormalizer(e, sessionID)
	out := make([]int64, len(clientStarts))
	for i, c := range clientStarts {
		s, _, _ := norm.Normalize(c)
		out[i] = s
	}
	return out
}
