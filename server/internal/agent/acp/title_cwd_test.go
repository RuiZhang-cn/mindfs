package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mindfs/server/internal/agent/types"
)

type titleListedSession struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
}

func titleListStep(cwd string, cursor *string, sessions []titleListedSession, next *string) titleProtocolStep {
	params := map[string]any{"cwd": normalizeComparablePath(cwd)}
	if cursor != nil {
		params["cursor"] = *cursor
	}
	request, _ := json.Marshal(params)
	result, _ := json.Marshal(map[string]any{"sessions": sessions, "nextCursor": next})
	return titleProtocolStep{Method: "session/list", Params: request, Result: result}
}

func TestImporterSessionTitleLegacyCwdIdentity(t *testing.T) {
	for _, mode := range []string{"raw-root", "template", "deduplicated", "windows-case", "ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "windows-case" && runtime.GOOS != "windows" {
				t.Skip("Windows path identity is case-insensitive")
			}
			root := normalizeComparablePath(t.TempDir())
			resolved := filepath.Join(root, "native")
			if err := os.Mkdir(resolved, 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "deduplicated" {
				resolved = root + string(os.PathSeparator) + "native" + string(os.PathSeparator) + ".."
			}
			if mode == "windows-case" {
				resolved = strings.ToUpper(root)
			}
			title := "MindFS title"
			in := types.SessionTitleRequest{SessionID: "native", Cwd: root, Title: &title, Source: "auto"}
			canonicalResolved := normalizeComparablePath(resolved)
			rootRows := []titleListedSession{{SessionID: "unrelated", Cwd: root}, {SessionID: "native", Cwd: filepath.Join(root, "outside")}}
			resolvedRows := []titleListedSession{}
			wantCwd, wantStatus := "", "not_found"
			switch mode {
			case "raw-root", "deduplicated":
				rootRows = append(rootRows, titleListedSession{SessionID: "native", Cwd: root})
				if mode == "deduplicated" {
					rootRows = append(rootRows, titleListedSession{SessionID: "native", Cwd: root + string(os.PathSeparator) + "."})
				}
				wantCwd, wantStatus = root, "updated"
			case "windows-case":
				rootRows = append(rootRows, titleListedSession{SessionID: "native", Cwd: strings.ToUpper(root)})
				wantCwd, wantStatus = root, "updated"
			case "template":
				resolvedRows = append(resolvedRows, titleListedSession{SessionID: "native", Cwd: resolved})
				wantCwd, wantStatus = canonicalResolved, "updated"
			case "ambiguous":
				rootRows = append(rootRows, titleListedSession{SessionID: "native", Cwd: root})
				resolvedRows = append(resolvedRows, titleListedSession{SessionID: "native", Cwd: resolved})
				wantStatus = "conflict"
			}
			cursor := "second page"
			steps := []titleProtocolStep{
				titleInitializeStep(true),
				titleListStep(root, nil, []titleListedSession{}, &cursor),
				titleListStep(root, &cursor, rootRows, nil),
			}
			methods := []string{"initialize", "session/list", "session/list"}
			if !sameSessionTitleCwd(canonicalResolved, root) {
				steps = append(steps, titleListStep(resolved, nil, resolvedRows, nil))
				methods = append(methods, "session/list")
			}
			if wantCwd != "" {
				wire := in
				wire.Cwd = wantCwd
				steps = append(steps, titleStep(wire, `{"sessionId":"native","title":"MindFS title","source":"auto","status":"updated"}`))
				methods = append(methods, "_omp/session/title")
			}
			opts, path := titleFixtureOptions(t, steps...)
			resolutions := 0
			opts.ResolveCwd = func(got string) string {
				resolutions++
				if got != root {
					t.Errorf("resolve input = %q, want root %q", got, root)
				}
				return resolved
			}
			result, err := NewImporter(opts).SessionTitle(context.Background(), in)
			if err != nil || result.SessionID != in.SessionID || result.Status != wantStatus || result.Cwd != wantCwd || resolutions != 1 {
				t.Fatalf("legacy result = %+v, %v; resolutions=%d, want %s cwd=%q", result, err, resolutions, wantStatus, wantCwd)
			}
			assertTitleTranscript(t, path, methods...)
		})
	}
}

