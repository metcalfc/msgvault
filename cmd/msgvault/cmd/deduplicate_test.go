package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/dedup"
	"go.kenn.io/msgvault/internal/opserr"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestPlanCLIDeduplicateRequiresConfirmationForDerivableBackfill(t *testing.T) {
	f := storetest.New(t)
	testCtx := withDeduplicateTestConfig(t)

	readyID := f.CreateMessage("ready-metadata")
	require.NoError(t, f.Store.UpsertMessageRaw(readyID, []byte(
		"Message-ID: <ready-metadata@example.test>\r\n\r\nBody")))
	failedID := f.CreateMessage("malformed-metadata")
	require.NoError(t, f.Store.UpsertMessageRaw(failedID, []byte(
		"From: sender@example.test\r\nSubject: no identifier\r\n\r\nBody")))

	plan, err := planCLIDeduplicate(testCtx, f.Store, api.CLIDeduplicatePlanRequest{
		Account: f.Source.Identifier,
	})

	require.NoError(t, err)
	require.Len(t, plan.Items, 1)
	item := plan.Items[0]
	assert.True(t, item.NeedsConfirmation, "derivable metadata is executable work")
	assert.Equal(t, int64(1), item.PendingBackfillCount)
	assert.Contains(t, item.Stdout, "2 messages with missing RFC822 Message-ID were inspected.")
	assert.Contains(t, item.Stdout, "1 RFC822 Message-ID value is ready to be derived from stored MIME after confirmation.")
	assert.Contains(t, item.Stdout, "1 message could not provide a usable Message-ID and will be skipped.")
}

func TestPlanCLIDeduplicateMalformedOnlyBackfillDoesNotRequireConfirmation(t *testing.T) {
	f := storetest.New(t)
	testCtx := withDeduplicateTestConfig(t)

	messageID := f.CreateMessage("malformed-only")
	require.NoError(t, f.Store.UpsertMessageRaw(messageID, []byte(
		"From: sender@example.test\r\nSubject: no identifier\r\n\r\nBody")))

	plan, err := planCLIDeduplicate(testCtx, f.Store, api.CLIDeduplicatePlanRequest{
		Account: f.Source.Identifier,
	})

	require.NoError(t, err)
	require.Len(t, plan.Items, 1)
	item := plan.Items[0]
	assert.False(t, item.NeedsConfirmation, "malformed-only metadata is not executable work")
	assert.Zero(t, item.PendingBackfillCount)
	assert.Contains(t, item.Stdout, "1 message with missing RFC822 Message-ID was inspected.")
	assert.NotContains(t, item.Stdout, "ready to be derived")
	assert.Contains(t, item.Stdout, "1 message could not provide a usable Message-ID and will be skipped.")
}

func TestDeduplicatePlanFingerprintBindsExactBackfillPlan(t *testing.T) {
	f := storetest.New(t)
	messageID := f.CreateMessage("fingerprint-metadata")
	require.NoError(t, f.Store.UpsertMessageRaw(messageID, []byte(
		"Message-ID: <first-plan@example.test>\r\n\r\nFirst body")))

	cfgScoped := dedup.Config{
		AccountSourceIDs: []int64{f.Source.ID},
		Account:          f.Source.Identifier,
	}
	engine := dedup.NewEngine(f.Store, cfgScoped, nil)
	firstReport, err := engine.Scan(t.Context())
	require.NoError(t, err)
	firstFingerprint, err := deduplicatePlanFingerprint(t.Context(), cfgScoped, firstReport)
	require.NoError(t, err)

	require.NoError(t, f.Store.UpsertMessageRaw(messageID, []byte(
		"Message-ID: <second-plan@example.test>\r\n\r\nSecond body")))
	secondReport, err := engine.Scan(t.Context())
	require.NoError(t, err)
	secondFingerprint, err := deduplicatePlanFingerprint(t.Context(), cfgScoped, secondReport)
	require.NoError(t, err)

	assert.Equal(t, firstReport.BackfillCandidates, secondReport.BackfillCandidates, "candidate count")
	assert.Equal(t, firstReport.PendingRFC822IDBackfill(), secondReport.PendingRFC822IDBackfill(), "ready count")
	assert.NotEqual(t, firstReport.BackfillPlanDigest, secondReport.BackfillPlanDigest, "store plan digest")
	assert.NotEqual(t, firstFingerprint, secondFingerprint, "CLI fingerprint must bind the exact derivation plan")
}

