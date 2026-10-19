package usecase

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mindfs/server/internal/agent"
	agenttypes "mindfs/server/internal/agent/types"
	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/session"
)

// This is a protocol/storage fixture, not an implementation of OMP. Native
// ownership and journal preservation are tested in OMP's own suite. By default
// it rejects chat startup; outcome tests explicitly enable protocol chat replies.
type titleFixtureSession struct {
	Cwd        string `json:"cwd"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Status     string `json:"status,omitempty"`
	RaceTitle  string `json:"race_title,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	WriteError string `json:"write_error,omitempty"`
}

type titleFixtureState struct {
	Sessions       map[string]titleFixtureSession   `json:"sessions"`
	Calls          []agenttypes.SessionTitleRequest `json:"calls"`
	Unsupported    bool                             `json:"unsupported"`
	Chat           bool                             `json:"chat,omitempty"`
	Reply          string                           `json:"reply,omitempty"`
	RootCwd        string                           `json:"root_cwd"`
	ListCalls      int                              `json:"list_calls"`
	ClosedSessions []string                         `json:"closed_sessions"`
}

func TestSessionTitleProtocolFixture(t *testing.T) {
	path := os.Getenv("MINDFS_TITLE_COORDINATOR_FIXTURE")
	if path == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	loaded := make(map[string]bool)
	for scanner.Scan() {
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &rpc) != nil || len(rpc.ID) == 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			os.Exit(2)
		}
		var state titleFixtureState
		if json.Unmarshal(data, &state) != nil {
			os.Exit(3)
		}
		var result any
		switch rpc.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"close": map[string]any{}}, "_meta": map[string]any{"omp.sessionTitle": !state.Unsupported}}}
		case "session/list":
			var params struct {
				Cwd *string `json:"cwd"`
			}
			if json.Unmarshal(rpc.Params, &params) != nil {
				os.Exit(7)
			}
			items := []map[string]any{}
			for id, native := range state.Sessions {
				if params.Cwd == nil || filepath.Clean(*params.Cwd) == filepath.Clean(native.Cwd) {
					items = append(items, map[string]any{"sessionId": id, "cwd": native.Cwd, "title": native.Title})
				}
			}
			state.ListCalls++
			payload, _ := json.Marshal(state)
			if os.WriteFile(path, payload, 0600) != nil {
				os.Exit(5)
			}
			result = map[string]any{"sessions": items}
		case "session/close":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(rpc.Params, &params) != nil || !loaded[params.SessionID] {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32602, "message": "fixture session is not loaded"}})
				continue
			}
			delete(loaded, params.SessionID)
			state.ClosedSessions = append(state.ClosedSessions, params.SessionID)
			payload, _ := json.Marshal(state)
			if os.WriteFile(path, payload, 0600) != nil {
				os.Exit(5)
			}
			result = map[string]any{}
		case "session/new", "session/load", "session/resume", "session/prompt":
			if !state.Chat {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32601, "message": "fixture forbids chat/session startup"}})
				continue
			}
			switch rpc.Method {
			case "session/new":
				var params struct {
					Cwd string `json:"cwd"`
				}
				if json.Unmarshal(rpc.Params, &params) != nil {
					os.Exit(8)
				}
				id := "fixture-created"
				if params.Cwd != state.RootCwd {
					id = "fixture-naming"
				}
				native := state.Sessions[id]
				native.Cwd = params.Cwd
				state.Sessions[id] = native
				loaded[id] = true
				payload, _ := json.Marshal(state)
				if os.WriteFile(path, payload, 0600) != nil {
					os.Exit(5)
				}
				result = map[string]any{"sessionId": id}
			case "session/load", "session/resume":
				var params struct {
					SessionID string `json:"sessionId"`
					Cwd       string `json:"cwd"`
				}
				if json.Unmarshal(rpc.Params, &params) != nil {
					os.Exit(9)
				}
				native, found := state.Sessions[params.SessionID]
				if !found || filepath.Clean(native.Cwd) != filepath.Clean(params.Cwd) {
					_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32602, "message": "fixture resume identity not found"}})
					continue
				}
				loaded[params.SessionID] = true
				result = map[string]any{}
			case "session/prompt":
				var params struct {
					SessionID string `json:"sessionId"`
				}
				if json.Unmarshal(rpc.Params, &params) != nil {
					os.Exit(6)
				}
				reply := state.Reply
				if reply == "" {
					reply = "Fixture assistant reply"
				}
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": params.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": reply}}}})
				result = map[string]any{"stopReason": "end_turn"}
			}
		case "_omp/session/title":
			var request agenttypes.SessionTitleRequest
			if json.Unmarshal(rpc.Params, &request) != nil {
				os.Exit(4)
			}
			state.Calls = append(state.Calls, request)
			native, found := state.Sessions[request.SessionID]
			found = found && filepath.Clean(native.Cwd) == filepath.Clean(request.Cwd)
			// Deliberately simulate native persistence I/O failure at the wire
			// boundary; MindFS still uses its real pool and SQLite store.
			if found && request.Title != nil && native.WriteError != "" {
				payload, _ := json.Marshal(state)
				if os.WriteFile(path, payload, 0600) != nil {
					os.Exit(5)
				}
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32603, "message": native.WriteError}})
				continue
			}
			status := "unchanged"
			if !found {
				status = "not_found"
			} else if native.Status != "" {
				status = native.Status
			} else if request.Title != nil {
				if native.RaceTitle != "" {
					native.Title, native.RaceTitle = native.RaceTitle, ""
				}
				switch {
				case request.ExpectedTitle != nil && *request.ExpectedTitle != native.Title:
					status = "conflict"
				case request.OnlyIfEmpty && native.Title != "":
					status = "kept"
				case request.Source != "user" && native.Source == "user":
					status = "kept"
				case native.Title == *request.Title && native.Source == request.Source:
				default:
					native.Title, native.Source, status = *request.Title, request.Source, "updated"
				}
				if status == "updated" && native.Limit > 0 && len(native.Title) > native.Limit {
					native.Title = native.Title[:native.Limit]
				}
				state.Sessions[request.SessionID] = native
			}
			result = agenttypes.SessionTitleResult{SessionID: request.SessionID, Title: native.Title, Source: native.Source, Status: status}
			payload, _ := json.Marshal(state)
			if os.WriteFile(path, payload, 0600) != nil {
				os.Exit(5)
			}
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32601, "message": "fixture forbids chat/session startup"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}
	os.Exit(0)
}

