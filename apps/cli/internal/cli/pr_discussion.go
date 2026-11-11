package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/spf13/cobra"
)

func newDiscussionCommand(opts *options, connection *forgeOptions) *cobra.Command {
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
			if err := connection.validate(opts); err != nil {
				return err
			}
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return errors.New("PR number must be positive")
			}
			if len(connection.targets) > 1 {
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
					return executionFailure(err)
				}
				body = string(data)
				if strings.TrimSpace(body) == "" {
					return errors.New("comment body must not be empty")
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			pr, err := connection.context(ctx, opts.repository, opts.home)
			if err != nil {
				return executionFailure(err)
			}
			key := forge.Key{Repository: pr.Client.Repository(pr.Targets[0]), Number: number}
			var result any
			switch operation {
			case "list":
				result, err = pr.Client.Discussions(ctx, key)
			case "show":
				result, err = pr.Client.Discussion(ctx, key, args[1])
			case "create":
				result, err = pr.Client.CreateComment(ctx, key, body)
			case "reply":
				result, err = pr.Client.Reply(ctx, key, args[1], body)
			case "resolve":
				result, err = pr.Client.ResolveDiscussion(ctx, key, args[1])
			}
			if err != nil {
				return operationFailure("forge.discussion_failed", operation, fmt.Sprintf("%s #%d", key.Repository.Path, number), err)
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			recordResult(cmd.OutOrStdout(), result)
			switch value := result.(type) {
			case []forge.Discussion:
				fmt.Fprintf(cmd.OutOrStdout(), "%d discussion(s)\n", len(value))
				for _, thread := range value {
					displayDiscussion(cmd.OutOrStdout(), thread, false)
				}
			case forge.Discussion:
				displayDiscussion(cmd.OutOrStdout(), value, true)
			case forge.CommentResult:
				fmt.Fprintf(cmd.OutOrStdout(), "Replied to thread %s\n", value.ThreadID)
				displayComment(cmd.OutOrStdout(), value.Comment)
			}
			return nil
		}
		root.AddCommand(command)
	}
	return root
}

func displayDiscussion(w io.Writer, thread forge.Discussion, full bool) {
	fmt.Fprintf(w, "Thread %s: %s; %d comment(s); replyable=%t resolvable=%t\n", thread.ID, thread.Resolution, len(thread.Comments), thread.Replyable, thread.Resolvable)
	for _, comment := range thread.Comments {
		if full {
			displayComment(w, comment)
		} else {
			fmt.Fprintf(w, "  comment %s by %s", comment.ID, comment.Author)
			if comment.Path != "" {
				fmt.Fprintf(w, " at %s", comment.Path)
				if comment.Line != nil {
					fmt.Fprintf(w, ":%d", *comment.Line)
				}
			}
			fmt.Fprintln(w)
		}
	}
}
func displayComment(w io.Writer, comment forge.Comment) {
	fmt.Fprintf(w, "\nComment %s by %s (%s)\n", comment.ID, comment.Author, comment.CreatedAt)
	if comment.Path != "" {
		fmt.Fprint(w, comment.Path)
		if comment.Line != nil {
			fmt.Fprintf(w, ":%d", *comment.Line)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, comment.Body)
}
