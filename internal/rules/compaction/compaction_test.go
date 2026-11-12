package compaction

import (
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/core/types"
)

func user(s string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.Block{types.Text(s)}}
}
func assistantTool() types.Message {
	return types.Message{Role: types.RoleAssistant, Content: []types.Block{{Type: types.BlockToolUse, ToolUseID: "x", Name: "t", Input: []byte(`{}`)}}}
}
func toolResult(s string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.Block{{Type: types.BlockToolResult, ToolUseID: "x", Text: s}}}
}
func assistant(s string) types.Message {
	return types.Message{Role: types.RoleAssistant, Content: []types.Block{types.Text(s)}}
}

func TestNoCompactionUnderLimit(t *testing.T) {
	if p := Decide([]types.Message{user("hi"), assistant("yo")}, 1000, 100, 0); p.Cut != 0 {
		t.Fatal(p)
	}
}

func TestCutOnTurnBoundary(t *testing.T) {
	big := strings.Repeat("x", 4000)
	var msgs []types.Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, user("q"), assistantTool(), toolResult(big), assistant("a"))
	}
	p := Decide(msgs, 4000, 400, 0)
	if p.Cut == 0 {
		t.Fatal("expected compaction")
	}
	if !IsBoundary(msgs[p.Cut]) {
		t.Fatalf("cut %d not on boundary", p.Cut)
	}
	if msgs[p.Cut-1].Role != types.RoleAssistant {
		t.Fatal("cut separates a tool pair")
	}
}
