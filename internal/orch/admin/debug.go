package admin

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/rules/assembly"
)

// The turn debugger (§12): what one turn did, what a role would assemble,
// and debug sessions to try drafts and alternative continuations. Debug
// sessions always ask before any non-read-only call and are excluded from
// business statistics.

// TimelineEntry is one row of a turn timeline, in time order.
type TimelineEntry struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"` // llm_call, tool_call, pending, event
	ID         string    `json:"id,omitempty"`
	Title      string    `json:"title"`
	Status     string    `json:"status,omitempty"`
	Decision   string    `json:"decision,omitempty"` // permission decision of a tool call / resolution of a pending
	DurationMs int64     `json:"duration_ms,omitempty"`
}

// TimelineTotals sums a turn.
type TimelineTotals struct {
	Iterations   int            `json:"iterations"`
	Usage        types.Usage    `json:"usage"`
	Tokens       int64          `json:"tokens"`
	LLMCalls     int            `json:"llm_calls"`
	LLMLatencyMs int64          `json:"llm_latency_ms"`
	ToolCalls    int            `json:"tool_calls"`
	ToolStatus   map[string]int `json:"tool_status"`
	Pendings     int            `json:"pendings"`
	DurationMs   int64          `json:"duration_ms,omitempty"`
}

// Timeline is everything recorded about one turn. LLM calls are listed
// without request/response bodies; ListLLMCalls returns those.
type Timeline struct {
	Turn      *types.Turn       `json:"turn"`
	Entries   []TimelineEntry   `json:"entries"`
	LLMCalls  []*types.LLMCall  `json:"llm_calls"`
	ToolCalls []*types.ToolCall `json:"tool_calls"`
	Pendings  []*types.Pending  `json:"pendings"`
	Events    []types.Event     `json:"events"`
	Totals    TimelineTotals    `json:"totals"`
}

// TurnTimeline assembles a turn's model calls, ledger entries (with their
// permission decisions), pending items and events (without text deltas).
func (s *Service) TurnTimeline(ctx context.Context, turnID string) (*Timeline, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.timeline(ctx, turnID)
}

func (s *Service) timeline(ctx context.Context, turnID string) (*Timeline, error) {
	t, err := s.Store.Turns.Get(ctx, turnID)
	if err != nil {
		return nil, err
	}
	calls, err := s.llmCalls(ctx, turnID)
	if err != nil {
		return nil, err
	}
	tcs, err := repo.All(ctx, s.Store.ToolCalls, repo.Query{Filter: repo.Filter{"turn_id": turnID}}, 5000)
	if err != nil {
		return nil, err
	}
	pends, err := repo.All(ctx, s.Store.Pendings, repo.Query{Filter: repo.Filter{"turn_id": turnID}}, 1000)
	if err != nil {
		return nil, err
	}
	tl := &Timeline{Turn: t, Entries: []TimelineEntry{}, LLMCalls: []*types.LLMCall{}, ToolCalls: tcs, Pendings: pends, Events: []types.Event{}}
	tl.Totals = TimelineTotals{Iterations: t.Iterations, Usage: t.Usage, Tokens: t.Usage.Total(), ToolStatus: map[string]int{}}
	if t.StartedAt != nil && t.EndedAt != nil {
		tl.Totals.DurationMs = t.EndedAt.Sub(*t.StartedAt).Milliseconds()
	}
	for _, c := range calls {
		light := *c
		light.Request, light.Response = nil, nil
		tl.LLMCalls = append(tl.LLMCalls, &light)
		status := "ok"
		if c.Error != "" {
			status = "error"
		}
		tl.Entries = append(tl.Entries, TimelineEntry{
			At: c.CreatedAt.Add(-time.Duration(c.LatencyMs) * time.Millisecond), Kind: "llm_call", ID: c.ID,
			Title: "model call #" + strconv.Itoa(c.Iteration) + " (" + c.Model + ")", Status: status, DurationMs: c.LatencyMs,
		})
		tl.Totals.LLMCalls++
		tl.Totals.LLMLatencyMs += c.LatencyMs
	}
	for _, c := range tcs {
		at := c.CreatedAt
		if c.StartedAt != nil {
			at = *c.StartedAt
		}
		tl.Entries = append(tl.Entries, TimelineEntry{At: at, Kind: "tool_call", ID: c.ID, Title: c.Tool, Status: string(c.Status), Decision: c.Decision, DurationMs: c.DurationMs})
		tl.Totals.ToolCalls++
		tl.Totals.ToolStatus[string(c.Status)]++
	}
	for _, p := range pends {
		e := TimelineEntry{At: p.CreatedAt, Kind: "pending", ID: p.ID, Title: string(p.Kind) + ": " + p.Title, Status: string(p.Status)}
		if p.Resolution != nil {
			e.Decision = string(p.Resolution.Decision)
		}
		tl.Entries = append(tl.Entries, e)
		tl.Totals.Pendings++
	}
	if s.Bus != nil {
		evs, _, err := s.Bus.Read(ctx, t.SessionID, 0, 20000)
		if err == nil {
			for _, ev := range evs {
				if ev.TurnID != turnID || ev.Type == types.EvMessageDelta {
					continue
				}
				tl.Events = append(tl.Events, ev)
				tl.Entries = append(tl.Entries, TimelineEntry{At: ev.TS, Kind: "event", ID: strconv.FormatInt(ev.Seq, 10), Title: string(ev.Type)})
			}
		}
	}
	sort.SliceStable(tl.Entries, func(i, j int) bool { return tl.Entries[i].At.Before(tl.Entries[j].At) })
	return tl, nil
}

