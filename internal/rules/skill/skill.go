// Package skill selects which skill headers are injected and which bodies a
// session may load.
package skill

import (
	"sort"

	"github.com/eternityx/lagent/internal/core/types"
)

// Header is what stays in context: name and description only.
type Header struct {
	Key         string
	Name        string
	Description string
	Priority    int
}

// Headers keeps the highest-priority headers within budget characters.
// Output is sorted by key so the prompt prefix stays stable.
func Headers(skills []types.Skill, budget int) (kept []Header, dropped []string) {
	sorted := append([]types.Skill(nil), skills...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].Key < sorted[j].Key
	})
	used := 0
	for _, s := range sorted {
		cost := len(s.Name) + len(s.Description) + 8
		if budget > 0 && used+cost > budget {
			dropped = append(dropped, s.Key)
			continue
		}
		used += cost
		kept = append(kept, Header{Key: s.Key, Name: s.Name, Description: s.Description, Priority: s.Priority})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	return kept, dropped
}

// CanLoad reports whether a role may load the body of skill name.
func CanLoad(roleSkills []string, name string) bool {
	for _, s := range roleSkills {
		if s == name || s == "*" {
			return true
		}
	}
	return false
}