type titleTestRegistry struct {
	*commandTestRegistry
	pool *agent.Pool
}

func (r *titleTestRegistry) GetAgentPool() *agent.Pool { return r.pool }

type titleTestHarness struct {
	service   *Service
	manager   *session.Manager
	root      rootfs.RootInfo
	statePath string
	readers   []*session.Manager
}

func newTitleTestHarness(t *testing.T, native map[string]titleFixtureSession) *titleTestHarness {
	t.Helper()
	root := rootfs.NewRootInfo("titles", "titles", t.TempDir())
	manager := session.NewManager(root)
	path := filepath.Join(t.TempDir(), "native-metadata.json")
	for id, item := range native {
		if item.Cwd == "" {
			item.Cwd = root.RootPath
			native[id] = item
		}
	}
	payload, err := json.Marshal(titleFixtureState{Sessions: native, RootCwd: root.RootPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{Name: "omp", Protocol: agent.ProtocolACP, Command: executable, Args: []string{"-test.run=^TestSessionTitleProtocolFixture$"}, Env: map[string]string{"MINDFS_TITLE_COORDINATOR_FIXTURE": path}}}})
	registry := &titleTestRegistry{commandTestRegistry: &commandTestRegistry{root: root, manager: manager}, pool: pool}
	h := &titleTestHarness{service: &Service{Registry: registry}, manager: manager, root: root, statePath: path}
	t.Cleanup(func() {
		pool.CloseAll()
		for _, reader := range h.readers {
			if err := reader.Shutdown(); err != nil {
				t.Errorf("close title reader: %v", err)
			}
		}
		if err := manager.Shutdown(); err != nil {
			t.Errorf("close title manager: %v", err)
		}
	})
	return h
}

func (h *titleTestHarness) reader() *session.Manager {
	reader := session.NewManager(h.root)
	h.readers = append(h.readers, reader)
	return reader
}

