package types

import "encoding/json"

// RiskLevel classifies what a tool call can do. Ordered from least to most risky.
type RiskLevel string

const (
	RiskReadOnly   RiskLevel = "read_only"
	RiskLocalWrite RiskLevel = "local_write"
	RiskCommand    RiskLevel = "command"
	RiskExternal   RiskLevel = "external"
)

// Rank orders risk levels; unknown levels rank as external.
func (r RiskLevel) Rank() int {
	switch r {
	case RiskReadOnly:
		return 0
	case RiskLocalWrite:
		return 1
	case RiskCommand:
		return 2
	default:
		return 3
	}
}

// Valid reports whether r is a known level.
func (r RiskLevel) Valid() bool {
	switch r {
	case RiskReadOnly, RiskLocalWrite, RiskCommand, RiskExternal:
		return true
	}
	return false
}

// PermissionMode is the session-level approval policy.
type PermissionMode string

const (
	// ModeAskAll asks before every call that is not read-only.
	ModeAskAll PermissionMode = "ask_all"
	// ModeAskExternal asks only for command and external calls.
	ModeAskExternal PermissionMode = "ask_external"
	// ModeAuto runs everything without asking.
	ModeAuto PermissionMode = "auto"
)

// Rank orders modes from strictest to loosest.
func (m PermissionMode) Rank() int {
	switch m {
	case ModeAskAll:
		return 0
	case ModeAskExternal:
		return 1
	case ModeAuto:
		return 2
	}
	return -1
}

func (m PermissionMode) Valid() bool { return m.Rank() >= 0 }

// Stricter returns the stricter of two modes.
func Stricter(a, b PermissionMode) PermissionMode {
	if !a.Valid() {
		return b
	}
	if !b.Valid() || a.Rank() <= b.Rank() {
		return a
	}
	return b
}

// ToolSource is where a tool came from.
type ToolSource string

const (
	SourceBuiltin   ToolSource = "builtin"
	SourceConnector ToolSource = "connector"
	SourceMCP       ToolSource = "mcp"
	SourcePlatform  ToolSource = "platform" // orchestration tools such as dispatch_subtask
)

// ToolSpec is what the model sees.
type ToolSpec struct {
	Name        string          `json:"name" bson:"name"`
	Description string          `json:"description" bson:"description"`
	InputSchema json.RawMessage `json:"input_schema" bson:"input_schema"`
}

// ToolMeta is what the platform knows about a tool.
type ToolMeta struct {
	Name     string     `json:"name" bson:"name"`
	Category string     `json:"category,omitempty" bson:"category,omitempty"`
	Source   ToolSource `json:"source" bson:"source"`
	Risk     RiskLevel  `json:"risk" bson:"risk"`
	// SideEffect marks calls whose effect outside the platform cannot be inferred
	// from inputs: never reused, never blindly retried.
	SideEffect bool `json:"side_effect" bson:"side_effect"`
	// ReadOnly tools may run concurrently.
	ReadOnly bool `json:"read_only" bson:"read_only"`
	// NeedsReview tools may not run until a moderation tool approved the content.
	NeedsReview bool `json:"needs_review,omitempty" bson:"needs_review,omitempty"`
	// Idempotent tools accept an idempotency key, so a crashed call can be retried.
	Idempotent bool   `json:"idempotent,omitempty" bson:"idempotent,omitempty"`
	Server     string `json:"server,omitempty" bson:"server,omitempty"` // MCP server name
}

// StandingGrant pre-approves one exact tool → target pair (e.g. replying in a
// Feishu thread, posting to one chat on a schedule). Never a whole risk level.
type StandingGrant struct {
	Tool string `json:"tool" bson:"tool"`
	// Field is the top-level input field compared against Value.
	Field string `json:"field" bson:"field"`
	Value string `json:"value" bson:"value"`
}
