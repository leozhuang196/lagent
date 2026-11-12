// Package postgres is the PostgreSQL repository backend (github.com/jackc/pgx/v5).
//
// Each collection is one table (id, tenant_id, version, doc jsonb). The doc is
// the entity's JSON encoding — the same encoding the memory backend keeps —
// so semantics match it; id / tenant_id / version are duplicated into columns
// for keys, tenant filtering and the optimistic-version check. Requires
// PostgreSQL 15+ (unique indexes use NULLS NOT DISTINCT so that, as in memory
// and Mongo, two documents both missing a unique field collide).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// DB is the subset of *pgxpool.Pool (or *pgx.Conn) the backend needs.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Collection is a repo.Collection backed by one table.
type Collection[E any] struct {
	name   string
	table  string // quoted identifier
	prefix string
	clk    clock.Clock
	db     DB
}

var _ repo.Collection[types.Session] = (*Collection[types.Session])(nil)

// NewCollection wraps the table of spec; prefix is used for generated ids.
// The table must exist (NewStore creates every table).
func NewCollection[E any](db DB, spec repo.CollectionSpec, prefix string, clk clock.Clock) *Collection[E] {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Collection[E]{name: spec.Name, table: pgx.Identifier{spec.Name}.Sanitize(), prefix: prefix, clk: clk, db: db}
}

func meta[E any](v *E) *types.Meta {
	ent, ok := any(v).(types.Entity)
	if !ok {
		panic(fmt.Sprintf("%T does not implement types.Entity", v))
	}
	return ent.M()
}

func (c *Collection[E]) wrap(err error, format string, args ...any) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" { // unique_violation
		return errs.Wrap(errs.CodeConflict, err, "%s: duplicate %s", c.name, pe.ConstraintName)
	}
	return errs.Wrap(errs.CodeUnavailable, err, "postgres %s: "+format, append([]any{c.name}, args...)...)
}

