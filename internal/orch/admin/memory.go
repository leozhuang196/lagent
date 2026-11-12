package admin

import (
	"context"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// Memory administration (§9.3). Read-only spaces are maintained here by
// admins; writable spaces are what the remember tool may write, and their
// entries carry the turn that wrote them (SourceTurnID).

// SpaceInput creates or changes a memory space. Pointers mean "unchanged" on update.
type SpaceInput struct {
	Name     string             `json:"name,omitempty"`
	Scope    *types.MemoryScope `json:"scope,omitempty"`
	OwnerID  *string            `json:"owner_id,omitempty"`
	ReadOnly *bool              `json:"read_only,omitempty"`
	Priority *int               `json:"priority,omitempty"`
}

// SpaceView is a space with its entry count.
type SpaceView struct {
	*types.MemorySpace
	Entries int64 `json:"entries"`
}

func validSpace(sp *types.MemorySpace) error {
	if err := validKey(sp.Name); err != nil {
		return errs.Invalid("space name %q must be 1-64 lowercase letters, digits, '-', '_' or '.'", sp.Name)
	}
	switch sp.Scope {
	case types.MemoryTenant:
		sp.OwnerID = ""
	case types.MemoryUser:
		if sp.OwnerID == "" {
			return errs.Invalid("user-scoped spaces need owner_id")
		}
	default:
		return errs.Invalid("scope must be tenant or user")
	}
	if sp.Priority < -1000 || sp.Priority > 1000 {
		return errs.Invalid("priority must be -1000..1000")
	}
	return nil
}

// ListSpaces lists the tenant's memory spaces.
func (s *Service) ListSpaces(ctx context.Context) ([]SpaceView, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	sps, err := repo.All(ctx, s.Store.MemorySpaces, repo.Query{}, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]SpaceView, 0, len(sps))
	for _, sp := range sps {
		n, _ := s.Store.MemoryEntries.Count(ctx, repo.Filter{"space": sp.Name})
		out = append(out, SpaceView{MemorySpace: sp, Entries: n})
	}
	return out, nil
}

// GetSpace returns one space by name.
func (s *Service) GetSpace(ctx context.Context, name string) (*SpaceView, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	sp, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": name})
	if err != nil {
		return nil, err
	}
	n, _ := s.Store.MemoryEntries.Count(ctx, repo.Filter{"space": name})
	return &SpaceView{MemorySpace: sp, Entries: n}, nil
}

// CreateSpace creates a space; names are unique per tenant (unique index).
func (s *Service) CreateSpace(ctx context.Context, in SpaceInput) (*types.MemorySpace, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	sp := &types.MemorySpace{Name: in.Name, Scope: types.MemoryTenant}
	applySpace(sp, in)
	if err := validSpace(sp); err != nil {
		return nil, err
	}
	if err := s.Store.MemorySpaces.Insert(ctx, sp); err != nil {
		if errs.Is(err, errs.CodeConflict) {
			return nil, errs.Conflict("memory space %q already exists", sp.Name)
		}
		return nil, err
	}
	s.audit(ctx, "memory.space.create", "memory:"+sp.Name, map[string]any{"diff": Diff(nil, sp)})
	return sp, nil
}

func applySpace(sp *types.MemorySpace, in SpaceInput) {
	if in.Scope != nil {
		sp.Scope = *in.Scope
	}
	if in.OwnerID != nil {
		sp.OwnerID = *in.OwnerID
	}
	if in.ReadOnly != nil {
		sp.ReadOnly = *in.ReadOnly
	}
	if in.Priority != nil {
		sp.Priority = *in.Priority
	}
}