func TestPrintDedupSummaryOmitsUndoForBackfillOnlyExecution(t *testing.T) {
	done := captureStdout(t)
	printDedupSummary(&dedup.ExecutionSummary{
		BatchID:             "metadata-only-batch",
		RFC822IDsBackfilled: 2,
		RawMIMEBackfilled:   3,
	})
	out := done()

	assert.Contains(t, out, "RFC822 Message-IDs derived: 2")
	assert.Contains(t, out, "Raw MIME backfilled: 3", "raw MIME remains a separate metric")
	assert.NotContains(t, out, "Batch ID:", "metadata-only execution creates no dedup batch")
	assert.NotContains(t, out, "To undo:", "metadata-only execution has no reversible dedup batch")
}

func TestDeduplicateSingleAndMultiSourceBackfillOnlyOmitUndo(t *testing.T) {
	t.Run("dry run stays read-only without prompting", func(t *testing.T) {
		f := storetest.New(t)
		messageID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"dry-run-metadata", "dry-run-metadata@example.test")
		cfgScoped := dedup.Config{
			DryRun:           true,
			AccountSourceIDs: []int64{f.Source.ID},
			Account:          f.Source.Identifier,
		}
		cmd := newDeduplicateCmd()
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader("y\n"))

		done := captureStdout(t)
		err := runDeduplicateOnce(
			cmd, f.Store, "", cfgScoped,
			dedup.NewEngine(f.Store, cfgScoped, nil),
		)
		out := done()

		require.NoError(t, err)
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, messageID))
		assert.Contains(t, out, "Dry run complete. No changes made.")
		assert.NotContains(t, out, "Proceed with deduplication")
		assert.NotContains(t, out, "RFC822 Message-IDs derived:")
	})

	t.Run("single source", func(t *testing.T) {
		f := storetest.New(t)
		messageID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"single-metadata", "single-metadata@example.test")
		cfgScoped := dedup.Config{
			AccountSourceIDs: []int64{f.Source.ID},
			Account:          f.Source.Identifier,
		}
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("yes", "true"))
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())

		done := captureStdout(t)
		err := runDeduplicateOnce(
			cmd, f.Store, "", cfgScoped,
			dedup.NewEngine(f.Store, cfgScoped, nil),
		)
		out := done()

		require.NoError(t, err)
		assert.Equal(t, "single-metadata@example.test", storedRFC822IDForCLI(t, f.Store, messageID))
		assert.Contains(t, out, "Deriving RFC822 Message-IDs...")
		assert.NotContains(t, out, "Merging duplicates...")
		assert.Contains(t, out, "RFC822 Message-IDs derived: 1")
		assert.NotContains(t, out, "Batch ID:")
		assert.NotContains(t, out, "To undo:")
		assert.NotContains(t, out, "To undo all of the above:")
	})

	t.Run("multiple per-source executions", func(t *testing.T) {
		f := storetest.New(t)
		firstID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"first-metadata", "first-metadata@example.test")
		secondSource, err := f.Store.GetOrCreateSource("mbox", "backup@example.test")
		require.NoError(t, err)
		secondConversation, err := f.Store.EnsureConversation(secondSource.ID, "second-thread", "Second")
		require.NoError(t, err)
		secondID := createPendingRFC822Message(t, f.Store, secondSource.ID, secondConversation,
			"second-metadata", "second-metadata@example.test")
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("yes", "true"))
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())

		done := captureStdout(t)
		err = runDeduplicatePerSource(cmd, f.Store, "", dedup.Config{}, testDiscardLogger())
		out := done()

		require.NoError(t, err)
		assert.Equal(t, "first-metadata@example.test", storedRFC822IDForCLI(t, f.Store, firstID))
		assert.Equal(t, "second-metadata@example.test", storedRFC822IDForCLI(t, f.Store, secondID))
		assert.Equal(t, 2, strings.Count(out, "Deriving RFC822 Message-IDs..."), "one execution prelude per source")
		assert.NotContains(t, out, "Merging duplicates...")
		assert.Equal(t, 2, strings.Count(out, "RFC822 Message-IDs derived: 1"), "one execution summary per source")
		assert.NotContains(t, out, "Batch ID:")
		assert.NotContains(t, out, "To undo:")
		assert.NotContains(t, out, "To undo all of the above:")
	})
}

