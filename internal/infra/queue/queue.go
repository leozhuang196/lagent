// Package queue is at-least-once job delivery (Asynq on Redis in production).
// Handlers must be idempotent: claims are made with conditional updates.
package queue

import (
	"context"
	"encoding/json"
	"time"
)

// Job is one unit of background work.
type Job struct {
	Type     string          `json:"type"`
	TenantID string          `json:"tenant_id"`
	Payload  json.RawMessage `json:"payload"`
	// UniqueKey de-duplicates jobs while one with the same key is pending.
	UniqueKey string        `json:"unique_key,omitempty"`
	ProcessAt time.Time     `json:"process_at,omitempty"`
	MaxRetry  int           `json:"max_retry,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
	// Queue lets dedicated workers serve one tenant (compute isolation).
	Queue string `json:"queue,omitempty"`
	// Attempt is set by the server: 0 on first delivery.
	Attempt int `json:"attempt,omitempty"`
}

// NewJob marshals payload into a job.
func NewJob(typ, tenantID string, payload any) (Job, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Job{}, err
	}
	return Job{Type: typ, TenantID: tenantID, Payload: raw}, nil
}

// Decode unmarshals the payload.
func (j Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// Queue enqueues jobs.
type Queue interface {
	Enqueue(ctx context.Context, j Job) error
}

// Handler processes a job. Returning an error retries with backoff until
// MaxRetry; returning SkipRetry(err) gives up immediately.
type Handler func(ctx context.Context, j Job) error

// Server consumes jobs.
type Server interface {
	Handle(typ string, h Handler)
	Start(ctx context.Context) error
	Stop()
}

type skipRetry struct{ err error }

func (s skipRetry) Error() string { return s.err.Error() }
func (s skipRetry) Unwrap() error { return s.err }

// SkipRetry marks err as permanent.
func SkipRetry(err error) error { return skipRetry{err} }

// IsSkipRetry reports whether err is permanent.
func IsSkipRetry(err error) bool { _, ok := err.(skipRetry); return ok }

// Job types used across the platform.
const (
	TypeTurnRun         = "turn.run"
	TypeTurnResume      = "turn.resume"
	TypeWorkflowAdvance = "workflow.advance"
	TypeWorkflowStep    = "workflow.step"
	TypeScheduleFire    = "schedule.fire"
	TypeWebhook         = "webhook.deliver"
	TypeFeishuDeliver   = "feishu.deliver"
	TypeFeishuInbound   = "feishu.inbound"
	TypeArchiveRetry    = "artifact.retry"
	TypeMCPSync         = "mcp.sync"
)
