package admin

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// Layers of a versioned asset. Built-in assets live in the _system tenant and
// are read-only; a tenant overrides a built-in key by publishing the same key
// in its own layer (§9.3).
const (
	LayerTenant  = "tenant"
	LayerBuiltin = "builtin"
)

// Assets is the release process of one kind of versioned asset (§9.3): edit a
// draft, test it in debug sessions, publish it as version n+1, roll back by
// publishing old content as a new version, disable instead of delete. History
// is never rewritten, and every write is audited with a before/after diff.
type Assets[E any] struct {
	s          *Service
	kind       string
	c          repo.Collection[E]
	validate   func(ctx context.Context, v *E) error
	memberRead bool
}

// Roles returns the role release use cases. Members may read roles (to pick one).
func (s *Service) Roles() *Assets[types.Role] {
	return &Assets[types.Role]{s: s, kind: "role", c: s.Store.Roles, validate: s.validateRole, memberRead: true}
}

// Skills returns the skill release use cases. Members may read skills.
func (s *Service) Skills() *Assets[types.Skill] {
	return &Assets[types.Skill]{s: s, kind: "skill", c: s.Store.Skills, validate: s.validateSkill, memberRead: true}
}

// WorkflowDefs returns the workflow template release use cases (admins only).
func (s *Service) WorkflowDefs() *Assets[types.WorkflowDef] {
	return &Assets[types.WorkflowDef]{s: s, kind: "workflow_def", c: s.Store.WorkflowDefs, validate: s.validateWorkflow}
}

// Kind names the asset type ("role", "skill", "workflow_def").
func (a *Assets[E]) Kind() string { return a.kind }

func vOf[E any](v *E) *types.Versioned { return any(v).(types.VersionedEntity).V() }
func mOf[E any](v *E) *types.Meta      { return any(v).(types.VersionedEntity).M() }

// Summary is one key in a list.
type Summary[E any] struct {
	Key string `json:"key"`
	// Layer holds the effective (latest published) version; a key that only
	// has a tenant draft reports "tenant".
	Layer string `json:"layer"`
	// Builtin reports that the key exists in the built-in layer; Overridden
	// that the tenant published its own versions of it.
	Builtin    bool `json:"builtin"`
	Overridden bool `json:"overridden"`
	Latest     *E   `json:"latest,omitempty"`
	LatestVer  int  `json:"latest_ver"`
	HasDraft   bool `json:"has_draft"`
	Versions   int  `json:"versions"`
}

// Version is one stored version and the layer it lives in.
type Version[E any] struct {
	Layer string `json:"layer"`
	Asset *E     `json:"asset"`
}

// Detail is a key with its draft and every version of both layers.
type Detail[E any] struct {
	Key          string       `json:"key"`
	Builtin      bool         `json:"builtin"`
	Effective    *Version[E]  `json:"effective,omitempty"`
	Draft        *E           `json:"draft,omitempty"`
	Versions     []Version[E] `json:"versions"`
	EffectiveVer int          `json:"effective_ver"`
}

func (a *Assets[E]) canRead(ctx context.Context) (tenant.Principal, error) {
	p, err := requireMember(ctx)
	if err != nil {
		return p, err
	}
	if !a.memberRead && !p.IsAdmin() && p.Kind != tenant.KindSystem {
		return p, errs.New(errs.CodeForbidden, "tenant admin only")
	}
	return p, nil
}

// canWrite also refuses the built-in tenant itself: built-ins change only
// through seeds shipped with the binary.
func (a *Assets[E]) canWrite(ctx context.Context) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	if t, _ := tenant.ID(ctx); t == tenant.System {
		return errs.New(errs.CodeForbidden, "built-in assets are read-only")
	}
	return nil
}

func (a *Assets[E]) layers(ctx context.Context) ([]*E, []*E, error) {
	own, err := repo.All(ctx, a.c, repo.Query{}, 0)
	if err != nil {
		return nil, nil, err
	}
	if t, _ := tenant.ID(ctx); t == tenant.System {
		return nil, own, nil
	}
	sys, err := repo.All(repo.SystemLayer(ctx), a.c, repo.Query{}, 0)
	if err != nil {
		return nil, nil, err
	}
	return own, sys, nil
}

func (a *Assets[E]) keyLayers(ctx context.Context, key string) ([]*E, []*E, error) {
	f := repo.Filter{"key": key}
	own, err := repo.All(ctx, a.c, repo.Query{Filter: f}, 0)
	if err != nil {
		return nil, nil, err
	}
	sys, err := repo.All(repo.SystemLayer(ctx), a.c, repo.Query{Filter: f}, 0)
	if err != nil {
		return nil, nil, err
	}
	if t, _ := tenant.ID(ctx); t == tenant.System {
		own = nil
	}
	return own, sys, nil
}