func TestDeduplicateLocalAndPerSourceMergeOutputIncludesBatch(t *testing.T) {
	t.Run("single source", func(t *testing.T) {
		f := storetest.New(t)
		messageIDs := f.CreateMessages(2)
		_, err := f.Store.DB().Exec(f.Store.Rebind(
			`UPDATE messages SET rfc822_message_id = ? WHERE id IN (?, ?)`),
			"local-merge@example.test", messageIDs[0], messageIDs[1])
		require.NoError(t, err)
		cfgScoped := dedup.Config{
			AccountSourceIDs: []int64{f.Source.ID},
			Account:          f.Source.Identifier,
		}
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("yes", "true"))
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())

		done := captureStdout(t)
		err = runDeduplicateOnce(
			cmd, f.Store, "", cfgScoped,
			dedup.NewEngine(f.Store, cfgScoped, nil),
		)
		out := done()

		require.NoError(t, err)
		assert.Contains(t, out, "Merging duplicates...")
		assert.NotContains(t, out, "Deriving RFC822 Message-IDs...")
		assert.Contains(t, out, "Batch ID:")
		assert.Contains(t, out, "Groups merged:       1")
	})

	t.Run("per source", func(t *testing.T) {
		f := storetest.New(t)
		messageIDs := f.CreateMessages(2)
		_, err := f.Store.DB().Exec(f.Store.Rebind(
			`UPDATE messages SET rfc822_message_id = ? WHERE id IN (?, ?)`),
			"per-source-merge@example.test", messageIDs[0], messageIDs[1])
		require.NoError(t, err)
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("yes", "true"))
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())

		done := captureStdout(t)
		err = runDeduplicatePerSource(cmd, f.Store, "", dedup.Config{}, testDiscardLogger())
		out := done()

		require.NoError(t, err)
		assert.Contains(t, out, "Merging duplicates...")
		assert.NotContains(t, out, "Deriving RFC822 Message-IDs...")
		assert.Contains(t, out, "Batch ID:")
		assert.Contains(t, out, "Groups merged:       1")
	})
}

