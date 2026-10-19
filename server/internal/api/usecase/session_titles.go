package usecase

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"sync"

	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/session"
)

// TitleSyncResult describes only native metadata synchronization; a failed sync
// does not roll back an already persisted MindFS name or binding.
type TitleSyncResult struct {
	SessionKey     string `json:"session_key"`
	AgentSessionID string `json:"agent_session_id"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

type SyncSessionTitlesInput struct {
	RootID string `json:"root_id"`
	Agent  string `json:"agent"`
}

type SyncSessionTitlesOutput struct {
	Results     []TitleSyncResult `json:"results"`
	Updated     int               `json:"updated"`
	Unchanged   int               `json:"unchanged"`
	Kept        int               `json:"kept"`
	Conflict    int               `json:"conflict"`
	Busy        int               `json:"busy"`
	NotFound    int               `json:"not_found"`
	Unsupported int               `json:"unsupported"`
	Failed      int               `json:"failed"`
}

// Stripes bound coordination memory without a global lock across network work.
// Session mutation guards in Manager additionally protect the actual binding and
// name throughout the native conditional write.
var nativeTitleLocks [64]sync.Mutex

func titleBinding(meta session.TitleMetadata) *session.AgentBinding {
	for i := range meta.Bindings {
		if meta.Bindings[i].Agent == "omp" {
			return &meta.Bindings[i]
		}
	}
	return nil
}

func (s *Service) SyncSessionTitles(ctx context.Context, in SyncSessionTitlesInput) (SyncSessionTitlesOutput, error) {
	out := SyncSessionTitlesOutput{Results: []TitleSyncResult{}}
	if err := s.ensureRegistry(); err != nil {
		return out, err
	}
	if strings.TrimSpace(in.RootID) == "" || in.Agent != "omp" {
		return out, errors.New("root_id and agent omp required")
	}
	manager, err := s.Registry.GetSessionManager(in.RootID)
	if err != nil {
		return out, err
	}
	items, err := manager.ListTitleMetadata(ctx)
	if err != nil {
		return out, err
	}
	for _, item := range items {
		if titleBinding(item) == nil {
			continue
		}
		result := s.SyncSessionTitle(ctx, in.RootID, item.Key, "backfill")
		out.Results = append(out.Results, result)
		switch result.Status {
		case "updated":
			out.Updated++
		case "unchanged":
			out.Unchanged++
		case "kept":
			out.Kept++
		case "conflict":
			out.Conflict++
		case "busy":
			out.Busy++
		case "not_found":
			out.NotFound++
		case "unsupported":
			out.Unsupported++
		default:
			out.Failed++
		}
	}
	return out, nil
}

// SyncSessionTitle is shared by naming, explicit rename, binding persistence and
// explicit backfill. mode is auto, user, or backfill. Native metadata writes are
// atomic; only user may replace an independently named native session.
func (s *Service) SyncSessionTitle(ctx context.Context, rootID, key, mode string) TitleSyncResult {
	result := TitleSyncResult{SessionKey: key, Status: "failed"}
	fail := func(err error) TitleSyncResult { result.Status = "failed"; result.Error = err.Error(); return result }
	if err := s.ensureRegistry(); err != nil {
		return fail(err)
	}
	manager, err := s.Registry.GetSessionManager(rootID)
	if err != nil {
		return fail(err)
	}
	initial, err := manager.GetTitleMetadata(ctx, key)
	if err != nil {
		return fail(err)
	}
	binding := titleBinding(initial)
	if binding == nil || strings.TrimSpace(binding.AgentSessionID) == "" {
		result.Status, result.Error = "skipped", "no persisted OMP binding"
		return result
	}
	result.AgentSessionID = binding.AgentSessionID
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(rootID + "\x00" + binding.AgentSessionID))
	lock := &nativeTitleLocks[hash.Sum32()%uint32(len(nativeTitleLocks))]
	lock.Lock()
	defer lock.Unlock()
	err = manager.WithTitleSync(ctx, key, func(meta session.TitleMetadata) error {
		currentBinding := titleBinding(meta)
		if currentBinding == nil || currentBinding.AgentSessionID != result.AgentSessionID {
			result.Status, result.Error = "conflict", "OMP binding changed before title synchronization"
			return nil
		}
		if strings.TrimSpace(meta.Name) == "" {
			result.Status, result.Error = "kept", "MindFS title is empty"
			return nil
		}
		conflict, err := manager.HasConflictingTitleBinding(ctx, key, "omp", currentBinding.AgentSessionID, meta.Name)
		if err != nil {
			return err
		}
		if conflict {
			result.Status, result.Error = "conflict", "multiple MindFS bindings have different titles for this native session"
			return nil
		}
		pool := s.Registry.GetAgentPool()
		if pool == nil {
			return errors.New("agent pool not configured")
		}
		cwd := currentBinding.NativeCwd
		if cwd == "" {
			root, err := s.Registry.GetRoot(rootID)
			if err != nil {
				return err
			}
			cwd = root.RootPath
			if meta.RelatedWorktree != nil && strings.TrimSpace(meta.RelatedWorktree.Path) != "" {
				cwd = meta.RelatedWorktree.Path
			}
		}
		request := agenttypes.SessionTitleRequest{SessionID: currentBinding.AgentSessionID, Cwd: cwd, ExactCwd: currentBinding.NativeCwd != "", Title: &meta.Name, Source: "auto"}
		if mode == "user" {
			request.Source = "user"
		} else if mode == "backfill" || currentBinding.NativeTitle == nil {
			request.OnlyIfEmpty = true
		} else {
			request.ExpectedTitle = currentBinding.NativeTitle
		}
		native, nativeErr := pool.SessionTitle(ctx, "omp", agentPoolSessionKey(key, "omp"), request)
		if native.Cwd != "" {
			acknowledged, err := manager.AcknowledgeBindingCwd(ctx, key, "omp", currentBinding.AgentSessionID, native.Cwd)
			if err != nil {
				return errors.Join(nativeErr, err)
			}
			if !acknowledged {
				result.Status, result.Error = "conflict", "OMP binding changed before native cwd acknowledgement"
				return nil
			}
		}
		if nativeErr != nil {
			return nativeErr
		}
		result.Status = native.Status
		if native.Status == "updated" || (native.Status == "unchanged" && (mode == "user" || (currentBinding.NativeTitle != nil && native.Title == *currentBinding.NativeTitle))) {
			acknowledged, err := manager.AcknowledgeBindingTitle(ctx, key, "omp", currentBinding.AgentSessionID, meta.Name, meta.NameRevision, native.Title)
			if err != nil {
				return err
			}
			if !acknowledged {
				result.Status, result.Error = "conflict", "MindFS binding or title changed before acknowledgement"
			}
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	return result
}

// SyncSessionTitleAutomatic isolates optional native metadata side effects from
// already successful local chat, fork, edit and naming operations. Reporting is
// deliberately a dedicated event, never a chat error or a task failure.
func (s *Service) SyncSessionTitleAutomatic(ctx context.Context, rootID, key string) {
	result := s.SyncSessionTitle(ctx, rootID, key, "auto")
	if result.Status == "skipped" {
		return
	}
	if broadcaster, ok := s.Registry.(interface{ BroadcastSessionTitleSync(string, TitleSyncResult) }); ok {
		broadcaster.BroadcastSessionTitleSync(rootID, result)
	}
}
