package types

import (
	"encoding/json"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// ParamType is the type of a workflow parameter.
type ParamType string

const (
	ParamString ParamType = "string"
	ParamNumber ParamType = "number"
	ParamList   ParamType = "list"
	// ParamPeriod is a relative period such as "last_week"; it is resolved once
	// against the frozen time at run start.
	ParamPeriod ParamType = "period"
)

// ParamDef declares one workflow parameter.
type ParamDef struct {
	Name     string          `json:"name" bson:"name"`
	Type     ParamType       `json:"type" bson:"type"`
	Required bool            `json:"required,omitempty" bson:"required,omitempty"`
	Default  json.RawMessage `json:"default,omitempty" bson:"default,omitempty"`
	Label    string          `json:"label,omitempty" bson:"label,omitempty"`
}

// FailurePolicy is what happens when a step finally fails.
type FailurePolicy string

const (
	FailAbort          FailurePolicy = "abort"
	FailSkipDependents FailurePolicy = "skip_dependents"
	FailContinue       FailurePolicy = "continue"
)

// StepDef declares one node of the execution graph.
type StepDef struct {
	ID   string `json:"id" bson:"id"`
	Name string `json:"name,omitempty" bson:"name,omitempty"`
	// Role overrides the run's role for this step.
	Role string `json:"role,omitempty" bson:"role,omitempty"`
	// Instruction is a template: {{params.x}}, {{item}}, {{steps.a.summary}}, {{frozen.date}}.
	Instruction string   `json:"instruction" bson:"instruction"`
	DependsOn   []string `json:"depends_on,omitempty" bson:"depends_on,omitempty"`
	// FanOut names a list parameter; one instance runs per element.
	FanOut     string        `json:"fan_out,omitempty" bson:"fan_out,omitempty"`
	Retries    int           `json:"retries,omitempty" bson:"retries,omitempty"`
	TimeoutSec int           `json:"timeout_sec,omitempty" bson:"timeout_sec,omitempty"`
	OnFailure  FailurePolicy `json:"on_failure,omitempty" bson:"on_failure,omitempty"`
	// SideEffect steps are never reused and never retried.
	SideEffect bool `json:"side_effect,omitempty" bson:"side_effect,omitempty"`
	// Sources lists retrieval sources the step depends on (availability check).
	Sources []string `json:"sources,omitempty" bson:"sources,omitempty"`
}

// WorkflowDef is a declarative flow template.
type WorkflowDef struct {
	Meta        `bson:",inline"`
	Versioned   `bson:",inline"`
	Name        string     `json:"name" bson:"name"`
	Description string     `json:"description,omitempty" bson:"description,omitempty"`
	Role        string     `json:"role,omitempty" bson:"role,omitempty"`
	Params      []ParamDef `json:"params,omitempty" bson:"params,omitempty"`
	Steps       []StepDef  `json:"steps" bson:"steps"`
	TimeoutSec  int        `json:"timeout_sec,omitempty" bson:"timeout_sec,omitempty"`
	MaxParallel int        `json:"max_parallel,omitempty" bson:"max_parallel,omitempty"`
	TokenBudget int64      `json:"token_budget,omitempty" bson:"token_budget,omitempty"`
}

// Frozen holds every non-deterministic input, evaluated once at run start.
type Frozen struct {
	Now    time.Time                  `json:"now" bson:"now"`
	TZ     string                     `json:"tz" bson:"tz"`
	Seed   int64                      `json:"seed" bson:"seed"`
	Params map[string]json.RawMessage `json:"params" bson:"params"` // resolved parameters
}

// RunStatus is the lifecycle of a workflow run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunPartial   RunStatus = "partial"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunPartial || s == RunFailed || s == RunCancelled
}

// RunMode distinguishes how a run was started relative to an earlier one.
type RunMode string

const (
	ModeStart    RunMode = "start"
	ModeContinue RunMode = "continue" // reuse frozen values: fingerprints of untouched steps hit
	ModeRerun    RunMode = "rerun"    // freeze again: full re-execution
)

