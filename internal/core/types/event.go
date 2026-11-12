package types

import "time"

// EventType names an event of the public event contract.
type EventType string

const (
	EvTurnStarted      EventType = "turn.started"
	EvMessageDelta     EventType = "message.delta"
	EvMessageCompleted EventType = "message.completed"
	EvToolRequested    EventType = "tool.requested"
	EvToolStarted      EventType = "tool.started"
	EvToolCompleted    EventType = "tool.completed"
	EvPendingCreated   EventType = "pending.created"
	EvPendingResolved  EventType = "pending.resolved"
	EvArtifactCreated  EventType = "artifact.created"
	EvTurnSuspended    EventType = "turn.suspended"
	EvTurnCompleted    EventType = "turn.completed"
	EvTurnFailed       EventType = "turn.failed"
	EvTurnCancelled    EventType = "turn.cancelled"
	EvRunCreated       EventType = "run.created"
	EvRunCompleted     EventType = "run.completed"
	EvStepStarted      EventType = "step.started"
	EvStepCompleted    EventType = "step.completed"
	EvStepReused       EventType = "step.reused"
	// EvStreamReset tells a client its cursor is older than the buffer; reload the snapshot.
	EvStreamReset EventType = "stream.reset"
)

// Event is the only contract between the engine and the outside world.
// Seq is strictly increasing per stream; it is the only ordering signal.
type Event struct {
	Seq      int64          `json:"seq"`
	Stream   string         `json:"stream"` // session id or run id
	TenantID string         `json:"tenant_id"`
	TurnID   string         `json:"turn_id,omitempty"`
	Type     EventType      `json:"type"`
	TS       time.Time      `json:"ts"`
	Data     map[string]any `json:"data,omitempty"`
}
