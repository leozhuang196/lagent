package admin

import (
	"encoding/json"
	"reflect"
	"sort"
)

// Change is one field-level difference between two asset contents. Path is
// dotted for nested objects; arrays are compared as a whole because element
// identity (e.g. a moved workflow step) is not knowable generically.
type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// envelopeFields are bookkeeping, not content: a diff of two versions should
// show what the agent will behave differently on, not that ids differ.
var envelopeFields = []string{"id", "tenant_id", "version", "created_at", "updated_at", "ver", "status", "published_at", "published_by", "note"}

// contentMap decodes v (any JSON-able asset) and drops envelope fields.
func contentMap(v any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return map[string]any{}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	for _, f := range envelopeFields {
		delete(m, f)
	}
	return m
}

// Diff compares the content of two assets (nil means "absent").
func Diff(before, after any) []Change {
	out := []Change{}
	diffMaps("", contentMap(before), contentMap(after), &out)
	return out
}

func diffMaps(prefix string, a, b map[string]any, out *[]Change) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		x, y := a[k], b[k]
		mx, okx := x.(map[string]any)
		my, oky := y.(map[string]any)
		if okx && oky {
			diffMaps(path, mx, my, out)
			continue
		}
		if empty(x) && empty(y) {
			continue
		}
		if !reflect.DeepEqual(x, y) {
			*out = append(*out, Change{Path: path, Before: x, After: y})
		}
	}
}

// empty treats omitted, null, "", 0, false and [] alike: omitempty makes them
// indistinguishable in storage anyway.
func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case float64:
		return t == 0
	case bool:
		return !t
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}
