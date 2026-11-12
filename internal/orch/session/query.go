package session

import (
	"context"
	"sort"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// Snapshot is what a client loads before following the event stream.
type Snapshot struct {
	Session  *types.Session   `json:"session"`
	Messages []*types.Message `json:"messages"`
	Pendings []*types.Pending `json:"pendings"`
	Turn     *types.Turn      `json:"active_turn,omitempty"`
	LastSeq  int64            `json:"last_seq"` // event cursor to resume from
}

// Snapshot returns the session state and the event cursor that matches it.
func (s *Service) Snapshot(ctx context.Context, sessionID string, maxMessages int) (*Snapshot, error) {
	p, _ := tenant.From(ctx)
	// Read the cursor first: events after it may duplicate snapshot content,
	// which clients de-duplicate by id; the reverse order could lose events.
	var last int64
	if s.Bus != nil {
		last, _ = s.Bus.LastSeq(ctx, sessionID)
	}
	sess, err := s.Store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.canUse(p, sess); err != nil {
		return nil, err
	}
	if maxMessages <= 0 {
		maxMessages = 200
	}
	page, err := s.Store.Messages.List(ctx, repo.Query{Filter: repo.Filter{"session_id": sessionID}, Desc: true, Limit: maxMessages})
	if err != nil {
		return nil, err
	}
	msgs := page.Items
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	pend, err := repo.All(ctx, s.Store.Pendings, repo.Query{Filter: repo.Filter{"session_id": sessionID, "status": string(types.PendingOpen)}}, 100)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Session: sess, Messages: msgs, Pendings: pend, LastSeq: last}
	if sess.ActiveTurnID != "" {
		snap.Turn, _ = s.Store.Turns.Get(ctx, sess.ActiveTurnID)
	}
	return snap, nil
}

// ListSessions lists the caller's sessions (admins see the tenant's).
func (s *Service) ListSessions(ctx context.Context, after string, limit int, all bool) (repo.Page[types.Session], error) {
	p, _ := tenant.From(ctx)
	f := repo.Filter{}
	if !(all && p.IsAdmin()) {
		f["created_by.id"] = p.ID
	}
	return s.Store.Sessions.List(ctx, repo.Query{Filter: f, Desc: true, After: after, Limit: limit})
}

// ListPendings lists open items for the pending center.
func (s *Service) ListPendings(ctx context.Context, status string, mine bool, after string, limit int) (repo.Page[types.Pending], error) {
	p, _ := tenant.From(ctx)
	f := repo.Filter{}
	if status != "" {
		f["status"] = status
	}
	if mine || !p.IsAdmin() {
		f["assignee"] = p.ID
	}
	return s.Store.Pendings.List(ctx, repo.Query{Filter: f, Desc: true, After: after, Limit: limit})
}

// TurnView is the public state of a turn; waiting_human is distinct from running.
type TurnView struct {
	*types.Turn
	State string `json:"state"`
}

// GetTurn returns a turn with its externally visible state.
func (s *Service) GetTurn(ctx context.Context, turnID string) (*TurnView, error) {
	t, err := s.Store.Turns.Get(ctx, turnID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSession(ctx, t.SessionID); err != nil {
		return nil, err
	}
	state := string(t.Status)
	if t.Status == types.TurnSuspended {
		state = "waiting_human"
		for _, pid := range t.WaitingOn {
			if p, err := s.Store.Pendings.Get(ctx, pid); err == nil && (p.Kind == types.PendingSubtasks || p.Kind == types.PendingExternalJob) {
				state = "waiting_subtasks"
			}
		}
	}
	return &TurnView{Turn: t, State: state}, nil
}

// GetPending returns one pending item of the tenant.
func (s *Service) GetPending(ctx context.Context, id string) (*types.Pending, error) {
	p, err := s.Store.Pendings.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizePending(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// GetArtifact returns one artifact record of the tenant.
func (s *Service) GetArtifact(ctx context.Context, id string) (*types.Artifact, error) {
	a, err := s.Store.Artifacts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeArtifact(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// ListArtifacts lists the artifacts of a session or run.
func (s *Service) ListArtifacts(ctx context.Context, sessionID, runID string) ([]*types.Artifact, error) {
	f := repo.Filter{}
	if sessionID != "" {
		f["session_id"] = sessionID
	}
	if runID != "" {
		f["run_id"] = runID
	}
	arts, err := repo.All(ctx, s.Store.Artifacts, repo.Query{Filter: f}, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]*types.Artifact, 0, len(arts))
	for _, a := range arts {
		if err := s.authorizeArtifact(ctx, a); err != nil {
			if errs.Is(err, errs.CodeForbidden) {
				continue
			}
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// authorizeSession keeps ownership checks shared by all child-resource APIs.
func (s *Service) authorizeSession(ctx context.Context, id string) error {
	sess, err := s.Store.Sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	p, _ := tenant.From(ctx)
	return s.canUse(p, sess)
}

// Assigned reviewers can read their item without owning the entire session.
func (s *Service) authorizePending(ctx context.Context, item *types.Pending) error {
	p, ok := tenant.From(ctx)
	if !ok {
		return errs.New(errs.CodeUnauthorized, "no principal")
	}
	if p.IsAdmin() || p.Kind == tenant.KindSystem || (item.Assignee != "" && item.Assignee == p.ID) {
		return nil
	}
	return s.authorizeSession(ctx, item.SessionID)
}

func (s *Service) authorizeArtifact(ctx context.Context, a *types.Artifact) error {
	if a.SessionID != "" {
		return s.authorizeSession(ctx, a.SessionID)
	}
	p, ok := tenant.From(ctx)
	if !ok {
		return errs.New(errs.CodeUnauthorized, "no principal")
	}
	if p.IsAdmin() || p.Kind == tenant.KindSystem {
		return nil
	}
	if a.RunID != "" {
		run, err := s.Store.Runs.Get(ctx, a.RunID)
		if err != nil {
			return err
		}
		if run.StartedBy.ID == p.ID {
			return nil
		}
	}
	return errs.New(errs.CodeForbidden, "not your artifact")
}
