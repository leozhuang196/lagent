package types

import (
	"encoding/json"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// SessionStatus is the admission state of a session.
type SessionStatus string

const (
	SessionIdle    SessionStatus = "idle"
	SessionQueued  SessionStatus = "queued"
	SessionRunning SessionStatus = "running"
	SessionWaiting SessionStatus = "waiting" // a turn is suspended on a pending item
)

// ChannelKind is where results and pending items are mirrored.
type ChannelKind string

const (
	ChannelStream  ChannelKind = "stream"  // SSE event stream
	ChannelFeishu  ChannelKind = "feishu"  // a Feishu thread
	ChannelInbox   ChannelKind = "inbox"   // the pending center; always present
	ChannelWebhook ChannelKind = "webhook" // completion callback of an internal system
)

// Channel is one delivery target of a session.
type Channel struct {
	Kind   ChannelKind `json:"kind" bson:"kind"`
	Target string      `json:"target,omitempty" bson:"target,omitempty"`
	Secret string      `json:"secret,omitempty" bson:"secret,omitempty"` // webhook HMAC key reference; redacted in API responses
}

// Origin is what created a session.
type Origin string

const (
	OriginWeb      Origin = "web"
	OriginFeishu   Origin = "feishu"
	OriginAPI      Origin = "api"
	OriginSchedule Origin = "schedule"
	OriginWorkflow Origin = "workflow"
	OriginSubtask  Origin = "subtask"
	OriginDebug    Origin = "debug"
)

// InboxItem is a user message submitted while a turn was running; the engine
// folds it into history before the next iteration.
type InboxItem struct {
	Text   string    `json:"text" bson:"text"`
	Author string    `json:"author,omitempty" bson:"author,omitempty"`
	At     time.Time `json:"at" bson:"at"`
}

// Session is a conversation and the unit of mutual exclusion.
type Session struct {
	Meta           `bson:",inline"`
	Title          string          `json:"title,omitempty" bson:"title,omitempty"`
	RoleKey        string          `json:"role_key" bson:"role_key"`
	RoleVer        int             `json:"role_ver" bson:"role_ver"`
	PermissionMode PermissionMode  `json:"permission_mode" bson:"permission_mode"`
	MemorySpaces   []string        `json:"memory_spaces,omitempty" bson:"memory_spaces,omitempty"`
	Channels       []Channel       `json:"channels" bson:"channels"`
	StandingGrants []StandingGrant `json:"standing_grants,omitempty" bson:"standing_grants,omitempty"`

	Status          SessionStatus `json:"status" bson:"status"`
	ActiveTurnID    string        `json:"active_turn_id,omitempty" bson:"active_turn_id,omitempty"`
	CancelRequested bool          `json:"cancel_requested,omitempty" bson:"cancel_requested,omitempty"`
	Inbox           []InboxItem   `json:"inbox,omitempty" bson:"inbox,omitempty"`
	LastSeq         int64         `json:"last_seq" bson:"last_seq"`

	Origin       Origin           `json:"origin" bson:"origin"`
	CreatedBy    tenant.Principal `json:"created_by" bson:"created_by"`
	Debug        bool             `json:"debug,omitempty" bson:"debug,omitempty"`
	ParentTurnID string           `json:"parent_turn_id,omitempty" bson:"parent_turn_id,omitempty"`
	RunID        string           `json:"run_id,omitempty" bson:"run_id,omitempty"`
	StepRunID    string           `json:"step_run_id,omitempty" bson:"step_run_id,omitempty"`
	// ToolDeny removes tools from what the role allows (e.g. dispatch_subtask in children).
	ToolDeny []string          `json:"tool_deny,omitempty" bson:"tool_deny,omitempty"`
	Labels   map[string]string `json:"labels,omitempty" bson:"labels,omitempty"`
}

// HasChannel reports whether s delivers to kind.
func (s *Session) HasChannel(kind ChannelKind) (Channel, bool) {
	for _, c := range s.Channels {
		if c.Kind == kind {
			return c, true
		}
	}
	return Channel{}, false
}

// TurnStatus is the lifecycle of a turn.
type TurnStatus string

const (
	TurnQueued    TurnStatus = "queued"
	TurnRunning   TurnStatus = "running"
	TurnSuspended TurnStatus = "suspended"
	TurnCompleted TurnStatus = "completed"
	TurnFailed    TurnStatus = "failed"
	TurnCancelled TurnStatus = "cancelled"
)

// Terminal reports whether s is final.
func (s TurnStatus) Terminal() bool {
	return s == TurnCompleted || s == TurnFailed || s == TurnCancelled
}

// TurnTrigger is what started a turn.
type TurnTrigger string

const (
	TriggerMessage  TurnTrigger = "message"
	TriggerSchedule TurnTrigger = "schedule"
	TriggerStep     TurnTrigger = "step"
	TriggerSubtask  TurnTrigger = "subtask"
)

// TurnError describes why a turn failed, readable by people.
type TurnError struct {
	Code    string `json:"code" bson:"code"`
	Message string `json:"message" bson:"message"`
}

// StepResult is the structured report of a step or subtask turn.
type StepResult struct {
	Summary   string          `json:"summary" bson:"summary"`
	Data      json.RawMessage `json:"data,omitempty" bson:"data,omitempty"`
	Artifacts []string        `json:"artifacts,omitempty" bson:"artifacts,omitempty"`
}

// Turn is one run of the agent loop: submit → loop until the model stops calling tools.
type Turn struct {
	Meta          `bson:",inline"`
	SessionID     string      `json:"session_id" bson:"session_id"`
	Trigger       TurnTrigger `json:"trigger" bson:"trigger"`
	Status        TurnStatus  `json:"status" bson:"status"`
	RoleKey       string      `json:"role_key" bson:"role_key"`
	RoleVer       int         `json:"role_ver" bson:"role_ver"`
	Model         string      `json:"model,omitempty" bson:"model,omitempty"`
	Epoch         int64       `json:"epoch" bson:"epoch"`
	Iterations    int         `json:"iterations" bson:"iterations"`
	Usage         Usage       `json:"usage" bson:"usage"`
	TokenBudget   int64       `json:"token_budget,omitempty" bson:"token_budget,omitempty"`
	MaxIterations int         `json:"max_iterations,omitempty" bson:"max_iterations,omitempty"`
	Error         *TurnError  `json:"error,omitempty" bson:"error,omitempty"`
	WaitingOn     []string    `json:"waiting_on,omitempty" bson:"waiting_on,omitempty"` // pending ids
	ParentTurnID  string      `json:"parent_turn_id,omitempty" bson:"parent_turn_id,omitempty"`
	StepRunID     string      `json:"step_run_id,omitempty" bson:"step_run_id,omitempty"`
	Result        *StepResult `json:"result,omitempty" bson:"result,omitempty"`
	FinalText     string      `json:"final_text,omitempty" bson:"final_text,omitempty"`
	Debug         bool        `json:"debug,omitempty" bson:"debug,omitempty"`
	IdemKey       string      `json:"idem_key,omitempty" bson:"idem_key,omitempty"`
	StartedAt     *time.Time  `json:"started_at,omitempty" bson:"started_at,omitempty"`
	EndedAt       *time.Time  `json:"ended_at,omitempty" bson:"ended_at,omitempty"`
}

// PendingKind distinguishes what a suspended turn waits for.
type PendingKind string

const (
	PendingApproval    PendingKind = "approval"
	PendingQuestion    PendingKind = "question"
	PendingSubtasks    PendingKind = "subtasks"
	PendingExternalJob PendingKind = "external_job"
	PendingBudget      PendingKind = "budget"
	PendingUnknown     PendingKind = "outcome_unknown" // a side-effect call crashed mid-flight
)

// PendingStatus is open or resolved; there is no auto-reject.
type PendingStatus string

const (
	PendingOpen     PendingStatus = "open"
	PendingResolved PendingStatus = "resolved"
)

// Decision is how a pending item was resolved.
type Decision string

const (
	DecisionApprove     Decision = "approve"
	DecisionReject      Decision = "reject"
	DecisionAnswer      Decision = "answer"   // question answered, or approval redirected with new instructions
	DecisionContinue    Decision = "continue" // budget extension
	DecisionStop        Decision = "stop"
	DecisionDone        Decision = "done"         // subtasks / external job finished
	DecisionHappened    Decision = "happened"     // outcome_unknown: the side effect did happen
	DecisionNotHappened Decision = "not_happened" // outcome_unknown: it did not; safe to retry
)

// Resolution records who resolved a pending item and how.
type Resolution struct {
	Decision Decision         `json:"decision" bson:"decision"`
	Answer   string           `json:"answer,omitempty" bson:"answer,omitempty"`
	By       tenant.Principal `json:"by" bson:"by"`
	Via      string           `json:"via" bson:"via"` // web, feishu, api, system
	At       time.Time        `json:"at" bson:"at"`
}

// Pending is the single source of truth of a human-in-the-loop item; every
// channel shows a view of it.
type Pending struct {
	Meta      `bson:",inline"`
	SessionID string        `json:"session_id" bson:"session_id"`
	TurnID    string        `json:"turn_id" bson:"turn_id"`
	Kind      PendingKind   `json:"kind" bson:"kind"`
	Status    PendingStatus `json:"status" bson:"status"`
	Title     string        `json:"title" bson:"title"`
	Detail    string        `json:"detail,omitempty" bson:"detail,omitempty"`

	// approval
	ToolCallID string          `json:"tool_call_id,omitempty" bson:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty" bson:"tool_name,omitempty"`
	Risk       RiskLevel       `json:"risk,omitempty" bson:"risk,omitempty"`
	Args       json.RawMessage `json:"args,omitempty" bson:"args,omitempty"` // redacted
	// TriggerSource says which step and which content led to the call (§8.2 injection guard).
	TriggerSource string `json:"trigger_source,omitempty" bson:"trigger_source,omitempty"`

	// question
	Question string   `json:"question,omitempty" bson:"question,omitempty"`
	Options  []string `json:"options,omitempty" bson:"options,omitempty"`

	// subtasks / external job
	ChildTurnIDs []string `json:"child_turn_ids,omitempty" bson:"child_turn_ids,omitempty"`
	ExternalRef  string   `json:"external_ref,omitempty" bson:"external_ref,omitempty"`

	Assignee   string      `json:"assignee,omitempty" bson:"assignee,omitempty"`
	Resolution *Resolution `json:"resolution,omitempty" bson:"resolution,omitempty"`
	RemindAt   *time.Time  `json:"remind_at,omitempty" bson:"remind_at,omitempty"`
}

// ToolCallStatus is the ledger state of one tool call.
type ToolCallStatus string

const (
	CallDecided   ToolCallStatus = "decided"
	CallAwaiting  ToolCallStatus = "awaiting_approval"
	CallStarted   ToolCallStatus = "started"
	CallSucceeded ToolCallStatus = "succeeded"
	CallFailed    ToolCallStatus = "failed"
	CallDenied    ToolCallStatus = "denied"
	CallUnknown   ToolCallStatus = "outcome_unknown"
)

// ToolCall is the ledger entry that makes crashes safe (§8).
type ToolCall struct {
	Meta           `bson:",inline"`
	SessionID      string          `json:"session_id" bson:"session_id"`
	TurnID         string          `json:"turn_id" bson:"turn_id"`
	ToolUseID      string          `json:"tool_use_id" bson:"tool_use_id"`
	Tool           string          `json:"tool" bson:"tool"`
	Input          json.RawMessage `json:"input" bson:"input"`
	Risk           RiskLevel       `json:"risk" bson:"risk"`
	SideEffect     bool            `json:"side_effect" bson:"side_effect"`
	Status         ToolCallStatus  `json:"status" bson:"status"`
	Decision       string          `json:"decision,omitempty" bson:"decision,omitempty"`
	IdempotencyKey string          `json:"idempotency_key" bson:"idempotency_key"`
	PendingID      string          `json:"pending_id,omitempty" bson:"pending_id,omitempty"`
	Output         string          `json:"output,omitempty" bson:"output,omitempty"`
	IsError        bool            `json:"is_error,omitempty" bson:"is_error,omitempty"`
	DurationMs     int64           `json:"duration_ms,omitempty" bson:"duration_ms,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty" bson:"started_at,omitempty"`
	EndedAt        *time.Time      `json:"ended_at,omitempty" bson:"ended_at,omitempty"`
}

// Compaction caches the summary of a message range.
type Compaction struct {
	Meta      `bson:",inline"`
	SessionID string `json:"session_id" bson:"session_id"`
	FromSeq   int64  `json:"from_seq" bson:"from_seq"`
	ToSeq     int64  `json:"to_seq" bson:"to_seq"`
	Summary   string `json:"summary" bson:"summary"`
}

// Artifact is a deliverable archived to object storage.
type Artifact struct {
	Meta        `bson:",inline"`
	SessionID   string `json:"session_id,omitempty" bson:"session_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty" bson:"turn_id,omitempty"`
	RunID       string `json:"run_id,omitempty" bson:"run_id,omitempty"`
	StepRunID   string `json:"step_run_id,omitempty" bson:"step_run_id,omitempty"`
	Name        string `json:"name" bson:"name"`
	ObjectKey   string `json:"object_key" bson:"object_key"`
	Size        int64  `json:"size" bson:"size"`
	SHA256      string `json:"sha256" bson:"sha256"`
	ContentType string `json:"content_type,omitempty" bson:"content_type,omitempty"`
}

// LLMCall keeps the raw request/response of one model call for the turn debugger.
type LLMCall struct {
	Meta      `bson:",inline"`
	SessionID string          `json:"session_id" bson:"session_id"`
	TurnID    string          `json:"turn_id" bson:"turn_id"`
	Iteration int             `json:"iteration" bson:"iteration"`
	Model     string          `json:"model" bson:"model"`
	Request   json.RawMessage `json:"request" bson:"request"`
	Response  json.RawMessage `json:"response,omitempty" bson:"response,omitempty"`
	Usage     Usage           `json:"usage" bson:"usage"`
	LatencyMs int64           `json:"latency_ms" bson:"latency_ms"`
	Error     string          `json:"error,omitempty" bson:"error,omitempty"`
}
