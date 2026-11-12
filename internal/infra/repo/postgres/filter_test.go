package postgres

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/repo"
)

func TestTranslateFilter(t *testing.T) {
	ts := time.Date(2026, 1, 1, 3, 0, 0, 0, time.FixedZone("x", 3600))
	utc := ts.UTC()
	timeExprNext := `(CASE WHEN jsonb_typeof(doc #> '{next_run_at}') = 'string' AND doc #>> '{next_run_at}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' THEN (doc #>> '{next_run_at}')::timestamptz END)`
	cases := []struct {
		name   string
		in     repo.Filter
		offset int
		sql    string
		args   []any
	}{
		{"empty", nil, 0, `TRUE`, nil},
		{"bare_eq_string", repo.Filter{"status": "idle"}, 1,
			`(doc @> $2::jsonb OR doc @> $3::jsonb)`, []any{`{"status":"idle"}`, `{"status":["idle"]}`}},
		{"eq_bool_number", repo.Filter{"enabled": true, "seq": 3}, 0,
			`(doc @> $1::jsonb OR doc @> $2::jsonb) AND (doc @> $3::jsonb OR doc @> $4::jsonb)`,
			[]any{`{"enabled":true}`, `{"enabled":[true]}`, `{"seq":3}`, `{"seq":[3]}`}},
		{"dotted", repo.Filter{"created_by.id": "u1"}, 0,
			`(doc @> $1::jsonb OR doc @> $2::jsonb)`, []any{`{"created_by":{"id":"u1"}}`, `{"created_by":{"id":["u1"]}}`}},
		{"eq_nil", repo.Filter{"error": nil}, 0, `coalesce(jsonb_typeof(doc #> '{error}'), 'null') = 'null'`, nil},
		{"eq_array_exact", repo.Filter{"tools": []string{"a", "b"}}, 0, `(doc #> '{tools}' = $1::jsonb)`, []any{`["a","b"]`}},
		{"eq_time", repo.Filter{"next_run_at": ts}, 0, `(` + timeExprNext + ` = $1::timestamptz)`, []any{utc}},
		{"ne", repo.Filter{"enabled": repo.Ne(true)}, 0,
			`NOT coalesce((doc @> $1::jsonb OR doc @> $2::jsonb), false)`, []any{`{"enabled":true}`, `{"enabled":[true]}`}},
		{"in", repo.Filter{"name": repo.In("s", "x")}, 0,
			`((doc @> $1::jsonb OR doc @> $2::jsonb) OR (doc @> $3::jsonb OR doc @> $4::jsonb))`,
			[]any{`{"name":"s"}`, `{"name":["s"]}`, `{"name":"x"}`, `{"name":["x"]}`}},
		{"in_empty", repo.Filter{"name": repo.In[string]()}, 0, `FALSE`, nil},
		{"id_eq", repo.Filter{"_id": "ses_1"}, 2, `id = $3`, []any{"ses_1"}},
		{"id_alias_in", repo.Filter{"id": repo.In("a", "b")}, 0, `id = ANY($1::text[])`, []any{[]string{"a", "b"}}},
		{"id_range", repo.Filter{"_id": repo.Gt("a")}, 0, `id > $1`, []any{"a"}},
		{"range_time", repo.Filter{"next_run_at": repo.Lte(ts)}, 0, timeExprNext + ` <= $1::timestamptz`, []any{utc}},
		{"range_time_ptr", repo.Filter{"next_run_at": repo.Lt(&ts)}, 0, timeExprNext + ` < $1::timestamptz`, []any{utc}},
		{"range_rfc3339_string", repo.Filter{"next_run_at": repo.Gt("2026-01-01T02:00:00Z")}, 0, timeExprNext + ` > $1::timestamptz`, []any{utc}},
		{"range_int", repo.Filter{"seq": repo.Gte(int32(4))}, 0,
			`(CASE WHEN jsonb_typeof(doc #> '{seq}') = 'number' THEN (doc #>> '{seq}')::numeric END) >= $1::numeric`, []any{int64(4)}},
		{"range_float", repo.Filter{"score": repo.Lt(0.5)}, 0,
			`(CASE WHEN jsonb_typeof(doc #> '{score}') = 'number' THEN (doc #>> '{score}')::numeric END) < $1::numeric`, []any{0.5}},
		{"range_string", repo.Filter{"key": repo.Gt("m")}, 0,
			`(CASE WHEN jsonb_typeof(doc #> '{key}') = 'string' THEN (doc #>> '{key}') COLLATE "C" END) > $1`, []any{"m"}},
		{"range_bool", repo.Filter{"enabled": repo.Gt(false)}, 0,
			`(CASE WHEN jsonb_typeof(doc #> '{enabled}') = 'boolean' THEN (doc #>> '{enabled}')::boolean END) > $1::boolean`, []any{false}},
		{"range_nil", repo.Filter{"x": repo.Lt(nil)}, 0, `FALSE`, nil},
		{"exists_true", repo.Filter{"ended_at": repo.Exists(true)}, 0, `NOT coalesce(jsonb_typeof(doc #> '{ended_at}'), 'null') = 'null'`, nil},
		{"exists_false", repo.Filter{"ended_at": repo.Exists(false)}, 0, `coalesce(jsonb_typeof(doc #> '{ended_at}'), 'null') = 'null'`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := TranslateFilter(tc.in, tc.offset)
			if err != nil {
				t.Fatal(err)
			}
			if sql != tc.sql {
				t.Fatalf("sql\n got  %s\n want %s", sql, tc.sql)
			}
			if !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("args\n got  %#v\n want %#v", args, tc.args)
			}
		})
	}
}

