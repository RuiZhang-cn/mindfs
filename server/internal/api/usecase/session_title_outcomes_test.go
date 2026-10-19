package usecase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mindfs/server/internal/session"
)

const titleOutcomeWriteFailure = "fixture native title write I/O failure"

type titleOutcomeEvent struct {
	rootID string
	result TitleSyncResult
}

// Collect only the optional publication boundary. Chat, binding changes, title
// coordination, ACP transport and SQLite persistence all use real implementations.
type titleOutcomeRegistry struct {
	*titleTestRegistry
	mu     sync.Mutex
	events []titleOutcomeEvent
}

func (r *titleOutcomeRegistry) BroadcastSessionTitleSync(rootID string, result TitleSyncResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, titleOutcomeEvent{rootID: rootID, result: result})
}

func (r *titleOutcomeRegistry) snapshot() []titleOutcomeEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]titleOutcomeEvent(nil), r.events...)
}

func newTitleOutcomeHarness(t *testing.T, status string) (*titleTestHarness, *titleOutcomeRegistry) {
	t.Helper()
	native := titleFixtureSession{}
	switch status {
	case "failed":
		native.WriteError = titleOutcomeWriteFailure
	case "busy":
		native.Status = "busy"
	case "unsupported":
	default:
		t.Fatalf("unknown fixture status %q", status)
	}
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"fixture-created": native, "source": {}})
	state := h.state(t)
	state.Chat = true
	state.Unsupported = status == "unsupported"
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.statePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	registry := &titleOutcomeRegistry{titleTestRegistry: h.service.Registry.(*titleTestRegistry)}
	h.service.Registry = registry
	return h, registry
}

func (h *titleTestHarness) installAutomaticTitleSync() {
	h.manager.SetTitleSync(func(ctx context.Context, key, agent string) {
		if agent == "omp" {
			h.service.SyncSessionTitleAutomatic(ctx, h.root.ID, key)
		}
	})
}

func requireTitleOutcome(t *testing.T, h *titleTestHarness, registry *titleOutcomeRegistry, key, status string) {
	t.Helper()
	events := registry.snapshot()
	if len(events) != 1 {
		t.Fatalf("want one automatic title outcome, got %#v", events)
	}
	event := events[0]
	if event.rootID != h.root.ID || event.result.SessionKey != key || event.result.AgentSessionID != "fixture-created" || event.result.Status != status {
		t.Fatalf("automatic outcome: %#v", event)
	}
	if status == "failed" && !strings.Contains(event.result.Error, titleOutcomeWriteFailure) {
		t.Fatalf("native persistence failure not visible: %#v", event)
	}
	state := h.state(t)
	if status == "unsupported" {
		if len(state.Calls) != 0 {
			t.Fatalf("unadvertised extension invoked: %#v", state.Calls)
		}
	} else if len(state.Calls) != 1 || state.Calls[0].Title == nil {
		t.Fatalf("want one atomic native title write, got %#v", state.Calls)
	}
}

func requirePersistedTitleOutcomeBinding(t *testing.T, h *titleTestHarness, key string) {
	t.Helper()
	binding, err := h.reader().GetAgentBinding(context.Background(), key, "omp")
	if err != nil || binding.AgentSessionID != "fixture-created" || binding.NativeTitle != nil || filepath.Clean(binding.NativeCwd) != filepath.Clean(h.root.RootPath) {
		t.Fatalf("actual binding not committed independently of title failure: %#v %v", binding, err)
	}
}

