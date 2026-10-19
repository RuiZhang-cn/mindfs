package agent

import (
	"context"
	"errors"
	"testing"

	agenttypes "mindfs/server/internal/agent/types"
)

type titleRoutingSession struct {
	agenttypes.Session
	id   string
	call func(context.Context, agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error)
}

func (s *titleRoutingSession) SessionID() string { return s.id }

func (s *titleRoutingSession) SessionTitle(ctx context.Context, in agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
	return s.call(ctx, in)
}

func TestPoolSessionTitleUsesExactLiveIdentityWithoutLocks(t *testing.T) {
	for _, key := range []string{"local", "absent-local-key"} {
		t.Run(key, func(t *testing.T) {
			pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolACP, Command: "must-not-start-an-importer"}}})
			defer pool.cancel()
			in := agenttypes.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir()}
			calls := 0
			sess := &titleRoutingSession{id: "native"}
			entry := &sessionEntry{agentName: "omp", sessionKey: "local", protocol: ProtocolACP, session: sess}
			pool.sessions["local"] = entry
			// Create the group mutex before the callback, so checking that it is
			// unlocked cannot accidentally create a different mutex.
			unlock := pool.lockRuntimeGroups([]runtimeGroup{{agentName: "omp", protocol: ProtocolACP}})
			unlock()
			sess.call = func(_ context.Context, got agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
				calls++
				if !pool.mu.TryLock() {
					t.Fatal("pool mutex held during title RPC")
				}
				if entry.activeUses != 1 {
					t.Errorf("live entry not pinned: activeUses=%d", entry.activeUses)
				}
				pool.mu.Unlock()
				lock := pool.runtimeLocks[runtimeGroup{agentName: "omp", protocol: ProtocolACP}]
				if !lock.TryLock() {
					t.Fatal("runtime mutex held during title RPC")
				}
				lock.Unlock()
				if got.SessionID != "native" || got.Cwd != in.Cwd {
					t.Fatalf("wrong native target: %+v", got)
				}
				return agenttypes.SessionTitleResult{SessionID: got.SessionID, Title: "Live", Status: "unchanged"}, nil
			}
			result, err := pool.SessionTitle(context.Background(), "omp", key, in)
			if err != nil || calls != 1 || result.Title != "Live" || entry.activeUses != 0 {
				t.Fatalf("live title route = %+v, %v; calls=%d activeUses=%d", result, err, calls, entry.activeUses)
			}
		})
	}
}

func TestPoolSessionTitleRejectsStaleIdentity(t *testing.T) {
	pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolACP, Command: "must-not-start"}}})
	defer pool.cancel()
	sess := &titleRoutingSession{id: "replacement", call: func(context.Context, agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
		t.Fatal("title RPC reached replacement session")
		return agenttypes.SessionTitleResult{}, nil
	}}
	pool.sessions["local"] = &sessionEntry{agentName: "omp", session: sess}
	result, err := pool.SessionTitle(context.Background(), "omp", "local", agenttypes.SessionTitleRequest{SessionID: "stale", Cwd: t.TempDir()})
	if err != nil || result.SessionID != "stale" || result.Status != "conflict" {
		t.Fatalf("stale route = %+v, %v", result, err)
	}
}

func TestPoolSessionTitleUsesStagedReplacementBesideStaleCanonicalKey(t *testing.T) {
	pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolACP, Command: "must-not-start"}}})
	defer pool.cancel()
	oldCalls, replacementCalls := 0, 0
	old := &titleRoutingSession{id: "old-native", call: func(context.Context, agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
		oldCalls++
		t.Fatal("title RPC reached old canonical session")
		return agenttypes.SessionTitleResult{}, nil
	}}
	replacement := &titleRoutingSession{id: "new-native", call: func(_ context.Context, in agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
		replacementCalls++
		if in.SessionID != "new-native" {
			t.Fatalf("staged route targeted %+v", in)
		}
		return agenttypes.SessionTitleResult{SessionID: in.SessionID, Title: "Replacement", Status: "unchanged"}, nil
	}}
	canonical := &sessionEntry{agentName: "omp", sessionKey: "local", session: old}
	staged := &sessionEntry{agentName: "omp", sessionKey: "local:staging", session: replacement}
	pool.sessions["local"] = canonical
	pool.sessions["local:staging"] = staged
	result, err := pool.SessionTitle(context.Background(), "omp", "local", agenttypes.SessionTitleRequest{SessionID: "new-native", Cwd: t.TempDir()})
	if err != nil || result.SessionID != "new-native" || result.Title != "Replacement" || replacementCalls != 1 || oldCalls != 0 {
		t.Fatalf("staged route = %+v, %v; replacementCalls=%d oldCalls=%d", result, err, replacementCalls, oldCalls)
	}
	if canonical.activeUses != 0 || staged.activeUses != 0 {
		t.Fatalf("title use leaked: canonical=%d staging=%d", canonical.activeUses, staged.activeUses)
	}
}

func TestPoolSessionTitleClosingOwnerIsBusy(t *testing.T) {
	pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolACP, Command: "must-not-start"}}})
	defer pool.cancel()
	pool.sessions["local"] = &sessionEntry{agentName: "omp", closing: true, session: &titleRoutingSession{id: "native"}}
	result, err := pool.SessionTitle(context.Background(), "omp", "local", agenttypes.SessionTitleRequest{SessionID: "native"})
	if err != nil || result.Status != "busy" {
		t.Fatalf("closing route = %+v, %v", result, err)
	}
}

func TestPoolSessionTitleUnsupportedTransport(t *testing.T) {
	pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolCodexSDK}}})
	defer pool.cancel()
	for _, name := range []string{"omp", "claude", "codex", "gemini"} {
		result, err := pool.SessionTitle(context.Background(), name, "local", agenttypes.SessionTitleRequest{SessionID: "native"})
		if err != nil || result.SessionID != "native" || result.Status != "unsupported" {
			t.Fatalf("%s route = %+v, %v", name, result, err)
		}
	}
}

func TestPoolSessionTitlePropagatesLiveFailure(t *testing.T) {
	pool := NewPool(Config{Agents: []Definition{{Name: "omp", Protocol: ProtocolACP, Command: "must-not-start"}}})
	defer pool.cancel()
	want := errors.New("native metadata persistence failed")
	entry := &sessionEntry{agentName: "omp", session: &titleRoutingSession{id: "native", call: func(context.Context, agenttypes.SessionTitleRequest) (agenttypes.SessionTitleResult, error) {
		return agenttypes.SessionTitleResult{}, want
	}}}
	pool.sessions["local"] = entry
	_, err := pool.SessionTitle(context.Background(), "omp", "local", agenttypes.SessionTitleRequest{SessionID: "native", Cwd: t.TempDir()})
	if !errors.Is(err, want) || entry.activeUses != 0 {
		t.Fatalf("failure = %v, activeUses=%d", err, entry.activeUses)
	}
}