func TestDeduplicateLocalAndDaemonPromptDescribeDerivationFence(t *testing.T) {
	const promptNote = "This will derive 1 RFC822 Message-ID value(s) from stored MIME after confirmation. " +
		"If that reveals a different duplicate plan, no messages will be hidden; rerun deduplicate to review it."

	t.Run("local", func(t *testing.T) {
		f := storetest.New(t)
		messageID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"local-prompt", "local-prompt@example.test")
		cfgScoped := dedup.Config{
			AccountSourceIDs: []int64{f.Source.ID},
			Account:          f.Source.Identifier,
		}
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader("n\n"))

		done := captureStdout(t)
		err := runDeduplicateOnce(cmd, f.Store, "", cfgScoped, dedup.NewEngine(f.Store, cfgScoped, nil))
		out := done()

		require.NoError(t, err)
		assert.Contains(t, out, promptNote)
		assert.Contains(t, out, "Proceed with RFC822 Message-ID derivation? [y/N]:")
		assert.NotContains(t, out, "reversible with --undo")
		assert.Contains(t, out, "Aborted.")
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, messageID), "declined derivation remains unapplied")
	})

	t.Run("daemon-backed", func(t *testing.T) {
		f := storetest.New(t)
		messageID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"daemon-prompt", "daemon-prompt@example.test")
		serverCfg := config.NewDefaultConfig()
		serverCfg.Data.DataDir = t.TempDir()
		apiServer := api.NewServerWithOptions(api.ServerOptions{
			Config: serverCfg,
			Store: &storeAPIAdapter{
				store:  f.Store,
				config: serverCfg,
				logger: slog.New(slog.DiscardHandler),
			},
			Logger:        slog.New(slog.DiscardHandler),
			DaemonVersion: Version,
		})
		httpServer := httptest.NewServer(apiServer.Router())
		t.Cleanup(httpServer.Close)
		testCtx := configureRemoteDaemonForTest(t, httpServer.URL)

		cmd := newDeduplicateRoutingTestCommand()
		cmd.SetContext(testCtx)
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetIn(strings.NewReader("n\n"))
		cmd.SetArgs([]string{"--account", f.Source.Identifier})

		require.NoError(t, cmd.Execute())
		assert.Contains(t, stdout.String(), promptNote)
		assert.Contains(t, stdout.String(), "Proceed with RFC822 Message-ID derivation? [y/N]:")
		assert.NotContains(t, stdout.String(), "reversible with --undo")
		assert.Contains(t, stdout.String(), "Aborted.")
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, messageID), "planning and cancellation stay read-only")
	})

	t.Run("per-source", func(t *testing.T) {
		f := storetest.New(t)
		messageID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"per-source-prompt", "per-source-prompt@example.test")
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader("n\n"))

		done := captureStdout(t)
		err := runDeduplicatePerSource(cmd, f.Store, "", dedup.Config{}, testDiscardLogger())
		out := done()

		require.NoError(t, err)
		assert.Contains(t, out, "Proceed with RFC822 Message-ID derivation for test@example.com? [y/N]:")
		assert.NotContains(t, out, "reversible with --undo")
		assert.Contains(t, out, "Skipped.")
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, messageID))
	})

	t.Run("multiple sources", func(t *testing.T) {
		f := storetest.New(t)
		firstID := createPendingRFC822Message(t, f.Store, f.Source.ID, f.ConvID,
			"multi-first-prompt", "multi-first-prompt@example.test")
		secondSource, err := f.Store.GetOrCreateSource("mbox", "backup@example.test")
		require.NoError(t, err)
		secondConversation, err := f.Store.EnsureConversation(secondSource.ID, "prompt-thread", "Prompt")
		require.NoError(t, err)
		secondID := createPendingRFC822Message(t, f.Store, secondSource.ID, secondConversation,
			"multi-second-prompt", "multi-second-prompt@example.test")
		cmd := newDeduplicateCmd()
		require.NoError(t, cmd.Flags().Set("no-backup", "true"))
		cmd.SetContext(t.Context())
		cmd.SetIn(strings.NewReader("n\nn\n"))

		done := captureStdout(t)
		err = runDeduplicatePerSource(cmd, f.Store, "", dedup.Config{}, testDiscardLogger())
		out := done()

		require.NoError(t, err)
		assert.Contains(t, out, "Proceed with RFC822 Message-ID derivation for test@example.com? [y/N]:")
		assert.Contains(t, out, "Proceed with RFC822 Message-ID derivation for backup@example.test? [y/N]:")
		assert.Equal(t, 2, strings.Count(out, "Proceed with RFC822 Message-ID derivation for"))
		assert.NotContains(t, out, "reversible with --undo")
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, firstID))
		assert.Empty(t, storedRFC822IDForCLI(t, f.Store, secondID))
	})
}

func TestDeduplicatePromptRetainsUndoGuidanceForDuplicateMerge(t *testing.T) {
	f := storetest.New(t)
	messageIDs := f.CreateMessages(2)
	_, err := f.Store.DB().Exec(f.Store.Rebind(
		`UPDATE messages SET rfc822_message_id = ? WHERE id IN (?, ?)`),
		"prompt-duplicate@example.test", messageIDs[0], messageIDs[1])
	require.NoError(t, err)
	cfgScoped := dedup.Config{
		AccountSourceIDs: []int64{f.Source.ID},
		Account:          f.Source.Identifier,
	}
	cmd := newDeduplicateCmd()
	require.NoError(t, cmd.Flags().Set("no-backup", "true"))
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader("n\n"))

	done := captureStdout(t)
	err = runDeduplicateOnce(cmd, f.Store, "", cfgScoped, dedup.NewEngine(f.Store, cfgScoped, nil))
	out := done()

	require.NoError(t, err)
	assert.Contains(t, out, "Proceed with deduplication? This will hide 1 duplicates (reversible with --undo). [y/N]:")
	assert.NotContains(t, out, "Proceed with RFC822 Message-ID derivation?")
}

