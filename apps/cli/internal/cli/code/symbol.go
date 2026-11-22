package code

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func symbolPosition(at string) (repokit.SymbolPosition, error) {
	var req repokit.SymbolPosition
	last := strings.LastIndexByte(at, ':')
	if last > 0 {
		prev := strings.LastIndexByte(at[:last], ':')
		if prev > 0 {
			line, e1 := strconv.Atoi(at[prev+1 : last])
			column, e2 := strconv.Atoi(at[last+1:])
			req.Path, req.Line, req.Column = path.Clean(at[:prev]), line, column
			if e1 == nil && e2 == nil && req.Validate() == nil {
				return req, nil
			}
		}
	}
	return req, fmt.Errorf("--at requires a repository-relative file:line:column (one-based byte column)")
}

func symbolCommand(opts *cmdutil.Options, name, short, help, kind string, run func(context.Context, io.Writer, repokit.SymbolPosition) error) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   name + " --at <file:line:column>",
		Short: short,
		Long:  short + ".\n\n" + help,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := symbolPosition(at)
			if err != nil {
				return err
			}
			if opts.Timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			req.Repository = opts.Repository
			return run(ctx, cmd.OutOrStdout(), req)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "position on the "+kind+" declaration name")
	_ = cmd.MarkFlagRequired("at")
	return cmd
}

func deliverLookup[T any](w io.Writer, structured bool, report T, snapshot string, err error, write func(io.Writer, T) error) error {
	if err != nil {
		if snapshot != "" {
			cmdutil.Record(w, report)
		}
		return cmdutil.ExecutionFailure(err)
	}
	if structured {
		return cmdutil.JSON(w, report)
	}
	cmdutil.Record(w, report)
	return cmdutil.ExecutionFailure(write(w, report))
}