func latestPublished[E any](items []*E) *E {
	var best *E
	for _, it := range items {
		v := vOf(it)
		if v.Status == types.AssetPublished && (best == nil || v.Ver > vOf(best).Ver) {
			best = it
		}
	}
	return best
}

// List returns every key of the tenant and built-in layers with its effective
// latest published version. Members only see keys that have one.
func (a *Assets[E]) List(ctx context.Context) ([]Summary[E], error) {
	p, err := a.canRead(ctx)
	if err != nil {
		return nil, err
	}
	own, sys, err := a.layers(ctx)
	if err != nil {
		return nil, err
	}
	group := func(items []*E) map[string][]*E {
		m := map[string][]*E{}
		for _, it := range items {
			k := vOf(it).Key
			m[k] = append(m[k], it)
		}
		return m
	}
	byOwn, bySys := group(own), group(sys)
	keys := map[string]bool{}
	for k := range byOwn {
		keys[k] = true
	}
	for k := range bySys {
		keys[k] = true
	}
	out := []Summary[E]{}
	for k := range keys {
		sm := Summary[E]{Key: k, Builtin: len(bySys[k]) > 0, Layer: LayerTenant}
		for _, it := range byOwn[k] {
			if vOf(it).Ver == 0 {
				sm.HasDraft = true
			} else {
				sm.Versions++
			}
		}
		sm.Versions += len(bySys[k])
		if l := latestPublished(byOwn[k]); l != nil {
			sm.Latest, sm.Overridden = l, sm.Builtin
		} else if l := latestPublished(bySys[k]); l != nil {
			sm.Latest, sm.Layer = l, LayerBuiltin
		}
		if sm.Latest != nil {
			sm.LatestVer = vOf(sm.Latest).Ver
		}
		if !p.IsAdmin() && p.Kind != tenant.KindSystem {
			if sm.Latest == nil {
				continue
			}
			sm.HasDraft = false
		}
		out = append(out, sm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Get returns a key with all versions of both layers, newest first.
func (a *Assets[E]) Get(ctx context.Context, key string) (*Detail[E], error) {
	p, err := a.canRead(ctx)
	if err != nil {
		return nil, err
	}
	own, sys, err := a.keyLayers(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(own) == 0 && len(sys) == 0 {
		return nil, errs.NotFound(a.kind, key)
	}
	d := &Detail[E]{Key: key, Builtin: len(sys) > 0, Versions: []Version[E]{}}
	admin := p.IsAdmin() || p.Kind == tenant.KindSystem
	for _, it := range own {
		if vOf(it).Ver == 0 {
			if admin {
				d.Draft = it
			}
			continue
		}
		d.Versions = append(d.Versions, Version[E]{Layer: LayerTenant, Asset: it})
	}
	for _, it := range sys {
		d.Versions = append(d.Versions, Version[E]{Layer: LayerBuiltin, Asset: it})
	}
	sort.SliceStable(d.Versions, func(i, j int) bool { return vOf(d.Versions[i].Asset).Ver > vOf(d.Versions[j].Asset).Ver })
	if l := latestPublished(own); l != nil {
		d.Effective = &Version[E]{Layer: LayerTenant, Asset: l}
	} else if l := latestPublished(sys); l != nil {
		d.Effective = &Version[E]{Layer: LayerBuiltin, Asset: l}
	}
	if d.Effective != nil {
		d.EffectiveVer = vOf(d.Effective.Asset).Ver
	}
	if !admin && d.Effective == nil {
		return nil, errs.NotFound(a.kind, key)
	}
	return d, nil
}

// GetVersion returns one version (0 = the tenant's draft), tenant layer
// first, like the engine resolves it.
func (a *Assets[E]) GetVersion(ctx context.Context, key string, ver int) (*Version[E], error) {
	p, err := a.canRead(ctx)
	if err != nil {
		return nil, err
	}
	if ver == 0 && !p.IsAdmin() && p.Kind != tenant.KindSystem {
		return nil, errs.New(errs.CodeForbidden, "drafts are visible to tenant admins only")
	}
	f := repo.Filter{"key": key, "ver": ver}
	if t, _ := tenant.ID(ctx); t != tenant.System {
		v, err := a.c.FindOne(ctx, f)
		if err == nil {
			return &Version[E]{Layer: LayerTenant, Asset: v}, nil
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return nil, err
		}
	}
	v, err := a.c.FindOne(repo.SystemLayer(ctx), f)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return nil, errs.NotFound(a.kind, key)
		}
		return nil, err
	}
	return &Version[E]{Layer: LayerBuiltin, Asset: v}, nil
}

func (a *Assets[E]) draft(ctx context.Context, key string) (*E, error) {
	return a.c.FindOne(ctx, repo.Filter{"key": key, "ver": 0})
}

// effective is what sessions get today, for "before" in audit diffs.
func (a *Assets[E]) effective(ctx context.Context, key string) *E {
	v, err := repo.LatestPublished(ctx, a.c, key)
	if err != nil {
		return nil
	}
	return v
}

// SaveDraft validates content and stores it as the tenant's draft of key
// (Ver 0). Saving a draft of a built-in key creates a tenant-layer draft: the
// built-in stays untouched and is overridden only once the draft is published.
func (a *Assets[E]) SaveDraft(ctx context.Context, key string, content *E) (*E, error) {
	if err := a.canWrite(ctx); err != nil {
		return nil, err
	}
	if err := validKey(key); err != nil {
		return nil, err
	}
	if content == nil {
		return nil, errs.Invalid("content is required")
	}
	*vOf(content) = types.Versioned{Key: key, Ver: 0, Status: types.AssetDraft}
	*mOf(content) = types.Meta{}
	if err := a.validate(ctx, content); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		cur, err := a.draft(ctx, key)
		switch {
		case err == nil:
			before := *cur
			saved, err := repo.Update(ctx, a.c, mOf(cur).ID, func(x *E) error {
				meta := *mOf(x)
				*x = *content
				*mOf(x) = meta
				return nil
			})
			if errs.Is(err, errs.CodeNotFound) {
				continue // published or deleted between the read and the write: create it
			}
			if err != nil {
				return nil, err
			}
			a.auditChange(ctx, "draft.save", key, &before, saved, nil)
			return saved, nil
		case errs.Is(err, errs.CodeNotFound):
			fresh := *content
			if err := a.c.Insert(ctx, &fresh); err != nil {
				if errs.Is(err, errs.CodeConflict) {
					continue // another admin created the draft meanwhile: update it
				}
				return nil, err
			}
			a.auditChange(ctx, "draft.save", key, a.effective(ctx, key), &fresh, nil)
			return &fresh, nil
		default:
			return nil, err
		}
	}
	return nil, errs.New(errs.CodeUnavailable, "too much contention on draft %s", key)
}

// DeleteDraft discards the tenant's draft of key.
func (a *Assets[E]) DeleteDraft(ctx context.Context, key string) error {
	if err := a.canWrite(ctx); err != nil {
		return err
	}
	cur, err := a.draft(ctx, key)
	if err != nil {
		return err
	}
	if err := a.c.Delete(ctx, mOf(cur).ID); err != nil {
		return err
	}
	a.auditChange(ctx, "draft.delete", key, cur, nil, nil)
	return nil
}

// nextVer is one more than the highest version of key in either layer, so a
// tenant override never reuses a built-in version number: sessions pin
// (key, ver) and the engine resolves it tenant layer first (repo.Version).
func (a *Assets[E]) nextVer(ctx context.Context, key string) (int, error) {
	own, sys, err := a.keyLayers(ctx, key)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, it := range append(own, sys...) {
		if v := vOf(it).Ver; v > max {
			max = v
		}
	}
	return max + 1, nil
}

// insertVersion stores content as a new published version. The unique
// (tenant, key, ver) index makes concurrent publishers collide on the number,
// never on the content: the loser recomputes and takes the next number.
func (a *Assets[E]) insertVersion(ctx context.Context, key string, content E, note string) (*E, error) {
	p, _ := tenant.From(ctx)
	now := a.s.now()
	for attempt := 0; attempt < 16; attempt++ {
		n, err := a.nextVer(ctx, key)
		if err != nil {
			return nil, err
		}
		v := content
		*mOf(&v) = types.Meta{}
		*vOf(&v) = types.Versioned{Key: key, Ver: n, Status: types.AssetPublished, PublishedAt: &now, PublishedBy: &p, Note: note}
		err = a.c.Insert(ctx, &v)
		if err == nil {
			return &v, nil
		}
		if !errs.Is(err, errs.CodeConflict) {
			return nil, err
		}
	}
	return nil, errs.New(errs.CodeUnavailable, "too much contention publishing %s", key)
}

// Publish turns the tenant's draft into version n+1. The draft is consumed by
// a conditional delete before the version is written: only the caller holding
// the version it read deletes it, so two admins publishing the same draft at
// once get one new version and one clean conflict, never two copies. (A
// conditional write would not do: Replace bumps the version, so the loser
// would still match and could publish the same content again.)
func (a *Assets[E]) Publish(ctx context.Context, key, note string) (*E, error) {
	if err := a.canWrite(ctx); err != nil {
		return nil, err
	}
	d, err := a.draft(ctx, key)
	if errs.Is(err, errs.CodeNotFound) {
		return nil, errs.Conflict("%s %q has no draft to publish", a.kind, key)
	}
	if err != nil {
		return nil, err
	}
	// Tools may have been removed since the draft was saved: validate again.
	if err := a.validate(ctx, d); err != nil {
		return nil, err
	}
	before := a.effective(ctx, key)
	if err := a.c.DeleteIf(ctx, mOf(d).ID, mOf(d).Version); err != nil {
		if errs.Is(err, errs.CodeVersionConflict) || errs.Is(err, errs.CodeNotFound) {
			return nil, errs.Conflict("the draft of %s %q changed or was published concurrently; reload and retry", a.kind, key)
		}
		return nil, err
	}
	v, err := a.insertVersion(ctx, key, *d, note)
	if err != nil {
		// The draft is gone; put it back so the admin can retry rather than
		// having to retype the content.
		restore := *d
		*mOf(&restore) = types.Meta{}
		if rerr := a.c.Insert(ctx, &restore); rerr != nil && a.s.Log != nil {
			a.s.Log.Warn("publish: could not restore draft", "kind", a.kind, "key", key, "err", rerr)
		}
		return nil, err
	}
	a.auditChange(ctx, "publish", key, before, v, map[string]any{"ver": vOf(v).Ver, "note": note})
	return v, nil
}

// Rollback publishes the content of an old version (either layer) as a new
// version. Content is not re-validated: it was valid when published, and a
// rollback is the emergency path (roles only narrow, so a tool that vanished
// since is simply absent at assembly).
func (a *Assets[E]) Rollback(ctx context.Context, key string, ver int, note string) (*E, error) {
	if err := a.canWrite(ctx); err != nil {
		return nil, err
	}
	if ver < 1 {
		return nil, errs.Invalid("rollback needs a published version (>= 1)")
	}
	src, err := a.GetVersion(ctx, key, ver)
	if err != nil {
		return nil, err
	}
	before := a.effective(ctx, key)
	if note == "" {
		note = "rollback"
	}
	note = strings.TrimSpace(note) + " (content of v" + strconv.Itoa(ver) + ")"
	v, err := a.insertVersion(ctx, key, *src.Asset, note)
	if err != nil {
		return nil, err
	}
	a.auditChange(ctx, "rollback", key, before, v, map[string]any{"ver": vOf(v).Ver, "from_ver": ver, "from_layer": src.Layer, "note": note})
	return v, nil
}

// SetDisabled disables or re-enables a published tenant-layer version.
// Sessions pinned to a disabled version keep running (§9.17); new sessions
// cannot pick it, and "latest" falls back to the newest enabled version.
func (a *Assets[E]) SetDisabled(ctx context.Context, key string, ver int, disabled bool) (*E, error) {
	if err := a.canWrite(ctx); err != nil {
		return nil, err
	}
	if ver < 1 {
		return nil, errs.Invalid("only published versions can be disabled; delete the draft instead")
	}
	cur, err := a.c.FindOne(ctx, repo.Filter{"key": key, "ver": ver})
	if errs.Is(err, errs.CodeNotFound) {
		if _, serr := a.c.FindOne(repo.SystemLayer(ctx), repo.Filter{"key": key, "ver": ver}); serr == nil {
			return nil, errs.New(errs.CodeForbidden, "built-in versions are read-only; publish an override of %q instead", key)
		}
		return nil, errs.NotFound(a.kind, key)
	}
	if err != nil {
		return nil, err
	}
	want, from := types.AssetDisabled, types.AssetPublished
	if !disabled {
		want, from = types.AssetPublished, types.AssetDisabled
	}
	changed := false
	v, err := repo.Update(ctx, a.c, mOf(cur).ID, func(x *E) error {
		switch vOf(x).Status {
		case want:
			return errs.New(errs.CodeAlreadyResolved, "already %s", want)
		case from:
			vOf(x).Status = want
			changed = true
			return nil
		}
		return errs.Conflict("version is %s", vOf(x).Status)
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return v, nil // idempotent: the requested state already holds
	}
	if err != nil {
		return nil, err
	}
	if changed {
		action := "enable"
		if disabled {
			action = "disable"
		}
		a.s.audit(ctx, a.kind+"."+action, a.kind+":"+key, map[string]any{"ver": ver, "diff": []Change{{Path: "status", Before: string(from), After: string(want)}}})
	}
	return v, nil
}

// DiffVersions compares two versions (0 = draft) field by field.
func (a *Assets[E]) DiffVersions(ctx context.Context, key string, va, vb int) ([]Change, error) {
	x, err := a.GetVersion(ctx, key, va)
	if err != nil {
		return nil, err
	}
	y, err := a.GetVersion(ctx, key, vb)
	if err != nil {
		return nil, err
	}
	return Diff(x.Asset, y.Asset), nil
}

func (a *Assets[E]) auditChange(ctx context.Context, action, key string, before, after *E, extra map[string]any) {
	d := map[string]any{"diff": Diff(before, after)}
	for k, v := range extra {
		d[k] = v
	}
	a.s.audit(ctx, a.kind+"."+action, a.kind+":"+key, d)
}
