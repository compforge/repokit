package cli

import (
	"context"
	"errors"
	"fmt"
	repoconfig "github.com/compforge/repokit/toolkit/go/config"
	"github.com/compforge/repokit/toolkit/go/forge"
	repostate "github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
	"os"
	"strconv"
	"time"
)

type forgeOptions struct {
	provider, apiHost   string
	targets             []string
	configuration       *repoconfig.Config
	configurationErr    error
	configurationLoaded bool
}

var errNoForgeOrigin = errors.New("checkout has no Forge origin")

func (o *forgeOptions) flags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringVar(&o.provider, "provider", "", "Forge provider: github or gitlab")
	flags.StringVar(&o.apiHost, "api-host", "", "Forge API URL override")
	flags.StringArrayVar(&o.targets, "target-repository", nil, "target repository path; repeat for worktree cleanup")
}
func (o *forgeOptions) validate(opts *options) error {
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if o.provider != "" && o.provider != "github" && o.provider != "gitlab" {
		return errors.New("--provider must be github or gitlab")
	}
	return nil
}
func (o *forgeOptions) context(ctx context.Context, repo string, stateRoot string) (*forge.Context, error) {
	identity, err := forge.RepositoryIdentity(ctx, repo)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errNoForgeOrigin
	}
	if !o.configurationLoaded {
		o.configuration, o.configurationErr = repoconfig.Load(stateRoot)
		o.configurationLoaded = true
	}
	if o.configurationErr != nil {
		return nil, o.configurationErr
	}
	access, err := o.configuration.Forge(identity.Forge.Name)
	if err != nil {
		return nil, err
	}
	if o.provider != "" && o.provider != access.Provider {
		return nil, operationFailure("forge.provider_mismatch", "connection", identity.Forge.Name, errors.New("provider override conflicts with configured Forge provider"))
	}
	if o.apiHost != "" {
		access.APIHost = o.apiHost
	}
	client, err := forge.NewClient(access)
	if err != nil {
		return nil, err
	}
	return forge.NewContext(ctx, client, repo, o.targets)
}

// worktreePullRequests adapts the shared Forge connection for both cleanup commands.
// Local repositories have no merged-PR stage; connection failures still allow count retention.
func (o *forgeOptions) worktreePullRequests(ctx context.Context, repo, home string) (repostate.WorktreePullRequests, error) {
	prContext, err := o.context(ctx, repo, home)
	if errors.Is(err, errNoForgeOrigin) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return prContext, nil
}

func newPRCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "pr", Short: "Create, query and update GitHub PRs or GitLab MRs"}
	var connection forgeOptions
	connection.flags(root)
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
			if err := connection.validate(opts); err != nil {
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
			if len(connection.targets) > 1 {
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
					return executionFailure(err)
				}
				value := string(data)
				body = &value
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			prContext, err := connection.context(ctx, opts.repository, opts.home)
			if err != nil {
				return executionFailure(err)
			}
			target := prContext.Targets[0]
			key := forge.Key{Repository: prContext.Client.Repository(target), Number: number}
			var result any
			switch operation {
			case "show":
				result, err = prContext.Client.Load(ctx, target, number, maxAge)
			case "list":
				result, err = prContext.Client.List(ctx, target, state, "", sourceBranch)
			case "create":
				if sourceBranch == "" {
					info, e := repostate.Checkout(ctx, opts.repository)
					if e != nil {
						return executionFailure(e)
					}
					entries, e := repostate.ListCheckouts(ctx, opts.repository)
					if e != nil {
						return executionFailure(e)
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
				return operationFailure("forge.operation_failed", operation, fmt.Sprintf("%s #%d source=%s target=%s", target, number, sourceBranch, targetBranch), err)
			}
			if !opts.json {
				recordResult(cmd.OutOrStdout(), result)
			}
			// JSON is a stable scripting interface; readable output keeps maintenance diagnostics visible.
			if opts.json {
				if err = writeJSON(cmd.OutOrStdout(), result); err != nil {
					return executionFailure(err)
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
