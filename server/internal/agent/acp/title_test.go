package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mindfs/server/internal/agent/types"
)

// This is a strict wire transcript fixture, not an implementation of native
// title storage. Native persistence/ownership semantics require the OMP tests.
// Unexpected load/resume/new/prompt/cancel requests fail the transcript.
type titleProtocolStep struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error,omitempty"`
	Delay  time.Duration   `json:"delay,omitempty"`
}

func TestSessionTitleProtocolFixture(t *testing.T) {
	path := os.Getenv("MINDFS_TITLE_FIXTURE")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var steps []titleProtocolStep
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}
	audit, err := os.OpenFile(path+".audit", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	var outputMu sync.Mutex
	index := 0
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&req); err != nil {
			os.Exit(0)
		}
		if req.Method == "$/cancel_request" && len(req.ID) == 0 {
			continue // SDK request cancellation must not become session/cancel.
		}
		if index >= len(steps) || steps[index].Method != req.Method {
			fmt.Fprintln(audit, "UNEXPECTED "+req.Method)
			os.Exit(2)
		}
		step := steps[index]
		if len(step.Params) != 0 {
			var want, got any
			if json.Unmarshal(step.Params, &want) != nil || json.Unmarshal(req.Params, &got) != nil || !reflect.DeepEqual(want, got) {
				fmt.Fprintf(audit, "BAD PARAMS %s %s\n", req.Method, req.Params)
				os.Exit(2)
			}
		}
		fmt.Fprintln(audit, req.Method)
		index++
		go func(id json.RawMessage, step titleProtocolStep) {
			time.Sleep(step.Delay)
			outputMu.Lock()
			defer outputMu.Unlock()
			response := map[string]any{"jsonrpc": "2.0", "id": id}
			if len(step.Error) != 0 {
				response["error"] = step.Error
			} else {
				response["result"] = step.Result
			}
			_ = encoder.Encode(response)
		}(req.ID, step)
	}
}

func titleInitializeStep(capability any) titleProtocolStep {
	caps := map[string]any{}
	if capability != nil {
		caps["_meta"] = map[string]any{"omp.sessionTitle": capability}
	}
	result, _ := json.Marshal(map[string]any{"protocolVersion": 1, "agentCapabilities": caps})
	return titleProtocolStep{Method: "initialize", Result: result}
}

func titleStep(in types.SessionTitleRequest, result string) titleProtocolStep {
	params, _ := json.Marshal(in)
	return titleProtocolStep{Method: "_omp/session/title", Params: params, Result: json.RawMessage(result)}
}

func titleFixtureOptions(t *testing.T, steps ...titleProtocolStep) (ImporterOptions, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.json")
	data, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ImporterOptions{
		AgentName: "omp",
		Command:   exe,
		Args:      []string{"-test.run=^TestSessionTitleProtocolFixture$"},
		Env:       map[string]string{"MINDFS_TITLE_FIXTURE": path},
	}, path
}

func startTitleFixture(t *testing.T, opts ImporterOptions) *Process {
	t.Helper()
	p, err := Start(context.Background(), opts.AgentName, opts.Command, opts.Args, t.TempDir(), opts.Env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertTitleTranscript(t *testing.T, path string, methods ...string) {
	t.Helper()
	data, err := os.ReadFile(path + ".audit")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), strings.Join(methods, "\n")+"\n"; got != want {
		t.Fatalf("wire transcript = %q, want %q", got, want)
	}
}

func TestSessionTitleCapabilityExactBoolean(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  any
	}{
		{"missing", nil}, {"false", false}, {"string", "true"},
		{"number", 1}, {"object", map[string]any{"enabled": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, path := titleFixtureOptions(t, titleInitializeStep(tc.cap))
			p := startTitleFixture(t, opts)
			in := types.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir()}
			result, err := p.SessionTitle(context.Background(), in)
			if err != nil || result.Status != "unsupported" || result.SessionID != in.SessionID {
				t.Fatalf("SessionTitle = %+v, %v", result, err)
			}
			assertTitleTranscript(t, path, "initialize")
		})
	}
}

func TestSessionTitleNonOMPUnsupported(t *testing.T) {
	opts, path := titleFixtureOptions(t, titleInitializeStep(true))
	opts.AgentName = "other-agent"
	p := startTitleFixture(t, opts)
	result, err := p.SessionTitle(context.Background(), types.SessionTitleRequest{SessionID: "native"})
	if err != nil || result.Status != "unsupported" {
		t.Fatalf("SessionTitle = %+v, %v", result, err)
	}
	assertTitleTranscript(t, path, "initialize")
	// No subprocess may be launched by a non-OMP importer either.
	result, err = NewImporter(ImporterOptions{AgentName: "other-agent", Command: "does-not-exist"}).SessionTitle(context.Background(), types.SessionTitleRequest{SessionID: "native"})
	if err != nil || result.Status != "unsupported" {
		t.Fatalf("importer SessionTitle = %+v, %v", result, err)
	}
}

