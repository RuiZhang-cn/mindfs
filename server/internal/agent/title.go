package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
)

// SessionTitle routes metadata to the exact live native identity when present,
// otherwise to an importer that does not load/resume/create a chat session.
// Neither the pool mutex nor a runtime-group lock is held during an RPC.
func (p *Pool) SessionTitle(ctx context.Context, agentName, poolSessionKey string, in agenttypes.SessionTitleRequest) (result agenttypes.SessionTitleResult, err error) {
	defer func() {
		if result.Cwd == "" && in.ExactCwd {
			result.Cwd = in.Cwd
		}
	}()
	unsupported := agenttypes.SessionTitleResult{SessionID: in.SessionID, Status: "unsupported"}
	if !strings.EqualFold(agentName, "omp") {
		return unsupported, nil
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return agenttypes.SessionTitleResult{}, errors.New("session title requires session id")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return agenttypes.SessionTitleResult{}, errors.New("agent pool closed")
	}
	def, ok := p.cfg.GetAgent(agentName)
	if !ok {
		p.mu.Unlock()
		return agenttypes.SessionTitleResult{}, errors.New("agent not configured: " + agentName)
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	if protocol != ProtocolACP {
		p.mu.Unlock()
		return unsupported, nil
	}
	entry := p.sessions[poolSessionKey]
	staleKey := entry != nil && (entry.agentName != agentName || entry.session == nil || entry.session.SessionID() != in.SessionID)
	if entry == nil || staleKey {
		entry = nil
		// Binding persistence can precede promoting a replacement from its
		// staging key. Find the exact live owner before rejecting a stale key.
		for _, candidate := range p.sessions {
			if candidate != nil && candidate.agentName == agentName && candidate.session != nil && candidate.session.SessionID() == in.SessionID {
				entry = candidate
				break
			}
		}
	}
	if entry == nil && staleKey {
		p.mu.Unlock()
		return agenttypes.SessionTitleResult{SessionID: in.SessionID, Status: "conflict"}, nil
	}
	if entry != nil {
		if entry.closing {
			p.mu.Unlock()
			return agenttypes.SessionTitleResult{SessionID: in.SessionID, Status: "busy"}, nil
		}
		syncer, ok := entry.session.(agenttypes.SessionTitleSyncer)
		if !ok {
			p.mu.Unlock()
			return unsupported, nil
		}
		entry.activeUses++
		entry.lastUsedAt = time.Now()
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			entry.activeUses--
			entry.lastUsedAt = time.Now()
			p.mu.Unlock()
		}()
		return syncer.SessionTitle(ctx, in)
	}
	// Copy mutable config before releasing the mutex, then perform all startup
	// and metadata I/O outside it.
	def.Args = def.BuildArgs(in.Cwd)
	def.Env = cloneEnv(def.Env)
	p.mu.Unlock()
	importer, err := NewExternalSessionImporter(def)
	if err != nil {
		return agenttypes.SessionTitleResult{}, err
	}
	syncer, ok := importer.(agenttypes.SessionTitleSyncer)
	if !ok {
		return unsupported, nil
	}
	return syncer.SessionTitle(ctx, in)
}
