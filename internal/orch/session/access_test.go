package session_test

import (
	"context"
	"testing"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
)

func TestChildResourceOwnership(t *testing.T) {
	e := orchtest.New(t)
	sess, err := e.Sessions.Create(e.Ctx, session.CreateInput{})
	if err != nil {
		t.Fatal(err)
	}
	turn := &types.Turn{SessionID: sess.ID, Status: types.TurnQueued}
	if err := e.Store.Turns.Insert(e.Ctx, turn); err != nil {
		t.Fatal(err)
	}
	item := &types.Pending{SessionID: sess.ID, TurnID: turn.ID, Kind: types.PendingApproval, Status: types.PendingOpen}
	if err := e.Store.Pendings.Insert(e.Ctx, item); err != nil {
		t.Fatal(err)
	}
	art := &types.Artifact{SessionID: sess.ID, TurnID: turn.ID, Name: "private"}
	if err := e.Store.Artifacts.Insert(e.Ctx, art); err != nil {
		t.Fatal(err)
	}
	other := tenant.With(context.Background(), tenant.Principal{ID: "u2", TenantID: "t1", Kind: tenant.KindAPIKey, Role: tenant.RoleMember})
	foreign := tenant.With(context.Background(), tenant.Principal{ID: "u1", TenantID: "t2", Kind: tenant.KindAPIKey, Role: tenant.RoleTenantAdmin})
	for _, tt := range []struct {
		name string
		ctx  context.Context
		code errs.Code
	}{
		{"owner", e.Ctx, ""}, {"admin", e.Admin, ""}, {"other member", other, errs.CodeForbidden}, {"other tenant", foreign, errs.CodeNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, a := e.Sessions.GetTurn(tt.ctx, turn.ID)
			_, b := e.Sessions.GetPending(tt.ctx, item.ID)
			_, c := e.Sessions.GetArtifact(tt.ctx, art.ID)
			for _, err := range []error{a, b, c} {
				if errs.CodeOf(err) != tt.code {
					t.Fatalf("want %s, got %v", tt.code, err)
				}
			}
		})
	}
	if _, err := e.Sessions.Resolve(other, item.ID, types.DecisionApprove, "", "web"); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("unassigned approval accessible: %v", err)
	}
	stored, _ := e.Store.Pendings.Get(e.Ctx, item.ID)
	if stored.Status != types.PendingOpen {
		t.Fatal("unauthorised resolve changed state")
	}
	arts, err := e.Sessions.ListArtifacts(other, sess.ID, "")
	if err != nil || len(arts) != 0 {
		t.Fatalf("artifact list leaked: %v %v", arts, err)
	}
}

func TestAssignedReviewerCanReadPending(t *testing.T) {
	e := orchtest.New(t)
	item := &types.Pending{SessionID: "another-session", Assignee: "u1", Status: types.PendingOpen}
	if err := e.Store.Pendings.Insert(e.Ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sessions.GetPending(e.Ctx, item.ID); err != nil {
		t.Fatal(err)
	}
}
