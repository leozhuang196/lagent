// Package types holds the entity types shared by every layer.
//
// Types here carry fields only. State-transition rules live in rules/*,
// persistence in infra/repo. Every entity embeds Meta so repositories can treat
// them uniformly (id, tenant, optimistic version, timestamps).
package types

import "time"

// Meta is embedded in every persisted entity.
type Meta struct {
	ID        string    `json:"id" bson:"_id"`
	TenantID  string    `json:"tenant_id" bson:"tenant_id"`
	Version   int64     `json:"version" bson:"version"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// M returns the embedded meta; it lets generic repositories reach it.
func (m *Meta) M() *Meta { return m }

// Entity is any persisted record.
type Entity interface {
	M() *Meta
}

// Usage counts model tokens.
type Usage struct {
	InputTokens      int64 `json:"input_tokens" bson:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens" bson:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens" bson:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens" bson:"cache_write_tokens"`
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}

// Total is the number of tokens counted against budgets.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Ptr returns a pointer to t; handy for optional timestamps.
func Ptr[T any](v T) *T { return &v }
