package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
	rootfs "mindfs/server/internal/fs"
)

func titleTestManager(t *testing.T) (*Manager, *Session) {
	t.Helper()
	m := NewManager(rootfs.NewRootInfo("titles", "titles", t.TempDir()))
	t.Cleanup(func() { _ = m.Shutdown() })
	s, err := m.Create(context.Background(), CreateInput{Type: TypeChat, Agent: "omp", Name: "Existing title"})
	if err != nil {
		t.Fatal(err)
	}
	return m, s
}

func TestTitleMetadataDoesNotReadBrokenHistoryAndEnumeratesDuplicateBindings(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	if err := m.AddExchangeForAgent(ctx, s, "user", "preserve this chat", "omp", "", "", ""); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(m.Root().RootPath, "worktree")
	if _, err := m.RecordRelatedWorktree(ctx, s.Key, m.Root().ID, worktree, "branch", "head"); err != nil {
		t.Fatal(err)
	}
	other, err := m.Create(ctx, CreateInput{Type: TypeChat, Name: "Different title"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{s.Key, other.Key} {
		if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: key, Agent: "omp", AgentSessionID: "shared-native-id"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Shutdown(); err != nil {
		t.Fatal(err)
	}
	path, err := m.exchangePath(s.Key)
	if err != nil {
		t.Fatal(err)
	}
	broken := []byte("not valid JSON\n")
	if err := m.root.WriteMetaFile(path, broken); err != nil {
		t.Fatal(err)
	}
	unreadablePath, err := m.exchangePath(other.Key)
	if err != nil {
		t.Fatal(err)
	}
	unreadablePath = filepath.Join(m.Root().MetaDir(), filepath.FromSlash(unreadablePath))
	if err := os.Remove(unreadablePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(unreadablePath, 0o755); err != nil {
		t.Fatal(err)
	}
	cold := NewManager(m.Root())
	defer cold.Shutdown()
	metadata, err := cold.GetTitleMetadata(ctx, s.Key)
	if err != nil || metadata.Name != s.Name || metadata.RelatedWorktree == nil || metadata.RelatedWorktree.Path != worktree {
		t.Fatalf("metadata = %+v, err = %v", metadata, err)
	}
	items, err := cold.ListTitleMetadata(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
	for _, item := range items {
		if len(item.Bindings) != 1 || item.Bindings[0].AgentSessionID != "shared-native-id" {
			t.Fatalf("binding omitted: %+v", item)
		}
	}
	if _, err := cold.Get(ctx, other.Key, 0); err == nil {
		t.Fatal("broken history unexpectedly readable")
	}
	data, err := m.root.ReadMetaFile(path)
	if err != nil || string(data) != string(broken) {
		t.Fatalf("metadata touched history: %q, %v", data, err)
	}
}

func TestBindingTitleAcknowledgementPersistsAndResetsWithoutChangingCursor(t *testing.T) {
	for _, replacement := range []string{"state", "binding"} {
		t.Run(replacement, func(t *testing.T) {
			ctx := context.Background()
			m, s := titleTestManager(t)
			if err := m.UpdateAgentState(ctx, s, "omp", 3, "native-1"); err != nil {
				t.Fatal(err)
			}
			cursor := agenttypes.ExternalSessionCursor{SourcePath: "native.jsonl", Offset: 123, ModTimeUnixNano: 456}
			if err := m.UpdateExternalSessionCursor(ctx, s.Key, "omp", cursor); err != nil {
				t.Fatal(err)
			}
			ok, err := m.AcknowledgeBindingTitle(ctx, s.Key, "omp", "native-1", s.Name, s.NameRevision, s.Name)
			if err != nil || !ok {
				t.Fatalf("ack = %v, %v", ok, err)
			}
			cwd := filepath.Join(m.Root().RootPath, "native process")
			if ok, err := m.AcknowledgeBindingCwd(ctx, s.Key, "omp", "native-1", cwd); err != nil || !ok {
				t.Fatalf("cwd ack = %v, %v", ok, err)
			}
			if err := m.UpdateAgentState(ctx, s, "omp", 4, "native-1"); err != nil {
				t.Fatal(err)
			}
			if err := m.Shutdown(); err != nil {
				t.Fatal(err)
			}
			cold := NewManager(m.Root())
			defer cold.Shutdown()
			b, err := cold.GetAgentBinding(ctx, s.Key, "omp")
			if err != nil || b.NativeCwd != cwd || b.NativeTitle == nil || *b.NativeTitle != s.Name || b.ExternalCursor() != cursor || b.AgentCtxSeq != 4 {
				t.Fatalf("persisted ack = %+v, %v", b, err)
			}
			metadata, err := cold.GetTitleMetadata(ctx, s.Key)
			if err != nil || metadata.Name != s.Name || metadata.NameRevision != s.NameRevision || len(metadata.Bindings) != 1 || metadata.Bindings[0].NativeCwd != cwd {
				t.Fatalf("persisted metadata = %+v, %v", metadata, err)
			}
			if replacement == "state" {
				err = cold.UpdateAgentState(ctx, s, "omp", 5, "native-2")
			} else {
				err = cold.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "native-2", AgentCtxSeq: 5})
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err = cold.GetAgentBinding(ctx, s.Key, "omp")
			if err != nil || b.NativeCwd != "" || b.NativeTitle != nil || b.ExternalCursor() != cursor || b.AgentCtxSeq != 5 {
				t.Fatalf("replacement = %+v, %v", b, err)
			}
			ok, err = cold.AcknowledgeBindingTitle(ctx, s.Key, "omp", "native-1", s.Name, s.NameRevision, "stale")
			if err != nil || ok {
				t.Fatalf("accepted stale identity: %v, %v", ok, err)
			}
			if ok, err := cold.AcknowledgeBindingCwd(ctx, s.Key, "omp", "native-1", cwd); err != nil || ok {
				t.Fatalf("accepted stale cwd identity: %v, %v", ok, err)
			}
		})
	}
}

func TestDelayedAutomaticRenameCannotOverwriteManualRenameEvenSameName(t *testing.T) {
	for _, manual := range []string{"Manual title", "Existing title"} {
		t.Run(manual, func(t *testing.T) {
			ctx := context.Background()
			m, s := titleTestManager(t)
			metadata, err := m.GetTitleMetadata(ctx, s.Key)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "native"}); err != nil {
				t.Fatal(err)
			}
			releaseGeneration := make(chan struct{})
			type renameResult struct {
				session *Session
				err     error
			}
			done := make(chan renameResult, 1)
			go func() {
				<-releaseGeneration
				renamed, err := m.RenameIfRevision(ctx, s.Key, "Generated title", metadata.NameRevision)
				done <- renameResult{renamed, err}
			}()
			if _, err := m.Rename(ctx, s.Key, manual); err != nil {
				t.Fatal(err)
			}
			close(releaseGeneration)
			result := <-done
			if result.err != nil || result.session != nil {
				t.Fatalf("stale automatic rename = %+v, %v", result.session, result.err)
			}
			if ok, err := m.AcknowledgeBindingTitle(ctx, s.Key, "omp", "native", metadata.Name, metadata.NameRevision, "stale"); err != nil || ok {
				t.Fatalf("accepted stale name/revision: %v, %v", ok, err)
			}
			if err := m.Shutdown(); err != nil {
				t.Fatal(err)
			}
			cold := NewManager(m.Root())
			defer cold.Shutdown()
			persisted, err := cold.GetTitleMetadata(ctx, s.Key)
			if err != nil || persisted.Name != manual || persisted.NameRevision != metadata.NameRevision+1 {
				t.Fatalf("persisted manual name = %+v, %v", persisted, err)
			}
		})
	}
}

func TestConditionalRenameSucceedsAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	if err := m.AddExchangeForAgent(ctx, s, "user", "unchanged chat", "omp", "", "", ""); err != nil {
		t.Fatal(err)
	}
	path, err := m.exchangePath(s.Key)
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.root.ReadMetaFile(path)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := m.RenameIfRevision(ctx, s.Key, "Generated title", 0)
	if err != nil || renamed == nil || renamed.NameRevision != 1 {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	after, err := m.root.ReadMetaFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("rename touched chat: %v", err)
	}
}

func TestWithTitleSyncSerializesSessionMutationsWithoutGlobalLock(t *testing.T) {
	for _, operation := range []string{"rename", "binding", "edit"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			m, s := titleTestManager(t)
			other, err := m.Create(ctx, CreateInput{Type: TypeChat})
			if err != nil {
				t.Fatal(err)
			}
			if err := m.AddExchangeForAgent(ctx, s, "user", "edit target", "omp", "", "", ""); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			syncDone := make(chan error, 1)
			go func() {
				syncDone <- m.WithTitleSync(ctx, s.Key, func(metadata TitleMetadata) error {
					close(entered)
					<-release
					return nil
				})
			}()
			<-entered
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			changed := make(chan error, 1)
			go func() {
				if operation == "rename" {
					_, err := m.Rename(ctx, s.Key, "Manual")
					changed <- err
				} else if operation == "edit" {
					_, err := m.ReplaceHistoryBefore(ctx, s.Key, s.Exchanges[0], &AgentBinding{Agent: "omp", AgentSessionID: "replacement"})
					changed <- err
				} else {
					changed <- m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "replacement"})
				}
			}()
			select {
			case err := <-changed:
				t.Fatalf("mutation crossed title guard: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			independent := make(chan error, 1)
			go func() { _, err := m.Rename(ctx, other.Key, "Independent"); independent <- err }()
			select {
			case err := <-independent:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("title operation held global manager lock")
			}
			close(release)
			if err := <-syncDone; err != nil {
				t.Fatal(err)
			}
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTitleBindingHookSeesCommittedActualIDsWithoutChangingPersistenceOutcome(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	var observed []string
	var hookErr error
	m.SetTitleSync(func(ctx context.Context, key, agent string) {
		hookErr = m.WithTitleSync(ctx, key, func(metadata TitleMetadata) error {
			if agent != "omp" || len(metadata.Bindings) != 1 {
				return errors.New("missing committed binding")
			}
			observed = append(observed, metadata.Bindings[0].AgentSessionID)
			if metadata.Bindings[0].NativeTitle != nil {
				return errors.New("new binding retained an old title acknowledgement")
			}
			return nil
		})
	})
	done := make(chan error, 1)
	go func() {
		if err := m.UpdateAgentState(ctx, s, "omp", 1, "initial"); err != nil {
			done <- err
			return
		}
		if err := m.UpdateAgentState(ctx, s, "omp", 2, "initial"); err != nil {
			done <- err
			return
		}
		if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "imported"}); err != nil {
			done <- err
			return
		}
		if ok, err := m.AcknowledgeBindingTitle(ctx, s.Key, "omp", "imported", s.Name, s.NameRevision, s.Name); err != nil || !ok {
			done <- errors.New("could not acknowledge pre-edit binding")
			return
		}
		if err := m.AddExchangeForAgent(ctx, s, "user", "edit target", "omp", "", "", ""); err != nil {
			done <- err
			return
		}
		_, err := m.ReplaceHistoryBefore(ctx, s.Key, s.Exchanges[0], &AgentBinding{Agent: "omp", AgentSessionID: "forked"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("binding hook ran while persistence locks were held")
	}
	if strings.Join(observed, ",") != "initial,imported,forked" {
		t.Fatalf("observed IDs = %v", observed)
	}
	err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "failed-sync"})
	if err != nil || hookErr != nil {
		t.Fatalf("binding or notification failed: %v, %v", err, hookErr)
	}
	if strings.Join(observed, ",") != "initial,imported,forked,failed-sync" {
		t.Fatalf("replacement notification lost: %v", observed)
	}
	binding, err := m.GetAgentBinding(ctx, s.Key, "omp")
	if err != nil || binding.AgentSessionID != "failed-sync" {
		t.Fatalf("successful binding persistence lost: %+v, %v", binding, err)
	}
}

func TestTitleColumnsMigrateExistingSQLite(t *testing.T) {
	root := rootfs.NewRootInfo("migration", "migration", t.TempDir())
	path := filepath.Join(root.MetaDir(), filepath.FromSlash(sessionDBPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLiteDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sessionTableSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_agent_bindings (session_key TEXT NOT NULL, agent TEXT NOT NULL, agent_session_id TEXT NOT NULL, agent_ctx_seq INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (session_key, agent))`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root)
	defer m.Shutdown()
	s, err := m.Create(context.Background(), CreateInput{Type: TypeChat, Name: "Migrated"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertAgentBinding(context.Background(), AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "migrated"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.AcknowledgeBindingTitle(context.Background(), s.Key, "omp", "migrated", s.Name, 0, s.Name); err != nil || !ok {
		t.Fatalf("migrated ack = %v, %v", ok, err)
	}
	binding, err := m.GetAgentBinding(context.Background(), s.Key, "omp")
	if err != nil || binding.NativeCwd != "" {
		t.Fatalf("legacy cwd must remain unknown: %+v, %v", binding, err)
	}
	if ok, err := m.AcknowledgeBindingCwd(context.Background(), s.Key, "omp", "migrated", root.RootPath); err != nil || !ok {
		t.Fatalf("migrated cwd ack = %v, %v", ok, err)
	}
	binding, err = m.GetAgentBinding(context.Background(), s.Key, "omp")
	if err != nil || binding.NativeCwd != root.RootPath || binding.NativeTitle == nil || *binding.NativeTitle != s.Name {
		t.Fatalf("migrated cwd/title = %+v, %v", binding, err)
	}
}

func TestBindingPersistenceUsesLatestNameInsteadOfStaleSession(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	stale := *s
	renamed, err := m.Rename(ctx, s.Key, "Latest manual title")
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := renamed.NameRevision
	var observed TitleMetadata
	var hookErr error
	m.SetTitleSync(func(ctx context.Context, key, agent string) {
		hookErr = m.WithTitleSync(ctx, key, func(metadata TitleMetadata) error {
			observed = metadata
			if len(metadata.Bindings) != 1 {
				return errors.New("missing binding")
			}
			binding := metadata.Bindings[0]
			ok, err := m.AcknowledgeBindingTitle(ctx, key, agent, binding.AgentSessionID, metadata.Name, metadata.NameRevision, metadata.Name)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("current snapshot rejected")
			}
			return nil
		})
	})
	if err := m.UpdateAgentState(ctx, &stale, "omp", 2, "new-actual-native-id"); err != nil {
		t.Fatal(err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if observed.Name != "Latest manual title" || observed.NameRevision != wantRevision {
		t.Fatalf("stale input replaced current name: %+v", observed)
	}
	if err := m.Shutdown(); err != nil {
		t.Fatal(err)
	}
	cold := NewManager(m.Root())
	defer cold.Shutdown()
	persisted, err := cold.GetTitleMetadata(ctx, s.Key)
	if err != nil || persisted.Name != observed.Name || persisted.NameRevision != wantRevision || len(persisted.Bindings) != 1 || persisted.Bindings[0].NativeTitle == nil || *persisted.Bindings[0].NativeTitle != observed.Name {
		t.Fatalf("persisted current name = %+v, %v", persisted, err)
	}
}

func TestEditedHistoryReturnsCommittedSessionAfterTitleNotification(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	if err := m.AddExchangeForAgent(ctx, s, "user", "edit me", "omp", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var observed TitleMetadata
	var hookErr error
	m.SetTitleSync(func(ctx context.Context, key, agent string) { observed, hookErr = m.GetTitleMetadata(ctx, key) })
	updated, err := m.ReplaceHistoryBefore(ctx, s.Key, s.Exchanges[0], &AgentBinding{Agent: "omp", AgentSessionID: "committed-fork"})
	if updated == nil || len(updated.Exchanges) != 0 || err != nil || hookErr != nil || observed.Key != s.Key || len(observed.Bindings) != 1 || observed.Bindings[0].AgentSessionID != "committed-fork" {
		t.Fatalf("committed edit = %+v, %v", updated, err)
	}
	if err := m.Shutdown(); err != nil {
		t.Fatal(err)
	}
	cold := NewManager(m.Root())
	defer cold.Shutdown()
	persisted, err := cold.Get(ctx, s.Key, 0)
	if err != nil || len(persisted.Exchanges) != 0 || persisted.HistoryRevision != 1 {
		t.Fatalf("edited history lost after sync failure: %+v, %v", persisted, err)
	}
	binding, err := cold.GetAgentBinding(ctx, s.Key, "omp")
	if err != nil || binding.AgentSessionID != "committed-fork" {
		t.Fatalf("edited binding lost after sync failure: %+v, %v", binding, err)
	}
}

func TestJoinedTitleMetadataPreservesBindingsAndTargetedConflicts(t *testing.T) {
	ctx := context.Background()
	m, first := titleTestManager(t)
	second, err := m.Create(ctx, CreateInput{Type: TypeChat, Name: first.Name})
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := m.Create(ctx, CreateInput{Type: TypeChat, Name: "Unbound"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{first.Key, second.Key} {
		if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: key, Agent: "omp", AgentSessionID: "shared"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: first.Key, Agent: "codex", AgentSessionID: "other-agent", AgentCtxSeq: 7}); err != nil {
		t.Fatal(err)
	}
	cursor := agenttypes.ExternalSessionCursor{SourcePath: "native.jsonl", Offset: 123, ModTimeUnixNano: 456}
	if err := m.UpdateExternalSessionCursor(ctx, first.Key, "omp", cursor); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.AcknowledgeBindingTitle(ctx, first.Key, "omp", "shared", first.Name, first.NameRevision, "native actual title"); err != nil || !ok {
		t.Fatalf("ack: %v %v", ok, err)
	}
	if ok, err := m.AcknowledgeBindingCwd(ctx, first.Key, "omp", "shared", m.Root().RootPath); err != nil || !ok {
		t.Fatalf("cwd ack: %v %v", ok, err)
	}
	items, err := m.ListTitleMetadata(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("joined items: %#v %v", items, err)
	}
	for _, item := range items {
		switch item.Key {
		case first.Key:
			if len(item.Bindings) != 2 {
				t.Fatalf("joined bindings: %#v", item)
			}
			for _, binding := range item.Bindings {
				if binding.Agent == "omp" && (binding.NativeCwd != m.Root().RootPath || binding.ExternalCursor() != cursor || binding.NativeTitle == nil || *binding.NativeTitle != "native actual title") {
					t.Fatalf("joined cursor/ack lost: %#v", binding)
				}
			}
		case second.Key:
			if len(item.Bindings) != 1 || item.Bindings[0].AgentSessionID != "shared" {
				t.Fatalf("second binding: %#v", item)
			}
		case unbound.Key:
			if len(item.Bindings) != 0 {
				t.Fatalf("phantom LEFT JOIN binding: %#v", item)
			}
		}
	}
	if conflict, err := m.HasConflictingTitleBinding(ctx, first.Key, "omp", "shared", first.Name); err != nil || conflict {
		t.Fatalf("equal titles conflict: %v %v", conflict, err)
	}
	if _, err := m.Rename(ctx, second.Key, "Different name"); err != nil {
		t.Fatal(err)
	}
	if conflict, err := m.HasConflictingTitleBinding(ctx, first.Key, "omp", "shared", first.Name); err != nil || !conflict {
		t.Fatalf("different titles not rejected: %v %v", conflict, err)
	}
	if conflict, err := m.HasConflictingTitleBinding(ctx, first.Key, "codex", "shared", first.Name); err != nil || conflict {
		t.Fatalf("agent identity mixed: %v %v", conflict, err)
	}
	if conflict, err := m.HasConflictingTitleBinding(ctx, first.Key, "omp", "another-native", first.Name); err != nil || conflict {
		t.Fatalf("native identity mixed: %v %v", conflict, err)
	}
}

func TestHistoryInvalidationClearsNativeCwdEvenWhenNativeIDIsReused(t *testing.T) {
	ctx := context.Background()
	m, s := titleTestManager(t)
	if err := m.AddExchangeForAgent(ctx, s, "user", "edit me", "omp", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertAgentBinding(ctx, AgentBinding{SessionKey: s.Key, Agent: "omp", AgentSessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.AcknowledgeBindingCwd(ctx, s.Key, "omp", "native", m.Root().RootPath); err != nil || !ok {
		t.Fatalf("cwd ack = %v, %v", ok, err)
	}
	if ok, err := m.AcknowledgeBindingTitle(ctx, s.Key, "omp", "native", s.Name, s.NameRevision, s.Name); err != nil || !ok {
		t.Fatalf("title ack = %v, %v", ok, err)
	}
	if _, err := m.ReplaceHistoryBefore(ctx, s.Key, s.Exchanges[0], &AgentBinding{Agent: "omp", AgentSessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	binding, err := m.GetAgentBinding(ctx, s.Key, "omp")
	if err != nil || binding.AgentSessionID != "native" || binding.NativeCwd != "" || binding.NativeTitle != nil {
		t.Fatalf("invalidated binding = %+v, %v", binding, err)
	}
}
