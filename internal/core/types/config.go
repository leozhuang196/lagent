package types

import (
	"encoding/json"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// AssetStatus is the publication state of a versioned configuration asset.
type AssetStatus string

const (
	AssetDraft     AssetStatus = "draft"
	AssetPublished AssetStatus = "published"
	AssetDisabled  AssetStatus = "disabled"
)

// Versioned is embedded in roles, skills and workflow definitions.
// A key has at most one draft (Ver 0) and any number of published versions
// (Ver 1..n). Versions are never deleted, only disabled (§9.17 resumability).
type Versioned struct {
	Key         string            `json:"key" bson:"key"`
	Ver         int               `json:"ver" bson:"ver"`
	Status      AssetStatus       `json:"status" bson:"status"`
	PublishedAt *time.Time        `json:"published_at,omitempty" bson:"published_at,omitempty"`
	PublishedBy *tenant.Principal `json:"published_by,omitempty" bson:"published_by,omitempty"`
	Note        string            `json:"note,omitempty" bson:"note,omitempty"`
}

// V returns the embedded version info.
func (v *Versioned) V() *Versioned { return v }

// VersionedEntity is an entity with versions.
type VersionedEntity interface {
	Entity
	V() *Versioned
}

// Role is an assistant role: the recipe that assembly turns into a system
// prompt and a tool list.
type Role struct {
	Meta        `bson:",inline"`
	Versioned   `bson:",inline"`
	Name        string `json:"name" bson:"name"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	Prompt      string `json:"prompt" bson:"prompt"`
	// Tools lists allowed tool names. "mcp:<server>" allows every tool of a
	// server; "*" allows every registered tool. Roles only narrow.
	Tools                 []string       `json:"tools" bson:"tools"`
	Skills                []string       `json:"skills,omitempty" bson:"skills,omitempty"`
	MemorySpaces          []string       `json:"memory_spaces,omitempty" bson:"memory_spaces,omitempty"`
	Templates             []string       `json:"templates,omitempty" bson:"templates,omitempty"`
	DefaultPermissionMode PermissionMode `json:"default_permission_mode" bson:"default_permission_mode"`
	Model                 string         `json:"model,omitempty" bson:"model,omitempty"`
	FallbackModel         string         `json:"fallback_model,omitempty" bson:"fallback_model,omitempty"`
	MaxIterations         int            `json:"max_iterations,omitempty" bson:"max_iterations,omitempty"`
	TokenBudget           int64          `json:"token_budget,omitempty" bson:"token_budget,omitempty"`
	Effort                string         `json:"effort,omitempty" bson:"effort,omitempty"`
}

// Skill is reusable instruction knowledge: name and description are always in
// context, the body is loaded on demand through load_skill.
type Skill struct {
	Meta        `bson:",inline"`
	Versioned   `bson:",inline"`
	Name        string   `json:"name" bson:"name"`
	Description string   `json:"description" bson:"description"`
	Body        string   `json:"body" bson:"body"`
	Attachments []string `json:"attachments,omitempty" bson:"attachments,omitempty"` // artifact ids
	Priority    int      `json:"priority,omitempty" bson:"priority,omitempty"`
}

// MemoryScope says who owns a memory space.
type MemoryScope string

const (
	MemoryTenant MemoryScope = "tenant"
	MemoryUser   MemoryScope = "user"
)

// MemorySpace groups memory entries.
type MemorySpace struct {
	Meta     `bson:",inline"`
	Name     string      `json:"name" bson:"name"`
	Scope    MemoryScope `json:"scope" bson:"scope"`
	OwnerID  string      `json:"owner_id,omitempty" bson:"owner_id,omitempty"`
	ReadOnly bool        `json:"read_only" bson:"read_only"` // only admins write; remember cannot
	Priority int         `json:"priority" bson:"priority"`
}

// MemoryEntry is one remembered fact.
type MemoryEntry struct {
	Meta         `bson:",inline"`
	Space        string `json:"space" bson:"space"`
	Key          string `json:"key" bson:"key"`
	Content      string `json:"content" bson:"content"`
	Priority     int    `json:"priority" bson:"priority"`
	SourceTurnID string `json:"source_turn_id,omitempty" bson:"source_turn_id,omitempty"`
}

// MCPTransport is how the platform reaches an MCP server.
type MCPTransport string

const (
	MCPStdio      MCPTransport = "stdio"
	MCPStreamable MCPTransport = "http"
	MCPSSE        MCPTransport = "sse"
)

// MCPServer is a configured MCP server.
type MCPServer struct {
	Meta      `bson:",inline"`
	Name      string            `json:"name" bson:"name"`
	Transport MCPTransport      `json:"transport" bson:"transport"`
	URL       string            `json:"url,omitempty" bson:"url,omitempty"`
	Command   []string          `json:"command,omitempty" bson:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty" bson:"env,omitempty"`
	// SecretRef points at the secret store; secrets never live in this record.
	SecretRef string `json:"secret_ref,omitempty" bson:"secret_ref,omitempty"`
	// AuthHeader is the header that carries the secret (default Authorization: Bearer).
	AuthHeader string     `json:"auth_header,omitempty" bson:"auth_header,omitempty"`
	Enabled    bool       `json:"enabled" bson:"enabled"`
	Status     string     `json:"status" bson:"status"` // ok, error, unknown
	LastError  string     `json:"last_error,omitempty" bson:"last_error,omitempty"`
	Tools      []ToolSpec `json:"tools,omitempty" bson:"tools,omitempty"`
	// RiskOverrides lowers per-tool risk below external; must be explicit.
	RiskOverrides map[string]RiskLevel `json:"risk_overrides,omitempty" bson:"risk_overrides,omitempty"`
	SyncedAt      *time.Time           `json:"synced_at,omitempty" bson:"synced_at,omitempty"`
}

// RetrievalSource is "an MCP tool + fixed arguments" under a stable name, so
// templates never mention MCP server names.
type RetrievalSource struct {
	Meta        `bson:",inline"`
	Name        string          `json:"name" bson:"name"`
	Description string          `json:"description,omitempty" bson:"description,omitempty"`
	Server      string          `json:"server" bson:"server"`
	Tool        string          `json:"tool" bson:"tool"`
	FixedArgs   json.RawMessage `json:"fixed_args,omitempty" bson:"fixed_args,omitempty"`
	QueryField  string          `json:"query_field" bson:"query_field"`
}

// RiskOverride raises (never lowers) the risk of a tool for a tenant or user.
type RiskOverride struct {
	Meta   `bson:",inline"`
	Tool   string    `json:"tool" bson:"tool"`
	Level  RiskLevel `json:"level" bson:"level"`
	UserID string    `json:"user_id,omitempty" bson:"user_id,omitempty"`
}

// Tenant is a business line or department.
type Tenant struct {
	Meta                 `bson:",inline"`
	Name                 string `json:"name" bson:"name"`
	Status               string `json:"status" bson:"status"` // active, disabled
	DailyTokenBudget     int64  `json:"daily_token_budget,omitempty" bson:"daily_token_budget,omitempty"`
	MaxConcurrentTurns   int    `json:"max_concurrent_turns,omitempty" bson:"max_concurrent_turns,omitempty"`
	DefaultRole          string `json:"default_role,omitempty" bson:"default_role,omitempty"`
	ArtifactRetentionDay int    `json:"artifact_retention_days,omitempty" bson:"artifact_retention_days,omitempty"`
	LLMCallRetentionDay  int    `json:"llm_call_retention_days,omitempty" bson:"llm_call_retention_days,omitempty"`
}

// APIKey authenticates a caller; the plaintext is shown once, only a hash is kept.
type APIKey struct {
	Meta              `bson:",inline"`
	Name              string         `json:"name" bson:"name"`
	Prefix            string         `json:"prefix" bson:"prefix"`
	Hash              string         `json:"hash,omitempty" bson:"hash"` // never returned by APIs: use Public()
	Role              tenant.Role    `json:"role" bson:"role"`
	MaxPermissionMode PermissionMode `json:"max_permission_mode" bson:"max_permission_mode"`
	Disabled          bool           `json:"disabled" bson:"disabled"`
	LastUsedAt        *time.Time     `json:"last_used_at,omitempty" bson:"last_used_at,omitempty"`
}

// FeishuChat binds a Feishu group to a tenant.
type FeishuChat struct {
	Meta        `bson:",inline"`
	ChatID      string `json:"chat_id" bson:"chat_id"`
	Name        string `json:"name,omitempty" bson:"name,omitempty"`
	DefaultRole string `json:"default_role,omitempty" bson:"default_role,omitempty"`
	Bound       bool   `json:"bound" bson:"bound"`
}

// FeishuThread binds a Feishu thread to a session.
type FeishuThread struct {
	Meta      `bson:",inline"`
	ChatID    string `json:"chat_id" bson:"chat_id"`
	RootMsgID string `json:"root_msg_id" bson:"root_msg_id"`
	SessionID string `json:"session_id" bson:"session_id"`
}

// AuditLog is append-only.
type AuditLog struct {
	Meta   `bson:",inline"`
	Actor  tenant.Principal `json:"actor" bson:"actor"`
	Action string           `json:"action" bson:"action"`
	Target string           `json:"target" bson:"target"`
	TurnID string           `json:"turn_id,omitempty" bson:"turn_id,omitempty"`
	Detail map[string]any   `json:"detail,omitempty" bson:"detail,omitempty"`
	Reason string           `json:"reason,omitempty" bson:"reason,omitempty"`
}

// Public returns k without its hash, for API responses.
func (k APIKey) Public() APIKey {
	k.Hash = ""
	return k
}
