// Package compaction chooses where history may be cut for summarising.
// Only outbound content is rewritten; stored history never changes.
package compaction

import "github.com/eternityx/lagent/internal/core/types"

// Estimate approximates the tokens of a message (≈4 chars per token).
func Estimate(m types.Message) int {
	n := 4
	for _, b := range m.Content {
		n += (len(b.Text) + len(b.Input) + len(b.Raw)) / 4
		n += 8
	}
	return n
}

// Plan is the compaction decision.
type Plan struct {
	// Cut: messages[:Cut] are replaced by a summary. 0 means no compaction.
	Cut int
	// Total is the estimated size before compaction.
	Total int
}

// Decide compacts when the estimated size exceeds limit-reserve (the reserve
// leaves room for the summarisation call itself). It cuts in one big step so
// the remainder fits in target (default limit/2): every compaction invalidates
// the prompt cache, so doing it rarely is cheaper.
//
// A cut always lands on a turn boundary: messages[Cut] is a user message that
// is not a tool result, so no tool_use is separated from its tool_result.
func Decide(msgs []types.Message, limit, reserve, target int) Plan {
	total := 0
	sizes := make([]int, len(msgs))
	for i, m := range msgs {
		sizes[i] = Estimate(m)
		total += sizes[i]
	}
	p := Plan{Total: total}
	if limit <= 0 || total <= limit-reserve {
		return p
	}
	if target <= 0 {
		target = limit / 2
	}
	tail := 0
	best := 0
	for i := len(msgs) - 1; i > 0; i-- {
		tail += sizes[i]
		if IsBoundary(msgs[i]) {
			if tail <= target {
				best = i
			} else {
				break
			}
		}
	}
	if best == 0 {
		// Even the last turn exceeds target: cut at the latest boundary we have.
		for i := len(msgs) - 1; i > 0; i-- {
			if IsBoundary(msgs[i]) {
				best = i
				break
			}
		}
	}
	p.Cut = best
	return p
}

// IsBoundary reports whether a user turn starts at m.
func IsBoundary(m types.Message) bool {
	if m.Role != types.RoleUser {
		return false
	}
	for _, b := range m.Content {
		if b.Type == types.BlockToolResult {
			return false
		}
	}
	return true
}
