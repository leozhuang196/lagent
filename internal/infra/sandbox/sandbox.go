// Package sandbox provides the per-session workspace where file and command
// tools run: process-level soft isolation (own directory, optional low-privilege
// user, resource limits, cleared environment, egress through a proxy).
//
// Instances are stateless, so a workspace is snapshotted to object storage at
// the end of every turn and restored when the next turn of the session lands
// on another instance.
package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/objectstore"
)

// Config tunes the sandbox.
type Config struct {
	Root           string // workspaces live under Root/{tenant}/{session}
	RunAsUID       int    // >0: drop to this uid (requires running as root)
	RunAsGID       int
	EgressProxy    string // HTTP(S)_PROXY for commands; empty = no proxy env
	CPUSeconds     int    // ulimit -t
	MemoryMB       int    // ulimit -v
	MaxOutput      int    // bytes of stdout+stderr kept
	MaxSnapshotMB  int    // snapshot size cap
	DefaultTimeout time.Duration
}

// Workspace is a session's working directory.
type Workspace struct {
	TenantID  string
	SessionID string
	Dir       string
}

// ExecResult is the outcome of a command.
type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

// Sandbox manages workspaces.
type Sandbox struct {
	cfg   Config
	store objectstore.Store
}

func New(cfg Config, store objectstore.Store) (*Sandbox, error) {
	if cfg.Root == "" {
		cfg.Root = filepath.Join(os.TempDir(), "lagent-ws")
	}
	if cfg.MaxOutput == 0 {
		cfg.MaxOutput = 64 << 10
	}
	if cfg.MaxSnapshotMB == 0 {
		cfg.MaxSnapshotMB = 200
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 2 * time.Minute
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, err
	}
	return &Sandbox{cfg: cfg, store: store}, nil
}

func snapshotKey(tenantID, sessionID string) string {
	return "tenants/" + tenantID + "/workspaces/" + sessionID + ".tar.gz"
}

// Open returns the session workspace, restoring the latest snapshot if the
// directory is not present on this instance.
func (s *Sandbox) Open(ctx context.Context, tenantID, sessionID string) (*Workspace, error) {
	dir := filepath.Join(s.cfg.Root, tenantID, sessionID)
	ws := &Workspace{TenantID: tenantID, SessionID: sessionID, Dir: dir}
	if _, err := os.Stat(dir); err == nil {
		return ws, nil
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	if s.store == nil {
		return ws, nil
	}
	rc, _, err := s.store.Get(ctx, snapshotKey(tenantID, sessionID))
	if errs.Is(err, errs.CodeNotFound) {
		return ws, nil
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return ws, untar(rc, dir)
}

// Snapshot uploads the workspace so another instance can restore it.
func (s *Sandbox) Snapshot(ctx context.Context, ws *Workspace) error {
	if s.store == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := tarDir(ws.Dir, &buf, int64(s.cfg.MaxSnapshotMB)<<20); err != nil {
		return err
	}
	return s.store.Put(ctx, snapshotKey(ws.TenantID, ws.SessionID), &buf, int64(buf.Len()), "application/gzip")
}

// Cleanup removes the local directory (the snapshot stays).
func (s *Sandbox) Cleanup(ws *Workspace) error { return os.RemoveAll(ws.Dir) }

// Resolve maps a workspace-relative path to an absolute one, refusing escapes.
func (ws *Workspace) Resolve(rel string) (string, error) {
	p := filepath.Join(ws.Dir, filepath.Clean("/"+rel))
	if p != ws.Dir && !strings.HasPrefix(p, ws.Dir+string(os.PathSeparator)) {
		return "", errs.Invalid("path %q escapes the workspace", rel)
	}
	return p, nil
}

// Exec runs a shell command inside the workspace.
func (s *Sandbox) Exec(ctx context.Context, ws *Workspace, command string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var limits []string
	if s.cfg.CPUSeconds > 0 {
		limits = append(limits, fmt.Sprintf("ulimit -t %d", s.cfg.CPUSeconds))
	}
	if s.cfg.MemoryMB > 0 {
		limits = append(limits, fmt.Sprintf("ulimit -v %d 2>/dev/null", s.cfg.MemoryMB*1024))
	}
	script := command
	if len(limits) > 0 {
		script = strings.Join(limits, "; ") + "; " + command
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Dir = ws.Dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + ws.Dir, "LANG=C.UTF-8", "TMPDIR=" + ws.Dir}
	if s.cfg.EgressProxy != "" {
		cmd.Env = append(cmd.Env, "HTTP_PROXY="+s.cfg.EgressProxy, "HTTPS_PROXY="+s.cfg.EgressProxy,
			"http_proxy="+s.cfg.EgressProxy, "https_proxy="+s.cfg.EgressProxy)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if s.cfg.RunAsUID > 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(s.cfg.RunAsUID), Gid: uint32(s.cfg.RunAsGID)}
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out := &capBuffer{max: s.cfg.MaxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	res := ExecResult{Output: out.String(), Truncated: out.truncated}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

type capBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func (c *capBuffer) String() string { return c.buf.String() }

func tarDir(dir string, w io.Writer, max int64) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var total int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." || !(info.Mode().IsRegular() || info.IsDir()) {
			return nil
		}
		total += info.Size()
		if total > max {
			return errs.Invalid("workspace exceeds snapshot limit of %d bytes", max)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.Clean("/"+hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o777); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
