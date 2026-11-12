package mongo

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// canon renders a query document as relaxed extended JSON for comparison.
func canon(t *testing.T, d bson.D) string {
	t.Helper()
	b, err := bson.MarshalExtJSON(marshal(t, d), false, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTranslateFilter(t *testing.T) {
	ts := time.Date(2026, 1, 1, 3, 0, 0, 0, time.FixedZone("x", 3600))
	cases := []struct {
		name string
		in   repo.Filter
		want string
	}{
		{"empty", nil, `{}`},
		{"bare_eq", repo.Filter{"status": "idle"}, `{"status":"idle"}`},
		{"eq_nil", repo.Filter{"error": nil}, `{"error":null}`},
		{"id_alias", repo.Filter{"id": "ses_1"}, `{"_id":"ses_1"}`},
		{"underscore_id", repo.Filter{"_id": repo.In("a", "b")}, `{"_id":{"$in":["a","b"]}}`},
		{"dotted", repo.Filter{"created_by.id": "u1"}, `{"created_by.id":"u1"}`},
		{"ne", repo.Filter{"enabled": repo.Ne(true)}, `{"enabled":{"$ne":true}}`},
		{"in_typed_slice", repo.Filter{"name": repo.Op{Kind: repo.OpIn, Value: []string{"s", "x"}}}, `{"name":{"$in":["s","x"]}}`},
		{"range_int", repo.Filter{"seq": repo.Gt(3)}, `{"seq":{"$gt":3}}`},
		{"range_time", repo.Filter{"next_run_at": repo.Lte(ts)}, `{"next_run_at":{"$lte":{"$date":"2026-01-01T02:00:00Z"}}}`},
		{"range_time_ptr", repo.Filter{"next_run_at": repo.Lt(&ts)}, `{"next_run_at":{"$lt":{"$date":"2026-01-01T02:00:00Z"}}}`},
		{"range_nil_matches_nothing", repo.Filter{"x": repo.Gte(nil)}, `{"x":{"$in":[]}}`},
		{"exists_true", repo.Filter{"ended_at": repo.Exists(true)}, `{"ended_at":{"$ne":null}}`},
		{"exists_false", repo.Filter{"ended_at": repo.Exists(false)}, `{"ended_at":null}`},
		{"sorted_keys", repo.Filter{"b": 1, "a": 2, "c": repo.Lt(int64(5))}, `{"a":2,"b":1,"c":{"$lt":5}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := TranslateFilter(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := canon(t, d); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestTranslateFilterErrors(t *testing.T) {
	for name, f := range map[string]repo.Filter{
		"in_not_slice":    {"a": repo.Op{Kind: repo.OpIn, Value: 3}},
		"exists_not_bool": {"a": repo.Op{Kind: repo.OpExists, Value: "yes"}},
		"unknown_op":      {"a": repo.Op{Kind: "regex", Value: ".*"}},
	} {
		if _, err := TranslateFilter(f); !errs.Is(err, errs.CodeInvalid) {
			t.Errorf("%s: want invalid, got %v", name, err)
		}
	}
}

func TestAndMergesOrNests(t *testing.T) {
	flat := and(bson.E{Key: "tenant_id", Value: "t"}, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: "a"}}})
	if got := canon(t, flat); got != `{"tenant_id":"t","_id":{"$gt":"a"}}` {
		t.Fatalf("flat: %s", got)
	}
	nested := and(bson.E{Key: "_id", Value: "x"}, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: "a"}}})
	if got := canon(t, nested); got != `{"$and":[{"_id":"x"},{"_id":{"$gt":"a"}}]}` {
		t.Fatalf("nested: %s", got)
	}
}

func TestIndexModels(t *testing.T) {
	ms := IndexModels(repo.Specs["step_runs"])
	if len(ms) != 3 {
		t.Fatalf("want 3 indexes, got %d", len(ms))
	}
	if got := canon(t, ms[1].Keys.(bson.D)); got != `{"tenant_id":1,"run_id":1,"step_id":1,"index":1}` {
		t.Fatalf("keys: %s", got)
	}
	for _, spec := range repo.Specs {
		for _, m := range IndexModels(spec) {
			if k := m.Keys.(bson.D); k[0].Key != "tenant_id" {
				t.Fatalf("%s: index not prefixed by tenant_id: %v", spec.Name, k)
			}
		}
	}
}

// marshal encodes v with the package registry, as the driver would.
func marshal(t *testing.T, v any) bson.Raw {
	t.Helper()
	var buf bytes.Buffer
	enc := bson.NewEncoder(bson.NewDocumentWriter(&buf))
	enc.SetRegistry(Registry)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bson.Raw(buf.Bytes())
}

// unmarshal decodes raw into v with the package registry.
func unmarshal(t *testing.T, raw bson.Raw, v any) {
	t.Helper()
	dec := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
	dec.SetRegistry(Registry)
	if err := dec.Decode(v); err != nil {
		t.Fatal(err)
	}
}

// Platform status and scheduler sweeps issue unscoped reads without filters.
func TestEmptyUnscopedQueryIsDocument(t *testing.T) {
	filter, err := query(scope.AllTenants(context.Background()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if filter == nil {
		t.Fatal("empty query must be an empty document, not BSON null")
	}
	if got := canon(t, filter); got != "{}" {
		t.Fatalf("filter = %s", got)
	}
}
