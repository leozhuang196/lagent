// Package mongo is the MongoDB repository backend (go.mongodb.org/mongo-driver/v2).
//
// Entities are stored with their bson tags; Meta is inlined so "_id" is the
// entity id and "tenant_id" / "version" are top-level fields. Semantics follow
// the memory backend and are checked by repo/repotest.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// Collection is a repo.Collection backed by one Mongo collection.
type Collection[E any] struct {
	name   string
	prefix string
	clk    clock.Clock
	coll   *mongo.Collection
}

var _ repo.Collection[types.Session] = (*Collection[types.Session])(nil)

// NewCollection wraps db.<spec.Name>; prefix is used for generated ids.
// Indexes are created by EnsureIndexes (NewStore does it for every spec).
func NewCollection[E any](db *mongo.Database, spec repo.CollectionSpec, prefix string, clk clock.Clock) *Collection[E] {
	if clk == nil {
		clk = clock.Real{}
	}
	coll := db.Collection(spec.Name, options.Collection().SetRegistry(Registry))
	return &Collection[E]{name: spec.Name, prefix: prefix, clk: clk, coll: coll}
}

// IndexModels returns the indexes of spec: one on (tenant_id, _id) for listing,
// then every declared index prefixed by tenant_id so unique keys are per tenant.
func IndexModels(spec repo.CollectionSpec) []mongo.IndexModel {
	out := []mongo.IndexModel{{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "_id", Value: 1}}}}
	for _, ix := range spec.Indexes {
		keys := bson.D{{Key: "tenant_id", Value: 1}}
		for _, f := range ix.Fields {
			keys = append(keys, bson.E{Key: f, Value: 1})
		}
		opts := options.Index().SetName(indexName(ix))
		if ix.Unique {
			opts.SetUnique(true)
		}
		out = append(out, mongo.IndexModel{Keys: keys, Options: opts})
	}
	return out
}

func indexName(ix repo.Index) string {
	n := "tenant_id_" + strings.ReplaceAll(strings.Join(ix.Fields, "_"), ".", "_")
	if ix.Unique {
		return n + "_uq"
	}
	return n + "_idx"
}

// EnsureIndexes creates the indexes of spec; it is idempotent.
func (c *Collection[E]) EnsureIndexes(ctx context.Context, spec repo.CollectionSpec) error {
	if _, err := c.coll.Indexes().CreateMany(ctx, IndexModels(spec)); err != nil {
		return fmt.Errorf("mongo: indexes of %s: %w", c.name, err)
	}
	return nil
}

func meta[E any](v *E) *types.Meta {
	ent, ok := any(v).(types.Entity)
	if !ok {
		panic(fmt.Sprintf("%T does not implement types.Entity", v))
	}
	return ent.M()
}

// tenantCond is the tenant filter for reads; ok=false means unscoped.
func tenantCond(ctx context.Context) (bson.E, bool, error) {
	if scope.IsAll(ctx) {
		return bson.E{}, false, nil
	}
	t, err := tenant.ID(ctx)
	if err != nil {
		return bson.E{}, false, err
	}
	return bson.E{Key: "tenant_id", Value: t}, true, nil
}

// query builds the full read filter: tenant, caller filter and extra conditions.
func query(ctx context.Context, f repo.Filter, extra ...bson.E) (bson.D, error) {
	var conds []bson.E
	tc, scoped, err := tenantCond(ctx)
	if err != nil {
		return nil, err
	}
	if scoped {
		conds = append(conds, tc)
	}
	fd, err := TranslateFilter(f)
	if err != nil {
		return nil, err
	}
	conds = append(conds, fd...)
	conds = append(conds, extra...)
	return and(conds...), nil
}

func (c *Collection[E]) wrap(err error, format string, args ...any) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errs.Wrap(errs.CodeUnavailable, err, "mongo %s: "+format, append([]any{c.name}, args...)...)
}

