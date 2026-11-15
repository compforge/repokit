package pr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/spf13/cobra"
)

func newCommentCommand(opts *cmdutil.Options, connection *forgeaccess.Options) *cobra.Command {
	root := &cobra.Command{Use: "comment", Short: "Read and manage all PR comments"}
	descriptions := map[string]string{"list": "List all PR comments including replies", "show": "Read one comment", "create": "Publish a conversation or code comment", "reply": "Reply to a comment", "resolve": "Resolve the containing discussion", "unresolve": "Reopen the containing discussion", "update": "Edit a comment", "delete": "Delete a comment"}
	for _, operation := range []string{"list", "show", "create", "reply", "update", "delete", "resolve", "unresolve"} {
		var bodyFile, anchorFile string
		var anchor *comment.Anchor
		command := &cobra.Command{Use: operation + " <number>", Short: descriptions[operation], Args: cobra.ExactArgs(1)}
		if operation != "list" && operation != "create" {
			command.Use += " <comment-id>"
			command.Args = cobra.ExactArgs(2)
		}
		if operation == "create" || operation == "reply" || operation == "update" {
			command.Flags().StringVar(&bodyFile, "body-file", "", "Markdown file, or - for stdin")
		}
		if operation == "create" {
			command.Flags().StringVar(&anchorFile, "anchor-file", "", "JSON file containing path, revision, side and optional line range")
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
				return errors.New("comment operations require one --target-repository")
			}
			if len(args) > 1 && strings.TrimSpace(args[1]) == "" {
				return errors.New("comment ID is required")
			}
			body := ""
			if operation == "create" || operation == "reply" || operation == "update" {
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
			if anchorFile != "" {
				data, e := os.ReadFile(anchorFile)
				if e != nil {
					return e
				}
				anchor = &comment.Anchor{}
				if e = json.Unmarshal(data, anchor); e != nil {
					return e
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
				result, err = pr.Client.Comments().List(ctx, key)
			case "show":
				result, err = pr.Client.Comments().Get(ctx, key, args[1])
			case "create":
				result, err = pr.Client.Comments().Create(ctx, key, body, anchor)
			case "reply":
				result, err = pr.Client.Comments().Reply(ctx, key, args[1], body)
			case "update":
				result, err = pr.Client.Comments().Update(ctx, key, args[1], body)
			case "delete":
				result, err = pr.Client.Comments().Delete(ctx, key, args[1])
			case "unresolve":
				result, err = pr.Client.Comments().Resolve(ctx, key, args[1], false)
			case "resolve":
				result, err = pr.Client.Comments().Resolve(ctx, key, args[1], true)
			}
			if err != nil {
				return errs.Wrap(err, "forge.comment_failed", operation, fmt.Sprintf("%s #%d", key.Repository.Path, number))
			}
			if opts.JSON {
				return cmdutil.JSON(cmd.OutOrStdout(), result)
			}
			cmdutil.Record(cmd.OutOrStdout(), result)
			switch value := result.(type) {
			case []comment.Comment:
				fmt.Fprintf(cmd.OutOrStdout(), "%d comment(s)\n", len(value))
				for _, item := range value {
					displayComment(cmd.OutOrStdout(), item)
				}
			case comment.Comment:
				displayComment(cmd.OutOrStdout(), value)
			case comment.DeleteResult:
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted comment %s\n", value.CommentID)
			}
			return nil
		}
		root.AddCommand(command)
	}
	return root
}