func (s *Service) llmCalls(ctx context.Context, turnID string) ([]*types.LLMCall, error) {
	calls, err := repo.All(ctx, s.Store.LLMCalls, repo.Query{Filter: repo.Filter{"turn_id": turnID}}, 1000)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].Iteration < calls[j].Iteration })
	return calls, nil
}

// ListLLMCalls returns a turn's model calls with raw request and response.
// Calls are recorded for debug sessions always, otherwise only when the
// platform enables RecordLLMCalls.
func (s *Service) ListLLMCalls(ctx context.Context, turnID string) ([]*types.LLMCall, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := s.Store.Turns.Get(ctx, turnID); err != nil {
		return nil, err
	}
	calls, err := s.llmCalls(ctx, turnID)
	if calls == nil && err == nil {
		calls = []*types.LLMCall{}
	}
	return calls, err
}

// PreviewInput selects what to assemble.
type PreviewInput struct {
	Role    string `json:"role"`
	Version *int   `json:"version,omitempty"` // nil = latest published, 0 = draft
	UserID  string `json:"user_id,omitempty"`
	// MemorySpaces simulates spaces attached to the session (besides the role's).
	MemorySpaces []string `json:"memory_spaces,omitempty"`
}

// PreviewSpace is a memory space that took part, and why.
type PreviewSpace struct {
	Name    string `json:"name"`
	Source  string `json:"source"` // role, session, user
	Found   bool   `json:"found"`
	Entries int    `json:"entries"`
}

// Preview is the assembled system prompt and tool list of a role, computed
// without calling a model (§12).
type Preview struct {
	RoleKey      string           `json:"role_key"`
	RoleVer      int              `json:"role_ver"`
	RoleStatus   string           `json:"role_status"`
	Model        string           `json:"model"`
	Parts        []assembly.Part  `json:"parts"`
	Tools        []string         `json:"tools"`
	MissingTools []string         `json:"missing_tools,omitempty"` // declared names nobody registered
	Skills       []string         `json:"skills"`
	MemorySpaces []PreviewSpace   `json:"memory_spaces"`
	Truncated    []string         `json:"truncated,omitempty"`
	CharsTotal   int              `json:"chars_total"`
	ToolChars    int              `json:"tool_chars"`
	// TokensEstimate is (system chars + tool spec chars) / 4: a rough figure
	// for comparing versions, not a provider count.
	TokensEstimate int `json:"tokens_estimate"`
}

