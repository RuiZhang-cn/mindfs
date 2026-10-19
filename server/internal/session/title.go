package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// TitleMetadata is a detached snapshot of SQLite metadata, never chat history.
type TitleMetadata struct {
	Key             string
	Name            string
	NameRevision    int64
	RelatedWorktree *RelatedWorktree
	Bindings        []AgentBinding
}

func (m *Manager) titleLock(key string) *sync.Mutex {
	if lock, ok := m.titleLocks.Load(key); ok {
		return lock.(*sync.Mutex)
	}
	lock, _ := m.titleLocks.LoadOrStore(key, new(sync.Mutex))
	return lock.(*sync.Mutex)
}

// SetTitleSync installs the post-persistence binding hook. It runs without either
// the manager mutex or the session title guard, so it may call WithTitleSync.
func (m *Manager) SetTitleSync(callback func(context.Context, string, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.titleSync = callback
}

func (m *Manager) notifyTitleSync(ctx context.Context, key, agent string) {
	m.mu.Lock()
	callback := m.titleSync
	m.mu.Unlock()
	if callback != nil {
		callback(ctx, key, agent)
	}
}

func (m *Manager) titleMetadataUnsafe(s *Session) (TitleMetadata, error) {
	bindings, err := m.listAgentBindingsUnsafe(s.Key)
	if err != nil {
		return TitleMetadata{}, err
	}
	return TitleMetadata{Key: s.Key, Name: s.Name, NameRevision: s.NameRevision, RelatedWorktree: s.RelatedWorktree, Bindings: bindings}, nil
}

func (m *Manager) GetTitleMetadata(_ context.Context, key string) (TitleMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.getSessionMetaUnsafe(key)
	if err != nil {
		return TitleMetadata{}, err
	}
	return m.titleMetadataUnsafe(s)
}

func (m *Manager) ListTitleMetadata(ctx context.Context) ([]TitleMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT s.key, s.name, s.name_revision, s.related_worktree_json,
COALESCE(b.agent, ''), COALESCE(b.agent_session_id, ''), COALESCE(b.agent_ctx_seq, 0),
COALESCE(b.external_source_path, ''), COALESCE(b.external_source_offset, 0), COALESCE(b.external_source_mtime_ns, 0), b.native_title, COALESCE(b.native_cwd, '')
FROM sessions s LEFT JOIN session_agent_bindings b ON b.session_key = s.key
ORDER BY s.updated_at DESC, s.key, b.agent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TitleMetadata, 0)
	for rows.Next() {
		var item TitleMetadata
		var worktree string
		var binding AgentBinding
		if err := rows.Scan(&item.Key, &item.Name, &item.NameRevision, &worktree,
			&binding.Agent, &binding.AgentSessionID, &binding.AgentCtxSeq, &binding.ExternalSourcePath,
			&binding.ExternalSourceOffset, &binding.ExternalSourceMtimeNS, &binding.NativeTitle, &binding.NativeCwd); err != nil {
			return nil, err
		}
		if len(items) == 0 || items[len(items)-1].Key != item.Key {
			if strings.TrimSpace(worktree) != "" {
				var related RelatedWorktree
				if json.Unmarshal([]byte(worktree), &related) == nil && strings.TrimSpace(related.Path) != "" {
					item.RelatedWorktree = &related
				}
			}
			items = append(items, item)
		}
		if binding.Agent != "" {
			binding.SessionKey = item.Key
			items[len(items)-1].Bindings = append(items[len(items)-1].Bindings, binding)
		}
	}
	return items, rows.Err()
}

// HasConflictingTitleBinding uses the native identity index rather than copying
// every session's metadata for each synchronization operation.
func (m *Manager) HasConflictingTitleBinding(ctx context.Context, key, agent, nativeID, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return false, err
	}
	var conflict bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_agent_bindings b
JOIN sessions s ON s.key = b.session_key
WHERE b.agent = ? AND b.agent_session_id = ? AND b.session_key <> ? AND s.name <> ?)`, agent, nativeID, key, name).Scan(&conflict)
	return conflict, err
}

// WithTitleSync excludes local rename and binding replacement for this session
// during a native conditional write. It never holds the global manager mutex during fn.
// fn must not call Rename, RenameIfRevision or any binding replacement method.
func (m *Manager) WithTitleSync(ctx context.Context, key string, fn func(TitleMetadata) error) error {
	lock := m.titleLock(key)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata, err := m.GetTitleMetadata(ctx, key)
	if err != nil {
		return err
	}
	return fn(metadata)
}

// AcknowledgeBindingTitle records only the title owned by this exact snapshot.
// It intentionally does not acquire the session guard: callers may already hold
// it via WithTitleSync. SQLite CAS also rejects stale independent callers.
func (m *Manager) AcknowledgeBindingTitle(ctx context.Context, key, agent, nativeID, name string, revision int64, title string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE session_agent_bindings SET native_title = ?
WHERE session_key = ? AND agent = ? AND agent_session_id = ?
AND EXISTS (SELECT 1 FROM sessions WHERE key = ? AND name = ? AND name_revision = ?)`,
		title, key, agent, nativeID, key, name, revision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n != 0, err
}

// AcknowledgeBindingCwd records verified native cwd independently of title/name.
// Callers may hold WithTitleSync; the exact native ID CAS rejects stale bindings.
func (m *Manager) AcknowledgeBindingCwd(ctx context.Context, key, agent, nativeID, cwd string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE session_agent_bindings SET native_cwd = ?
WHERE session_key = ? AND agent = ? AND agent_session_id = ?`, cwd, key, agent, nativeID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n != 0, err
}

// RenameIfRevision returns nil when a manual or automatic rename has superseded
// the naming request. Even an explicit rename to the same name advances revision.
func (m *Manager) RenameIfRevision(ctx context.Context, key, name string, revision int64) (*Session, error) {
	return m.renameTitle(ctx, key, name, &revision)
}

func (m *Manager) renameTitle(ctx context.Context, key, name string, expected *int64) (*Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("session name required")
	}
	lock := m.titleLock(key)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.getSessionMetaUnsafe(key)
	if err != nil {
		return nil, err
	}
	if expected != nil && s.NameRevision != *expected {
		return nil, nil
	}
	loaded, err := m.getSessionUnsafe(key, 0)
	if err != nil {
		return nil, err
	}
	updatedAt := m.now().UTC()
	result, err := m.db.ExecContext(ctx, `UPDATE sessions SET name = ?, name_revision = name_revision + 1, updated_at = ? WHERE key = ? AND name_revision = ?`, name, updatedAt.Format(time.RFC3339Nano), key, s.NameRevision)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	revision := s.NameRevision + 1
	s = loaded
	s.Name = name
	s.NameRevision = revision
	s.UpdatedAt = updatedAt
	return s, nil
}