func (c *Collection[E]) Get(ctx context.Context, idv string) (*E, error) {
	q, err := query(ctx, nil, bson.E{Key: "_id", Value: idv})
	if err != nil {
		return nil, err
	}
	v := new(E)
	if err := c.coll.FindOne(ctx, q).Decode(v); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errs.NotFound(c.name, idv)
		}
		return nil, c.wrap(err, "get %s", idv)
	}
	return v, nil
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
	var extra []bson.E
	dir := 1
	if q.Desc {
		dir = -1
	}
	if q.After != "" {
		cmp := "$gt"
		if q.Desc {
			cmp = "$lt"
		}
		extra = append(extra, bson.E{Key: "_id", Value: bson.D{{Key: cmp, Value: q.After}}})
	}
	filter, err := query(ctx, q.Filter, extra...)
	if err != nil {
		return repo.Page[E]{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = repo.DefaultLimit
	}
	if limit > repo.MaxLimit {
		limit = repo.MaxLimit
	}
	// Fetch one extra row to learn whether another page exists.
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: dir}}).SetLimit(int64(limit + 1))
	cur, err := c.coll.Find(ctx, filter, opts)
	if err != nil {
		return repo.Page[E]{}, c.wrap(err, "list")
	}
	var items []*E
	if err := cur.All(ctx, &items); err != nil {
		return repo.Page[E]{}, c.wrap(err, "list")
	}
	var page repo.Page[E]
	if len(items) > limit {
		items = items[:limit]
		page.Next = meta(items[limit-1]).ID
	}
	page.Items = items
	return page, nil
}

func (c *Collection[E]) Count(ctx context.Context, f repo.Filter) (int64, error) {
	filter, err := query(ctx, f)
	if err != nil {
		return 0, err
	}
	n, err := c.coll.CountDocuments(ctx, filter)
	if err != nil {
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
	if _, err := c.coll.InsertOne(ctx, v); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errs.Wrap(errs.CodeConflict, err, "%s: duplicate key", c.name)
		}
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
	filter := bson.D{{Key: "_id", Value: m.ID}, {Key: "tenant_id", Value: m.TenantID}, {Key: "version", Value: prevVersion}}
	res, err := c.coll.ReplaceOne(ctx, filter, v)
	if err == nil && res.MatchedCount == 1 {
		return nil
	}
	m.Version, m.UpdatedAt = prevVersion, prevUpdated
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errs.Wrap(errs.CodeConflict, err, "%s: duplicate key", c.name)
		}
		return c.wrap(err, "replace %s", m.ID)
	}
	// No match: either the document is gone or someone else bumped the version.
	n, err := c.coll.CountDocuments(ctx, bson.D{{Key: "_id", Value: m.ID}, {Key: "tenant_id", Value: m.TenantID}})
	if err != nil {
		return c.wrap(err, "replace %s", m.ID)
	}
	if n == 0 {
		return errs.NotFound(c.name, m.ID)
	}
	return errs.New(errs.CodeVersionConflict, "%s %s: version %d is stale", c.name, m.ID, prevVersion)
}

// DeleteIf deletes id only if its stored version is version; the caller that
// wins may consume the record (e.g. claim a draft for publishing).
func (c *Collection[E]) DeleteIf(ctx context.Context, idv string, version int64) error {
	q, err := query(ctx, nil, bson.E{Key: "_id", Value: idv}, bson.E{Key: "version", Value: version})
	if err != nil {
		return err
	}
	res, err := c.coll.DeleteOne(ctx, q)
	if err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if res.DeletedCount == 1 {
		return nil
	}
	// No match: gone already, or a newer version exists.
	q, err = query(ctx, nil, bson.E{Key: "_id", Value: idv})
	if err != nil {
		return err
	}
	n, err := c.coll.CountDocuments(ctx, q)
	if err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if n == 0 {
		return errs.NotFound(c.name, idv)
	}
	return errs.New(errs.CodeVersionConflict, "%s %s: version %d is stale", c.name, idv, version)
}

