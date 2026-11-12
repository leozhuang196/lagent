package mongo

import (
	"reflect"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// TranslateFilter turns a repo.Filter into a Mongo query document. It is pure
// so the semantics can be unit-tested without a server.
//
// The mapping keeps the memory backend's semantics (repo/memory/match.go):
//   - a bare value is equality; against an array field it matches containment,
//     which is Mongo's native behaviour;
//   - nil equality matches a missing or null field (also native);
//   - ne / not-in match documents where the field is missing;
//   - range operators never match a missing or null field (native type
//     bracketing);
//   - Exists(true) means "present and not null", as in memory.
//
// Keys are emitted in sorted order so the output is deterministic.
func TranslateFilter(f repo.Filter) (bson.D, error) {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(bson.D, 0, len(keys))
	for _, k := range keys {
		e, err := translateOne(fieldPath(k), f[k])
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// fieldPath maps the JSON id name to Mongo's primary key; every other path is
// already a bson path (bson and json names agree except for the id).
func fieldPath(k string) string {
	if k == "id" {
		return "_id"
	}
	return k
}

func translateOne(path string, want any) (bson.E, error) {
	op, isOp := want.(repo.Op)
	if !isOp {
		op = repo.Op{Kind: repo.OpEq, Value: want}
	}
	switch op.Kind {
	case repo.OpEq:
		return bson.E{Key: path, Value: normalize(op.Value)}, nil
	case repo.OpNe:
		return bson.E{Key: path, Value: bson.D{{Key: "$ne", Value: normalize(op.Value)}}}, nil
	case repo.OpIn:
		vs, err := sliceValues(op.Value)
		if err != nil {
			return bson.E{}, err
		}
		return bson.E{Key: path, Value: bson.D{{Key: "$in", Value: vs}}}, nil
	case repo.OpLt, repo.OpLte, repo.OpGt, repo.OpGte:
		v := normalize(op.Value)
		if v == nil {
			// memory never matches a range against null, but Mongo's
			// $lte:null would match nulls and missing fields: match nothing.
			return bson.E{Key: path, Value: bson.D{{Key: "$in", Value: bson.A{}}}}, nil
		}
		return bson.E{Key: path, Value: bson.D{{Key: "$" + string(op.Kind), Value: v}}}, nil
	case repo.OpExists:
		b, ok := op.Value.(bool)
		if !ok {
			return bson.E{}, errs.Invalid("filter %s: exists needs a bool", path)
		}
		// $exists alone would treat an explicit null as present; memory does not.
		if b {
			return bson.E{Key: path, Value: bson.D{{Key: "$ne", Value: nil}}}, nil
		}
		return bson.E{Key: path, Value: nil}, nil
	}
	return bson.E{}, errs.Invalid("filter %s: unknown operator %q", path, op.Kind)
}

// normalize unwraps time pointers so nil pointers become null and times are
// compared as BSON dates in UTC.
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
func sliceValues(v any) (bson.A, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, errs.Invalid("filter: in needs a slice, got %T", v)
	}
	out := make(bson.A, rv.Len())
	for i := range out {
		out[i] = normalize(rv.Index(i).Interface())
	}
	return out, nil
}

// and merges conditions into one document, falling back to $and when two
// conditions constrain the same path (e.g. a caller's _id filter plus the
// pagination cursor).
func and(conds ...bson.E) bson.D {
	// Unscoped reads without filters still need a BSON document. A nil D
	// encodes as null, which the MongoDB driver rejects at the top level.
	if len(conds) == 0 {
		return bson.D{}
	}
	seen := make(map[string]bool, len(conds))
	dup := false
	for _, c := range conds {
		if seen[c.Key] {
			dup = true
			break
		}
		seen[c.Key] = true
	}
	if !dup {
		return bson.D(conds)
	}
	parts := make(bson.A, len(conds))
	for i, c := range conds {
		parts[i] = bson.D{c}
	}
	return bson.D{{Key: "$and", Value: parts}}
}
