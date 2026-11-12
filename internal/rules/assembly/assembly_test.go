package assembly

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/core/types"
)

func TestAllowedToolsOnlyNarrows(t *testing.T) {
	reg := []types.ToolMeta{
		{Name: "read_file", Source: types.SourceBuiltin},
		{Name: "write_file", Source: types.SourceBuiltin},
		{Name: "ads__bid", Source: types.SourceMCP, Server: "ads"},
		{Name: "crm__get", Source: types.SourceMCP, Server: "crm"},
		{Name: "dispatch_subtask", Source: types.SourcePlatform},
	}
	got := AllowedTools([]string{"read_file", "mcp:ads", "not_registered"}, reg, nil)
	if !reflect.DeepEqual(got, []string{"ads__bid", "read_file"}) {
		t.Fatalf("got %v", got)
	}
	got = AllowedTools([]string{"*"}, reg, []string{"dispatch_subtask"})
	if len(got) != 4 {
		t.Fatalf("deny not applied: %v", got)
	}
}

func TestAssembleIsDeterministic(t *testing.T) {
	in := Input{
		Role:   types.Role{Prompt: "You optimise ads.", Tools: []string{"*"}},
		Skills: []types.Skill{{Versioned: types.Versioned{Key: "b"}, Name: "b", Description: "B"}, {Versioned: types.Versioned{Key: "a"}, Name: "a", Description: "A"}},
		Memory: []types.MemoryEntry{{Space: "s", Key: "z", Content: "1"}, {Space: "s", Key: "y", Content: "2"}},
	}
	a, b := Assemble(in), Assemble(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("assembly not deterministic")
	}
	if a.Parts[0].Name != "platform" || !a.Parts[len(a.Parts)-1].Cache {
		t.Fatalf("parts order/cache wrong: %+v", a.Parts)
	}
	skills := a.Parts[2].Text
	if strings.Index(skills, "- a:") > strings.Index(skills, "- b:") {
		t.Fatal("skills not sorted")
	}
}

func TestAssembleBudgetTruncates(t *testing.T) {
	var mem []types.MemoryEntry
	for i := 0; i < 50; i++ {
		mem = append(mem, types.MemoryEntry{Space: "s", Key: string(rune('a'+i%26)) + string(rune('a'+i/26)), Content: strings.Repeat("x", 100)})
	}
	out := Assemble(Input{Role: types.Role{Prompt: "p"}, Memory: mem, InjectionBudget: 1000})
	if len(out.Truncated) == 0 {
		t.Fatal("expected truncation")
	}
}