func (h *titleTestHarness) state(t *testing.T) titleFixtureState {
	t.Helper()
	payload, err := os.ReadFile(h.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state titleFixtureState
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func (h *titleTestHarness) bind(t *testing.T, name, id string) *session.Session {
	t.Helper()
	created, err := h.manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manager.UpsertAgentBinding(context.Background(), session.AgentBinding{SessionKey: created.Key, Agent: "omp", AgentSessionID: id}); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestSyncSessionTitlesMetadataOnlyPartialResults(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"empty": {}, "owned": {Title: "Native user title", Source: "user"}, "busy": {Status: "busy"}})
	empty := h.bind(t, "MindFS title", "empty")
	h.bind(t, "Should not overwrite", "owned")
	h.bind(t, "Busy title", "busy")
	h.bind(t, "Missing title", "missing")
	// A corrupt history file must not prevent metadata-only backfill.
	history := filepath.Join(h.root.MetaDir(), "sessions", empty.Key+".jsonl")
	corrupt := []byte("{unreadable history\n")
	if err := os.WriteFile(history, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := h.service.SyncSessionTitles(context.Background(), SyncSessionTitlesInput{RootID: h.root.ID, Agent: "omp"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Updated != 1 || out.Kept != 1 || out.Busy != 1 || out.NotFound != 1 || len(out.Results) != 4 || out.Failed != 0 {
		t.Fatalf("batch: %#v", out)
	}
	state := h.state(t)
	if state.Sessions["empty"].Title != "MindFS title" || state.Sessions["owned"].Title != "Native user title" {
		t.Fatalf("native persisted state: %#v", state)
	}
	if payload, err := os.ReadFile(history); err != nil || string(payload) != string(corrupt) {
		t.Fatalf("history changed: %q %v", payload, err)
	}
	binding, err := h.reader().GetAgentBinding(context.Background(), empty.Key, "omp")
	if err != nil || binding.NativeTitle == nil || *binding.NativeTitle != "MindFS title" {
		t.Fatalf("persisted acknowledgement: %#v %v", binding, err)
	}
}

func TestSessionTitleAutomaticFallbackAndManualPrecedence(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {}})
	ctx := context.Background()
	h.manager.SetTitleSync(func(ctx context.Context, key, agent string) { h.service.SyncSessionTitleAutomatic(ctx, h.root.ID, key) })
	created := h.bind(t, "First message fallback", "native")
	if h.state(t).Sessions["native"].Title != "First message fallback" {
		t.Fatal("binding did not synchronize early fallback")
	}
	meta, err := h.manager.GetTitleMetadata(ctx, created.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.RenameIfRevision(ctx, created.Key, "Generated title", meta.NameRevision); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, created.Key, "auto"); result.Status != "updated" {
		t.Fatalf("generated sync: %#v", result)
	}
	if state := h.state(t); state.Sessions["native"].Title != "Generated title" || state.Sessions["native"].Source != "auto" {
		t.Fatalf("generated native: %#v", state)
	}
	out, err := h.service.RenameSession(ctx, RenameSessionInput{RootID: h.root.ID, Key: created.Key, Name: "Manual title"})
	if err != nil || out.TitleSync == nil || out.TitleSync.Status != "updated" {
		t.Fatalf("manual rename: %#v %v", out, err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, created.Key, "auto"); result.Status != "kept" {
		t.Fatalf("manual precedence: %#v", result)
	}
	if state := h.state(t); state.Sessions["native"].Title != "Manual title" || state.Sessions["native"].Source != "user" {
		t.Fatalf("manual native: %#v", state)
	}
}

func TestSessionTitleLateBindingUsesLatestName(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {}})
	ctx := context.Background()
	created, err := h.manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "Fallback"})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := h.manager.GetTitleMetadata(ctx, created.Key)
	if _, err := h.manager.RenameIfRevision(ctx, created.Key, "Already generated", meta.NameRevision); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, created.Key, "auto"); result.Status != "skipped" {
		t.Fatalf("unbound: %#v", result)
	}
	h.manager.SetTitleSync(func(ctx context.Context, key, agent string) { h.service.SyncSessionTitleAutomatic(ctx, h.root.ID, key) })
	if err := h.manager.UpdateAgentState(ctx, created, "omp", 0, "native"); err != nil {
		t.Fatal(err)
	}
	if state := h.state(t); state.Sessions["native"].Title != "Already generated" {
		t.Fatalf("late binding stale title: %#v", state)
	}
}

