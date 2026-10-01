package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/fbmessenger"
)

func (flags messengerCommandOptions) runImportMessenger(cmd *cobra.Command, rootDir string) error {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return errors.New("configuration is unavailable")
	}
	cfg := state.cfg
	logger := state.logger
	if info, err := os.Stat(rootDir); err != nil {
		return fmt.Errorf("source directory not found: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("source path is not a directory: %s", rootDir)
	}

	dbPath := cfg.DatabaseDSN()
	s, cleanup, err := openWritableStoreAndInitForIngestInvocation(state)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		select {
		case <-sigChan:
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "\nInterrupted. Saving checkpoint...")
			cancel()
		case <-ctx.Done():
		}
	}()

	opts := fbmessenger.ImportOptions{
		Me:              flags.importMessengerMe,
		RootDir:         rootDir,
		Format:          flags.importMessengerFormat,
		AttachmentsDir:  cfg.AttachmentsDir(),
		Limit:           flags.importMessengerLimit,
		NoResume:        flags.importMessengerNoResume,
		CheckpointEvery: flags.importMessengerCheckpointEvery,
		Logger:          logger,
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Importing Facebook Messenger DYI from %s\n", rootDir)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Me: %s\n", flags.importMessengerMe)
	_, _ = fmt.Fprintln(cmd.OutOrStdout())

	summary, err := fbmessenger.ImportDYI(ctx, s, opts)
	if err != nil {
		if ctx.Err() != nil {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nImport interrupted. Re-run to continue.")
			return rebuildCacheAfterWrite(dbPath, state)
		}
		return fmt.Errorf("import failed: %w", err)
	}

	if err := runPostSourceCreateMigrationsForInvocation(s, state); err != nil {
		return fmt.Errorf("post-source-create migrations: %w", err)
	}

	_, _ = fmt.Fprintln(cmd.OutOrStdout())
	if summary.WasResumed {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Resumed from checkpoint.")
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Import complete!")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Duration:       %s\n", summary.Duration.Round(time.Millisecond))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Threads:        %d processed, %d skipped\n",
		summary.ThreadsProcessed, summary.ThreadsSkipped)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Files skipped:  %d (unrecognized siblings)\n", summary.FilesSkipped)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Messages:       %d processed, %d added, %d skipped\n",
		summary.MessagesProcessed, summary.MessagesAdded, summary.MessagesSkipped)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Participants:   %d\n", summary.ParticipantsResolved)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Attachments:    %d found, %d stored\n", summary.AttachmentsFound, summary.AttachmentsStored)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Reactions:      %d\n", summary.ReactionsAdded)
	if summary.Errors > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Errors:         %d\n", summary.Errors)
	}
	if summary.MessagesAdded > 0 && summary.FromMeCount == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"\n  Warning: no messages matched --me %q (slug: %q).\n"+
				"  The --me value must match the slug of your display name in the export.\n",
			flags.importMessengerMe, fbmessenger.Slug(fbmessenger.StripDomain(flags.importMessengerMe)))
	}

	return rebuildCacheAfterWrite(dbPath, state)
}

func init() {
	registerCommandFactory(newImportMessengerCommand)
}

func newImportMessengerCommand() *cobra.Command {
	var flags messengerCommandOptions
	importMessengerCmd := &cobra.Command{
		Use:   "import-messenger <dyi-export-dir>",
		Short: "Import Facebook Messenger from a Download Your Information export",
		Long: `Import Facebook Messenger conversations from a DYI export (JSON or HTML).

Both JSON and HTML DYI formats are supported. When a thread contains both, the
JSON form wins because it preserves timestamps at millisecond precision and
reactions with relational fidelity. Use --format both to import both copies
into a single conversation with disambiguated source_message_id values.

Participants are synthesized as <slug>@facebook.messenger addresses. Two
participants whose display names produce the same slug are merged with a
warning — DYI exports do not expose stable user IDs, so this is the best we
can do without false-splitting one person into two phantom participants.

Your own identifier is required via --me and must itself be a
<slug>@facebook.messenger address; this value becomes the source identifier
and drives is_from_me on outbound messages.

HTML exports do not expose timezone information; timestamps are stored as
UTC. JSON exports have millisecond-precision timestamps that are preserved
verbatim.

Examples:
  msgvault import-messenger --me test.user@facebook.messenger ~/downloads/facebook-export
  msgvault import-messenger --me test.user@facebook.messenger --format both ./dyi
  msgvault import-messenger --me test.user@facebook.messenger --limit 100 ./dyi
	`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isDaemonCLISubprocess() {
				return runDaemonCLICommandHTTPFromCobra(cmd, args)
			}
			return flags.runImportMessenger(cmd, args[0])
		},
	}
	importMessengerCmd.Flags().StringVar(&flags.importMessengerMe, "me", "", "your <slug>@facebook.messenger identifier (required)")
	importMessengerCmd.Flags().StringVar(&flags.importMessengerFormat, "format", "auto", "format to import: auto|json|html|both")
	importMessengerCmd.Flags().IntVar(&flags.importMessengerLimit, "limit", 0, "limit number of messages (for testing)")
	importMessengerCmd.Flags().BoolVar(&flags.importMessengerNoResume, "no-resume", false, "ignore any existing checkpoint and start fresh")
	importMessengerCmd.Flags().IntVar(&flags.importMessengerCheckpointEvery, "checkpoint-interval", 200, "checkpoint every N messages")
	_ = importMessengerCmd.MarkFlagRequired("me")
	return importMessengerCmd
}

type messengerCommandOptions struct {
	importMessengerMe              string
	importMessengerFormat          string
	importMessengerLimit           int
	importMessengerNoResume        bool
	importMessengerCheckpointEvery int
}
