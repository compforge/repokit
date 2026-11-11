package pr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/discussion"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/spf13/cobra"
)

func newDiscussionCommand(opts *cmdutil.Options, connection *forgeaccess.Options) *cobra.Command {
	root := &cobra.Command{Use: "discussion", Short: "Read PR conversations and operate on discussion threads"}
	descriptions := map[string]string{"list": "List complete PR discussions", "show": "Read a full thread and its replies", "create": "Publish an unanchored conversation comment", "reply": "Reply to a platform-supported thread", "resolve": "Resolve a platform-supported thread"}
	for _, operation := range []string{"list", "show", "create", "reply", "resolve"} {
		var bodyFile string
		command := &cobra.Command{Use: operation + " <number>", Short: descriptions[operation], Args: cobra.ExactArgs(1)}
		if operation == "show" || operation == "reply" || operation == "resolve" {
			command.Use += " <thread-id>"
			command.Args = cobra.ExactArgs(2)
		}
		if operation == "create" || operation == "reply" {
			command.Flags().StringVar(&bodyFile, "body-file", "", "Markdown file, or - for stdin")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if err := connection.Validate(opts); err != nil {
				return err
			}
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return errors.New("PR number must be positive")
			}
			if len(connection.Targets) > 1 {
				return errors.New("discussion operations require one --target-repository")
			}
			if len(args) > 1 && strings.TrimSpace(args[1]) == "" {
				return errors.New("thread ID is required")
			}
			body := ""
			if operation == "create" || operation == "reply" {
				if bodyFile == "" {
					return errors.New("--body-file is required")
				}
				var data []byte
				if bodyFile == "-" {
					data, err = io.ReadAll(cmd.InOrStdin())
				} else {
					data, err = os.ReadFile(bodyFile)
				}
				if err != nil {
					return errs.Wrap(err, "execution.failed", "execution", "")
				}
				body = string(data)
				if strings.TrimSpace(body) == "" {
					return errors.New("comment body must not be empty")
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			pr, err := connection.Context(ctx, opts.Repository, opts.Home)
			if err != nil {
				return errs.Wrap(err, "execution.failed", "execution", "")
			}
			key := model.Key{Repository: pr.Client.Repository(pr.Targets[0]), Number: number}
			var result any
			switch operation {
			case "list":
				result, err = pr.Client.Discussions().List(ctx, key)
			case "show":
				result, err = pr.Client.Discussions().Get(ctx, key, args[1])
			case "create":
				result, err = pr.Client.Discussions().Create(ctx, key, body)
			case "reply":
				result, err = pr.Client.Discussions().Reply(ctx, key, args[1], body)
			case "resolve":
				result, err = pr.Client.Discussions().Resolve(ctx, key, args[1])
			}
			if err != nil {
				return errs.Wrap(err, "forge.discussion_failed", operation, fmt.Sprintf("%s #%d", key.Repository.Path, number))
			}
			if opts.JSON {
				return cmdutil.JSON(cmd.OutOrStdout(), result)
			}
			cmdutil.Record(cmd.OutOrStdout(), result)
			switch value := result.(type) {
			case []discussion.Discussion:
				fmt.Fprintf(cmd.OutOrStdout(), "%d discussion(s)\n", len(value))
				for _, thread := range value {
					displayDiscussion(cmd.OutOrStdout(), thread, false)
				}
			case discussion.Discussion:
				displayDiscussion(cmd.OutOrStdout(), value, true)
			case discussion.CommentResult:
				fmt.Fprintf(cmd.OutOrStdout(), "Replied to thread %s\n", value.ThreadID)
				displayComment(cmd.OutOrStdout(), value.Comment)
			}
			return nil
		}
		root.AddCommand(command)
	}
	return root
}
