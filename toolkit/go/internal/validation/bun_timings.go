package validation

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	bunBanner    = regexp.MustCompile(`(?m)^bun test v[0-9]+\.[0-9]+\.[0-9]+[^\r\n]*$`)
	bunFile      = regexp.MustCompile(`^([^\s].*\.[cm]?[jt]sx?):$`)
	bunResult    = regexp.MustCompile(`^(\(pass\)|\(fail\)|\(skip\)|✓|✗|✕|×|✔|✘|»|-) (.+) \[([0-9]+(?:\.[0-9]+)?)(ms|s)\]$`)
	consoleColor = regexp.MustCompile("\x1b\\[[0-9;]*m")
)

func testTimings(stdout, stderr string) []TestTiming {
	rows := goTestTimings(stdout)
	// Bun writes its banner to stdout and test results to stderr. Inspect each
	// stream independently so a file heading cannot leak across stream boundaries.
	if bunBanner.MatchString(consoleColor.ReplaceAllString(stdout, "")) || bunBanner.MatchString(consoleColor.ReplaceAllString(stderr, "")) {
		rows = append(rows, bunTestTimings(stdout)...)
		rows = append(rows, bunTestTimings(stderr)...)
	}
	return rows
}

// +spec=Bun console timings are optional observations: retain only named results with explicit elapsed time under a file heading; never synthesize totals or infer a check verdict.
// +link=component://docs/validation.md
func bunTestTimings(output string) []TestTiming {
	var rows []TestTiming
	file := ""
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSuffix(consoleColor.ReplaceAllString(line, ""), "\r")
		if strings.HasPrefix(line, "bun test v") || strings.HasPrefix(line, "Ran ") {
			file = ""
			continue
		}
		if match := bunFile.FindStringSubmatch(line); match != nil {
			file = match[1]
			continue
		}
		match := bunResult.FindStringSubmatch(line)
		if file == "" || match == nil {
			continue
		}
		seconds, err := strconv.ParseFloat(match[3], 64)
		if err != nil {
			continue
		}
		if match[4] == "ms" {
			seconds /= 1000
		}
		status := "passed"
		switch match[1] {
		case "(fail)", "✗", "✕", "×", "✘":
			status = "failed"
		case "(skip)", "»", "-":
			status = "skipped"
		}
		rows = append(rows, TestTiming{Package: file, Test: match[2], Status: status, DurationSeconds: seconds})
	}
	return rows
}
