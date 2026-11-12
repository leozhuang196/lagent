// Package memory merges layered memory spaces under an injection budget.
package memory

import (
	"sort"

	"github.com/eternityx/lagent/internal/core/types"
)

// Merge orders entries by space priority then entry priority (both desc),
// de-duplicates keys (the higher-priority space wins) and keeps entries until
// budget characters are used. It returns kept entries and dropped keys.
func Merge(spaces []types.MemorySpace, entries []types.MemoryEntry, budget int) (kept []types.MemoryEntry, dropped []string) {
	prio := map[string]int{}
	for _, s := range spaces {
		prio[s.Name] = s.Priority
	}
	sorted := append([]types.MemoryEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if prio[a.Space] != prio[b.Space] {
			return prio[a.Space] > prio[b.Space]
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.Space != b.Space {
			return a.Space < b.Space
		}
		return a.Key < b.Key
	})
	seen := map[string]bool{}
	used := 0
	for _, e := range sorted {
		if seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		cost := len(e.Key) + len(e.Content) + 4
		if budget > 0 && used+cost > budget {
			dropped = append(dropped, e.Space+"/"+e.Key)
			continue
		}
		used += cost
		kept = append(kept, e)
	}
	// Stable output order (by key) keeps the prompt prefix cacheable.
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Space != kept[j].Space {
			return kept[i].Space < kept[j].Space
		}
		return kept[i].Key < kept[j].Key
	})
	return kept, dropped
}

// Writable reports whether the remember tool may write to space.
func Writable(space types.MemorySpace) bool { return !space.ReadOnly }