func TestImporterSessionTitleExactCwdBypassesTemplate(t *testing.T) {
	for _, status := range []string{"updated", "unchanged", "kept", "conflict", "busy", "not_found"} {
		t.Run(status, func(t *testing.T) {
			in := types.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir(), ExactCwd: true}
			response, _ := json.Marshal(map[string]string{"sessionId": in.SessionID, "title": "Existing", "status": status})
			opts, path := titleFixtureOptions(t, titleInitializeStep(true), titleStep(in, string(response)))
			opts.ResolveCwd = func(string) string {
				t.Fatal("verified cwd must not be resolved as a template input")
				return ""
			}
			result, err := NewImporter(opts).SessionTitle(context.Background(), in)
			if err != nil || result.Status != status || result.Cwd != in.Cwd {
				t.Fatalf("exact result = %+v, %v", result, err)
			}
			assertTitleTranscript(t, path, "initialize", "_omp/session/title")
		})
	}
}

func TestImporterSessionTitleOldNativeSkipsLookup(t *testing.T) {
	for _, exact := range []bool{false, true} {
		in := types.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir(), ExactCwd: exact}
		opts, path := titleFixtureOptions(t, titleInitializeStep(nil))
		result, err := NewImporter(opts).SessionTitle(context.Background(), in)
		wantCwd := ""
		if exact {
			wantCwd = in.Cwd
		}
		if err != nil || result.Status != "unsupported" || result.Cwd != wantCwd {
			t.Fatalf("old native exact=%v result = %+v, %v", exact, result, err)
		}
		assertTitleTranscript(t, path, "initialize")
	}
}

func TestSessionTitleLiveUnsupportedStillReturnsActualCwd(t *testing.T) {
	cwd := t.TempDir()
	p := &Process{agentName: "omp", sessions: map[string]*sessionState{"local": {ID: "native", cwd: cwd}}}
	s := &session{proc: p, sessionKey: "local"}
	result, err := s.SessionTitle(context.Background(), types.SessionTitleRequest{SessionID: "native", Cwd: "stale"})
	if err != nil || result.Status != "unsupported" || result.Cwd != cwd {
		t.Fatalf("live unsupported result = %+v, %v", result, err)
	}
}

func TestImporterSessionTitlePreservesProvenCwdOnNativeError(t *testing.T) {
	for _, exact := range []bool{false, true} {
		in := types.SessionTitleRequest{SessionID: "native", Cwd: normalizeComparablePath(t.TempDir()), ExactCwd: exact}
		steps := []titleProtocolStep{titleInitializeStep(true)}
		methods := []string{"initialize"}
		if !exact {
			steps = append(steps, titleListStep(in.Cwd, nil, []titleListedSession{{SessionID: in.SessionID, Cwd: in.Cwd}}, nil))
			methods = append(methods, "session/list")
		}
		step := titleStep(in, "null")
		step.Error = json.RawMessage(`{"code":-32603,"message":"native persistence failed"}`)
		steps = append(steps, step)
		methods = append(methods, "_omp/session/title")
		opts, path := titleFixtureOptions(t, steps...)
		result, err := NewImporter(opts).SessionTitle(context.Background(), in)
		if err == nil || result.Cwd != in.Cwd {
			t.Fatalf("native error exact=%v result = %+v, %v", exact, result, err)
		}
		assertTitleTranscript(t, path, methods...)
	}
}

func TestImporterSessionTitleIncompleteLookupDoesNotProveCwd(t *testing.T) {
	root := normalizeComparablePath(t.TempDir())
	resolved := filepath.Join(root, "native")
	if err := os.Mkdir(resolved, 0700); err != nil {
		t.Fatal(err)
	}
	failed := titleListStep(resolved, nil, nil, nil)
	failed.Error = json.RawMessage(`{"code":-32603,"message":"metadata index unavailable"}`)
	opts, path := titleFixtureOptions(t, titleInitializeStep(true),
		titleListStep(root, nil, []titleListedSession{{SessionID: "native", Cwd: root}}, nil), failed)
	opts.ResolveCwd = func(string) string { return resolved }
	result, err := NewImporter(opts).SessionTitle(context.Background(), types.SessionTitleRequest{SessionID: "native", Cwd: root})
	if err == nil || result.Cwd != "" {
		t.Fatalf("incomplete lookup result = %+v, %v", result, err)
	}
	assertTitleTranscript(t, path, "initialize", "session/list", "session/list")
}

func TestSessionTitleInternalCwdFieldsStayOffWire(t *testing.T) {
	request, err := json.Marshal(types.SessionTitleRequest{SessionID: "native", Cwd: "wire-cwd", ExactCwd: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(types.SessionTitleResult{SessionID: "native", Cwd: "private-cwd", Title: "Title", Status: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if string(request) != `{"sessionId":"native","cwd":"wire-cwd"}` || string(result) != `{"sessionId":"native","title":"Title","status":"kept"}` {
		t.Fatalf("internal identity leaked: request=%s result=%s", request, result)
	}
}
