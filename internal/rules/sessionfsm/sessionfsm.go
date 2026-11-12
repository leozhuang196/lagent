// Package sessionfsm holds the session and turn state transitions. Each
// function mutates the given struct or refuses; callers persist with repo.Update.
package sessionfsm

import (
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

// Admit reserves an idle session for a new turn: idle → queued.
func Admit(s *types.Session, turnID string) error {
	if s.Status != types.SessionIdle && s.Status != "" {
		return errs.Conflict("session is %s (turn %s)", s.Status, s.ActiveTurnID).With("active_turn_id", s.ActiveTurnID)
	}
	s.Status = types.SessionQueued
	s.ActiveTurnID = turnID
	s.CancelRequested = false
	return nil
}

// Enqueue adds a message to the inbox of a busy session (web channel, §8.5).
func Enqueue(s *types.Session, item types.InboxItem) error {
	if s.Status == types.SessionIdle || s.Status == "" {
		return errs.Conflict("session is idle; submit a new turn instead")
	}
	if len(s.Inbox) >= 20 {
		return errs.Conflict("too many queued messages")
	}
	s.Inbox = append(s.Inbox, item)
	return nil
}

// Start marks the session running for turnID (from queued, or waiting on resume).
func Start(s *types.Session, turnID string) error {
	if s.ActiveTurnID != turnID {
		return errs.Conflict("session's active turn is %q, not %q", s.ActiveTurnID, turnID)
	}
	switch s.Status {
	case types.SessionQueued, types.SessionWaiting, types.SessionRunning:
		s.Status = types.SessionRunning
		return nil
	}
	return errs.Conflict("cannot start turn on %s session", s.Status)
}

// Suspend parks the session while its turn waits on a pending item.
func Suspend(s *types.Session, turnID string) error {
	if s.ActiveTurnID != turnID {
		return errs.Conflict("not the active turn")
	}
	s.Status = types.SessionWaiting
	return nil
}

// Requeue moves a waiting session back to queued when its pending item resolves.
func Requeue(s *types.Session, turnID string) error {
	if s.ActiveTurnID != turnID {
		return errs.Conflict("not the active turn")
	}
	if s.Status != types.SessionWaiting && s.Status != types.SessionQueued {
		return errs.Conflict("session is %s", s.Status)
	}
	s.Status = types.SessionQueued
	return nil
}

// Finish releases the session after a terminal turn.
func Finish(s *types.Session, turnID string) error {
	if s.ActiveTurnID != turnID {
		return errs.Conflict("not the active turn")
	}
	s.Status = types.SessionIdle
	s.ActiveTurnID = ""
	s.CancelRequested = false
	return nil
}

// RequestCancel sets the durable cancel flag. Idempotent.
func RequestCancel(s *types.Session) bool {
	if s.Status == types.SessionIdle || s.Status == "" {
		return false
	}
	s.CancelRequested = true
	return true
}

// TakeInbox drains the inbox.
func TakeInbox(s *types.Session) []types.InboxItem {
	items := s.Inbox
	s.Inbox = nil
	return items
}

// StartTurn moves a turn to running with a new fencing epoch.
func StartTurn(t *types.Turn, epoch int64, now time.Time) error {
	switch t.Status {
	case types.TurnQueued, types.TurnSuspended, types.TurnRunning:
	default:
		return errs.Conflict("turn is %s", t.Status)
	}
	if epoch <= t.Epoch {
		return errs.New(errs.CodeLeaseLost, "stale epoch %d <= %d", epoch, t.Epoch)
	}
	t.Status = types.TurnRunning
	t.Epoch = epoch
	if t.StartedAt == nil {
		t.StartedAt = &now
	}
	return nil
}

// CheckEpoch guards every turn write: a writer whose lease was taken over loses.
func CheckEpoch(t *types.Turn, epoch int64) error {
	if t.Epoch != epoch || t.Status != types.TurnRunning {
		return errs.New(errs.CodeLeaseLost, "turn now at epoch %d (%s), writer has %d", t.Epoch, t.Status, epoch)
	}
	return nil
}

// SuspendTurn parks the turn on pending ids.
func SuspendTurn(t *types.Turn, epoch int64, pendingIDs []string) error {
	if err := CheckEpoch(t, epoch); err != nil {
		return err
	}
	t.Status = types.TurnSuspended
	t.WaitingOn = pendingIDs
	return nil
}

// EndTurn sets a terminal status.
func EndTurn(t *types.Turn, epoch int64, status types.TurnStatus, e *types.TurnError, now time.Time) error {
	if !status.Terminal() {
		return errs.Invalid("%s is not terminal", status)
	}
	if t.Status.Terminal() {
		return errs.New(errs.CodeAlreadyResolved, "turn already %s", t.Status)
	}
	// Cancelling a queued or suspended turn needs no lease.
	if t.Status == types.TurnRunning {
		if err := CheckEpoch(t, epoch); err != nil {
			return err
		}
	}
	t.Status = status
	t.Error = e
	t.WaitingOn = nil
	t.EndedAt = &now
	return nil
}