// WorkflowRun is one execution of a definition; its graph is fixed at creation.
type WorkflowRun struct {
	Meta           `bson:",inline"`
	DefKey         string           `json:"def_key" bson:"def_key"`
	DefVer         int              `json:"def_ver" bson:"def_ver"`
	DefTenant      string           `json:"def_tenant" bson:"def_tenant"`
	RoleKey        string           `json:"role_key" bson:"role_key"`
	RoleVer        int              `json:"role_ver" bson:"role_ver"`
	Frozen         Frozen           `json:"frozen" bson:"frozen"`
	Status         RunStatus        `json:"status" bson:"status"`
	Mode           RunMode          `json:"mode" bson:"mode"`
	OriginRunID    string           `json:"origin_run_id,omitempty" bson:"origin_run_id,omitempty"`
	ScheduleID     string           `json:"schedule_id,omitempty" bson:"schedule_id,omitempty"`
	StartedBy      tenant.Principal `json:"started_by" bson:"started_by"`
	PermissionMode PermissionMode   `json:"permission_mode" bson:"permission_mode"`
	StandingGrants []StandingGrant  `json:"standing_grants,omitempty" bson:"standing_grants,omitempty"`
	Error          *TurnError       `json:"error,omitempty" bson:"error,omitempty"`
	Debug          bool             `json:"debug,omitempty" bson:"debug,omitempty"`
	Usage          Usage            `json:"usage" bson:"usage"`
	Deadline       *time.Time       `json:"deadline,omitempty" bson:"deadline,omitempty"`
	EndedAt        *time.Time       `json:"ended_at,omitempty" bson:"ended_at,omitempty"`
	// Params are the parameters as submitted (before freezing); a rerun
	// freezes them again (§9.15).
	Params map[string]json.RawMessage `json:"params,omitempty" bson:"params,omitempty"`
}

// StepStatus is the lifecycle of a step instance.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepDispatched StepStatus = "dispatched"
	StepRunning    StepStatus = "running"
	StepSucceeded  StepStatus = "succeeded"
	StepFailed     StepStatus = "failed"
	StepSkipped    StepStatus = "skipped"
	StepCancelled  StepStatus = "cancelled"
	StepReused     StepStatus = "reused"
)

func (s StepStatus) Terminal() bool {
	switch s {
	case StepSucceeded, StepFailed, StepSkipped, StepCancelled, StepReused:
		return true
	}
	return false
}

// Ok reports whether s produced a usable result.
func (s StepStatus) Ok() bool { return s == StepSucceeded || s == StepReused }

// StepRun is one step instance of a run (one per fan-out element).
type StepRun struct {
	Meta       `bson:",inline"`
	RunID      string          `json:"run_id" bson:"run_id"`
	StepID     string          `json:"step_id" bson:"step_id"`
	Index      int             `json:"index" bson:"index"`
	Item       json.RawMessage `json:"item,omitempty" bson:"item,omitempty"`
	DependsOn  []string        `json:"depends_on,omitempty" bson:"depends_on,omitempty"` // step ids
	StepKey    string          `json:"step_key" bson:"step_key"`
	Input      string          `json:"input" bson:"input"` // resolved instruction (may still refer to upstream results)
	RoleKey    string          `json:"role_key" bson:"role_key"`
	RoleVer    int             `json:"role_ver" bson:"role_ver"`
	Status     StepStatus      `json:"status" bson:"status"`
	Attempts   int             `json:"attempts" bson:"attempts"`
	MaxRetries int             `json:"max_retries" bson:"max_retries"`
	OnFailure  FailurePolicy   `json:"on_failure" bson:"on_failure"`
	SideEffect bool            `json:"side_effect,omitempty" bson:"side_effect,omitempty"`
	TimeoutSec int             `json:"timeout_sec,omitempty" bson:"timeout_sec,omitempty"`
	SessionID  string          `json:"session_id,omitempty" bson:"session_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty" bson:"turn_id,omitempty"`
	Result     *StepResult     `json:"result,omitempty" bson:"result,omitempty"`
	ReusedFrom string          `json:"reused_from,omitempty" bson:"reused_from,omitempty"`
	Error      *TurnError      `json:"error,omitempty" bson:"error,omitempty"`
	NotBefore  *time.Time      `json:"not_before,omitempty" bson:"not_before,omitempty"` // retry backoff
	StartedAt  *time.Time      `json:"started_at,omitempty" bson:"started_at,omitempty"`
	EndedAt    *time.Time      `json:"ended_at,omitempty" bson:"ended_at,omitempty"`
	// DispatchedAt starts the step timeout: queue wait counts against it so a
	// step stuck before its turn starts is still failed by the sweeper.
	DispatchedAt *time.Time `json:"dispatched_at,omitempty" bson:"dispatched_at,omitempty"`
}

