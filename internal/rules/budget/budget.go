// Package budget decides when spending stops (§10.1).
package budget

import (
	"fmt"
	"time"

	"github.com/eternityx/lagent/internal/core/types"
)

// Defaults when a role sets nothing.
const (
	DefaultMaxIterations = 50
	DefaultTokenBudget   = 2_000_000
)

// TurnLimits are the effective limits of a turn.
type TurnLimits struct {
	MaxIterations int
	TokenBudget   int64
}

// Limits picks the role's limits or defaults; extra raises them after a person
// chose to continue.
func Limits(role *types.Role, extraIterations int, extraTokens int64) TurnLimits {
	l := TurnLimits{MaxIterations: DefaultMaxIterations, TokenBudget: DefaultTokenBudget}
	if role != nil {
		if role.MaxIterations > 0 {
			l.MaxIterations = role.MaxIterations
		}
		if role.TokenBudget > 0 {
			l.TokenBudget = role.TokenBudget
		}
	}
	l.MaxIterations += extraIterations
	l.TokenBudget += extraTokens
	return l
}

// TurnExhausted reports whether a turn must pause and ask to continue.
func TurnExhausted(l TurnLimits, iterations int, used types.Usage) (bool, string) {
	if iterations >= l.MaxIterations {
		return true, fmt.Sprintf("reached %d iterations", l.MaxIterations)
	}
	if l.TokenBudget > 0 && used.Total() >= l.TokenBudget {
		return true, fmt.Sprintf("used %d of %d tokens", used.Total(), l.TokenBudget)
	}
	return false, ""
}

// DailyExceeded reports whether a tenant's daily budget is spent.
func DailyExceeded(used, limit int64) bool { return limit > 0 && used >= limit }

// Day is the counter bucket of t (UTC).
func Day(t time.Time) string { return t.UTC().Format("20060102") }
