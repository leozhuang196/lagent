package flowdef

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
)

var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.\-]+)\s*\}\}`)

// Ref is a parsed placeholder.
type Ref struct {
	Raw   string
	Space string // params, item, frozen, steps
	Path  []string
}

// Refs lists the placeholders of a template.
func Refs(tpl string) []Ref {
	var out []Ref
	for _, m := range placeholder.FindAllStringSubmatch(tpl, -1) {
		parts := strings.Split(m[1], ".")
		out = append(out, Ref{Raw: m[0], Space: parts[0], Path: parts[1:]})
	}
	return out
}

// render replaces placeholders using lookup; ok=false leaves the placeholder as is.
func render(tpl string, lookup func(Ref) (string, bool, error)) (string, error) {
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(tpl, func(s string) string {
		m := placeholder.FindStringSubmatch(s)
		parts := strings.Split(m[1], ".")
		v, ok, err := lookup(Ref{Raw: s, Space: parts[0], Path: parts[1:]})
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if !ok {
			return s
		}
		return v
	})
	return out, firstErr
}

// jsonText renders a JSON value: strings bare, everything else as JSON.
func jsonText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// dig walks a JSON value by path.
func dig(raw json.RawMessage, path []string) (json.RawMessage, error) {
	cur := raw
	for _, p := range path {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cur, &m); err != nil {
			return nil, errs.Invalid("cannot read %q of a non-object", p)
		}
		v, ok := m[p]
		if !ok {
			return nil, errs.Invalid("field %q not found", p)
		}
		cur = v
	}
	return cur, nil
}