func TestDeduplicatePlanChangedOutputReportsCommitAndNoBatch(t *testing.T) {
	f := storetest.New(t)
	messageIDs := f.CreateMessages(2)
	const sharedID = "revealed-duplicate@example.test"
	_, err := f.Store.DB().Exec(f.Store.Rebind(
		`UPDATE messages SET rfc822_message_id = ? WHERE id = ?`), sharedID, messageIDs[0])
	require.NoError(t, err)
	require.NoError(t, f.Store.UpsertMessageRaw(messageIDs[1], []byte(
		"Message-ID: <"+sharedID+">\r\n\r\nRevealed duplicate")))
	cfgScoped := dedup.Config{
		AccountSourceIDs: []int64{f.Source.ID},
		Account:          f.Source.Identifier,
	}
	cmd := newDeduplicateCmd()
	require.NoError(t, cmd.Flags().Set("yes", "true"))
	require.NoError(t, cmd.Flags().Set("no-backup", "true"))
	cmd.SetContext(t.Context())

	done := captureStdout(t)
	err = runDeduplicateOnce(
		cmd, f.Store, "", cfgScoped,
		dedup.NewEngine(f.Store, cfgScoped, nil),
	)
	out := done()

	require.Error(t, err)
	require.ErrorIs(t, err, dedup.ErrPlanChangedAfterRFC822Backfill)
	assert.Contains(t, err.Error(), "1 RFC822 Message-ID derivation was committed")
	assert.Contains(t, err.Error(), "no duplicate messages were hidden")
	assert.Contains(t, err.Error(), "no dedup batch was created")
	assert.Contains(t, err.Error(), "rerun deduplicate to review the updated plan")
	assert.NotContains(t, out, "Batch ID:")
	assert.Equal(t, sharedID, storedRFC822IDForCLI(t, f.Store, messageIDs[1]), "derivation committed")
	var hidden int
	require.NoError(t, f.Store.DB().QueryRow(f.Store.Rebind(
		`SELECT COUNT(*) FROM messages WHERE id IN (?, ?) AND deleted_at IS NOT NULL`),
		messageIDs[0], messageIDs[1],
	).Scan(&hidden))
	assert.Zero(t, hidden, "plan fence hides no messages")
}

func withDeduplicateTestConfig(t *testing.T) context.Context {
	t.Helper()
	cfg := config.NewDefaultConfig()
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	cfg.Data.DataDir = t.TempDir()
	return testCtx
}

func createPendingRFC822Message(
	t *testing.T,
	st *store.Store,
	sourceID, conversationID int64,
	sourceMessageID, rfc822MessageID string,
) int64 {
	t.Helper()
	id, err := st.UpsertMessage(&store.Message{
		ConversationID:  conversationID,
		SourceID:        sourceID,
		SourceMessageID: sourceMessageID,
		MessageType:     "email",
		SizeEstimate:    1000,
	})
	require.NoError(t, err)
	require.NoError(t, st.UpsertMessageRaw(id, []byte(
		"Message-ID: <"+rfc822MessageID+">\r\nFrom: sender@example.test\r\n\r\nBody")))
	return id
}

func storedRFC822IDForCLI(t *testing.T, st *store.Store, messageID int64) string {
	t.Helper()
	var value sql.NullString
	require.NoError(t, st.DB().QueryRow(st.Rebind(
		`SELECT rfc822_message_id FROM messages WHERE id = ?`), messageID).Scan(&value))
	return value.String
}

func TestDeduplicateNonInteractiveFormsUseDaemonRunner(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		want   []string
		stdout string
	}{
		{
			name:   "dry-run",
			args:   []string{"--account", "alice@example.com", "--dry-run"},
			want:   []string{deduplicateCommandName, "--account=alice@example.com", "--dry-run"},
			stdout: "Dry run complete. No changes made.\n",
		},
		{
			name:   "yes",
			args:   []string{"--account", "alice@example.com", "--yes"},
			want:   []string{deduplicateCommandName, "--account=alice@example.com", "--yes"},
			stdout: "Deduplication complete.\n",
		},
		{
			name:   "undo",
			args:   []string{"--undo", "batch-a", "--undo", "batch-b"},
			want:   []string{deduplicateCommandName, "--undo=batch-a", "--undo=batch-b"},
			stdout: "Restored 2 messages.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdoutJSON, err := json.Marshal(tt.stdout)
			require.NoError(t, err, "marshal stdout")
			server, requests := newDaemonCLIRunnerTestServer(t, func(req daemonCLIRunTestRequest) {
				assert.Equal(t, tt.want, req.Args, "args")
			}, `{"type":"stdout","data":`+string(stdoutJSON)+`}`, `{"type":"complete"}`)
			testCtx := configureRemoteDaemonForTest(t, server.URL)

			cmd := newDeduplicateRoutingTestCommand()
			cmd.SetContext(testCtx)
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.Execute(), "deduplicate")
			assert.Equal(t, 1, int(requests.Load()), "runner endpoint calls")
			assert.Equal(t, tt.stdout, stdout.String(), "stdout")
		})
	}
}

