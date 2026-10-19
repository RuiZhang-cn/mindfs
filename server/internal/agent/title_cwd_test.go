package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
)

// This subprocess checks the actual process cwd and native session wire cwd.
// It is only a protocol fixture, not an implementation of OMP title persistence.
func TestSessionTitleCwdProtocolFixture(t *testing.T) {
	mode := os.Getenv("MINDFS_TITLE_CWD_FIXTURE")
	if mode == "" {
		return
	}
	root, resolved := os.Getenv("MINDFS_TITLE_CWD_ROOT"), os.Getenv("MINDFS_TITLE_CWD_RESOLVED")
	processCwd, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	respondError := func(id json.RawMessage, message string) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": message}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	sessionCwd := root
	if mode == "resume" || mode == "legacy-template" || mode == "exact-template" {
		sessionCwd = resolved
	}
	live := false
	legacy := mode == "legacy-root" || mode == "legacy-template"
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cwd       string `json:"cwd"`
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			// Live and legacy processes start at the configured template cwd;
			// a known cold session starts directly at its recorded native cwd.
			if filepath.Clean(processCwd) != filepath.Clean(resolved) && filepath.Clean(processCwd) != filepath.Clean(sessionCwd) {
				respondError(request.ID, fmt.Sprintf("unexpected process cwd %q", processCwd))
				continue
			}
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{
				"_meta":               map[string]any{"omp.sessionTitle": true},
				"loadSession":         true,
				"sessionCapabilities": map[string]any{"close": map[string]any{}},
			}}
		case "session/new":
			if mode != "new" || request.Params.Cwd != root || filepath.Clean(processCwd) != filepath.Clean(resolved) {
				respondError(request.ID, "session/new must retain its original RootPath, not the process template cwd")
				continue
			}
			live = true
			result = map[string]any{"sessionId": "cwd-native"}
		case "session/resume":
			if mode != "resume" || filepath.Clean(request.Params.Cwd) != filepath.Clean(resolved) || request.Params.SessionID != "cwd-native" {
				respondError(request.ID, "session/resume did not use its resolved cwd and exact ID")
				continue
			}
			live = true
			result = map[string]any{}
		case "session/close":
			if !live || request.Params.SessionID != "cwd-native" {
				respondError(request.ID, "only an explicitly opened session may be released idle")
				continue
			}
			live = false
			result = map[string]any{}
		case "session/list":
			if !legacy || filepath.Clean(processCwd) != filepath.Clean(resolved) {
				respondError(request.ID, "only legacy cold bindings may require session/list")
				continue
			}
			if filepath.Clean(request.Params.Cwd) != filepath.Clean(root) && filepath.Clean(request.Params.Cwd) != filepath.Clean(resolved) {
				respondError(request.ID, "metadata lookup escaped the legal candidate roots")
				continue
			}
			sessions := []map[string]string{}
			if filepath.Clean(request.Params.Cwd) == filepath.Clean(sessionCwd) {
				sessions = append(sessions, map[string]string{"sessionId": "cwd-native", "cwd": sessionCwd})
			}
			result = map[string]any{"sessions": sessions}
		case "_omp/session/title":
			wantProcessCwd := sessionCwd
			if live || legacy {
				wantProcessCwd = resolved
			}
			if filepath.Clean(processCwd) != filepath.Clean(wantProcessCwd) || filepath.Clean(request.Params.Cwd) != filepath.Clean(sessionCwd) || request.Params.SessionID != "cwd-native" {
				respondError(request.ID, fmt.Sprintf("metadata process=%q cwd=%q, want process=%q native cwd=%q", processCwd, request.Params.Cwd, wantProcessCwd, sessionCwd))
				continue
			}
			result = agenttypes.SessionTitleResult{SessionID: "cwd-native", Title: sessionCwd, Status: "unchanged"}
		default:
			respondError(request.ID, "unexpected native method: "+request.Method)
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestPoolSessionTitleUsesActualNativeCwdWithTemplate(t *testing.T) {
	for _, mode := range []string{"legacy-root", "legacy-template", "exact-root", "exact-template", "new", "resume"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			resolved := filepath.Join(root, "native")
			if err := os.Mkdir(resolved, 0700); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			pool := NewPool(Config{Agents: []Definition{{
				Name: "omp", Protocol: ProtocolACP, Command: executable,
				Args:        []string{"-test.run=^TestSessionTitleCwdProtocolFixture$"},
				CwdTemplate: filepath.Join("{root}", "native"),
				Env: map[string]string{
					"MINDFS_TITLE_CWD_FIXTURE":  mode,
					"MINDFS_TITLE_CWD_ROOT":     root,
					"MINDFS_TITLE_CWD_RESOLVED": resolved,
				},
			}}})
			t.Cleanup(pool.CloseAll)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := agenttypes.SessionTitleRequest{SessionID: "cwd-native", Cwd: root}
			wantCwd := root
			if mode == "resume" || mode == "legacy-template" || mode == "exact-template" {
				wantCwd = resolved
			}
			if mode == "exact-root" || mode == "exact-template" {
				request.Cwd, request.ExactCwd = wantCwd, true
			}
			live := mode == "new" || mode == "resume"
			if live {
				open := agenttypes.OpenSessionInput{SessionKey: "bound", AgentName: "omp", RootPath: root}
				if mode == "resume" {
					open.AgentSessionID = "cwd-native"
				}
				loaded, err := pool.GetOrCreate(ctx, open)
				if err != nil || loaded == nil || loaded.SessionID() != "cwd-native" {
					t.Fatalf("open exact session: %#v %v", loaded, err)
				}
				// Live native identity wins over even a stale persisted cwd.
				request.Cwd, request.ExactCwd = filepath.Join(root, "stale-worktree"), true
			}
			result, err := pool.SessionTitle(ctx, "omp", "bound", request)
			if err != nil || result.Status != "unchanged" || filepath.Clean(result.Title) != filepath.Clean(wantCwd) || filepath.Clean(result.Cwd) != filepath.Clean(wantCwd) {
				t.Fatalf("%s title cwd result: %#v %v (want %q)", mode, result, err, wantCwd)
			}
			if live {
				if released := pool.ReleaseIdleSessions(time.Hour, time.Now().Add(2*time.Hour)); released != 1 {
					t.Fatalf("idle released %d sessions, want 1", released)
				}
				// The coordinator persists this exact result on the binding.
				// Once idle cleanup removes runtime state, it must not be templated
				// again or rediscovered through metadata/session startup.
				request.Cwd, request.ExactCwd = result.Cwd, true
				cold, err := pool.SessionTitle(ctx, "omp", "bound", request)
				if err != nil || cold.Status != "unchanged" || filepath.Clean(cold.Title) != filepath.Clean(wantCwd) || cold.Cwd != result.Cwd {
					t.Fatalf("%s idle-to-cold identity: %#v %v, live=%#v", mode, cold, err, result)
				}
			}
		})
	}
}
