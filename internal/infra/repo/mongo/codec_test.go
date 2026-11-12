package mongo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

// roundTrip encodes v with the package registry, decodes into a fresh value
// and checks the JSON views are identical (JSON is what the API exposes).
func roundTrip[E any](t *testing.T, v *E) *E {
	t.Helper()
	raw := marshal(t, v)
	out := new(E)
	unmarshal(t, raw, out)
	a, _ := json.Marshal(v)
	b, _ := json.Marshal(out)
	if string(a) != string(b) {
		t.Fatalf("round trip differs:\n in  %s\n out %s", a, b)
	}
	return out
}

// ms is a timestamp BSON can hold exactly (millisecond precision, UTC).
var ms = time.Date(2026, 3, 4, 5, 6, 7, 8_000_000, time.UTC)

func testMeta(id string) types.Meta {
	return types.Meta{ID: id, TenantID: "tA", Version: 3, CreatedAt: ms, UpdatedAt: ms.Add(time.Second)}
}

func TestBSONInlineMetaAndRawMessage(t *testing.T) {
	end := ms.Add(time.Minute)
	tc := &types.ToolCall{
		Meta:    testMeta("tc_1"),
		Tool:    "shell",
		Input:   json.RawMessage(`{"cmd": "ls",  "args":[1,2.5,null]}`),
		Status:  types.ToolCallStatus("done"),
		EndedAt: &end,
	}
	out := roundTrip(t, tc)
	// RawMessage must survive byte-for-byte, whitespace included.
	if string(out.Input) != string(tc.Input) {
		t.Fatalf("raw message changed: %s", out.Input)
	}
	if out.StartedAt != nil || !out.EndedAt.Equal(end) {
		t.Fatalf("time pointers: %v %v", out.StartedAt, out.EndedAt)
	}
	raw := marshal(t, tc)
	for _, k := range []string{"_id", "tenant_id", "version", "created_at", "updated_at"} {
		if _, err := raw.LookupErr(k); err != nil {
			t.Fatalf("meta field %s not inlined: %v", k, err)
		}
	}
	if raw.Lookup("_id").StringValue() != "tc_1" {
		t.Fatal("_id is not the entity id")
	}
	// nil RawMessage without omitempty is stored as null and decodes to nil.
	empty := roundTrip(t, &types.ToolCall{Meta: testMeta("tc_2")})
	if empty.Input != nil {
		t.Fatalf("nil input became %q", empty.Input)
	}
}

func TestBSONNestedStructsAndMaps(t *testing.T) {
	run := &types.WorkflowRun{
		Meta:   testMeta("run_1"),
		DefKey: "d",
		Frozen: types.Frozen{Now: ms, TZ: "Asia/Shanghai", Seed: 42, Params: map[string]json.RawMessage{"n": json.RawMessage(`7`), "s": json.RawMessage(`"x"`)}},
		Status: types.RunStatus("running"),
		Error:  &types.TurnError{Code: "c", Message: "m"},
		Usage:  types.Usage{InputTokens: 10, OutputTokens: 5},
		Params: map[string]json.RawMessage{"obj": json.RawMessage(`{"a":[1,{"b":true}]}`)},
	}
	roundTrip(t, run)

	pub := ms
	role := &types.Role{Meta: testMeta("rol_1"), Versioned: types.Versioned{Key: "k", Ver: 2, PublishedAt: &pub,
		PublishedBy: &tenant.Principal{Kind: tenant.KindAPIKey, ID: "u", TenantID: "tA"}}, Tools: []string{"a", "b"}}
	out := roundTrip(t, role)
	raw := marshal(t, role)
	if _, err := raw.LookupErr("key"); err != nil {
		t.Fatal("Versioned not inlined")
	}
	if out.PublishedBy == nil || out.PublishedBy.ID != "u" {
		t.Fatalf("pointer struct lost: %+v", out.PublishedBy)
	}

	srv := &types.MCPServer{Meta: testMeta("mcp_1"), Name: "n", Env: map[string]string{"A": "1"}, Command: []string{"x"}}
	roundTrip(t, srv)
}

func TestBSONAnyMapUsesJSONShapes(t *testing.T) {
	a := &types.AuditLog{Meta: testMeta("aud_1"), Action: "x", Detail: map[string]any{
		"n":    3,
		"f":    1.5,
		"when": ms,
		"list": []any{"a", 2, map[string]any{"k": nil}},
		"obj":  map[string]any{"b": true},
	}}
	out := roundTrip(t, a)
	if _, ok := out.Detail["n"].(float64); !ok {
		t.Fatalf("number decoded as %T", out.Detail["n"])
	}
	if s, ok := out.Detail["when"].(string); !ok || s != ms.Format(time.RFC3339Nano) {
		t.Fatalf("time decoded as %T %v", out.Detail["when"], out.Detail["when"])
	}
	list, ok := out.Detail["list"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("array decoded as %T", out.Detail["list"])
	}
	if _, ok := list[2].(map[string]any); !ok {
		t.Fatalf("nested object decoded as %T", list[2])
	}
	if _, ok := out.Detail["obj"].(map[string]any); !ok {
		t.Fatalf("object decoded as %T", out.Detail["obj"])
	}
	// omitempty nil map stays absent and decodes to nil
	if out := roundTrip(t, &types.AuditLog{Meta: testMeta("aud_2")}); out.Detail != nil {
		t.Fatalf("nil detail became %v", out.Detail)
	}
}
