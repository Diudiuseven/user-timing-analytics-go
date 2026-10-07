package engine

import (
	"github.com/Diudiuseven/user-timing-analytics-go/timesync"
)

func newTestNormalizer(e *Engine, sessionID string) *timesync.Normalizer {
	off := timesync.EstimateOffset(e.state.ClockDeltas[sessionID])
	return timesync.NewNormalizer(off, e.cfg.MaxClockJumpMs)
}