func addTitleOutcomeHistory(t *testing.T, h *titleTestHarness, current *session.Session) {
	t.Helper()
	for i, text := range []string{"first question", "first answer", "wrong question", "old answer"} {
		role := "user"
		if i%2 == 1 {
			role = "agent"
		}
		if err := h.manager.AddExchangeForAgent(context.Background(), current, role, text, "omp", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSendMessageTitleOutcomeDoesNotFailChatOrRepeatUnchangedBinding(t *testing.T) {
	for _, status := range []string{"failed", "busy", "unsupported"} {
		t.Run(status, func(t *testing.T) {
			h, registry := newTitleOutcomeHarness(t, status)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			current, err := h.manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "Chat title"})
			if err != nil {
				t.Fatal(err)
			}
			h.installAutomaticTitleSync()
			for _, text := range []string{"first question", "second question"} {
				if err := h.service.SendMessage(ctx, SendMessageInput{RootID: h.root.ID, Key: current.Key, Agent: "omp", Content: text}); err != nil {
					t.Fatalf("title %s failed successful chat: %v", status, err)
				}
			}
			persisted, err := h.reader().Get(ctx, current.Key, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(persisted.Exchanges) != 4 || persisted.Exchanges[0].Content != "first question" || persisted.Exchanges[1].Role != "agent" || persisted.Exchanges[1].Content != "Fixture assistant reply" || persisted.Exchanges[2].Content != "second question" || persisted.Exchanges[3].Content != "Fixture assistant reply" {
				t.Fatalf("successful assistant replies not persisted: %#v", persisted.Exchanges)
			}
			requirePersistedTitleOutcomeBinding(t, h, current.Key)
			requireTitleOutcome(t, h, registry, current.Key, status)
		})
	}
}

func TestForkSessionTitleFailureReturnsCommittedCreatedSession(t *testing.T) {
	for _, status := range []string{"failed", "busy"} {
		t.Run(status, func(t *testing.T) {
			h, registry := newTitleOutcomeHarness(t, status)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			original := h.bind(t, "Original title", "source")
			addTitleOutcomeHistory(t, h, original)
			h.installAutomaticTitleSync()
			out, err := h.service.ForkSession(ctx, ForkSessionInput{RootID: h.root.ID, Key: original.Key, Seq: original.Exchanges[1].Seq})
			if err != nil || out.Session == nil || out.Session.Key == "" || out.Session.Key == original.Key {
				t.Fatalf("title %s hid successful fork: %#v %v", status, out, err)
			}
			persisted, err := h.reader().Get(ctx, out.Session.Key, 0)
			if err != nil || persisted.ParentSessionKey != original.Key || len(persisted.Exchanges) != 2 || persisted.Exchanges[0].Content != "first question" || persisted.Exchanges[1].Content != "first answer" {
				t.Fatalf("fork rolled back or changed copied history: %#v %v", persisted, err)
			}
			unchanged, err := h.reader().Get(ctx, original.Key, 0)
			if err != nil || len(unchanged.Exchanges) != 4 || unchanged.HistoryRevision != 0 {
				t.Fatalf("source fork history changed: %#v %v", unchanged, err)
			}
			requirePersistedTitleOutcomeBinding(t, h, out.Session.Key)
			requireTitleOutcome(t, h, registry, out.Session.Key, status)
		})
	}
}

func TestPrepareEditedSessionTitleFailureReturnsCommittedHistoryAndBinding(t *testing.T) {
	for _, status := range []string{"failed", "busy"} {
		t.Run(status, func(t *testing.T) {
			h, registry := newTitleOutcomeHarness(t, status)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			original := h.bind(t, "Edited title", "source")
			addTitleOutcomeHistory(t, h, original)
			target := original.Exchanges[2]
			h.installAutomaticTitleSync()
			updated, edited, err := h.service.PrepareEditedSession(ctx, EditSessionMessageInput{RootID: h.root.ID, Key: original.Key, Seq: target.Seq, OriginalContent: target.Content, OriginalTimestamp: target.Timestamp})
			if err != nil || updated == nil || updated.Key != original.Key || updated.HistoryRevision != 1 || len(updated.Exchanges) != 2 || edited.Seq != target.Seq || edited.Content != target.Content || edited.Agent != "omp" {
				t.Fatalf("title %s hid committed edit: %#v %#v %v", status, updated, edited, err)
			}
			persisted, err := h.reader().Get(ctx, original.Key, 0)
			if err != nil || persisted.HistoryRevision != 1 || len(persisted.Exchanges) != 2 || persisted.Exchanges[0].Content != "first question" || persisted.Exchanges[1].Content != "first answer" {
				t.Fatalf("edited prefix not committed: %#v %v", persisted, err)
			}
			requirePersistedTitleOutcomeBinding(t, h, original.Key)
			requireTitleOutcome(t, h, registry, original.Key, status)
		})
	}
}

func TestSuggestSessionNameTitleFailureReturnsRenamedSessionAndPublishes(t *testing.T) {
	for _, status := range []string{"failed", "busy"} {
		t.Run(status, func(t *testing.T) {
			h, registry := newTitleOutcomeHarness(t, status)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			message := "Please investigate how automatic session naming preserves successful local results despite native metadata failure"
			current := h.bind(t, BuildFallbackSessionName(message), "fixture-created")
			h.installAutomaticTitleSync()
			renamed, err := h.service.SuggestSessionName(ctx, SuggestSessionNameInput{RootID: h.root.ID, SessionKey: current.Key, Agent: "omp", FirstMessage: message})
			if err != nil || renamed == nil || renamed.Key != current.Key || renamed.Name != "Fixture assistant reply" {
				t.Fatalf("title %s changed successful naming result: %#v %v", status, renamed, err)
			}
			meta, err := h.reader().GetTitleMetadata(ctx, current.Key)
			if err != nil || meta.Name != "Fixture assistant reply" {
				t.Fatalf("automatic name not committed: %#v %v", meta, err)
			}
			requirePersistedTitleOutcomeBinding(t, h, current.Key)
			requireTitleOutcome(t, h, registry, current.Key, status)
		})
	}
}

func TestRenameSessionNativeWriteIOFailureKeepsPartialSuccessResponse(t *testing.T) {
	h, _ := newTitleOutcomeHarness(t, "failed")
	current := h.bind(t, "Old title", "fixture-created")
	out, err := h.service.RenameSession(context.Background(), RenameSessionInput{RootID: h.root.ID, Key: current.Key, Name: "Saved locally"})
	if err != nil || out == nil || out.Session == nil || out.Session.Key != current.Key || out.Session.Name != "Saved locally" || out.TitleSync == nil || out.TitleSync.Status != "failed" || !strings.Contains(out.TitleSync.Error, titleOutcomeWriteFailure) {
		t.Fatalf("manual rename partial response changed: %#v %v", out, err)
	}
	meta, err := h.reader().GetTitleMetadata(context.Background(), current.Key)
	if err != nil || meta.Name != "Saved locally" {
		t.Fatalf("manual name rolled back: %#v %v", meta, err)
	}
}

func TestAutomaticTitleBindingPublishesEveryOutcomeOnce(t *testing.T) {
	for _, status := range []string{"updated", "unchanged", "kept", "conflict", "busy", "not_found", "unsupported", "failed"} {
		t.Run(status, func(t *testing.T) {
			native := titleFixtureSession{}
			switch status {
			case "unchanged", "conflict", "busy":
				native.Status = status
			case "kept":
				native.Title, native.Source = "Independent title", "user"
			case "failed":
				native.WriteError = titleOutcomeWriteFailure
			}
			sessions := map[string]titleFixtureSession{"native": native}
			if status == "not_found" {
				delete(sessions, "native")
			}
			h := newTitleTestHarness(t, sessions)
			if status == "unsupported" {
				state := h.state(t)
				state.Unsupported = true
				payload, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.statePath, payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			registry := &titleOutcomeRegistry{titleTestRegistry: h.service.Registry.(*titleTestRegistry)}
			h.service.Registry = registry
			h.installAutomaticTitleSync()
			current := h.bind(t, "MindFS title", "native")
			// Cursor-only persistence and an identical binding upsert are not
			// actual native identity changes, including after a failed attempt.
			ctx := context.Background()
			if err := h.manager.UpdateAgentState(ctx, current, "omp", 7, "native"); err != nil {
				t.Fatal(err)
			}
			if err := h.manager.UpsertAgentBinding(ctx, session.AgentBinding{SessionKey: current.Key, Agent: "omp", AgentSessionID: "native", AgentCtxSeq: 7}); err != nil {
				t.Fatal(err)
			}
			events := registry.snapshot()
			if len(events) != 1 || events[0].rootID != h.root.ID || events[0].result.SessionKey != current.Key || events[0].result.AgentSessionID != "native" || events[0].result.Status != status {
				t.Fatalf("binding outcome was omitted, repeated, or changed: %#v", events)
			}
			if status == "failed" && !strings.Contains(events[0].result.Error, titleOutcomeWriteFailure) {
				t.Fatalf("failure hidden: %#v", events)
			}
		})
	}
}

func TestAutomaticNamingUnboundDoesNotPublishOMPOutcome(t *testing.T) {
	h, registry := newTitleOutcomeHarness(t, "failed")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	message := "Please investigate automatic naming before an actual native conversation binding has been established"
	current, err := h.manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: BuildFallbackSessionName(message)})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := h.service.SuggestSessionName(ctx, SuggestSessionNameInput{RootID: h.root.ID, SessionKey: current.Key, Agent: "omp", FirstMessage: message})
	if err != nil || renamed == nil || renamed.Name != "Fixture assistant reply" {
		t.Fatalf("unbound naming: %#v %v", renamed, err)
	}
	events := registry.snapshot()
	if len(events) != 0 {
		t.Fatalf("unbound naming published an OMP outcome: %#v", events)
	}
	if calls := h.state(t).Calls; len(calls) != 0 {
		t.Fatalf("unbound naming touched native metadata: %#v", calls)
	}
}

func TestSessionTitleDoesNotClaimIndependentMatchingNativeTitle(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {Title: "Matching title", Source: "auto"}})
	current := h.bind(t, "Matching title", "native")
	ctx := context.Background()
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, current.Key, "auto"); result.Status != "kept" {
		t.Fatalf("independent title: %#v", result)
	}
	binding, err := h.manager.GetAgentBinding(ctx, current.Key, "omp")
	if err != nil || binding.NativeTitle != nil {
		t.Fatalf("independent matching title claimed: %#v %v", binding, err)
	}
	meta, err := h.manager.GetTitleMetadata(ctx, current.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.RenameIfRevision(ctx, current.Key, "Later generated title", meta.NameRevision); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, current.Key, "auto"); result.Status != "kept" {
		t.Fatalf("independent title overwritten after naming: %#v", result)
	}
	if native := h.state(t).Sessions["native"]; native.Title != "Matching title" {
		t.Fatalf("native title changed: %#v", native)
	}
}
