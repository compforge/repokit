package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type diagnostic struct {
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

type artifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"` // absolute path; explicitly requested export, caller-owned retention.
}

type commandResult struct {
	SchemaVersion string        `json:"schemaVersion"`
	Command       string        `json:"command"`
	Outcome       string        `json:"outcome"`
	Summary       string        `json:"summary"`
	Diagnostics   []diagnostic  `json:"diagnostics"`
	Data          any           `json:"data"`
	Artifacts     []artifact    `json:"artifacts"`
	Error         *commandError `json:"error,omitempty"`
	Cost          commandCost   `json:"cost"`
}

// resultOutput retains typed data until the command's final outcome is known.
// +spec=JSON commands emit one final result, including errors and partial data, on stdout
// +why=Serializing in handlers loses results when a later export or maintenance step fails
// +link=../../../../docs/cli-output.md
type resultOutput struct {
	io.Writer
	result     commandResult
	structured bool
	recorded   bool
	writeErr   error
	started    time.Time
	exportPath string
}

func (o *resultOutput) Write(p []byte) (int, error) {
	n, err := o.Writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.writeErr = err
	}
	return n, err
}

func recordResult(w io.Writer, data any) {
	if o, ok := w.(*resultOutput); ok {
		o.recorded = true
		o.result.Data = data
		describeResult(&o.result, data)
	}
}

// JSON reports are recorded at the command boundary and serialized only once the outcome is known.
func writeJSON(w io.Writer, data any) error {
	o, ok := w.(*resultOutput)
	if !ok {
		return errors.New("JSON output requires the command result boundary")
	}
	recordResult(o, data)
	o.structured = true
	return nil
}

func addDiagnostic(w io.Writer, d diagnostic) {
	if o, ok := w.(*resultOutput); ok {
		o.result.Diagnostics = append(o.result.Diagnostics, d)
	}
}

// resultFailure carries only the exit signal; the recorded report already owns
// the detailed diagnosis and must not acquire a second generic failure.
type resultFailure struct{ message string }

func (e *resultFailure) Error() string { return e.message }

func (o *resultOutput) applyError(err error) {
	var reported *resultFailure
	if err == nil || errors.As(err, &reported) {
		return
	}
	var problem *commandError
	if !errors.As(err, &problem) {
		problem = usageFailure(err)
	}
	o.result.Error, o.result.Outcome = problem, problem.outcome
	if !o.recorded {
		o.result.Summary = problem.Message
	} else {
		o.result.Summary += "; " + problem.Message
	}
}

func (o *resultOutput) finish(command string, err error, code int, stderr io.Writer) (int, error) {
	r := &o.result
	// Both destinations receive the same completed result. Delivery is outside measured command cost.
	r.Cost.WallSeconds = time.Since(o.started).Seconds()
	r.SchemaVersion, r.Command = "1.0", command
	if r.Outcome == "" {
		r.Outcome = "succeeded"
	}
	if r.Summary == "" {
		r.Summary = command + " completed"
	}
	if r.Diagnostics == nil {
		r.Diagnostics = []diagnostic{}
	}
	if r.Artifacts == nil {
		r.Artifacts = []artifact{}
	}
	o.applyError(err)
	var data []byte
	if o.exportPath != "" {
		path, exportErr := filepath.Abs(o.exportPath)
		if exportErr == nil {
			r.Artifacts = append(r.Artifacts, artifact{Kind: "command_result", Path: path})
			data, exportErr = encodeResult(r)
			if exportErr == nil {
				exportErr = writeResultFile(path, data)
			}
			if exportErr != nil {
				r.Artifacts = r.Artifacts[:len(r.Artifacts)-1]
			}
		}
		if exportErr != nil {
			problem := operationFailure("output.export_failed", "export", o.exportPath, exportErr)
			if r.Error == nil {
				o.applyError(problem)
			} else {
				// Keep the original terminal error, including unknown remote writes.
				r.Diagnostics = append(r.Diagnostics, diagnostic{Code: "output.export_failed", Severity: "error", Phase: "export", Subject: o.exportPath, Message: exportErr.Error()})
			}
			err = errors.Join(err, problem)
			if code == 0 {
				code = 1
			}
			data = nil
		}
	}
	if !o.structured {
		if err != nil {
			fmt.Fprintln(stderr, "repocli:", err)
		}
		return code, err
	}
	// +spec=Output delivery failure does not imply the underlying action failed or may be safely retried
	var deliveryErr error
	if data == nil {
		data, deliveryErr = encodeResult(r)
	}
	if deliveryErr == nil {
		var n int
		n, deliveryErr = o.Writer.Write(data)
		if deliveryErr == nil && n != len(data) {
			deliveryErr = io.ErrShortWrite
		}
	}
	if deliveryErr != nil {
		o.writeErr = deliveryErr
		fmt.Fprintf(stderr, "repocli: output.write_failed: %v; command outcome=%s; reconcile side effects before retrying\n", deliveryErr, r.Outcome)
		if code == 0 {
			code = 1
		}
		err = errors.Join(err, executionFailure(deliveryErr))
	}
	return code, err
}

func encodeResult(result *commandResult) ([]byte, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// +spec=An explicit file export contains the same final result as JSON stdout, including diagnostics and cost
func writeResultFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".repocli-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Find JSON intent even if Cobra stops on an earlier invalid flag. Consume known
// flag values so a literal --json in a title/path is never interpreted as a flag.
func requestedJSON(root *cobra.Command, args []string) bool {
	flags := map[string]*pflag.Flag{}
	command, _, _ := root.Find(args)
	if command == nil {
		command = root
	}
	for cmd := command; cmd != nil; cmd = cmd.Parent() {
		collect := func(f *pflag.Flag) {
			flags["--"+f.Name] = f
			if f.Shorthand != "" {
				flags["-"+f.Shorthand] = f
			}
		}
		cmd.Flags().VisitAll(collect)
		cmd.PersistentFlags().VisitAll(collect)
	}
	enabled := map[string]bool{"--json": false}
	for _, name := range strings.Split(command.Annotations["result-json-flags"], ",") {
		if name != "" {
			enabled["--"+name] = false
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, value, assigned := strings.Cut(arg, "=")
		if _, known := enabled[name]; known {
			if !assigned {
				enabled[name] = true
			} else if v, e := strconv.ParseBool(value); e == nil {
				enabled[name] = v
			}
			continue
		}
		if f := flags[name]; f != nil && !assigned && f.NoOptDefVal == "" {
			i++
		}
	}
	for _, active := range enabled {
		if active {
			return true
		}
	}
	return false
}
