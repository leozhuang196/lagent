package memory

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/infra/repo"
)

// Match evaluates a filter against a JSON-decoded document.
func Match(data map[string]any, f repo.Filter) bool {
	for path, want := range f {
		got, present := lookup(data, path)
		op, isOp := want.(repo.Op)
		if !isOp {
			op = repo.Op{Kind: repo.OpEq, Value: want}
		}
		if !evalOp(got, present, op) {
			return false
		}
	}
	return true
}

func evalOp(got any, present bool, op repo.Op) bool {
	switch op.Kind {
	case repo.OpExists:
		b, _ := op.Value.(bool)
		return (present && got != nil) == b
	case repo.OpEq:
		return equalOrContains(got, normalize(op.Value))
	case repo.OpNe:
		return !equalOrContains(got, normalize(op.Value))
	case repo.OpIn:
		vs, _ := op.Value.([]any)
		for _, v := range vs {
			if equalOrContains(got, normalize(v)) {
				return true
			}
		}
		return false
	case repo.OpLt, repo.OpLte, repo.OpGt, repo.OpGte:
		if !present || got == nil {
			return false
		}
		c, ok := compare(got, normalize(op.Value))
		if !ok {
			return false
		}
		switch op.Kind {
		case repo.OpLt:
			return c < 0
		case repo.OpLte:
			return c <= 0
		case repo.OpGt:
			return c > 0
		default:
			return c >= 0
		}
	}
	return false
}

// lookup follows a dotted path; "_id" maps to the JSON "id".
func lookup(data map[string]any, path string) (any, bool) {
	if path == "_id" {
		path = "id"
	}
	var cur any = data
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// normalize converts a Go value to its JSON-decoded form for comparison.
func normalize(v any) any {
	switch t := v.(type) {
	case nil, string, bool, float64:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case *time.Time:
		if t == nil {
			return nil
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// equalOrContains matches scalars, and array fields containing the value (Mongo semantics).
func equalOrContains(got, want any) bool {
	if arr, ok := got.([]any); ok {
		if _, wantArr := want.([]any); !wantArr {
			for _, e := range arr {
				if equal(e, want) {
					return true
				}
			}
			return false
		}
	}
	if want == nil {
		return got == nil
	}
	return equal(got, want)
}

func equal(a, b any) bool {
	if c, ok := compare(a, b); ok {
		return c == 0
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		ta, ea := time.Parse(time.RFC3339Nano, x)
		tb, eb := time.Parse(time.RFC3339Nano, y)
		if ea == nil && eb == nil {
			return ta.Compare(tb), true
		}
		return strings.Compare(x, y), true
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		if x == y {
			return 0, true
		}
		return 1, true
	}
	return 0, false
}
