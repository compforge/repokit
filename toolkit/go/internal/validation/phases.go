package validation

import (
	"context"
	"log/slog"
	"time"
)

// Phase is wall time spent in one sequential validation stage of this invocation.
// It is not a sum of concurrent checks or historical runner durations.
type Phase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// +spec=Phase observations measure actual sequential boundaries, including early returns; they never contribute to validation identity or reuse.
// +link=component://docs/validation.md
type phaseRecorder struct {
	ctx     context.Context
	phases  []Phase
	name    string
	started time.Time
}

func (r *phaseRecorder) start(name string) {
	r.finish()
	r.name, r.started = name, time.Now()
}

func (r *phaseRecorder) finish() {
	if r.name == "" {
		return
	}
	seconds := time.Since(r.started).Seconds()
	r.phases = append(r.phases, Phase{Name: r.name, DurationSeconds: seconds})
	slog.DebugContext(r.ctx, "validation phase finished", "phase", r.name, "duration_seconds", seconds)
	r.name = ""
}
