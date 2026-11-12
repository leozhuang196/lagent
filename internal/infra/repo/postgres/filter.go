package postgres

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// args collects positional parameters; placeholders continue after offset.
type args struct {
	offset int
	vals   []any
}

func (a *args) add(v any) string {
	a.vals = append(a.vals, v)
	return fmt.Sprintf("$%d", a.offset+len(a.vals))
}

// TranslateFilter turns f into a SQL boolean expression over the columns
// (id, doc) of a collection table. Placeholders start at $offset+1; the
// returned args fill them in order. It is pure so the semantics can be
// unit-tested without a server.
//
// The mapping keeps the memory backend's semantics (repo/memory/match.go):
//   - "_id" (or "id") is the id column; other paths address the JSON document,
//     dots for nesting, as doc #> '{a,b}';
//   - a bare scalar is equality by jsonb containment: doc @> {"a":{"b":v}}
//     OR doc @> {"a":{"b":[v]}}, so an array field matches when it contains v
//     (Mongo semantics) and numbers compare numerically. Both forms use the
//     GIN index on doc;
//   - nil equality matches a missing or null field; an array/object value
//     must match exactly;
//   - ne / not-in are the negation of equality, so a missing field matches;
//   - range operators are typed by the filter value: numbers cast to numeric,
//     time.Time (and strings that parse as RFC 3339) cast to timestamptz,
//     booleans to boolean, other strings compare bytewise (COLLATE "C"). A
//     stored value of another JSON type never matches, as in memory;
//   - Exists(true) means "present and not null".
//
// Keys are emitted in sorted order so the output is deterministic.
func TranslateFilter(f repo.Filter, offset int) (string, []any, error) {
	a := &args{offset: offset}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	conds := make([]string, 0, len(keys))
	for _, k := range keys {
		c, err := translateOne(a, k, f[k])
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, c)
	}
	if len(conds) == 0 {
		return "TRUE", nil, nil
	}
	return strings.Join(conds, " AND "), a.vals, nil
}

var segment = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// splitPath validates a dotted path; segments are inlined into SQL literals,
// so only identifier characters are accepted.
func splitPath(p string) ([]string, error) {
	parts := strings.Split(p, ".")
	for _, s := range parts {
		if !segment.MatchString(s) {
			return nil, errs.Invalid("filter: invalid field path %q", p)
		}
	}
	return parts, nil
}

// jsonPath is the jsonb at path; textPath its text form (NULL when missing).
func jsonPath(parts []string) string { return "doc #> '{" + strings.Join(parts, ",") + "}'" }
func textPath(parts []string) string { return "doc #>> '{" + strings.Join(parts, ",") + "}'" }

// isNull is true when the path is missing or JSON null.
func isNull(parts []string) string {
	return "coalesce(jsonb_typeof(" + jsonPath(parts) + "), 'null') = 'null'"
}

func translateOne(a *args, path string, want any) (string, error) {
	op, isOp := want.(repo.Op)
	if !isOp {
		op = repo.Op{Kind: repo.OpEq, Value: want}
	}
	if path == "_id" || path == "id" {
		return idCond(a, op)
	}
	parts, err := splitPath(path)
	if err != nil {
		return "", err
	}
	switch op.Kind {
	case repo.OpEq:
		return eqCond(a, parts, op.Value)
	case repo.OpNe:
		c, err := eqCond(a, parts, op.Value)
		if err != nil {
			return "", err
		}
		return "NOT coalesce(" + c + ", false)", nil
	case repo.OpIn:
		vs, err := sliceValues(op.Value)
		if err != nil {
			return "", err
		}
		if len(vs) == 0 {
			return "FALSE", nil
		}
		ors := make([]string, len(vs))
		for i, v := range vs {
			if ors[i], err = eqCond(a, parts, v); err != nil {
				return "", err
			}
		}
		return "(" + strings.Join(ors, " OR ") + ")", nil
	case repo.OpLt, repo.OpLte, repo.OpGt, repo.OpGte:
		return rangeCond(a, parts, op)
	case repo.OpExists:
		b, ok := op.Value.(bool)
		if !ok {
			return "", errs.Invalid("filter %s: exists needs a bool", path)
		}
		if b {
			return "NOT " + isNull(parts), nil
		}
		return isNull(parts), nil
	}
	return "", errs.Invalid("filter %s: unknown operator %q", path, op.Kind)
}

var sqlOps = map[repo.OpKind]string{repo.OpLt: "<", repo.OpLte: "<=", repo.OpGt: ">", repo.OpGte: ">="}

