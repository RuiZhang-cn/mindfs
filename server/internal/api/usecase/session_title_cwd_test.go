package usecase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mindfs/server/internal/session"
)

func configureTitleCwdTemplate(t *testing.T, h *titleTestHarness) string {
	t.Helper()
	resolved := filepath.Join(h.root.RootPath, "native")
	if err := os.Mkdir(resolved, 0700); err != nil {
		t.Fatal(err)
	}
	pool := h.service.Registry.GetAgentPool()
	cfg := pool.Config()
	cfg.Agents[0].CwdTemplate = filepath.Join("{root}", "native")
	pool.UpdateConfig(cfg)
	return resolved
}

func saveTitleCwdFixture(t *testing.T, h *titleTestHarness, state titleFixtureState) {
	t.Helper()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.statePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
}

func requirePersistedNativeCwd(t *testing.T, h *titleTestHarness, key, id, cwd string) {
	t.Helper()
	cold := h.reader()
	binding, err := cold.GetAgentBinding(context.Background(), key, "omp")
	if err != nil || binding.AgentSessionID != id || filepath.Clean(binding.NativeCwd) != filepath.Clean(cwd) {
		t.Fatalf("verified native cwd not persisted: %#v %v (want %q)", binding, err, cwd)
	}
}

func TestTitleSyncNewSessionIdleReleaseColdPreservesRawNativeCwd(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"fixture-created": {}})
	resolved := configureTitleCwdTemplate(t, h)
	state := h.state(t)
	state.Chat = true
	saveTitleCwdFixture(t, h, state)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	current, err := h.manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "First fallback"})
	if err != nil {
		t.Fatal(err)
	}
	h.installAutomaticTitleSync()
	if err := h.service.SendMessage(ctx, SendMessageInput{RootID: h.root.ID, Key: current.Key, Agent: "omp", Content: "Preserve this conversation"}); err != nil {
		t.Fatal(err)
	}
	requirePersistedNativeCwd(t, h, current.Key, "fixture-created", h.root.RootPath)
	pool := h.service.Registry.GetAgentPool()
	if released := pool.ReleaseInactiveSessionsDetailed(time.Now()); released.ReleasedSessions != 1 || released.FailedSessions != 0 {
		t.Fatalf("idle release: %#v", released)
	}
	if closed := h.state(t).ClosedSessions; len(closed) != 1 || closed[0] != "fixture-created" {
		t.Fatalf("idle release did not close the loaded native session: %#v", closed)
	}
	meta, err := h.manager.GetTitleMetadata(ctx, current.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.RenameIfRevision(ctx, current.Key, "Generated after idle", meta.NameRevision); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, current.Key, "auto"); result.Status != "updated" {
		t.Fatalf("cold native sync: %#v", result)
	}
	requirePersistedNativeCwd(t, h, current.Key, "fixture-created", h.root.RootPath)
	state = h.state(t)
	if state.ListCalls != 0 || len(state.Calls) != 2 || state.Sessions["fixture-created"].Title != "Generated after idle" {
		t.Fatalf("known identity performed lookup or missed native write: %#v", state)
	}
	for _, request := range state.Calls {
		if filepath.Clean(request.Cwd) != filepath.Clean(h.root.RootPath) || filepath.Clean(request.Cwd) == filepath.Clean(resolved) {
			t.Fatalf("raw-root native identity templated again: %#v", request)
		}
	}
	cold := h.reader()
	persisted, err := cold.Get(ctx, current.Key, 0)
	if err != nil || len(persisted.Exchanges) != 2 || persisted.Exchanges[0].Content != "Preserve this conversation" || persisted.Exchanges[1].Content != "Fixture assistant reply" {
		t.Fatalf("cold metadata sync changed chat history: %#v %v", persisted, err)
	}
}

func TestTitleSyncLegacyBindingsResolveRawAndTemplateIDsBeforeColdWrites(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"raw-native": {}, "template-native": {}})
	resolved := configureTitleCwdTemplate(t, h)
	state := h.state(t)
	templated := state.Sessions["template-native"]
	templated.Cwd = resolved
	state.Sessions["template-native"] = templated
	saveTitleCwdFixture(t, h, state)
	raw := h.bind(t, "Raw imported title", "raw-native")
	template := h.bind(t, "Template imported title", "template-native")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, target := range []struct {
		session *session.Session
		id, cwd string
	}{{raw, "raw-native", h.root.RootPath}, {template, "template-native", resolved}} {
		if result := h.service.SyncSessionTitle(ctx, h.root.ID, target.session.Key, "auto"); result.Status != "updated" {
			t.Fatalf("legacy %s metadata lookup: %#v", target.id, result)
		}
		requirePersistedNativeCwd(t, h, target.session.Key, target.id, target.cwd)
	}
	lookups := h.state(t).ListCalls
	if lookups == 0 {
		t.Fatal("legacy bindings wrote without native metadata identity lookup")
	}
	for _, target := range []struct {
		session *session.Session
		id, cwd string
	}{{raw, "raw-native", h.root.RootPath}, {template, "template-native", resolved}} {
		meta, err := h.manager.GetTitleMetadata(ctx, target.session.Key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.manager.RenameIfRevision(ctx, target.session.Key, meta.Name+" updated", meta.NameRevision); err != nil {
			t.Fatal(err)
		}
		if result := h.service.SyncSessionTitle(ctx, h.root.ID, target.session.Key, "auto"); result.Status != "updated" {
			t.Fatalf("known %s cold identity: %#v", target.id, result)
		}
		requirePersistedNativeCwd(t, h, target.session.Key, target.id, target.cwd)
	}
	state = h.state(t)
	if state.ListCalls != lookups || len(state.Calls) != 4 {
		t.Fatalf("verified identities were rediscovered or not synchronized: %#v", state)
	}
	for _, request := range state.Calls {
		want := h.root.RootPath
		if request.SessionID == "template-native" {
			want = resolved
		}
		if filepath.Clean(request.Cwd) != filepath.Clean(want) {
			t.Fatalf("native ID bound to arbitrary cwd: %#v (want %q)", request, want)
		}
	}
}
