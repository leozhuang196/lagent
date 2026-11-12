// Package memory is an in-process repository for local development and tests.
// It stores JSON copies so callers never share pointers with the store.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

type doc struct {
	tenant string
	raw    []byte
	data   map[string]any
}

// Collection is an in-memory repo.Collection.
type Collection[E any] struct {
	name    string
	prefix  string
	indexes []repo.Index
	clk     clock.Clock

	mu   sync.RWMutex
	docs map[string]*doc
}

// NewCollection builds a collection; prefix is used for generated ids.
func NewCollection[E any](spec repo.CollectionSpec, prefix string, clk clock.Clock) *Collection[E] {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Collection[E]{name: spec.Name, prefix: prefix, indexes: spec.Indexes, clk: clk, docs: map[string]*doc{}}
}

func meta[E any](v *E) *types.Meta {
	ent, ok := any(v).(types.Entity)
	if !ok {
		panic(fmt.Sprintf("%T does not implement types.Entity", v))
	}
	return ent.M()
}

func encode[E any](v *E) (*doc, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return &doc{tenant: meta(v).TenantID, raw: raw, data: data}, nil
}

func decode[E any](d *doc) (*E, error) {
	v := new(E)
	if err := json.Unmarshal(d.raw, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (c *Collection[E]) visible(ctx context.Context, d *doc) (bool, error) {
	if scope.IsAll(ctx) {
		return true, nil
	}
	t, err := tenant.ID(ctx)
	if err != nil {
		return false, err
	}
	return d.tenant == t, nil
}

func (c *Collection[E]) Get(ctx context.Context, idv string) (*E, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.docs[idv]
	if !ok {
		return nil, errs.NotFound(c.name, idv)
	}
	vis, err := c.visible(ctx, d)
	if err != nil {
		return nil, err
	}
	if !vis {
		return nil, errs.NotFound(c.name, idv)
	}
	return decode[E](d)
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

func (c *Collection[E]) matching(ctx context.Context, f repo.Filter) ([]string, error) {
	var ids []string
	for k, d := range c.docs {
		vis, err := c.visible(ctx, d)
		if err != nil {
			return nil, err
		}
		if vis && Match(d.data, f) {
			ids = append(ids, k)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (c *Collection[E]) List(ctx context.Context, q repo.Query) (repo.Page[E], error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids, err := c.matching(ctx, q.Filter)
	if err != nil {
		return repo.Page[E]{}, err
	}
	if q.Desc {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = repo.DefaultLimit
	}
	if limit > repo.MaxLimit {
		limit = repo.MaxLimit
	}
	var page repo.Page[E]
	for _, k := range ids {
		if q.After != "" {
			if (!q.Desc && k <= q.After) || (q.Desc && k >= q.After) {
				continue
			}
		}
		if len(page.Items) == limit {
			page.Next = meta(page.Items[len(page.Items)-1]).ID
			break
		}
		v, err := decode[E](c.docs[k])
		if err != nil {
			return repo.Page[E]{}, err
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}

func (c *Collection[E]) Count(ctx context.Context, f repo.Filter) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids, err := c.matching(ctx, f)
	return int64(len(ids)), err
}

func (c *Collection[E]) checkUnique(d *doc, selfID string) error {
	for _, ix := range c.indexes {
		if !ix.Unique {
			continue
		}
		for k, other := range c.docs {
			if k == selfID || other.tenant != d.tenant {
				continue
			}
			same := true
			for _, f := range ix.Fields {
				a, _ := lookup(d.data, f)
				b, _ := lookup(other.data, f)
				if !equal(a, b) {
					same = false
					break
				}
			}
			if same {
				return errs.Conflict("%s: duplicate %s", c.name, strings.Join(ix.Fields, "+"))
			}
		}
	}
	return nil
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
	d, err := encode(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.docs[m.ID]; ok {
		return errs.Conflict("%s %s already exists", c.name, m.ID)
	}
	if err := c.checkUnique(d, m.ID); err != nil {
		return err
	}
	c.docs[m.ID] = d
	return nil
}

func (c *Collection[E]) Replace(ctx context.Context, v *E) error {
	m := meta(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.docs[m.ID]
	if !ok {
		return errs.NotFound(c.name, m.ID)
	}
	vis, err := c.visible(ctx, cur)
	if err != nil {
		return err
	}
	if !vis || cur.tenant != m.TenantID {
		return errs.NotFound(c.name, m.ID)
	}
	stored, _ := cur.data["version"].(float64)
	if int64(stored) != m.Version {
		return errs.New(errs.CodeVersionConflict, "%s %s: version %d, have %d", c.name, m.ID, int64(stored), m.Version)
	}
	m.Version++
	m.UpdatedAt = c.clk.Now()
	d, err := encode(v)
	if err != nil {
		m.Version--
		return err
	}
	if err := c.checkUnique(d, m.ID); err != nil {
		m.Version--
		return err
	}
	c.docs[m.ID] = d
	return nil
}

// DeleteIf deletes id only if its stored version is version; the caller that
// wins may consume the record (e.g. claim a draft for publishing).
func (c *Collection[E]) DeleteIf(ctx context.Context, idv string, version int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[idv]
	if !ok {
		return errs.NotFound(c.name, idv)
	}
	vis, err := c.visible(ctx, d)
	if err != nil {
		return err
	}
	if !vis {
		return errs.NotFound(c.name, idv)
	}
	stored, _ := d.data["version"].(float64)
	if int64(stored) != version {
		return errs.New(errs.CodeVersionConflict, "%s %s: version %d is stale", c.name, idv, version)
	}
	delete(c.docs, idv)
	return nil
}

func (c *Collection[E]) Delete(ctx context.Context, idv string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[idv]
	if !ok {
		return errs.NotFound(c.name, idv)
	}
	vis, err := c.visible(ctx, d)
	if err != nil {
		return err
	}
	if !vis {
		return errs.NotFound(c.name, idv)
	}
	delete(c.docs, idv)
	return nil
}

// NewStore builds a Store with every collection in memory.
func NewStore(clk clock.Clock) *repo.Store {
	s := repo.Specs
	return &repo.Store{
		Tenants:       NewCollection[types.Tenant](s["tenants"], id.Tenant, clk),
		APIKeys:       NewCollection[types.APIKey](s["api_keys"], id.APIKey, clk),
		Sessions:      NewCollection[types.Session](s["sessions"], id.Session, clk),
		Messages:      NewCollection[types.Message](s["messages"], id.Message, clk),
		Turns:         NewCollection[types.Turn](s["turns"], id.Turn, clk),
		Pendings:      NewCollection[types.Pending](s["pendings"], id.Pending, clk),
		ToolCalls:     NewCollection[types.ToolCall](s["tool_calls"], id.ToolCall, clk),
		Compactions:   NewCollection[types.Compaction](s["compactions"], id.Compaction, clk),
		Artifacts:     NewCollection[types.Artifact](s["artifacts"], id.Artifact, clk),
		LLMCalls:      NewCollection[types.LLMCall](s["llm_calls"], id.LLMCall, clk),
		Roles:         NewCollection[types.Role](s["roles"], id.Role, clk),
		Skills:        NewCollection[types.Skill](s["skills"], id.Skill, clk),
		MemorySpaces:  NewCollection[types.MemorySpace](s["memory_spaces"], id.Memory, clk),
		MemoryEntries: NewCollection[types.MemoryEntry](s["memory_entries"], id.Memory, clk),
		MCPServers:    NewCollection[types.MCPServer](s["mcp_servers"], id.MCPServer, clk),
		Retrievals:    NewCollection[types.RetrievalSource](s["retrieval_sources"], id.Retrieval, clk),
		RiskOverrides: NewCollection[types.RiskOverride](s["risk_overrides"], id.RiskOverride, clk),
		FeishuChats:   NewCollection[types.FeishuChat](s["feishu_chats"], id.FeishuChat, clk),
		FeishuThreads: NewCollection[types.FeishuThread](s["feishu_threads"], id.FeishuThread, clk),
		Audit:         NewCollection[types.AuditLog](s["audit_logs"], id.Audit, clk),
		WorkflowDefs:  NewCollection[types.WorkflowDef](s["workflow_defs"], id.Workflow, clk),
		Runs:          NewCollection[types.WorkflowRun](s["workflow_runs"], id.Run, clk),
		StepRuns:      NewCollection[types.StepRun](s["step_runs"], id.StepRun, clk),
		Schedules:     NewCollection[types.Schedule](s["schedules"], id.Schedule, clk),
		ScheduleRuns:  NewCollection[types.ScheduleRun](s["schedule_runs"], id.ScheduleRun, clk),
	}
}