func TestSessionTitleDuplicateConflictAndCASConflict(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"duplicate": {}, "racing": {}, "independent": {RaceTitle: "Independently named"}})
	ctx := context.Background()
	a := h.bind(t, "First title", "duplicate")
	h.bind(t, "Second title", "duplicate")
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, a.Key, "user"); result.Status != "conflict" {
		t.Fatalf("duplicate: %#v", result)
	}
	if calls := h.state(t).Calls; len(calls) != 0 {
		t.Fatalf("ambiguous binding invoked native: %#v", calls)
	}
	b := h.bind(t, "Generated", "racing")
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, b.Key, "auto"); result.Status != "updated" {
		t.Fatalf("initial acknowledgement: %#v", result)
	}
	state := h.state(t)
	native := state.Sessions["racing"]
	native.RaceTitle = "Independently renamed after acknowledgement"
	state.Sessions["racing"] = native
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.statePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, b.Key, "auto"); result.Status != "conflict" {
		t.Fatalf("CAS: %#v", result)
	}
	binding, err := h.manager.GetAgentBinding(ctx, b.Key, "omp")
	if err != nil || binding.NativeTitle == nil || *binding.NativeTitle != "Generated" {
		t.Fatalf("failed CAS changed acknowledgement: %#v %v", binding, err)
	}
	if h.state(t).Sessions["racing"].Title != "Independently renamed after acknowledgement" {
		t.Fatal("CAS overwrote independent change")
	}
	c := h.bind(t, "Generated independently", "independent")
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, c.Key, "auto"); result.Status != "kept" {
		t.Fatalf("unacknowledged native title: %#v", result)
	}
	binding, err = h.manager.GetAgentBinding(ctx, c.Key, "omp")
	if err != nil || binding.NativeTitle != nil {
		t.Fatalf("independent title claimed: %#v %v", binding, err)
	}
}

func TestRenameSessionReportsNativeFailureWithoutRollback(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {Status: "busy"}})
	created := h.bind(t, "Old title", "native")
	out, err := h.service.RenameSession(context.Background(), RenameSessionInput{RootID: h.root.ID, Key: created.Key, Name: "Saved locally"})
	if err != nil || out.TitleSync == nil || out.TitleSync.Status != "busy" {
		t.Fatalf("rename result: %#v %v", out, err)
	}
	meta, err := h.reader().GetTitleMetadata(context.Background(), created.Key)
	if err != nil || meta.Name != "Saved locally" {
		t.Fatalf("local rename rolled back: %#v %v", meta, err)
	}
}

func TestSyncSessionTitlesUnsupportedAndNoBinding(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {}})
	created := h.bind(t, "Old native", "native")
	state := h.state(t)
	state.Unsupported = true
	payload, _ := json.Marshal(state)
	if err := os.WriteFile(h.statePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := h.service.SyncSessionTitles(context.Background(), SyncSessionTitlesInput{RootID: h.root.ID, Agent: "omp"})
	if err != nil || out.Unsupported != 1 || out.Updated != 0 {
		t.Fatalf("unsupported: %#v %v", out, err)
	}
	if state := h.state(t); len(state.Calls) != 0 || state.ListCalls != 0 {
		t.Fatal("metadata lookup or extension called despite absent title capability")
	}
	if result := h.service.SyncSessionTitle(context.Background(), h.root.ID, created.Key, "auto"); result.Status != "unsupported" {
		t.Fatalf("unsupported automatic: %#v", result)
	}
	if _, err := h.service.SyncSessionTitles(context.Background(), SyncSessionTitlesInput{RootID: h.root.ID, Agent: "codex"}); err == nil {
		t.Fatal("other agent accepted by OMP-only batch")
	}
}

func TestDelayedTitleNamingProtocolFixture(t *testing.T) {
	ready := os.Getenv("MINDFS_DELAYED_TITLE_READY")
	if ready == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	respond := func(id json.RawMessage, result any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		switch request.Method {
		case "initialize":
			respond(request.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}})
		case "session/new":
			respond(request.ID, map[string]any{"sessionId": "naming-only"})
		case "session/prompt":
			if os.WriteFile(ready, []byte("ready"), 0600) != nil {
				os.Exit(2)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(os.Getenv("MINDFS_DELAYED_TITLE_RELEASE")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(3)
				}
				time.Sleep(5 * time.Millisecond)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "naming-only", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Generated too late"}}}})
			respond(request.ID, map[string]any{"stopReason": "end_turn"})
		default:
			respond(request.ID, map[string]any{})
		}
	}
	os.Exit(0)
}

