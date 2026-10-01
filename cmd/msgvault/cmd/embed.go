package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const (
	embeddingsCommandName        = "embeddings"
	embeddingsOptimizeWorkerName = "__optimize-worker"
)

func newEmbeddingsBuildCmd(use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Build or update the vector embedding index (incremental by default; --full-rebuild for a new generation)",
		Long: `Build or update the vector embedding index for hybrid search.
Writes vectors to the co-located vectors.db. In the default incremental
mode, the command embeds any messages still needing embedding for the
active generation. With --full-rebuild, it creates a new building
generation, embeds the entire corpus, and (on a clean completion)
atomically activates it.

Requires [vector] to be enabled in config.toml and [vector.embeddings]
to point at a running OpenAI-compatible endpoint.`,
		RunE: runEmbeddingsBuild,
	}
	cmd.Flags().Bool("full-rebuild", false, "Create a new generation and rebuild from scratch")
	cmd.Flags().Bool("yes", false, "Skip confirmation prompts")
	cmd.Flags().Bool("backstop", false,
		"Full-scan pass that ignores the per-generation watermark, catching any straggler messages the incremental scan skipped (idempotent)")
	cmd.Flags().StringArray("account", nil,
		"Limit embedding to this account (repeatable); overrides [vector.embed.scope] accounts for this run")
	cmd.Flags().StringArray("collection", nil,
		"Limit embedding to this collection's accounts (repeatable); overrides [vector.embed.scope] accounts for this run")
	return cmd
}

func runEmbeddingsBuild(cmd *cobra.Command, args []string) error {
	if !isDaemonCLISubprocess() {
		return runEmbeddingsBuildHTTP(cmd, args)
	}
	return runEmbeddingsBuildLocal(cmd)
}

func runEmbeddingsBuildLocal(cmd *cobra.Command) error {
	return runEmbeddingsBuildLocalWithOptions(cmd, readEmbeddingCommandOptions(cmd))
}

func runEmbeddingsBuildLocalWithOptions(cmd *cobra.Command, flags embeddingCommandOptions) error {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return errors.New("configuration is unavailable")
	}
	cfg := state.cfg
	if !cfg.Vector.Enabled {
		return errors.New("vector search not enabled; add [vector] enabled=true to config.toml first")
	}
	if cfg.Vector.Embeddings.Endpoint == "" || cfg.Vector.Embeddings.Model == "" {
		return errors.New("[vector.embeddings] endpoint and model are required")
	}
	return runEmbed(cmd, flags)
}

func runEmbeddingsBuildHTTP(cmd *cobra.Command, args []string) error {
	flags := readEmbeddingCommandOptions(cmd)
	if flags.embedFullRebuild && !flags.embedYes {
		if !confirmEmbed(cmd, "Start a full rebuild? This builds a new generation and atomically swaps it in when complete. ") {
			return errors.New("aborted")
		}
		if err := cmd.Flags().Set("yes", "true"); err != nil {
			return fmt.Errorf("set --yes after confirmation: %w", err)
		}
	}
	return runDaemonCLICommandHTTPFromCobraWithEnv(cmd, args, embeddingsForwardEnv(invocationFromCommand(cmd)))
}

// embeddingsForwardEnv carries the caller's embedding API key into the
// daemon-spawned subprocess, which otherwise sees only the daemon's
// environment: a key exported in the user's shell would silently not apply.

func embeddingsForwardEnv(state *invocation) map[string]string {
	state = invocationState(context.Background(), state)
	if state == nil || state.cfg == nil {
		return nil
	}
	cfg := state.cfg
	name := cfg.Vector.Embeddings.APIKeyEnv
	if name == "" {
		return nil
	}
	value := os.Getenv(name)
	if value == "" {
		return nil
	}
	return map[string]string{name: value}
}

func runEmbeddingsResume(cmd *cobra.Command, args []string) error {
	return runEmbeddingsBuild(cmd, args)
}

func runEmbeddingsListCommand(cmd *cobra.Command, args []string) error {
	if !isDaemonCLISubprocess() {
		return runDaemonCLICommandHTTPFromCobra(cmd, args)
	}
	return runEmbeddingsList(cmd, args)
}

func init() {
	registerCommandFactory(newEmbeddingsCommand)
	registerCommandFactory(func() *cobra.Command {
		cmd := newEmbeddingsBuildCmd("build-embeddings")
		cmd.Deprecated = "use 'msgvault embeddings build' instead"
		return cmd
	})
}

