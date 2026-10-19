package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"mindfs/server/internal/agent/types"
)

const sessionTitleTimeout = 5 * time.Second

func unsupportedSessionTitle(in types.SessionTitleRequest) types.SessionTitleResult {
	return types.SessionTitleResult{SessionID: in.SessionID, Status: "unsupported"}
}

// SessionTitle invokes only the advertised metadata extension. Its deadline is
// local to this RPC: it never cancels a prompt or closes the shared process.
func (p *Process) SessionTitle(ctx context.Context, in types.SessionTitleRequest) (result types.SessionTitleResult, err error) {
	defer func() {
		if in.ExactCwd {
			result.Cwd = in.Cwd
		}
	}()
	if !strings.EqualFold(p.agentName, "omp") || !p.capability.SupportsSessionTitle {
		return unsupportedSessionTitle(in), nil
	}
	if err := validateSessionTitleRequest(in); err != nil {
		return types.SessionTitleResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, sessionTitleTimeout)
	defer cancel()
	raw, err := p.conn.CallExtension(ctx, "_omp/session/title", in)
	if err != nil {
		if ctx.Err() != nil {
			return types.SessionTitleResult{}, ctx.Err()
		}
		return types.SessionTitleResult{}, err
	}
	// A required title field must not be mistaken for an empty native title.
	var wire struct {
		SessionID string  `json:"sessionId"`
		Title     *string `json:"title"`
		Source    string  `json:"source"`
		Status    string  `json:"status"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return types.SessionTitleResult{}, fmt.Errorf("decode session title response: %w", err)
	}
	if wire.SessionID != in.SessionID {
		return types.SessionTitleResult{}, errors.New("session title response identity mismatch")
	}
	if wire.Title == nil {
		return types.SessionTitleResult{}, errors.New("session title response missing title")
	}
	switch wire.Status {
	case "updated", "unchanged", "kept", "conflict", "busy", "not_found":
	default:
		return types.SessionTitleResult{}, fmt.Errorf("invalid session title response status %q", wire.Status)
	}
	if wire.Source != "" && wire.Source != "auto" && wire.Source != "user" {
		return types.SessionTitleResult{}, fmt.Errorf("invalid session title response source %q", wire.Source)
	}
	return types.SessionTitleResult{
		SessionID: wire.SessionID,
		Title:     *wire.Title,
		Source:    wire.Source,
		Status:    wire.Status,
	}, nil
}

func validateSessionTitleRequest(in types.SessionTitleRequest) error {
	if strings.TrimSpace(in.SessionID) == "" {
		return errors.New("session title requires session id")
	}
	if strings.TrimSpace(in.Cwd) == "" {
		return errors.New("session title requires cwd")
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return errors.New("session title must not be empty")
	}
	if in.Source != "" && in.Source != "auto" && in.Source != "user" {
		return errors.New("session title source must be auto or user")
	}
	return nil
}

func (s *session) SessionTitle(ctx context.Context, in types.SessionTitleRequest) (types.SessionTitleResult, error) {
	state := s.proc.getSessionByKey(s.sessionKey)
	if state == nil || string(state.ID) != in.SessionID {
		return types.SessionTitleResult{SessionID: in.SessionID, Status: "conflict"}, nil
	}
	// The process cwd/template is not necessarily the cwd used by session/new.
	// Reuse the exact native session identity instead of resolving a template again.
	in.Cwd = state.cwd
	in.ExactCwd = true
	return s.proc.SessionTitle(ctx, in)
}

// SessionTitle starts a metadata-only process for a cold target. In particular
// it must not reuse ImportExternalSession, which calls session/load for history.
func (i *Importer) SessionTitle(ctx context.Context, in types.SessionTitleRequest) (result types.SessionTitleResult, err error) {
	defer func() {
		if in.ExactCwd {
			result.Cwd = in.Cwd
		}
	}()
	if !strings.EqualFold(i.agentName, "omp") {
		return unsupportedSessionTitle(in), nil
	}
	if err := validateSessionTitleRequest(in); err != nil {
		return types.SessionTitleResult{}, err
	}
	// Bound initialization, all metadata pages, and the extension together.
	// This private subprocess never loads/resumes/creates a native session.
	ctx, cancel := context.WithTimeout(ctx, sessionTitleTimeout)
	defer cancel()
	var proc *Process
	var cwd string
	if in.ExactCwd {
		proc, cwd, err = i.openProcessAtCwd(ctx, in.Cwd)
	} else {
		proc, cwd, err = i.openProcess(ctx, in.Cwd)
	}
	if err != nil {
		return types.SessionTitleResult{}, err
	}
	defer proc.Close()
	// Old native agents must not receive even a session/list request.
	if !proc.capability.SupportsSessionTitle {
		return unsupportedSessionTitle(in), nil
	}
	if !in.ExactCwd {
		result, err = proc.resolveSessionTitleCwd(ctx, in.SessionID, in.Cwd, cwd)
		if err != nil || result.Cwd == "" {
			return result, err
		}
		in.Cwd = result.Cwd
		in.ExactCwd = true
	}
	return proc.SessionTitle(ctx, in)
}

func sameSessionTitleCwd(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// A legacy binding has no cwd proof. Search only its legal canonical roots,
// exhausting every page/candidate before a write so duplicate IDs cannot pick
// an arbitrary native session. session/list is metadata-only.
func (p *Process) resolveSessionTitleCwd(ctx context.Context, sessionID, root, resolved string) (types.SessionTitleResult, error) {
	result := types.SessionTitleResult{SessionID: sessionID, Status: "not_found"}
	candidates := []string{normalizeComparablePath(root)}
	if cwd := normalizeComparablePath(resolved); !sameSessionTitleCwd(cwd, candidates[0]) {
		candidates = append(candidates, cwd)
	}
	for _, cwd := range candidates {
		var cursor *string
		for {
			page, err := p.conn.ListSessions(ctx, acpsdk.ListSessionsRequest{Cwd: &cwd, Cursor: cursor})
			if err != nil {
				return types.SessionTitleResult{SessionID: sessionID}, err
			}
			for _, item := range page.Sessions {
				if string(item.SessionId) != sessionID || !sameSessionTitleCwd(normalizeComparablePath(item.Cwd), cwd) {
					continue
				}
				if result.Cwd != "" && !sameSessionTitleCwd(result.Cwd, cwd) {
					return types.SessionTitleResult{SessionID: sessionID, Status: "conflict"}, nil
				}
				result.Cwd = cwd
			}
			if page.NextCursor == nil || strings.TrimSpace(*page.NextCursor) == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	return result, nil
}

var _ types.SessionTitleSyncer = (*session)(nil)
var _ types.SessionTitleSyncer = (*Importer)(nil)