func TestTranslateFilterErrors(t *testing.T) {
	for name, f := range map[string]repo.Filter{
		"injection":       {"a'; DROP TABLE x; --": 1},
		"empty_segment":   {"a..b": 1},
		"in_not_slice":    {"a": repo.Op{Kind: repo.OpIn, Value: 3}},
		"exists_not_bool": {"a": repo.Op{Kind: repo.OpExists, Value: "yes"}},
		"unknown_op":      {"a": repo.Op{Kind: "regex", Value: ".*"}},
		"id_not_string":   {"_id": 3},
		"range_struct":    {"a": repo.Gt(struct{}{})},
	} {
		if _, _, err := TranslateFilter(f, 0); !errs.Is(err, errs.CodeInvalid) {
			t.Errorf("%s: want invalid, got %v", name, err)
		}
	}
}

func TestTableDDL(t *testing.T) {
	ddl := TableDDL(repo.Specs["step_runs"])
	want := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS "step_runs_run_id_step_id_index_uq" ON "step_runs" (tenant_id, (doc #>> '{run_id}'), (doc #>> '{step_id}'), (doc #>> '{index}')) NULLS NOT DISTINCT`,
		`CREATE INDEX IF NOT EXISTS "step_runs_step_key_idx" ON "step_runs" (tenant_id, (doc #>> '{step_key}'))`,
	}
	if !reflect.DeepEqual(ddl[3:], want) {
		t.Fatalf("got %q", ddl[3:])
	}
	dotted := TableDDL(repo.Specs["sessions"])
	if !strings.Contains(strings.Join(dotted, "\n"), `"sessions_created_by_id_idx" ON "sessions" (tenant_id, (doc #>> '{created_by,id}'))`) {
		t.Fatalf("dotted index: %q", dotted)
	}
}

// TestMigrationFileInSync keeps the reference SQL file equal to DDL().
// Regenerate with LAGENT_UPDATE_MIGRATION=1 go test ./internal/infra/repo/postgres/.
func TestMigrationFileInSync(t *testing.T) {
	const path = "../../../../migrations/postgres/0001_init.sql"
	want := MigrationSQL()
	if os.Getenv("LAGENT_UPDATE_MIGRATION") != "" {
		if err := os.MkdirAll("../../../../migrations/postgres", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale; regenerate with LAGENT_UPDATE_MIGRATION=1", path)
	}
}
