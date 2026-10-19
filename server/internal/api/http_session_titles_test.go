package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mindfs/server/internal/agent"
	"mindfs/server/internal/api/usecase"
	"mindfs/server/internal/fs"
	"mindfs/server/internal/session"
)

func TestSessionTitleHTTPBatchAndRenameUnsupported(t *testing.T) {
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The existing ACP fixture does not advertise the optional extension.
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{Name: "omp", Protocol: agent.ProtocolACP, Command: executable, Args: []string{"-test.run=^TestEditACPProcess$"}, Env: map[string]string{"MINDFS_EDIT_ACP_HELPER": "1"}}}})
	app := &AppContext{Dirs: registry, Agents: pool}
	var reader *session.Manager
	t.Cleanup(func() {
		pool.CloseAll()
		if reader != nil {
			if err := reader.Shutdown(); err != nil {
				t.Errorf("close HTTP title reader: %v", err)
			}
		}
		app.ReleaseRootResources(root.ID)
	})
	manager, err := app.GetSessionManager(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "Old title"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpsertAgentBinding(context.Background(), session.AgentBinding{SessionKey: created.Key, Agent: "omp", AgentSessionID: "persisted-native-id"}); err != nil {
		t.Fatal(err)
	}
	router := (&HTTPHandler{AppContext: app}).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/titles/sync", strings.NewReader(`{"root_id":"`+root.ID+`","agent":"omp"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("batch status %d: %s", response.Code, response.Body.String())
	}
	var batch map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"updated", "unchanged", "kept", "conflict", "busy", "not_found", "unsupported", "failed"} {
		if _, ok := batch[field]; !ok {
			t.Fatalf("missing zero-valued counter %q: %s", field, response.Body.String())
		}
	}
	if string(batch["unsupported"]) != "1" || string(batch["updated"]) != "0" {
		t.Fatalf("unsupported batch: %s", response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.Key+"/rename?root="+root.ID, strings.NewReader(`{"name":"Saved locally"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rename status %d: %s", response.Code, response.Body.String())
	}
	var rename struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		TitleSync struct {
			SessionKey     string `json:"session_key"`
			AgentSessionID string `json:"agent_session_id"`
			Status         string `json:"status"`
		} `json:"title_sync"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rename); err != nil {
		t.Fatal(err)
	}
	if rename.Key != created.Key || rename.Name != "Saved locally" || rename.TitleSync.Status != "unsupported" || rename.TitleSync.AgentSessionID != "persisted-native-id" || rename.TitleSync.SessionKey != created.Key {
		t.Fatalf("rename response: %s", response.Body.String())
	}
	reader = session.NewManager(root)
	meta, err := reader.GetTitleMetadata(context.Background(), created.Key)
	if err != nil || meta.Name != "Saved locally" {
		t.Fatalf("local persistence: %#v %v", meta, err)
	}
}

func TestSessionTitleHTTPUnboundRenameAndEmptyBatch(t *testing.T) {
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &AppContext{Dirs: registry}
	t.Cleanup(func() { app.ReleaseRootResources(root.ID) })
	manager, err := app.GetSessionManager(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "Unbound"})
	if err != nil {
		t.Fatal(err)
	}
	router := (&HTTPHandler{AppContext: app}).Routes()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.Key+"/rename?root="+root.ID, strings.NewReader(`{"name":"No binding yet"}`)))
	var rename map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rename); err != nil {
		t.Fatal(err)
	}
	result, ok := rename["title_sync"].(map[string]any)
	if response.Code != http.StatusOK || !ok || result["status"] != "skipped" {
		t.Fatalf("unbound rename: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/titles/sync", strings.NewReader(`{"root_id":"`+root.ID+`","agent":"omp"}`)))
	var batch struct {
		Results []any `json:"results"`
		Failed  int   `json:"failed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || batch.Results == nil || len(batch.Results) != 0 || batch.Failed != 0 {
		t.Fatalf("empty batch: %s", response.Body.String())
	}
}

// An ACP protocol fixture, not native OMP: metadata failure/busy is deliberately
// injected here while AppContext, the pool, SQLite, and websocket writes are real.
func TestSessionTitleOutcomeACPProcess(t *testing.T) {
	mode := os.Getenv("MINDFS_API_TITLE_OUTCOME_FIXTURE")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"_meta": map[string]any{"omp.sessionTitle": mode != "unsupported"}}}
		case "session/new":
			result = map[string]any{"sessionId": "api-title-created"}
		case "session/prompt":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(request.Params, &params) != nil {
				os.Exit(2)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": params.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "API fixture assistant reply"}}}})
			result = map[string]any{"stopReason": "end_turn"}
		case "_omp/session/title":
			if mode != "busy" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "fixture native title write I/O failure"}})
				continue
			}
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(request.Params, &params) != nil {
				os.Exit(3)
			}
			result = map[string]any{"sessionId": params.SessionID, "title": "", "status": "busy"}
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unexpected fixture method"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestSessionTitleAutomaticOutcomeWebsocketKeepsSuccessfulChat(t *testing.T) {
	for _, status := range []string{"failed", "busy", "unsupported"} {
		t.Run(status, func(t *testing.T) {
			registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
			root, err := registry.Upsert(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			pool := agent.NewPool(agent.Config{Agents: []agent.Definition{{Name: "omp", Protocol: agent.ProtocolACP, Command: executable, Args: []string{"-test.run=^TestSessionTitleOutcomeACPProcess$"}, Env: map[string]string{"MINDFS_API_TITLE_OUTCOME_FIXTURE": status}}}})
			app := &AppContext{Dirs: registry, Agents: pool}
			var reader *session.Manager
			t.Cleanup(func() {
				pool.CloseAll()
				if reader != nil {
					if err := reader.Shutdown(); err != nil {
						t.Errorf("close websocket title reader: %v", err)
					}
				}
				app.ReleaseRootResources(root.ID)
			})
			manager, err := app.GetSessionManager(root.ID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			current, err := manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "omp", Name: "Visible title outcome"})
			if err != nil {
				t.Fatal(err)
			}
			hub := app.GetSessionStreamHub()
			ready := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				// A root-metadata listener need not subscribe to the chat stream.
				hub.RegisterClient("title-observer", conn)
				defer hub.UnregisterClient("title-observer", conn)
				ready <- struct{}{}
				<-release
			}))
			defer server.Close()
			defer close(release)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("websocket registration timed out")
			}
			service := &usecase.Service{Registry: app}
			for _, text := range []string{"first question", "second question"} {
				if err := service.SendMessage(ctx, usecase.SendMessageInput{RootID: root.ID, Key: current.Key, Agent: "omp", Content: text}); err != nil {
					t.Fatalf("native title %s failed successful chat: %v", status, err)
				}
			}
			// An ordered marker bounds the absence/duplicate assertions without
			// sleeping or intentionally timing out a websocket read.
			hub.BroadcastAll(WSResponse{Type: "fixture.title_outcomes_complete"})
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			titleEvents := 0
			for {
				var event struct {
					Type    string `json:"type"`
					Payload struct {
						RootID         string `json:"root_id"`
						SessionKey     string `json:"session_key"`
						Agent          string `json:"agent"`
						AgentSessionID string `json:"agent_session_id"`
						Status         string `json:"status"`
						Error          string `json:"error"`
					} `json:"payload"`
				}
				if err := conn.ReadJSON(&event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "fixture.title_outcomes_complete" {
					break
				}
				if event.Type == "message.failed" || strings.HasPrefix(event.Type, "task.") {
					t.Fatalf("title side effect changed core outcome: %#v", event)
				}
				if event.Type != "session.title_sync" {
					continue
				}
				titleEvents++
				if event.Payload.RootID != root.ID || event.Payload.SessionKey != current.Key || event.Payload.Agent != "omp" || event.Payload.AgentSessionID != "api-title-created" || event.Payload.Status != status {
					t.Fatalf("websocket title outcome: %#v", event)
				}
				if status == "failed" && !strings.Contains(event.Payload.Error, "fixture native title write I/O failure") {
					t.Fatalf("native failure not visible: %#v", event)
				}
			}
			if titleEvents != 1 {
				t.Fatalf("want one binding outcome across two successful turns, got %d", titleEvents)
			}
			reader = session.NewManager(root)
			persisted, err := reader.Get(ctx, current.Key, 0)
			if err != nil || len(persisted.Exchanges) != 4 || persisted.Exchanges[1].Content != "API fixture assistant reply" || persisted.Exchanges[3].Content != "API fixture assistant reply" {
				t.Fatalf("assistant replies not committed: %#v %v", persisted, err)
			}
			binding, err := reader.GetAgentBinding(ctx, current.Key, "omp")
			if err != nil || binding.AgentSessionID != "api-title-created" || binding.NativeTitle != nil {
				t.Fatalf("binding not committed: %#v %v", binding, err)
			}
		})
	}
}