// UpdateSpace changes scope, owner, read-only flag or priority. The name is
// the identity sessions and roles refer to, so it cannot change.
func (s *Service) UpdateSpace(ctx context.Context, name string, in SpaceInput) (*types.MemorySpace, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if in.Name != "" && in.Name != name {
		return nil, errs.Invalid("a space cannot be renamed")
	}
	cur, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": name})
	if err != nil {
		return nil, err
	}
	before := *cur
	sp, err := repo.Update(ctx, s.Store.MemorySpaces, cur.ID, func(x *types.MemorySpace) error {
		applySpace(x, in)
		return validSpace(x)
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "memory.space.update", "memory:"+name, map[string]any{"diff": Diff(&before, sp)})
	return sp, nil
}

// DeleteSpace removes an empty space. Entries must be deleted first so a
// slip of the hand cannot drop a team's accumulated memory.
func (s *Service) DeleteSpace(ctx context.Context, name string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	cur, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": name})
	if err != nil {
		return err
	}
	if n, err := s.Store.MemoryEntries.Count(ctx, repo.Filter{"space": name}); err != nil {
		return err
	} else if n > 0 {
		return errs.Conflict("memory space %q still has %d entries", name, n)
	}
	if err := s.Store.MemorySpaces.Delete(ctx, cur.ID); err != nil {
		return err
	}
	s.audit(ctx, "memory.space.delete", "memory:"+name, map[string]any{"diff": Diff(cur, nil)})
	return nil
}

// EntryQuery pages entries of a space, optionally by key prefix.
type EntryQuery struct {
	Prefix string
	After  string
	Limit  int
}

// ListEntries pages a space's entries ordered by id. Prefix search uses a
// range on key, the only portable "starts with" across backends (P5).
func (s *Service) ListEntries(ctx context.Context, space string, q EntryQuery) (repo.Page[types.MemoryEntry], error) {
	if err := requireAdmin(ctx); err != nil {
		return repo.Page[types.MemoryEntry]{}, err
	}
	if _, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": space}); err != nil {
		return repo.Page[types.MemoryEntry]{}, err
	}
	f := repo.Filter{"space": space}
	if q.Prefix != "" {
		// Two range ops on one field cannot share a Filter key; the upper
		// bound is applied after loading the page instead.
		f["key"] = repo.Gte(q.Prefix)
	}
	limit := q.Limit
	if limit <= 0 || limit > repo.MaxLimit {
		limit = repo.DefaultLimit
	}
	if q.Prefix == "" {
		return s.Store.MemoryEntries.List(ctx, repo.Query{Filter: f, After: q.After, Limit: limit})
	}
	// Filter by prefix while keeping cursor semantics: scan pages until the
	// requested number of matches is found.
	out := repo.Page[types.MemoryEntry]{Items: []*types.MemoryEntry{}}
	after := q.After
	for {
		p, err := s.Store.MemoryEntries.List(ctx, repo.Query{Filter: f, After: after, Limit: repo.MaxLimit})
		if err != nil {
			return out, err
		}
		for _, e := range p.Items {
			if strings.HasPrefix(e.Key, q.Prefix) {
				out.Items = append(out.Items, e)
				if len(out.Items) == limit {
					out.Next = e.ID
					return out, nil
				}
			}
		}
		if p.Next == "" {
			return out, nil
		}
		after = p.Next
	}
}

// GetEntry returns one entry.
func (s *Service) GetEntry(ctx context.Context, space, key string) (*types.MemoryEntry, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.Store.MemoryEntries.FindOne(ctx, repo.Filter{"space": space, "key": key})
}

// EntryInput writes an entry.
type EntryInput struct {
	Key      string `json:"key,omitempty"`
	Content  string `json:"content"`
	Priority int    `json:"priority,omitempty"`
}

// PutEntry creates or replaces an entry. Admins may write read-only spaces:
// those are exactly the ones admins maintain. An admin write clears the
// source turn, since the content no longer comes from that turn.
func (s *Service) PutEntry(ctx context.Context, space string, in EntryInput) (*types.MemoryEntry, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Key) == "" || len(in.Key) > MaxMemoryKeyChars {
		return nil, errs.Invalid("key is required (max %d characters)", MaxMemoryKeyChars)
	}
	if strings.TrimSpace(in.Content) == "" || len(in.Content) > MaxMemoryContentChar {
		return nil, errs.Invalid("content is required (max %d characters)", MaxMemoryContentChar)
	}
	if _, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": space}); err != nil {
		return nil, err
	}
	target := "memory:" + space + "/" + in.Key
	for attempt := 0; attempt < 4; attempt++ {
		cur, err := s.Store.MemoryEntries.FindOne(ctx, repo.Filter{"space": space, "key": in.Key})
		switch {
		case err == nil:
			before := *cur
			e, err := repo.Update(ctx, s.Store.MemoryEntries, cur.ID, func(x *types.MemoryEntry) error {
				x.Content, x.Priority, x.SourceTurnID = in.Content, in.Priority, ""
				return nil
			})
			if err != nil {
				return nil, err
			}
			s.audit(ctx, "memory.entry.update", target, map[string]any{"diff": Diff(&before, e)})
			return e, nil
		case errs.Is(err, errs.CodeNotFound):
			e := &types.MemoryEntry{Space: space, Key: in.Key, Content: in.Content, Priority: in.Priority}
			if err := s.Store.MemoryEntries.Insert(ctx, e); err != nil {
				if errs.Is(err, errs.CodeConflict) {
					continue
				}
				return nil, err
			}
			s.audit(ctx, "memory.entry.create", target, map[string]any{"diff": Diff(nil, e)})
			return e, nil
		default:
			return nil, err
		}
	}
	return nil, errs.New(errs.CodeUnavailable, "too much contention on %s", target)
}

// DeleteEntry removes an entry.
func (s *Service) DeleteEntry(ctx context.Context, space, key string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	cur, err := s.Store.MemoryEntries.FindOne(ctx, repo.Filter{"space": space, "key": key})
	if err != nil {
		return err
	}
	if err := s.Store.MemoryEntries.Delete(ctx, cur.ID); err != nil {
		return err
	}
	s.audit(ctx, "memory.entry.delete", "memory:"+space+"/"+key, map[string]any{"diff": Diff(cur, nil)})
	return nil
}