// idCond filters the id column; ids are strings and the column is COLLATE "C"
// so ordering matches Go string comparison (and the memory backend).
func idCond(a *args, op repo.Op) (string, error) {
	str := func(v any) (string, error) {
		s, ok := v.(string)
		if !ok {
			return "", errs.Invalid("filter _id: want a string, got %T", v)
		}
		return s, nil
	}
	switch op.Kind {
	case repo.OpEq, repo.OpNe:
		if op.Value == nil {
			if op.Kind == repo.OpEq {
				return "FALSE", nil
			}
			return "TRUE", nil
		}
		s, err := str(op.Value)
		if err != nil {
			return "", err
		}
		if op.Kind == repo.OpEq {
			return "id = " + a.add(s), nil
		}
		return "id <> " + a.add(s), nil
	case repo.OpIn:
		vs, err := sliceValues(op.Value)
		if err != nil {
			return "", err
		}
		ss := make([]string, 0, len(vs))
		for _, v := range vs {
			s, err := str(v)
			if err != nil {
				return "", err
			}
			ss = append(ss, s)
		}
		return "id = ANY(" + a.add(ss) + "::text[])", nil
	case repo.OpLt, repo.OpLte, repo.OpGt, repo.OpGte:
		s, err := str(op.Value)
		if err != nil {
			return "", err
		}
		return "id " + sqlOps[op.Kind] + " " + a.add(s), nil
	case repo.OpExists:
		b, _ := op.Value.(bool)
		if b {
			return "TRUE", nil
		}
		return "FALSE", nil
	}
	return "", errs.Invalid("filter _id: unknown operator %q", op.Kind)
}

// nest builds {"a":{"b":v}} for a containment query.
func nest(parts []string, v json.RawMessage) string {
	var b strings.Builder
	for _, p := range parts {
		k, _ := json.Marshal(p)
		b.WriteString("{")
		b.Write(k)
		b.WriteString(":")
	}
	b.Write(v)
	b.WriteString(strings.Repeat("}", len(parts)))
	return b.String()
}

func eqCond(a *args, parts []string, v any) (string, error) {
	v = normalize(v)
	if v == nil {
		return isNull(parts), nil
	}
	if t, ok := v.(time.Time); ok {
		return "(" + timeExpr(parts) + " = " + a.add(t) + "::timestamptz)", nil
	}
	js, err := json.Marshal(v)
	if err != nil {
		return "", errs.Invalid("filter: cannot encode %T: %v", v, err)
	}
	switch js[0] {
	case 'n':
		return isNull(parts), nil
	case '[', '{':
		return "(" + jsonPath(parts) + " = " + a.add(string(js)) + "::jsonb)", nil
	}
	// Scalar: the field equals v, or is an array containing v.
	scalar := a.add(nest(parts, js))
	inArray := a.add(nest(parts, json.RawMessage("["+string(js)+"]")))
	return "(doc @> " + scalar + "::jsonb OR doc @> " + inArray + "::jsonb)", nil
}

// timeExpr reads the path as timestamptz, NULL unless it looks like an
// RFC 3339 string (a bad cast would abort the whole query).
func timeExpr(parts []string) string {
	return "(CASE WHEN jsonb_typeof(" + jsonPath(parts) + ") = 'string' AND " + textPath(parts) +
		" ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' THEN (" + textPath(parts) + ")::timestamptz END)"
}

// typed reads the path as text only when its JSON type is jsonType.
func typed(parts []string, jsonType, cast string) string {
	return "(CASE WHEN jsonb_typeof(" + jsonPath(parts) + ") = '" + jsonType + "' THEN (" + textPath(parts) + ")" + cast + " END)"
}

func rangeCond(a *args, parts []string, op repo.Op) (string, error) {
	sqlOp := sqlOps[op.Kind]
	v := normalize(op.Value)
	if v == nil {
		return "FALSE", nil // memory never matches a range against null
	}
	switch x := v.(type) {
	case time.Time:
		return timeExpr(parts) + " " + sqlOp + " " + a.add(x) + "::timestamptz", nil
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return timeExpr(parts) + " " + sqlOp + " " + a.add(t) + "::timestamptz", nil
		}
		return typed(parts, "string", ` COLLATE "C"`) + " " + sqlOp + " " + a.add(x), nil
	case bool:
		return typed(parts, "boolean", "::boolean") + " " + sqlOp + " " + a.add(x) + "::boolean", nil
	}
	rv := reflect.ValueOf(v)
	var n any
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n = rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n = rv.Uint()
	case reflect.Float32, reflect.Float64:
		n = rv.Float()
	case reflect.String: // named string types
		return rangeCond(a, parts, repo.Op{Kind: op.Kind, Value: rv.String()})
	case reflect.Bool:
		return rangeCond(a, parts, repo.Op{Kind: op.Kind, Value: rv.Bool()})
	default:
		return "", errs.Invalid("filter: cannot range-compare %T", v)
	}
	return typed(parts, "number", "::numeric") + " " + sqlOp + " " + a.add(n) + "::numeric", nil
}

// normalize unwraps time pointers and puts times in UTC.
func normalize(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t == nil {
			return nil
		}
		return t.UTC()
	}
	return v
}

// sliceValues accepts the []any produced by repo.In as well as any other slice.
func sliceValues(v any) ([]any, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, errs.Invalid("filter: in needs a slice, got %T", v)
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, nil
}