// AssemblyPreview runs the same fetch-then-pure-assembly steps as the engine
// (turn.prepare) for a role version. A user id adds that user's user-scoped
// spaces, as a session created for that user with its spaces would see.
func (s *Service) AssemblyPreview(ctx context.Context, in PreviewInput) (*Preview, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if in.Role == "" {
		return nil, errs.Invalid("role is required")
	}
	var role *types.Role
	var err error
	if in.Version == nil {
		role, err = repo.LatestPublished(ctx, s.Store.Roles, in.Role)
	} else {
		role, err = repo.Version(ctx, s.Store.Roles, in.Role, *in.Version)
	}
	if err != nil {
		return nil, err
	}
	skills, err := roleSkills(ctx, s.Store, role.Skills)
	if err != nil {
		return nil, err
	}
	pv := &Preview{RoleKey: role.Key, RoleVer: role.Ver, RoleStatus: string(role.Status), Model: role.Model, Skills: []string{}, MemorySpaces: []PreviewSpace{}}
	if pv.Model == "" {
		pv.Model = s.Info.DefaultModel
	}
	for _, sk := range skills {
		pv.Skills = append(pv.Skills, sk.Key)
	}
	sort.Strings(pv.Skills)

	type src struct{ name, source string }
	var want []src
	seen := map[string]bool{}
	add := func(n, source string) {
		if n != "" && !seen[n] {
			seen[n] = true
			want = append(want, src{n, source})
		}
	}
	for _, n := range role.MemorySpaces {
		add(n, "role")
	}
	for _, n := range in.MemorySpaces {
		add(n, "session")
	}
	if in.UserID != "" {
		ups, err := repo.All(ctx, s.Store.MemorySpaces, repo.Query{Filter: repo.Filter{"scope": string(types.MemoryUser), "owner_id": in.UserID}}, 100)
		if err != nil {
			return nil, err
		}
		for _, sp := range ups {
			add(sp.Name, "user")
		}
	}
	var spaces []types.MemorySpace
	var entries []types.MemoryEntry
	for _, w := range want {
		ps := PreviewSpace{Name: w.name, Source: w.source}
		sp, err := s.Store.MemorySpaces.FindOne(ctx, repo.Filter{"name": w.name})
		if err == nil {
			ps.Found = true
			spaces = append(spaces, *sp)
			// Same cap as the engine, so truncation matches what a turn sees.
			es, err := repo.All(ctx, s.Store.MemoryEntries, repo.Query{Filter: repo.Filter{"space": w.name}}, 500)
			if err != nil {
				return nil, err
			}
			for _, x := range es {
				entries = append(entries, *x)
			}
			ps.Entries = len(es)
		}
		pv.MemorySpaces = append(pv.MemorySpaces, ps)
	}

	var registered []types.ToolMeta
	if s.Registry != nil {
		if registered, err = s.Registry.List(ctx); err != nil {
			return nil, err
		}
	}
	out := assembly.Assemble(assembly.Input{Role: *role, Skills: skills, MemorySpaces: spaces, Memory: entries, Registered: registered})
	pv.Parts, pv.Tools, pv.Truncated, pv.CharsTotal = out.Parts, out.Tools, out.Truncated, out.CharsTotal
	if pv.Tools == nil {
		pv.Tools = []string{}
	}
	regNames := map[string]bool{}
	for _, m := range registered {
		regNames[m.Name] = true
	}
	for _, d := range role.Tools {
		if d != "*" && !regNames[d] && len(d) > 0 && !(len(d) > 4 && d[:4] == "mcp:") {
			pv.MissingTools = append(pv.MissingTools, d)
		}
	}
	if s.Registry != nil {
		tools, _, err := s.Registry.Resolve(ctx, out.Tools)
		if err != nil {
			return nil, err
		}
		for _, t := range tools {
			sp := t.Spec()
			pv.ToolChars += len(sp.Name) + len(sp.Description) + len(sp.InputSchema)
		}
	}
	pv.TokensEstimate = (pv.CharsTotal + pv.ToolChars) / 4
	return pv, nil
}

// roleSkills mirrors turn.(*run).roleSkills: the latest published skills a
// role declares; "*" means all skills of both layers, tenant versions win.
// It must stay in step with the engine or previews would lie.
func roleSkills(ctx context.Context, store *repo.Store, keys []string) ([]types.Skill, error) {
	all := false
	for _, k := range keys {
		if k == "*" {
			all = true
		}
	}
	if !all {
		var out []types.Skill
		for _, k := range keys {
			if sk, err := repo.LatestPublished(ctx, store.Skills, k); err == nil {
				out = append(out, *sk)
			}
		}
		return out, nil
	}
	best := map[string]*types.Skill{}
	for _, cx := range []context.Context{repo.SystemLayer(ctx), ctx} {
		items, err := repo.All(cx, store.Skills, repo.Query{Filter: repo.Filter{"status": string(types.AssetPublished)}}, 1000)
		if err != nil {
			return nil, err
		}
		layer := map[string]*types.Skill{}
		for _, it := range items {
			if cur, ok := layer[it.Key]; !ok || it.Ver > cur.Ver {
				layer[it.Key] = it
			}
		}
		for k, v := range layer {
			best[k] = v
		}
	}
	var out []types.Skill
	for _, v := range best {
		out = append(out, *v)
	}
	return out, nil
}

// PlaygroundInput starts a debug session on a role version.
type PlaygroundInput struct {
	Role         string   `json:"role"`
	Version      *int     `json:"version,omitempty"` // nil = latest published, 0 = draft
	MemorySpaces []string `json:"memory_spaces,omitempty"`
	Title        string   `json:"title,omitempty"`
}

