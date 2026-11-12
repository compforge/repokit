package command

import (
	"errors"
	"io"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Phase    string `json:"phase"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Version  string `json:"version,omitempty"`
	Evidence string `json:"evidence,omitempty"` // JSON Pointer within this result.
	Hint     string `json:"hint,omitempty"`
}

// Phase is current-invocation elapsed time; it does not charge historical execution.
type Phase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// ResultFailure signals a nonzero exit when the recorded report already owns the diagnosis.
type ResultFailure struct{ Message string }

func (e *ResultFailure) Error() string { return e.Message }

func ExecutionFailure(err error) error {
	var reported *ResultFailure
	if errors.As(err, &reported) {
		return err
	}
	return errs.Wrap(err, "execution.failed", "execution", "")
}

func AddDiagnostic(w io.Writer, d Diagnostic) {
	if output, ok := w.(interface{ RecordDiagnostic(Diagnostic) }); ok {
		output.RecordDiagnostic(d)
	}
}

func MeasurePhase(w io.Writer, name string) func() {
	start := time.Now()
	return func() {
		if output, ok := w.(interface{ RecordPhase(Phase) }); ok {
			output.RecordPhase(Phase{Name: name, DurationSeconds: time.Since(start).Seconds()})
		}
	}
}

func Timing(w io.Writer) (float64, []Phase, bool) {
	if output, ok := w.(interface{ CurrentTiming() (float64, []Phase) }); ok {
		wall, phases := output.CurrentTiming()
		return wall, phases, true
	}
	return 0, nil, false
}