// where builds the WHERE clause: tenant (unless unscoped), caller filter and
// extra conditions, with args numbered after the given ones.
func where(ctx context.Context, f repo.Filter, args []any) (string, []any, error) {
	var conds []string
	if !scope.IsAll(ctx) {
		t, err := tenant.ID(ctx)
		if err != nil {
			return "", nil, err
		}
		args = append(args, t)
		conds = append(conds, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	fs, fargs, err := TranslateFilter(f, len(args))
	if err != nil {
		return "", nil, err
	}
	if fs != "TRUE" {
		conds = append(conds, fs)
		args = append(args, fargs...)
	}
	if len(conds) == 0 {
		return "TRUE", args, nil
	}
	return strings.Join(conds, " AND "), args, nil
}

func decode[E any](raw []byte) (*E, error) {
	v := new(E)
	if err := json.Unmarshal(raw, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (c *Collection[E]) Get(ctx context.Context, idv string) (*E, error) {
	w, args, err := where(ctx, nil, []any{idv})
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = c.db.QueryRow(ctx, "SELECT doc FROM "+c.table+" WHERE id = $1 AND "+w, args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.NotFound(c.name, idv)
	}
	if err != nil {
		return nil, c.wrap(err, "get %s", idv)
	}
	return decode[E](raw)
}

func (c *Collection[E]) FindOne(ctx context.Context, f repo.Filter) (*E, error) {
	p, err := c.List(ctx, repo.Query{Filter: f, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(p.Items) == 0 {
		return nil, errs.New(errs.CodeNotFound, "%s matching filter not found", c.name)
	}
	return p.Items[0], nil
}

func (c *Collection[E]) List(ctx context.Context, q repo.Query) (repo.Page[E], error) {
	w, args, err := where(ctx, q.Filter, nil)
	if err != nil {
		return repo.Page[E]{}, err
	}
	order := "ASC"
	if q.Desc {
		order = "DESC"
	}
	if q.After != "" {
		cmp := ">"
		if q.Desc {
			cmp = "<"
		}
		args = append(args, q.After)
		w += fmt.Sprintf(" AND id %s $%d", cmp, len(args))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = repo.DefaultLimit
	}
	if limit > repo.MaxLimit {
		limit = repo.MaxLimit
	}
	// Fetch one extra row to learn whether another page exists.
	sql := fmt.Sprintf("SELECT doc FROM %s WHERE %s ORDER BY id %s LIMIT %d", c.table, w, order, limit+1)
	rows, err := c.db.Query(ctx, sql, args...)
	if err != nil {
		return repo.Page[E]{}, c.wrap(err, "list")
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return repo.Page[E]{}, c.wrap(err, "list")
	}
	var page repo.Page[E]
	more := len(raws) > limit
	if more {
		raws = raws[:limit]
	}
	for _, raw := range raws {
		v, err := decode[E](raw)
		if err != nil {
			return repo.Page[E]{}, err
		}
		page.Items = append(page.Items, v)
	}
	if more {
		page.Next = meta(page.Items[len(page.Items)-1]).ID
	}
	return page, nil
}

func (c *Collection[E]) Count(ctx context.Context, f repo.Filter) (int64, error) {
	w, args, err := where(ctx, f, nil)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := c.db.QueryRow(ctx, "SELECT count(*) FROM "+c.table+" WHERE "+w, args...).Scan(&n); err != nil {
		return 0, c.wrap(err, "count")
	}
	return n, nil
}

func (c *Collection[E]) Insert(ctx context.Context, v *E) error {
	m := meta(v)
	if m.TenantID == "" {
		t, err := tenant.ID(ctx)
		if err != nil {
			return err
		}
		m.TenantID = t
	}
	if m.ID == "" {
		m.ID = id.NewWithPrefix(c.prefix)
	}
	now := c.clk.Now()
	m.Version = 1
	m.CreatedAt, m.UpdatedAt = now, now
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(ctx, "INSERT INTO "+c.table+" (id, tenant_id, version, doc) VALUES ($1, $2, $3, $4)",
		m.ID, m.TenantID, m.Version, string(raw))
	if err != nil {
		return c.wrap(err, "insert %s", m.ID)
	}
	return nil
}

func (c *Collection[E]) Replace(ctx context.Context, v *E) error {
	m := meta(v)
	// Writes use the entity's own tenant; a scoped caller may only touch its own.
	if !scope.IsAll(ctx) {
		t, err := tenant.ID(ctx)
		if err != nil {
			return err
		}
		if t != m.TenantID {
			return errs.NotFound(c.name, m.ID)
		}
	}
	prevVersion, prevUpdated := m.Version, m.UpdatedAt
	m.Version++
	m.UpdatedAt = c.clk.Now()
	raw, err := json.Marshal(v)
	if err != nil {
		m.Version, m.UpdatedAt = prevVersion, prevUpdated
		return err
	}
	tag, err := c.db.Exec(ctx, "UPDATE "+c.table+" SET version = $1, doc = $2 WHERE id = $3 AND tenant_id = $4 AND version = $5",
		m.Version, string(raw), m.ID, m.TenantID, prevVersion)
	if err == nil && tag.RowsAffected() == 1 {
		return nil
	}
	m.Version, m.UpdatedAt = prevVersion, prevUpdated
	if err != nil {
		return c.wrap(err, "replace %s", m.ID)
	}
	// No row: either it is gone or someone else bumped the version.
	var exists bool
	err = c.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+c.table+" WHERE id = $1 AND tenant_id = $2)", m.ID, m.TenantID).Scan(&exists)
	if err != nil {
		return c.wrap(err, "replace %s", m.ID)
	}
	if !exists {
		return errs.NotFound(c.name, m.ID)
	}
	return errs.New(errs.CodeVersionConflict, "%s %s: version %d is stale", c.name, m.ID, prevVersion)
}

// DeleteIf deletes id only if its stored version is version; the caller that
// wins may consume the record (e.g. claim a draft for publishing).
func (c *Collection[E]) DeleteIf(ctx context.Context, idv string, version int64) error {
	w, args, err := where(ctx, nil, []any{idv, version})
	if err != nil {
		return err
	}
	tag, err := c.db.Exec(ctx, "DELETE FROM "+c.table+" WHERE id = $1 AND version = $2 AND "+w, args...)
	if err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// No row: gone already, or a newer version exists.
	w, args, err = where(ctx, nil, []any{idv})
	if err != nil {
		return err
	}
	var exists bool
	if err := c.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+c.table+" WHERE id = $1 AND "+w+")", args...).Scan(&exists); err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if !exists {
		return errs.NotFound(c.name, idv)
	}
	return errs.New(errs.CodeVersionConflict, "%s %s: version %d is stale", c.name, idv, version)
}

func (c *Collection[E]) Delete(ctx context.Context, idv string) error {
	w, args, err := where(ctx, nil, []any{idv})
	if err != nil {
		return err
	}
	tag, err := c.db.Exec(ctx, "DELETE FROM "+c.table+" WHERE id = $1 AND "+w, args...)
	if err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound(c.name, idv)
	}
	return nil
}