func (c *Collection[E]) Delete(ctx context.Context, idv string) error {
	q, err := query(ctx, nil, bson.E{Key: "_id", Value: idv})
	if err != nil {
		return err
	}
	res, err := c.coll.DeleteOne(ctx, q)
	if err != nil {
		return c.wrap(err, "delete %s", idv)
	}
	if res.DeletedCount == 0 {
		return errs.NotFound(c.name, idv)
	}
	return nil
}

// NewStore builds a Store on database dbName with the same id prefixes as
// memory.NewStore, creating every index from repo.Specs (idempotent).
func NewStore(ctx context.Context, client *mongo.Client, dbName string, clk clock.Clock) (*repo.Store, error) {
	b := &builder{db: client.Database(dbName), clk: clk}
	st := &repo.Store{
		Tenants:       build[types.Tenant](b, "tenants", id.Tenant),
		APIKeys:       build[types.APIKey](b, "api_keys", id.APIKey),
		Sessions:      build[types.Session](b, "sessions", id.Session),
		Messages:      build[types.Message](b, "messages", id.Message),
		Turns:         build[types.Turn](b, "turns", id.Turn),
		Pendings:      build[types.Pending](b, "pendings", id.Pending),
		ToolCalls:     build[types.ToolCall](b, "tool_calls", id.ToolCall),
		Compactions:   build[types.Compaction](b, "compactions", id.Compaction),
		Artifacts:     build[types.Artifact](b, "artifacts", id.Artifact),
		LLMCalls:      build[types.LLMCall](b, "llm_calls", id.LLMCall),
		Roles:         build[types.Role](b, "roles", id.Role),
		Skills:        build[types.Skill](b, "skills", id.Skill),
		MemorySpaces:  build[types.MemorySpace](b, "memory_spaces", id.Memory),
		MemoryEntries: build[types.MemoryEntry](b, "memory_entries", id.Memory),
		MCPServers:    build[types.MCPServer](b, "mcp_servers", id.MCPServer),
		Retrievals:    build[types.RetrievalSource](b, "retrieval_sources", id.Retrieval),
		RiskOverrides: build[types.RiskOverride](b, "risk_overrides", id.RiskOverride),
		FeishuChats:   build[types.FeishuChat](b, "feishu_chats", id.FeishuChat),
		FeishuThreads: build[types.FeishuThread](b, "feishu_threads", id.FeishuThread),
		Audit:         build[types.AuditLog](b, "audit_logs", id.Audit),
		WorkflowDefs:  build[types.WorkflowDef](b, "workflow_defs", id.Workflow),
		Runs:          build[types.WorkflowRun](b, "workflow_runs", id.Run),
		StepRuns:      build[types.StepRun](b, "step_runs", id.StepRun),
		Schedules:     build[types.Schedule](b, "schedules", id.Schedule),
		ScheduleRuns:  build[types.ScheduleRun](b, "schedule_runs", id.ScheduleRun),
	}
	if len(b.ensure) != len(repo.Specs) {
		return nil, fmt.Errorf("mongo: %d collections wired, %d specs declared", len(b.ensure), len(repo.Specs))
	}
	for _, f := range b.ensure {
		if err := f(ctx); err != nil {
			return nil, err
		}
	}
	return st, nil
}

type builder struct {
	db     *mongo.Database
	clk    clock.Clock
	ensure []func(context.Context) error
}

func build[E any](b *builder, key, prefix string) *Collection[E] {
	spec, ok := repo.Specs[key]
	if !ok {
		panic("mongo: no spec for " + key)
	}
	c := NewCollection[E](b.db, spec, prefix, b.clk)
	b.ensure = append(b.ensure, func(ctx context.Context) error { return c.EnsureIndexes(ctx, spec) })
	return c
}