// PlaygroundSession creates a debug session; messages are then submitted
// through the normal session API. Debug sessions always run in ask_all mode
// (session.Create enforces it), so a draft cannot cause unapproved effects.
func (s *Service) PlaygroundSession(ctx context.Context, in PlaygroundInput) (*types.Session, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		return nil, errs.New(errs.CodeUnavailable, "sessions are not wired")
	}
	if in.Role == "" {
		return nil, errs.Invalid("role is required")
	}
	title := in.Title
	if title == "" {
		title = "playground: " + in.Role
		if in.Version != nil {
			title += " v" + strconv.Itoa(*in.Version)
		}
	}
	sess, err := s.Sessions.Create(ctx, session.CreateInput{
		Title: title, RoleKey: in.Role, RoleVer: in.Version, MemorySpaces: in.MemorySpaces,
		Channels: []types.Channel{{Kind: types.ChannelStream}}, Origin: types.OriginDebug, Debug: true,
		Labels: map[string]string{"debug": "playground"},
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "debug.playground", sess.ID, map[string]any{"role": sess.RoleKey, "ver": sess.RoleVer})
	return sess, nil
}

// ForkInput copies a session's history into a new debug session.
type ForkInput struct {
	UptoSeq     int64 `json:"upto_seq"`
	RoleVersion *int  `json:"role_version,omitempty"` // nil = the source session's version
}

// Fork copies messages with seq <= UptoSeq into a new debug session so the
// conversation can continue there, optionally on another role version.
// Standing grants are not copied and the mode is ask_all, so side-effect
// tools always ask in the fork even if the original ran them unattended.
func (s *Service) Fork(ctx context.Context, sessionID string, in ForkInput) (*types.Session, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		return nil, errs.New(errs.CodeUnavailable, "sessions are not wired")
	}
	src, err := s.Store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if in.UptoSeq < 1 {
		return nil, errs.Invalid("upto_seq must be >= 1")
	}
	msgs, err := repo.All(ctx, s.Store.Messages, repo.Query{Filter: repo.Filter{"session_id": sessionID, "seq": repo.Lte(in.UptoSeq)}}, 0)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errs.Invalid("no messages up to seq %d", in.UptoSeq)
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	if last := msgs[len(msgs)-1]; last.Role == types.RoleAssistant && len(last.ToolUses()) > 0 {
		// The engine would execute those calls on the fork's next turn; cut
		// after the tool results (or before the call) instead.
		return nil, errs.Invalid("seq %d ends on a tool call without its result; fork at the tool result or before the call", last.Seq)
	}
	ver := in.RoleVersion
	if ver == nil {
		v := src.RoleVer
		ver = &v
	}
	labels := map[string]string{"debug": "fork", "forked_from": src.ID, "forked_upto": strconv.FormatInt(in.UptoSeq, 10)}
	fork, err := s.Sessions.Create(ctx, session.CreateInput{
		Title: "fork of " + titleOr(src), RoleKey: src.RoleKey, RoleVer: ver, MemorySpaces: src.MemorySpaces,
		Channels: []types.Channel{{Kind: types.ChannelStream}}, Origin: types.OriginDebug, Debug: true,
		ToolDeny: src.ToolDeny, Labels: labels,
	})
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		c := &types.Message{SessionID: fork.ID, TurnID: m.TurnID, Seq: m.Seq, Role: m.Role, Content: cloneBlocks(m.Content), Author: m.Author}
		c.TenantID = fork.TenantID
		if err := s.Store.Messages.Insert(ctx, c); err != nil {
			return nil, err
		}
	}
	s.audit(ctx, "debug.fork", fork.ID, map[string]any{"from": src.ID, "upto_seq": in.UptoSeq, "role": fork.RoleKey, "ver": fork.RoleVer})
	return fork, nil
}

func titleOr(s *types.Session) string {
	if s.Title != "" {
		return s.Title
	}
	return s.ID
}

func cloneBlocks(bs []types.Block) []types.Block {
	raw, _ := json.Marshal(bs)
	var out []types.Block
	_ = json.Unmarshal(raw, &out)
	return out
}

// sessionTenantCtx acts inside the tenant that owns an entity found across
// tenants, keeping the actor for audit.
func sessionTenantCtx(ctx context.Context, tenantID string) context.Context {
	p, _ := tenant.From(ctx)
	p.TenantID = tenantID
	return tenant.With(ctx, p)
}