func TestSuggestSessionNameDelayedGenerationPreservesManualRename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := rootfs.NewRootInfo("delayed-title", "delayed-title", t.TempDir())
	manager := session.NewManager(root)
	t.Cleanup(func() {
		if err := manager.Shutdown(); err != nil {
			t.Errorf("close naming manager: %v", err)
		}
	})
	message := "Please investigate the delayed session naming race and preserve the explicit manual title"
	fallback := BuildFallbackSessionName(message)
	created, err := manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: fallback})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready, release := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "release")
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{Name: "title-race-fixture", Protocol: agent.ProtocolACP, Command: executable, Args: []string{"-test.run=^TestDelayedTitleNamingProtocolFixture$"}, Env: map[string]string{"MINDFS_DELAYED_TITLE_READY": ready, "MINDFS_DELAYED_TITLE_RELEASE": release}}}})
	t.Cleanup(pool.CloseAll)
	service := &Service{Registry: &titleTestRegistry{commandTestRegistry: &commandTestRegistry{root: root, manager: manager}, pool: pool}}
	type completion struct {
		session *session.Session
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		renamed, err := service.SuggestSessionName(ctx, SuggestSessionNameInput{RootID: root.ID, SessionKey: created.Key, Agent: "title-race-fixture", FirstMessage: message})
		done <- completion{renamed, err}
	}()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("naming request did not reach fixture")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := service.RenameSession(ctx, RenameSessionInput{RootID: root.ID, Key: created.Key, Name: "Chosen manually"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.session != nil {
			t.Fatalf("stale naming completion: %#v %v", result.session, result.err)
		}
	case <-ctx.Done():
		t.Fatal("naming completion did not finish")
	}
	reader := session.NewManager(root)
	defer func() {
		if err := reader.Shutdown(); err != nil {
			t.Errorf("close naming reader: %v", err)
		}
	}()
	meta, err := reader.GetTitleMetadata(ctx, created.Key)
	if err != nil || meta.Name != "Chosen manually" {
		t.Fatalf("manual name overwritten: %#v %v", meta, err)
	}
}

func TestSyncSessionTitlesContinuesAfterNativeProtocolFailure(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"broken": {Status: "invalid-native-status"}, "healthy": {}})
	h.bind(t, "Broken response", "broken")
	healthy := h.bind(t, "Healthy title", "healthy")
	out, err := h.service.SyncSessionTitles(context.Background(), SyncSessionTitlesInput{RootID: h.root.ID, Agent: "omp"})
	if err != nil || out.Failed != 1 || out.Updated != 1 || len(out.Results) != 2 {
		t.Fatalf("partial batch: %#v %v", out, err)
	}
	foundFailure := false
	for _, result := range out.Results {
		if result.AgentSessionID == "broken" {
			foundFailure = result.Status == "failed" && result.Error != ""
		}
	}
	if !foundFailure {
		t.Fatalf("native protocol failure swallowed: %#v", out)
	}
	binding, err := h.reader().GetAgentBinding(context.Background(), healthy.Key, "omp")
	if err != nil || binding.NativeTitle == nil || *binding.NativeTitle != "Healthy title" {
		t.Fatalf("independent item not committed: %#v %v", binding, err)
	}
}

func TestSessionTitleAcknowledgesActualNativeTitle(t *testing.T) {
	h := newTitleTestHarness(t, map[string]titleFixtureSession{"native": {Limit: 8}})
	created := h.bind(t, "Fallback title longer than native storage", "native")
	ctx := context.Background()
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, created.Key, "auto"); result.Status != "updated" {
		t.Fatalf("initial sync: %#v", result)
	}
	binding, err := h.reader().GetAgentBinding(ctx, created.Key, "omp")
	if err != nil || binding.NativeTitle == nil || *binding.NativeTitle != "Fallback" {
		t.Fatalf("acknowledged request instead of actual title: %#v %v", binding, err)
	}
	meta, err := h.manager.GetTitleMetadata(ctx, created.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.RenameIfRevision(ctx, created.Key, "Generated title longer than native storage", meta.NameRevision); err != nil {
		t.Fatal(err)
	}
	if result := h.service.SyncSessionTitle(ctx, h.root.ID, created.Key, "auto"); result.Status != "updated" {
		t.Fatalf("actual-title CAS: %#v", result)
	}
	binding, err = h.reader().GetAgentBinding(ctx, created.Key, "omp")
	if err != nil || binding.NativeTitle == nil || *binding.NativeTitle != "Generate" {
		t.Fatalf("normalized acknowledgement: %#v %v", binding, err)
	}
	meta, err = h.manager.GetTitleMetadata(ctx, created.Key)
	if err != nil || meta.Name != "Generated title longer than native storage" {
		t.Fatalf("full MindFS name changed: %#v %v", meta, err)
	}
}
