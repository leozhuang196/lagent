// Package repotest is the contract every repo backend must pass, including
// tenant isolation ("tenant A writes, tenant B cannot read").
package repotest

import (
	"context"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

func ctxFor(t string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindSystem, ID: "test", TenantID: t, Role: tenant.RoleTenantAdmin})
}

// Run executes the contract against a fresh store returned by newStore.
func Run(t *testing.T, newStore func(t *testing.T) *repo.Store) {
	t.Run("crud_and_version", func(t *testing.T) {
		s := newStore(t)
		ctx := ctxFor("tA")
		sess := &types.Session{RoleKey: "r", Status: types.SessionIdle, Origin: types.OriginWeb}
		if err := s.Sessions.Insert(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if sess.ID == "" || sess.TenantID != "tA" || sess.Version != 1 {
			t.Fatalf("meta not set: %+v", sess.Meta)
		}
		got, err := s.Sessions.Get(ctx, sess.ID)
		if err != nil || got.RoleKey != "r" {
			t.Fatalf("get: %v %+v", err, got)
		}
		got.Status = types.SessionQueued
		if err := s.Sessions.Replace(ctx, got); err != nil {
			t.Fatal(err)
		}
		// stale write loses
		sess.Status = types.SessionRunning
		if err := s.Sessions.Replace(ctx, sess); !errs.Is(err, errs.CodeVersionConflict) {
			t.Fatalf("want version conflict, got %v", err)
		}
		after, _ := s.Sessions.Get(ctx, sess.ID)
		if after.Status != types.SessionQueued || after.Version != 2 {
			t.Fatalf("unexpected %+v", after)
		}
	})

	t.Run("tenant_isolation", func(t *testing.T) {
		s := newStore(t)
		a, b := ctxFor("tA"), ctxFor("tB")
		sess := &types.Session{RoleKey: "r"}
		if err := s.Sessions.Insert(a, sess); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions.Get(b, sess.ID); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("tenant B read tenant A: %v", err)
		}
		p, err := s.Sessions.List(b, repo.Query{})
		if err != nil || len(p.Items) != 0 {
			t.Fatalf("tenant B listed tenant A: %v %d", err, len(p.Items))
		}
		if err := s.Sessions.Delete(b, sess.ID); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("tenant B deleted tenant A: %v", err)
		}
		if _, err := s.Sessions.Get(context.Background(), sess.ID); !errs.Is(err, errs.CodeUnauthorized) {
			t.Fatalf("missing tenant must fail: %v", err)
		}
		all, err := s.Sessions.List(scope.AllTenants(context.Background()), repo.Query{})
		if err != nil || len(all.Items) != 1 {
			t.Fatalf("unscoped list: %v %d", err, len(all.Items))
		}
		// unique indexes are per tenant
		if err := s.Roles.Insert(a, &types.Role{Versioned: types.Versioned{Key: "k", Ver: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles.Insert(b, &types.Role{Versioned: types.Versioned{Key: "k", Ver: 1}}); err != nil {
			t.Fatalf("same key in other tenant: %v", err)
		}
		if err := s.Roles.Insert(a, &types.Role{Versioned: types.Versioned{Key: "k", Ver: 1}}); !errs.Is(err, errs.CodeConflict) {
			t.Fatalf("want unique conflict, got %v", err)
		}
	})

	t.Run("filters_and_paging", func(t *testing.T) {
		s := newStore(t)
		ctx := ctxFor("tA")
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 7; i++ {
			next := base.Add(time.Duration(i) * time.Hour)
			sc := &types.Schedule{Name: "s", Enabled: i%2 == 0, NextRunAt: &next, Cron: "* * * * *"}
			if err := s.Schedules.Insert(ctx, sc); err != nil {
				t.Fatal(err)
			}
		}
		due := base.Add(3 * time.Hour)
		items, err := repo.All(ctx, s.Schedules, repo.Query{Filter: repo.Filter{"enabled": true, "next_run_at": repo.Lte(due)}}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 { // i = 0, 2
			t.Fatalf("want 2 due, got %d", len(items))
		}
		p1, _ := s.Schedules.List(ctx, repo.Query{Limit: 3})
		if len(p1.Items) != 3 || p1.Next == "" {
			t.Fatalf("page1: %d %q", len(p1.Items), p1.Next)
		}
		p2, _ := s.Schedules.List(ctx, repo.Query{Limit: 3, After: p1.Next})
		if len(p2.Items) != 3 || p2.Items[0].ID <= p1.Items[2].ID {
			t.Fatal("page2 order")
		}
		desc, _ := s.Schedules.List(ctx, repo.Query{Desc: true, Limit: 1})
		if desc.Items[0].ID < p2.Items[2].ID {
			t.Fatal("desc order")
		}
		n, _ := s.Schedules.Count(ctx, repo.Filter{"enabled": repo.Ne(true)})
		if n != 3 {
			t.Fatalf("count ne: %d", n)
		}
		n, _ = s.Schedules.Count(ctx, repo.Filter{"name": repo.In("s", "x")})
		if n != 7 {
			t.Fatalf("count in: %d", n)
		}
	})

	t.Run("delete_if_claims_once", func(t *testing.T) {
		s := newStore(t)
		ctx := ctxFor("tA")
		p := &types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen}
		if err := s.Pendings.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
		// A stale version never deletes: that is what makes "claim" atomic.
		if err := s.Pendings.DeleteIf(ctx, p.ID, p.Version+1); !errs.Is(err, errs.CodeVersionConflict) {
			t.Fatalf("stale claim: %v", err)
		}
		if _, err := s.Pendings.Get(ctx, p.ID); err != nil {
			t.Fatalf("stale claim must not delete: %v", err)
		}
		if err := s.Pendings.DeleteIf(ctx, p.ID, p.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pendings.Get(ctx, p.ID); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("claimed record still there: %v", err)
		}
		// Only one caller can claim a record.
		if err := s.Pendings.DeleteIf(ctx, p.ID, p.Version); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("second claim: %v", err)
		}
		// Another tenant can neither see nor claim it.
		other := &types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen}
		if err := s.Pendings.Insert(ctx, other); err != nil {
			t.Fatal(err)
		}
		if err := s.Pendings.DeleteIf(ctxFor("tB"), other.ID, other.Version); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("cross-tenant claim: %v", err)
		}
	})

	t.Run("update_helper", func(t *testing.T) {
		s := newStore(t)
		ctx := ctxFor("tA")
		p := &types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen}
		if err := s.Pendings.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
		resolve := func(pp *types.Pending) error {
			if pp.Status != types.PendingOpen {
				return errs.New(errs.CodeAlreadyResolved, "resolved")
			}
			pp.Status = types.PendingResolved
			return nil
		}
		if _, err := repo.Update(ctx, s.Pendings, p.ID, resolve); err != nil {
			t.Fatal(err)
		}
		cur, err := repo.Update(ctx, s.Pendings, p.ID, resolve)
		if !errs.Is(err, errs.CodeAlreadyResolved) || cur.Status != types.PendingResolved {
			t.Fatalf("second resolve: %v", err)
		}
	})
}
