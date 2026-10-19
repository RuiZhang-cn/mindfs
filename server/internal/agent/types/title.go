package types

import "context"

// SessionTitleRequest addresses an exact native session without opening a turn.
// A nil Title reads metadata; ExpectedTitle is an exact compare-and-set guard,
// including when it points to the empty string.
type SessionTitleRequest struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	// ExactCwd marks Cwd as verified native identity, not a template input.
	ExactCwd      bool    `json:"-"`
	Title         *string `json:"title,omitempty"`
	ExpectedTitle *string `json:"expectedTitle,omitempty"`
	OnlyIfEmpty   bool    `json:"onlyIfEmpty,omitempty"`
	Source        string  `json:"source,omitempty"`
}

type SessionTitleResult struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status"`
	// Cwd is verified native identity, including when the metadata RPC fails.
	Cwd string `json:"-"`
}

// SessionTitleSyncer is optional: unsupported transports need not implement it.
// Unsupported implementations return Status "unsupported", never a prompt or
// a filesystem-based approximation of the native metadata operation.
type SessionTitleSyncer interface {
	SessionTitle(context.Context, SessionTitleRequest) (SessionTitleResult, error)
}