func TestDeduplicateInteractiveAccountPlansPromptsAndExecutesThroughDaemon(t *testing.T) {
	server, runRequests, planRequests := newDaemonCLIDeduplicateTestServer(t, func(req daemonCLIDeduplicatePlanTestRequest) {
		assert.Equal(t, "alice@example.com", req.Account, "account")
		assert.Empty(t, req.Collection, "collection")
	}, map[string]any{
		"items": []map[string]any{
			{
				"stdout":              "Scanning for duplicate messages...\n\n=== Deduplication Report ===\nDuplicate groups found: 1\nMessages to prune:      2\n",
				"duplicate_messages":  2,
				"plan_fingerprint":    "fp-account",
				"needs_confirmation":  true,
				"scope_label":         "alice@example.com",
				"source_id":           0,
				"scope_is_collection": false,
			},
		},
	}, func(req daemonCLIRunTestRequest) {
		assert.Equal(t, []string{
			deduplicateCommandName,
			"--account=alice@example.com",
			"--dedup-plan-confirmed",
			"--dedup-plan-fingerprint=fp-account",
			"--yes",
		}, req.Args, "runner args")
	}, `{"type":"stdout","data":"Merging duplicates...\n"}`, `{"type":"complete"}`)
	testCtx := configureRemoteDaemonForTest(t, server.URL)
	_ = testCtx

	cmd := newDeduplicateRoutingTestCommand()
	cmd.SetContext(testCtx)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetArgs([]string{"--account", "alice@example.com"})

	require.NoError(t, cmd.Execute(), "deduplicate")
	assert.Equal(t, 1, int(planRequests.Load()), "plan endpoint calls")
	assert.Equal(t, 1, int(runRequests.Load()), "runner endpoint calls")
	assert.Contains(t, stdout.String(), "Scanning for duplicate messages...", "plan stdout")
	assert.Contains(t, stdout.String(), "Proceed with deduplication? This will hide 2 duplicates", "prompt")
	assert.Contains(t, stdout.String(), "Merging duplicates...", "runner stdout")
}

func TestDeduplicateInteractiveAccountCancelDoesNotExecute(t *testing.T) {
	server, runRequests, planRequests := newDaemonCLIDeduplicateTestServer(t, nil, map[string]any{
		"items": []map[string]any{
			{
				"stdout":             "Scanning for duplicate messages...\nDuplicate groups found: 1\n",
				"duplicate_messages": 1,
				"plan_fingerprint":   "fp-account",
				"needs_confirmation": true,
			},
		},
	}, nil)
	testCtx := configureRemoteDaemonForTest(t, server.URL)
	_ = testCtx

	cmd := newDeduplicateRoutingTestCommand()
	cmd.SetContext(testCtx)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"--account", "alice@example.com"})

	require.NoError(t, cmd.Execute(), "deduplicate")
	assert.Equal(t, 1, int(planRequests.Load()), "plan endpoint calls")
	assert.Equal(t, 0, int(runRequests.Load()), "runner endpoint calls")
	assert.Contains(t, stdout.String(), "Aborted.", "cancel output")
}

func TestDeduplicateInteractivePerSourcePromptsShareInput(t *testing.T) {
	server, runRequests, planRequests := newDaemonCLIDeduplicateTestServer(t, nil, map[string]any{
		"prefix_stdout": "No --account specified; deduping each source independently.\n\n",
		"items": []map[string]any{
			{
				"stdout":             "--- alice@example.com (gmail) ---\nDuplicate groups found: 1\n",
				"duplicate_messages": 1,
				"plan_fingerprint":   "fp-alice",
				"needs_confirmation": true,
				"source_id":          101,
				"scope_label":        "alice@example.com",
			},
			{
				"stdout":             "--- bob@example.com (gmail) ---\nDuplicate groups found: 1\n",
				"duplicate_messages": 1,
				"plan_fingerprint":   "fp-bob",
				"needs_confirmation": true,
				"source_id":          202,
				"scope_label":        "bob@example.com",
			},
		},
	}, func(req daemonCLIRunTestRequest) {
		assert.Contains(t, req.Args, "--dedup-source-plan=101:fp-alice", "alice approval")
		assert.Contains(t, req.Args, "--dedup-source-plan=202:fp-bob", "bob approval")
	}, `{"type":"complete"}`)
	testCtx := configureRemoteDaemonForTest(t, server.URL)
	_ = testCtx

	cmd := newDeduplicateRoutingTestCommand()
	cmd.SetContext(testCtx)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetIn(strings.NewReader("y\ny\n"))

	require.NoError(t, cmd.Execute(), "deduplicate")
	assert.Equal(t, 1, int(planRequests.Load()), "plan endpoint calls")
	assert.Equal(t, 1, int(runRequests.Load()), "runner endpoint calls")
	assert.Contains(t, stdout.String(), "alice@example.com", "first prompt")
	assert.Contains(t, stdout.String(), "bob@example.com", "second prompt")
}

