package command

import (
	"errors"
	"io"
)

// Recorder is implemented by the root execution boundary, which owns the final envelope.
type Recorder interface {
	RecordResult(any)
	WriteJSON(any) error
}

func Record(w io.Writer, data any) {
	if output, ok := w.(Recorder); ok {
		output.RecordResult(data)
	}
}
func JSON(w io.Writer, data any) error {
	output, ok := w.(Recorder)
	if !ok {
		return errors.New("JSON output requires the command result boundary")
	}
	return output.WriteJSON(data)
}