// OverlapPolicy is what a schedule does when the previous run is still going.
type OverlapPolicy string

const (
	OverlapSkip     OverlapPolicy = "skip"
	OverlapQueue    OverlapPolicy = "queue"
	OverlapParallel OverlapPolicy = "parallel"
)

// ScheduleTarget is what a schedule runs: a prompt for a role, or a workflow.
type ScheduleTarget struct {
	Prompt      string                     `json:"prompt,omitempty" bson:"prompt,omitempty"`
	RoleKey     string                     `json:"role_key,omitempty" bson:"role_key,omitempty"`
	WorkflowKey string                     `json:"workflow_key,omitempty" bson:"workflow_key,omitempty"`
	Params      map[string]json.RawMessage `json:"params,omitempty" bson:"params,omitempty"`
}

// Schedule is a recurring trigger.
type Schedule struct {
	Meta           `bson:",inline"`
	Name           string           `json:"name" bson:"name"`
	Cron           string           `json:"cron" bson:"cron"`
	TZ             string           `json:"tz" bson:"tz"`
	Overlap        OverlapPolicy    `json:"overlap" bson:"overlap"`
	Target         ScheduleTarget   `json:"target" bson:"target"`
	PermissionMode PermissionMode   `json:"permission_mode" bson:"permission_mode"`
	StandingGrants []StandingGrant  `json:"standing_grants,omitempty" bson:"standing_grants,omitempty"`
	Enabled        bool             `json:"enabled" bson:"enabled"`
	NextRunAt      *time.Time       `json:"next_run_at,omitempty" bson:"next_run_at,omitempty"`
	LastRunAt      *time.Time       `json:"last_run_at,omitempty" bson:"last_run_at,omitempty"`
	ActiveRef      string           `json:"active_ref,omitempty" bson:"active_ref,omitempty"` // session or run still running
	LastDefVer     int              `json:"last_def_ver,omitempty" bson:"last_def_ver,omitempty"`
	DefUpdated     bool             `json:"def_updated,omitempty" bson:"def_updated,omitempty"` // "template updated, next run uses new version"
	Owner          tenant.Principal `json:"owner" bson:"owner"`
}

// ScheduleOutcome records what happened at one planned time.
type ScheduleOutcome string

const (
	OutcomeRan     ScheduleOutcome = "ran"
	OutcomeSkipped ScheduleOutcome = "skipped"
	OutcomeMissed  ScheduleOutcome = "missed"
)

// ScheduleRun is one firing (or non-firing) of a schedule.
type ScheduleRun struct {
	Meta       `bson:",inline"`
	ScheduleID string          `json:"schedule_id" bson:"schedule_id"`
	PlannedAt  time.Time       `json:"planned_at" bson:"planned_at"`
	Outcome    ScheduleOutcome `json:"outcome" bson:"outcome"`
	Reason     string          `json:"reason,omitempty" bson:"reason,omitempty"`
	SessionID  string          `json:"session_id,omitempty" bson:"session_id,omitempty"`
	RunID      string          `json:"run_id,omitempty" bson:"run_id,omitempty"`
}