func newEmbeddingsCommand() *cobra.Command {
	embeddingsCmd := &cobra.Command{
		Use:   embeddingsCommandName,
		Short: "Manage vector embeddings",
	}

	embeddingsBuildCmd := newEmbeddingsBuildCmd("build")
	embeddingsResumeCmd := &cobra.Command{
		Use:   cmdUseResume,
		Short: "Resume or top up the current vector embedding generation",
		Long: `Resume or top up the current vector embedding generation.
If a matching generation is building, this embeds any messages still
needing embedding for it and activates it when complete. Otherwise it
embeds any messages still needing embedding for the active generation.
Pass --backstop for a full-scan pass that ignores the per-generation
watermark, catching any straggler messages the incremental scan skipped.`,
		RunE: runEmbeddingsResume,
	}
	embeddingsListCmd := &cobra.Command{
		Use:   cmdUseList,
		Short: "List vector embedding generations",
		Args:  cobra.NoArgs,
		RunE:  runEmbeddingsListCommand,
	}
	embeddingsRetireCmd := &cobra.Command{
		Use:   "retire <generation-id>",
		Short: "Retire a vector embedding generation",
		Args:  cobra.ExactArgs(1),
		RunE:  runEmbeddingsRetireCommand,
	}
	embeddingsActivateCmd := &cobra.Command{
		Use:   "activate <generation-id>",
		Short: "Activate a completed vector embedding generation",
		Args:  cobra.ExactArgs(1),
		RunE:  runEmbeddingsActivateCommand,
	}
	embeddingsPruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove embeddings for hard-deleted messages",
		Args:  cobra.NoArgs,
		RunE:  runEmbeddingsPruneCommand,
	}
	embeddingsOptimizeCmd := newEmbeddingsOptimizeCommand()
	embeddingsOptimizeWorkerCmd := newEmbeddingsOptimizeWorkerCommand()

	embeddingsResumeCmd.Flags().Bool("backstop", false,
		"Full-scan pass that ignores the per-generation watermark, catching any straggler messages the incremental scan skipped (idempotent)")
	embeddingsResumeCmd.Flags().StringArray("account", nil,
		"Limit embedding to this account (repeatable); overrides [vector.embed.scope] accounts for this run")
	embeddingsResumeCmd.Flags().StringArray("collection", nil,
		"Limit embedding to this collection's accounts (repeatable); overrides [vector.embed.scope] accounts for this run")
	embeddingsRetireCmd.Flags().Bool("yes", false, "Skip confirmation prompt")
	embeddingsRetireCmd.Flags().Bool("force-active", false, "Allow retiring the active generation")
	embeddingsActivateCmd.Flags().Bool("yes", false, "Skip confirmation prompt")
	embeddingsActivateCmd.Flags().Bool("force", false, "Allow activation despite incomplete message or person coverage, or a fingerprint mismatch")
	embeddingsCmd.AddCommand(embeddingsBuildCmd)
	embeddingsCmd.AddCommand(embeddingsResumeCmd)
	embeddingsCmd.AddCommand(embeddingsListCmd)
	embeddingsCmd.AddCommand(embeddingsRetireCmd)
	embeddingsCmd.AddCommand(embeddingsActivateCmd)
	embeddingsCmd.AddCommand(embeddingsPruneCmd)
	embeddingsOptimizeCmd.Flags().Bool("drop", false, "Remove the accelerator while keeping exact vectors")
	embeddingsCmd.AddCommand(embeddingsOptimizeCmd)
	embeddingsCmd.AddCommand(embeddingsOptimizeWorkerCmd)

	return embeddingsCmd
}

type embeddingCommandOptions struct {
	embedFullRebuild            bool
	embedYes                    bool
	embedBackstop               bool
	embedAccounts               []string
	embedCollections            []string
	embeddingsRetireYes         bool
	embeddingsRetireForceActive bool
	embeddingsActivateForce     bool
	embeddingsActivateYes       bool
}

func readEmbeddingCommandOptions(cmd *cobra.Command) embeddingCommandOptions {
	var flags embeddingCommandOptions
	flags.embedFullRebuild, _ = cmd.Flags().GetBool("full-rebuild")
	flags.embedYes, _ = cmd.Flags().GetBool("yes")
	flags.embedBackstop, _ = cmd.Flags().GetBool("backstop")
	flags.embedAccounts, _ = cmd.Flags().GetStringArray("account")
	flags.embedCollections, _ = cmd.Flags().GetStringArray("collection")
	flags.embeddingsRetireYes, _ = cmd.Flags().GetBool("yes")
	flags.embeddingsRetireForceActive, _ = cmd.Flags().GetBool("force-active")
	flags.embeddingsActivateForce, _ = cmd.Flags().GetBool("force")
	flags.embeddingsActivateYes, _ = cmd.Flags().GetBool("yes")
	return flags
}