func TestSessionTitleReadAndCASWire(t *testing.T) {
	cwd := t.TempDir()
	empty, next := "", "MindFS generated title"
	read := types.SessionTitleRequest{SessionID: "native", Cwd: cwd, ExactCwd: true}
	write := types.SessionTitleRequest{SessionID: "native", Cwd: cwd, ExactCwd: true, Title: &next, ExpectedTitle: &empty, OnlyIfEmpty: true, Source: "auto"}
	opts, path := titleFixtureOptions(t,
		titleInitializeStep(true),
		titleStep(read, `{"sessionId":"native","title":"","status":"unchanged"}`),
		titleStep(write, `{"sessionId":"native","title":"MindFS generated title","source":"auto","status":"updated"}`),
		titleStep(write, `{"sessionId":"native","title":"Independently named","source":"user","status":"conflict"}`),
	)
	p := startTitleFixture(t, opts)
	for _, tc := range []struct {
		in     types.SessionTitleRequest
		status string
		title  string
	}{{read, "unchanged", ""}, {write, "updated", next}, {write, "conflict", "Independently named"}} {
		result, err := p.SessionTitle(context.Background(), tc.in)
		if err != nil || result.Status != tc.status || result.Title != tc.title || result.Cwd != cwd {
			t.Fatalf("SessionTitle = %+v, %v", result, err)
		}
	}
	assertTitleTranscript(t, path, "initialize", "_omp/session/title", "_omp/session/title", "_omp/session/title")
}

func TestImporterSessionTitleMetadataOnly(t *testing.T) {
	in := types.SessionTitleRequest{SessionID: "native", Cwd: normalizeComparablePath(t.TempDir())}
	opts, path := titleFixtureOptions(t, titleInitializeStep(true),
		titleListStep(in.Cwd, nil, []titleListedSession{{SessionID: "native", Cwd: in.Cwd}}, nil),
		titleStep(in, `{"sessionId":"native","title":"Existing title","source":"user","status":"unchanged"}`))
	result, err := NewImporter(opts).SessionTitle(context.Background(), in)
	if err != nil || result.Title != "Existing title" || result.Status != "unchanged" || result.Cwd != normalizeComparablePath(in.Cwd) {
		t.Fatalf("SessionTitle = %+v, %v", result, err)
	}
	assertTitleTranscript(t, path, "initialize", "session/list", "_omp/session/title")
}

func TestSessionTitleRejectsInvalidResponse(t *testing.T) {
	for name, raw := range map[string]string{
		"identity":      `{"sessionId":"sibling","title":"Wrong","status":"updated"}`,
		"status":        `{"sessionId":"native","title":"Title","status":"success"}`,
		"missing title": `{"sessionId":"native","status":"unchanged"}`,
		"null title":    `{"sessionId":"native","title":null,"status":"unchanged"}`,
		"source":        `{"sessionId":"native","title":"Title","source":"model","status":"unchanged"}`,
	} {
		t.Run(name, func(t *testing.T) {
			in := types.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir(), ExactCwd: true}
			opts, _ := titleFixtureOptions(t, titleInitializeStep(true), titleStep(in, raw))
			p := startTitleFixture(t, opts)
			if result, err := p.SessionTitle(context.Background(), in); err == nil || result.Cwd != in.Cwd {
				t.Fatalf("invalid response result: %+v, %v", result, err)
			}
		})
	}
}

func TestSessionTitleTimeoutKeepsLiveProcess(t *testing.T) {
	in := types.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir()}
	delayed := titleStep(in, `{"sessionId":"native","title":"Old title","status":"unchanged"}`)
	delayed.Delay = 300 * time.Millisecond
	opts, path := titleFixtureOptions(t, titleInitializeStep(true),
		titleProtocolStep{Method: "session/new", Result: json.RawMessage(`{"sessionId":"native"}`)},
		delayed, titleStep(in, `{"sessionId":"native","title":"Still alive","status":"unchanged"}`))
	p := startTitleFixture(t, opts)
	if err := p.NewSession(context.Background(), "local", in.Cwd); err != nil {
		t.Fatal(err)
	}
	s := &session{proc: p, sessionKey: "local"}
	pid := p.ProcessID()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if result, err := s.SessionTitle(ctx, in); !errors.Is(err, context.DeadlineExceeded) || result.Cwd != in.Cwd {
		t.Fatalf("timeout result = %+v, %v", result, err)
	}
	if p.ProcessID() != pid || pid <= 0 || s.SessionID() != "native" {
		t.Fatal("metadata timeout discarded live process/session identity")
	}
	select {
	case <-p.conn.Done():
		t.Fatal("metadata timeout closed shared ACP connection")
	default:
	}
	result, err := s.SessionTitle(context.Background(), in)
	if err != nil || result.Title != "Still alive" || result.Cwd != in.Cwd {
		t.Fatalf("live process after timeout = %+v, %v", result, err)
	}
	assertTitleTranscript(t, path, "initialize", "session/new", "_omp/session/title", "_omp/session/title")
}

func TestSessionTitleLiveIdentityMismatch(t *testing.T) {
	p := &Process{sessions: map[string]*sessionState{"local": {ID: "actual"}}}
	s := &session{proc: p, sessionKey: "local"}
	result, err := s.SessionTitle(context.Background(), types.SessionTitleRequest{SessionID: "stale"})
	if err != nil || result.SessionID != "stale" || result.Status != "conflict" {
		t.Fatalf("SessionTitle = %+v, %v", result, err)
	}
}