func TestDeduplicatePlanFingerprintChangesWhenRemoteDeletionTargetsChange(t *testing.T) {
	f := storetest.New(t)
	messageIDs := f.CreateMessages(2)

	const rfc822MessageID = "remote-target-change@example.test"
	_, err := f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages
		SET rfc822_message_id = ?
		WHERE id IN (?, ?)`), rfc822MessageID, messageIDs[0], messageIDs[1])
	require.NoError(t, err, "set shared RFC822 Message-ID")

	raw := []byte("Message-ID: <remote-target-change@example.test>\r\n" +
		"From: sender@example.test\r\nSubject: Same message\r\n\r\nSame body")
	require.NoError(t, f.Store.UpsertMessageRaw(messageIDs[0], raw), "store first raw MIME")
	require.NoError(t, f.Store.UpsertMessageRaw(messageIDs[1], raw), "store second raw MIME")

	cfg := dedup.Config{
		AccountSourceIDs:           []int64{f.Source.ID},
		Account:                    f.Source.Identifier,
		DeleteDupsFromSourceServer: true,
	}
	engine := dedup.NewEngine(f.Store, cfg, nil)
	confirmedReport, err := engine.Scan(t.Context())
	require.NoError(t, err, "scan confirmed plan")
	confirmedFingerprint, err := deduplicatePlanFingerprint(t.Context(), cfg, confirmedReport)
	require.NoError(t, err, "fingerprint confirmed plan")

	changedRaw := []byte("Message-ID: <remote-target-change@example.test>\r\n" +
		"From: sender@example.test\r\nSubject: Different message\r\n\r\nDifferent body")
	require.NoError(t, f.Store.UpsertMessageRaw(messageIDs[1], changedRaw), "change duplicate raw MIME")
	changedReport, err := engine.Scan(t.Context())
	require.NoError(t, err, "rescan changed plan")
	changedFingerprint, err := deduplicatePlanFingerprint(t.Context(), cfg, changedReport)
	require.NoError(t, err, "fingerprint changed plan")

	require.Len(t, confirmedReport.Groups, 1, "confirmed duplicate groups")
	require.Len(t, changedReport.Groups, 1, "changed duplicate groups")
	assert.Equal(t,
		confirmedReport.Groups[0].Messages[confirmedReport.Groups[0].Survivor].ID,
		changedReport.Groups[0].Messages[changedReport.Groups[0].Survivor].ID,
		"survivor ID",
	)
	assert.NotEqual(t, confirmedFingerprint, changedFingerprint,
		"fingerprint must cover MIME-dependent remote deletion targets")
}

func newDeduplicateRoutingTestCommand() *cobra.Command { return newDeduplicateCmd() }

// TestDeduplicateMutualExclusion confirms that passing both --account and
// --collection to the deduplicate command is rejected by cobra.
func TestDeduplicateMutualExclusion(t *testing.T) {
	cmd := newDeduplicateCmd()
	cmd.SetArgs([]string{"--account", "alpha@example.com", "--collection", "work"})

	err := cmd.Execute()
	require.Error(t, err, "expected error when both --account and --collection are set")
	msg := err.Error()
	assert.Contains(t, msg, "account", "error should mention account flag name")
	assert.Contains(t, msg, "collection", "error should mention collection flag name")
}

func TestDeduplicateAccountResolutionExcludesCalendarSources(t *testing.T) {
	f, accountID, _ := setupScopeFixture(t)

	cal, err := f.Store.GetOrCreateSource(sourceTypeCalendar, accountID+"/primary")
	require.NoError(t, err, "GetOrCreateSource calendar")
	cfg, err := json.Marshal(map[string]string{
		"account_email": accountID,
		"calendar_id":   "primary",
	})
	require.NoError(t, err, "marshal sync_config")
	require.NoError(t, f.Store.UpdateSourceSyncConfig(cal.ID, string(cfg)), "UpdateSourceSyncConfig")

	scope, err := ResolveEmailAccountFlag(f.Store, accountID)
	require.NoError(t, err)

	assert.ElementsMatch(t, []int64{f.Source.ID}, scope.SourceIDs())
	assert.NotContains(t, scope.SourceIDs(), cal.ID, "dedup account scope must not include Calendar sources")
}

// TestDeduplicateCollectionResolution confirms that --collection resolves
// successfully when the name matches a real collection in the store.
func TestDeduplicateCollectionResolution(t *testing.T) {
	f, _, collectionName := setupScopeFixture(t)

	scope, err := ResolveCollectionFlag(f.Store, collectionName)
	require.NoError(t, err)
	require.NotNil(t, scope.Collection, "expected Collection to be populated")
	assert.Equal(t, collectionName, scope.Collection.Name, "collection name")
	ids := scope.SourceIDs()
	assert.NotEmpty(t, ids, "expected non-empty SourceIDs for collection")
}

// TestDeduplicateCollectionResolution_MultiSource confirms SourceIDs expands
// to all members when a collection has more than one source.
func TestDeduplicateCollectionResolution_MultiSource(t *testing.T) {
	f := storetest.New(t)

	src2, err := f.Store.GetOrCreateSource("mbox", "backup@example.com")
	require.NoError(t, err, "GetOrCreateSource src2")

	collName := "two-account-collection"
	_, err = f.Store.CreateCollection(collName, "", []int64{f.Source.ID, src2.ID})
	require.NoError(t, err, "CreateCollection")

	scope, err := ResolveCollectionFlag(f.Store, collName)
	require.NoError(t, err)
	ids := scope.SourceIDs()
	assert.Len(t, ids, 2, "expected 2 source IDs, got %v", ids)
	assert.Equal(t, collName, scope.DisplayName(), "DisplayName")
}

func TestResolveDeduplicateScopeNonEmailCollectionReturnsInvalidError(t *testing.T) {
	f := storetest.New(t)

	calendarSource, err := f.Store.GetOrCreateSource("gcal", "calendar@example.com")
	require.NoError(t, err, "GetOrCreateSource calendar")
	_, err = f.Store.CreateCollection("calendars", "", []int64{calendarSource.ID})
	require.NoError(t, err, "CreateCollection")

	_, err = resolveDeduplicateScope(f.Store, deduplicateScopeRequest{
		Collection: "calendars",
	})

	require.Error(t, err, "expected non-email collection to be rejected")
	assert.Equal(t, opserr.KindInvalid, opserr.KindOf(err), "error kind")
	assert.Contains(t, err.Error(), `--collection "calendars" has no member accounts`, "error message")
}

// TestPrintAccumulatedUndoHint asserts the helper's behavior:
// no-op for <2 batches, prints recipe for ≥2. Iter15 follow-up:
// the exit-on-Execute-error path now also calls this helper so a
// user who hits an error mid-loop still sees how to undo what
// already ran.
func TestPrintAccumulatedUndoHint(t *testing.T) {
	for _, tc := range []struct {
		name         string
		batches      []string
		wantContains []string
		wantNoOutput bool
	}{
		{
			name:         "no batches",
			batches:      nil,
			wantNoOutput: true,
		},
		{
			name:         "single batch",
			batches:      []string{"dedup-1"},
			wantNoOutput: true,
		},
		{
			name:    "two batches",
			batches: []string{"dedup-a", "dedup-b"},
			wantContains: []string{
				"To undo all of the above",
				"--undo dedup-a",
				"--undo dedup-b",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := captureStdout(t)
			printAccumulatedUndoHint(tc.batches)
			out := done()
			if tc.wantNoOutput {
				assert.Empty(t, out, "expected no output")
				return
			}
			for _, want := range tc.wantContains {
				assert.Contains(t, out, want, "output missing %q", want)
			}
		})
	}
}
