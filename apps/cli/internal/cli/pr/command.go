// Package pr adapts PR lifecycle and discussion operations to Cobra commands.
package pr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	repostate "github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func NewCommand(opts *cmdutil.Options) *cobra.Command {
	root := &cobra.Command{Use: "pr", Short: "Create, query and update GitHub PRs or GitLab MRs"}
	var connection forgeaccess.Options
	connection.Flags(root)
	root.AddCommand(newDiscussionCommand(opts, &connection))
	for _, operation := range []string{"create", "show", "list", "update", "close", "merge"} {
		var title, bodyFile, sourceBranch, targetBranch, state, expectedSHA, method string
		var draft bool
		var maxAge time.Duration
		command := &cobra.Command{Use: operation, Args: cobra.NoArgs}
		if operation == "show" || operation == "update" || operation == "close" || operation == "merge" {
			command.Use += " <number>"
			command.Args = cobra.ExactArgs(1)
		}
		if operation == "show" {
			command.Flags().DurationVar(&maxAge, "max-age", time.Minute, "maximum age of cached PR metadata; 0 forces refresh")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if err := connection.Validate(opts); err != nil {
				return err
			}
			number := 0
			if len(args) > 0 {
				var err error
				number, err = strconv.Atoi(args[0])
				if err != nil || number <= 0 {
					return errors.New("PR number must be positive")
				}
			}
			if len(connection.Targets) > 1 {
				return errors.New("PR operations require one --target-repository")
			}
			if operation == "create" && (title == "" || targetBranch == "") {
				return errors.New("create requires --title and --base")
			}
			if operation == "merge" && expectedSHA == "" {
				return errors.New("merge requires --expected-sha")
			}
			if operation == "merge" && method != "merge" && method != "squash" && method != "rebase" {
				return errors.New("invalid --method")
			}
			if operation == "list" && state != "open" && state != "closed" && state != "merged" && state != "all" {
				return errors.New("invalid --state")
			}
			if operation == "update" && !cmd.Flags().Changed("title") && !cmd.Flags().Changed("body-file") && !cmd.Flags().Changed("base") {
				return errors.New("update requires --title, --body-file or --base")
			}
			var body *string
			if cmd.Flags().Changed("body-file") {
				data, err := os.ReadFile(bodyFile)
				if err != nil {
					return errs.Wrap(err, "execution.failed", "execution", "")
				}
				value := string(data)
				body = &value
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			prContext, err := connection.Context(ctx, opts.Repository, opts.Home)
			if err != nil {
				return errs.Wrap(err, "execution.failed", "execution", "")
			}
			target := prContext.Targets[0]
			key := model.Key{Repository: prContext.Client.Repository(target), Number: number}
			var result any
			switch operation {
			case "show":
				result, err = prContext.Client.Load(ctx, target, number, maxAge)
			case "list":
				result, err = prContext.Client.List(ctx, target, state, "", sourceBranch)
			case "create":
				if sourceBranch == "" {
					info, e := repostate.Checkout(ctx, opts.Repository)
					if e != nil {
						return errs.Wrap(e, "execution.failed", "execution", "")
					}
					entries, e := repostate.ListCheckouts(ctx, opts.Repository)
					if e != nil {
						return errs.Wrap(e, "execution.failed", "execution", "")
					}
					for _, entry := range entries {
						if entry.Path == info.Root {
							sourceBranch = entry.Branch
						}
					}
				}
				if sourceBranch == "" {
					return errors.New("create requires a source branch")
				}
				description := ""
				if body != nil {
					description = *body
				}
				result, err = prContext.Ensure(ctx, forge.CreateRequest{SourceBranch: sourceBranch, TargetRepository: target, TargetBranch: targetBranch, Title: title, Body: description, Draft: draft})
			case "update":
				request := forge.UpdateRequest{Body: body}
				if cmd.Flags().Changed("title") {
					request.Title = &title
				}
				if cmd.Flags().Changed("base") {
					request.TargetBranch = &targetBranch
				}
				result, err = prContext.Update(ctx, key, request)
			case "close":
				result, err = prContext.Close(ctx, key)
			case "merge":
				result, err = prContext.Merge(ctx, key, expectedSHA, method)
			}
			if err != nil {
				return errs.Wrap(err, "forge.operation_failed", operation, fmt.Sprintf("%s #%d source=%s target=%s", target, number, sourceBranch, targetBranch))
			}
			if !opts.JSON {
				cmdutil.Record(cmd.OutOrStdout(), result)
			}
			// JSON is a stable scripting interface; readable output keeps maintenance diagnostics visible.
			if opts.JSON {
				if err = cmdutil.JSON(cmd.OutOrStdout(), result); err != nil {
					return errs.Wrap(err, "execution.failed", "execution", "")
				}
				return nil
			}
			display := func(pr forge.PullRequest) {
				fmt.Fprintf(cmd.OutOrStdout(), "#%d %s %s\n%s\n", pr.Key.Number, pr.State, pr.Title, pr.URL)
			}
			switch value := result.(type) {
			case forge.PullRequest:
				display(value)
			case []forge.PullRequest:
				for _, pr := range value {
					display(pr)
				}
			case forge.OperationResult:
				display(value.PullRequest)
				for _, issue := range value.Maintenance.Errors {
					fmt.Fprintln(cmd.ErrOrStderr(), "maintenance warning:", issue)
				}
			}
			return nil
		}
		flags := command.Flags()
		switch operation {
		case "create", "update":
			flags.StringVar(&title, "title", "", "PR title")
			flags.StringVar(&bodyFile, "body-file", "", "file containing the PR description")
			flags.StringVar(&targetBranch, "base", "", "target branch")
		}
		if operation == "create" {
			flags.BoolVar(&draft, "draft", false, "create a draft PR")
		}
		if operation == "create" || operation == "list" {
			flags.StringVar(&sourceBranch, "head", "", "source branch (current branch for create)")
		}
		if operation == "list" {
			flags.StringVar(&state, "state", "open", "open, closed, merged or all")
		}
		if operation == "merge" {
			flags.StringVar(&expectedSHA, "expected-sha", "", "required expected source commit")
			flags.StringVar(&method, "method", "merge", "merge, squash or rebase")
		}
		root.AddCommand(command)
	}
	return root
}
