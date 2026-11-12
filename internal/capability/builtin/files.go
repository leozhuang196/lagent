// Package builtin provides the platform's own tools.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/sandbox"
)

// Deps are the infrastructure builtins use.
type Deps struct {
	Sandbox *sandbox.Sandbox
	Store   *repo.Store
}

// All returns every builtin tool.
func All(d Deps) []tool.Tool {
	return []tool.Tool{
		readFile(), writeFile(), listFiles(), runCommand(d.Sandbox), publishArtifact(),
		remember(d.Store), loadSkill(d.Store), askUser(), submitResult(),
	}
}

const maxRead = 256 << 10

func readFile() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "read_file",
			Description: "Read a text file from the session workspace.",
			InputSchema: tool.Schema(map[string]any{"path": tool.Str("Path relative to the workspace")}, "path"),
		},
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "files", Risk: types.RiskReadOnly, ReadOnly: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Path string `json:"path"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			p, err := env.Workspace.Resolve(a.Path)
			if err != nil {
				return tool.Errorf("%v", err), nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return tool.Errorf("cannot read %s: %v", a.Path, err), nil
			}
			note := ""
			if len(b) > maxRead {
				b, note = b[:maxRead], fmt.Sprintf("\n[truncated at %d bytes]", maxRead)
			}
			return tool.Result{Content: tool.WrapData("file:"+a.Path, string(b)+note)}, nil
		},
	}
}

func writeFile() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "write_file",
			Description: "Create or overwrite a text file in the session workspace.",
			InputSchema: tool.Schema(map[string]any{
				"path":    tool.Str("Path relative to the workspace"),
				"content": tool.Str("Full file content"),
			}, "path", "content"),
		},
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "files", Risk: types.RiskLocalWrite, Idempotent: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			p, err := env.Workspace.Resolve(a.Path)
			if err != nil {
				return tool.Errorf("%v", err), nil
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
				return tool.Errorf("%v", err), nil
			}
			if err := os.WriteFile(p, []byte(a.Content), 0o666); err != nil {
				return tool.Errorf("cannot write %s: %v", a.Path, err), nil
			}
			return tool.Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)}, nil
		},
	}
}

func listFiles() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "list_files",
			Description: "List files in the session workspace.",
			InputSchema: tool.Schema(map[string]any{"path": tool.Str("Directory relative to the workspace; empty for the root")}),
		},
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "files", Risk: types.RiskReadOnly, ReadOnly: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Path string `json:"path"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			root, err := env.Workspace.Resolve(a.Path)
			if err != nil {
				return tool.Errorf("%v", err), nil
			}
			var lines []string
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || p == root {
					return nil
				}
				rel, _ := filepath.Rel(env.Workspace.Dir, p)
				if d.IsDir() {
					lines = append(lines, rel+"/")
				} else if info, err := d.Info(); err == nil {
					lines = append(lines, fmt.Sprintf("%s (%d bytes)", rel, info.Size()))
				}
				if len(lines) >= 500 {
					return filepath.SkipAll
				}
				return nil
			})
			if len(lines) == 0 {
				return tool.Result{Content: "(empty)"}, nil
			}
			return tool.Result{Content: strings.Join(lines, "\n")}, nil
		},
	}
}

func runCommand(sb *sandbox.Sandbox) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "run_command",
			Description: "Run a shell command in the session workspace. Network access goes through the platform proxy and may be blocked.",
			InputSchema: tool.Schema(map[string]any{
				"command":     tool.Str("Shell command"),
				"timeout_sec": map[string]any{"type": "integer", "description": "Timeout in seconds (max 600)"},
			}, "command"),
		},
		// Always "command": approval is required even in auto mode (§9.1).
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "exec", Risk: types.RiskCommand, SideEffect: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Command    string `json:"command"`
				TimeoutSec int    `json:"timeout_sec"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if sb == nil {
				return tool.Errorf("command execution is not available"), nil
			}
			to := time.Duration(min(max(a.TimeoutSec, 1), 600)) * time.Second
			if a.TimeoutSec == 0 {
				to = 0
			}
			res, err := sb.Exec(ctx, env.Workspace, a.Command, to)
			if err != nil {
				return tool.Errorf("command failed to start: %v", err), nil
			}
			out := fmt.Sprintf("exit code %d", res.ExitCode)
			if res.TimedOut {
				out = "timed out"
			}
			if res.Truncated {
				out += " (output truncated)"
			}
			return tool.Result{Content: out + "\n" + tool.WrapData("command", res.Output), IsError: res.ExitCode != 0}, nil
		},
	}
}

func publishArtifact() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "publish_artifact",
			Description: "Archive a workspace file as a deliverable the user can download. Call it as soon as a deliverable is ready.",
			InputSchema: tool.Schema(map[string]any{
				"path": tool.Str("Workspace file to archive"),
				"name": tool.Str("Display file name; defaults to the file's name"),
			}, "path"),
		},
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "files", Risk: types.RiskLocalWrite, Idempotent: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Path string `json:"path"`
				Name string `json:"name"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if env.Archive == nil {
				return tool.Errorf("archiving is not available"), nil
			}
			if a.Name == "" {
				a.Name = filepath.Base(a.Path)
			}
			idv, err := env.Archive(ctx, a.Path, a.Name)
			if err != nil {
				// An archive failure is a failure: downstream must not get a dangling reference.
				return tool.Result{}, err
			}
			return tool.Result{Content: fmt.Sprintf("archived %s as artifact %s", a.Name, idv), Artifacts: []string{idv}}, nil
		},
	}
}
