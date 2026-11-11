// Package command defines the shared CLI options and result-writing boundary.
package command

import "time"

type Options struct {
	Home       string
	Repository string
	JSON       bool
	Timeout    time.Duration
}
