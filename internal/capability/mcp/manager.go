// Package mcp connects to tenant-configured MCP servers and exposes their tools
// in the same shape as every other tool (§6.1–6.2): the engine never knows a
// tool came from MCP.
//
// Sessions are cached per (tenant, server), opened lazily on first use and
// re-opened when the connection dies or the server configuration changes.
// Credentials are read from the secret store at connect time and only ever
// travel in a request header (or the child process environment for stdio);
// they never enter prompts, records or logs.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/secret"
)

// DialFunc builds the transport for a server. secretValue is empty when the
// server has no credential. Tests replace it with in-memory transports.
type DialFunc func(ctx context.Context, srv *types.MCPServer, secretValue string) (sdk.Transport, error)

// Options tune a Manager.
type Options struct {
	// ConnectTimeout bounds the transport handshake (default 15s).
	ConnectTimeout time.Duration
	// ProviderTTL is how long the provider caches a tenant's server list
	// (default 10s). Tools are read on every turn; configuration changes go
	// through Invalidate, so the TTL only bounds staleness across instances.
	ProviderTTL time.Duration
	// Dial overrides transport construction (tests).
	Dial DialFunc
	// HTTPClient is the base client for HTTP transports (default http.DefaultClient's transport).
	HTTPClient *http.Client
	Clock      clock.Clock
	Log        *slog.Logger
}

// Manager owns the MCP client sessions of every tenant on this instance.
type Manager struct {
	store   *repo.Store
	secrets secret.Store
	opts    Options
	client  *sdk.Client

	mu       sync.Mutex
	sessions map[string]*conn // tenant + "\x00" + server name
	lists    map[string]cachedServers
}

type conn struct {
	mu          sync.Mutex // serialises (re)connects of this entry
	fingerprint string
	cs          *sdk.ClientSession
	cancel      context.CancelFunc
	dead        chan struct{} // closed when the session ends
}

type cachedServers struct {
	at      time.Time
	servers []*types.MCPServer
}

// New builds a manager. store and secrets are required.
func New(store *repo.Store, secrets secret.Store, opts Options) *Manager {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 15 * time.Second
	}
	if opts.ProviderTTL <= 0 {
		opts.ProviderTTL = 10 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Manager{
		store:    store,
		secrets:  secrets,
		opts:     opts,
		client:   sdk.NewClient(&sdk.Implementation{Name: "lagent", Version: "1"}, nil),
		sessions: map[string]*conn{},
		lists:    map[string]cachedServers{},
	}
}

// CallResult is the flattened outcome of an MCP tool call.
type CallResult struct {
	Text    string
	IsError bool
}

// Lookup loads a server configuration of the tenant in ctx by name.
func (m *Manager) Lookup(ctx context.Context, name string) (*types.MCPServer, error) {
	return m.store.MCPServers.FindOne(ctx, repo.Filter{"name": name})
}

// ListTools lists the server's tools live (used by sync and test connection).
func (m *Manager) ListTools(ctx context.Context, srv *types.MCPServer) ([]types.ToolSpec, error) {
	var out []types.ToolSpec
	err := m.withSession(ctx, srv, true, func(cs *sdk.ClientSession) error {
		out = out[:0]
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			out = append(out, specOf(t))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CallTool calls one tool of srv. args must be a JSON object (nil means {}).
func (m *Manager) CallTool(ctx context.Context, srv *types.MCPServer, toolName string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	var res *sdk.CallToolResult
	// A call is retried on a fresh session only when the server certainly did
	// not run it (see retryable); tool calls may have side effects (§8.3).
	err := m.withSession(ctx, srv, false, func(cs *sdk.ClientSession) error {
		var err error
		res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: toolName, Arguments: args})
		return err
	})
	if err != nil {
		return CallResult{}, err
	}
	return flatten(res), nil
}

// Probe opens a throwaway session to check a configuration end to end
// (console "test connection"); it never touches the cache.
func (m *Manager) Probe(ctx context.Context, srv *types.MCPServer) ([]types.ToolSpec, error) {
	c := &conn{}
	if err := m.connect(ctx, srv, c); err != nil {
		return nil, err
	}
	defer m.closeConn(c)
	var out []types.ToolSpec
	for t, err := range c.cs.Tools(ctx, nil) {
		if err != nil {
			return nil, safeErr(srv, err)
		}
		out = append(out, specOf(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Invalidate drops the cached session of one server (empty name: every
// server) and the provider cache of the tenant in ctx. Call it after any
// configuration change so the next use reconnects with fresh settings.
func (m *Manager) Invalidate(ctx context.Context, name string) {
	tid, err := tenant.ID(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	delete(m.lists, tid)
	var drop []*conn
	for k, c := range m.sessions {
		t, n, _ := strings.Cut(k, "\x00")
		if t == tid && (name == "" || n == name) {
			drop = append(drop, c)
			delete(m.sessions, k)
		}
	}
	m.mu.Unlock()
	for _, c := range drop {
		c.mu.Lock()
		m.closeConn(c)
		c.mu.Unlock()
	}
}

// Close ends every session (process shutdown).
func (m *Manager) Close() {
	m.mu.Lock()
	all := m.sessions
	m.sessions = map[string]*conn{}
	m.lists = map[string]cachedServers{}
	m.mu.Unlock()
	for _, c := range all {
		c.mu.Lock()
		m.closeConn(c)
		c.mu.Unlock()
	}
}

// withSession runs fn on the cached session of srv, connecting lazily. When
// the session turns out to be dead, it reconnects once and retries fn if
// idempotent is true or the error proves the request never reached the server.
func (m *Manager) withSession(ctx context.Context, srv *types.MCPServer, idempotent bool, fn func(*sdk.ClientSession) error) error {
	if !srv.Enabled {
		return errs.Conflict("MCP server %q is disabled", srv.Name)
	}
	tid, err := tenant.ID(ctx)
	if err != nil {
		return err
	}
	if srv.TenantID != "" && srv.TenantID != tid {
		// Defence in depth: a server record of another tenant must never be
		// dialled with this tenant's identity (or the other way round).
		return errs.New(errs.CodeForbidden, "MCP server belongs to another tenant")
	}
	key := tid + "\x00" + srv.Name
	m.mu.Lock()
	c := m.sessions[key]
	if c == nil {
		c = &conn{}
		m.sessions[key] = c
	}
	m.mu.Unlock()

	for attempt := 0; ; attempt++ {
		cs, err := m.ensure(ctx, srv, c)
		if err != nil {
			return err
		}
		err = fn(cs)
		if err == nil {
			return nil
		}
		if attempt == 0 && (m.sessionDead(c, cs) || transportGone(err)) {
			m.reset(c, cs)
			// The connection failed, so what was sent is unknown: re-run only
			// calls that are safe to repeat, or errors that prove the request
			// never reached the server.
			if idempotent || retryable(err) || notDelivered(err) {
				continue
			}
		}
		return safeErr(srv, err)
	}
}

// sessionDead reports whether the session behind cs has ended. The check is
// structural (the connection's Wait returned) rather than error-based: a
// server restart surfaces as a bare EOF, which carries no SDK sentinel.
func (m *Manager) sessionDead(c *conn, cs *sdk.ClientSession) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cs != cs || c.dead == nil {
		return true
	}
	select {
	case <-c.dead:
		return true
	default:
		return false
	}
}

// sessionDeadOnly reports an error that proves nothing was executed: the
// transport failed before the request could be written.
func (m *Manager) sessionDeadOnly(err error) bool { return notDelivered(err) }

// ensure returns a live session for c, (re)connecting when there is none,
// it died, or the configuration fingerprint changed.
func (m *Manager) ensure(ctx context.Context, srv *types.MCPServer, c *conn) (*sdk.ClientSession, error) {
	fp := fingerprint(srv)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cs != nil {
		select {
		case <-c.dead:
			m.closeConn(c)
		default:
			if c.fingerprint == fp {
				return c.cs, nil
			}
			m.closeConn(c)
		}
	}
	if err := m.connect(ctx, srv, c); err != nil {
		return nil, err
	}
	c.fingerprint = fp
	return c.cs, nil
}

func (m *Manager) reset(c *conn, cs *sdk.ClientSession) {
	c.mu.Lock()
	if c.cs == cs {
		m.closeConn(c)
	}
	c.mu.Unlock()
}

// connect opens a session into c (c.mu held or c private).
func (m *Manager) connect(ctx context.Context, srv *types.MCPServer, c *conn) error {
	sec := ""
	if srv.SecretRef != "" {
		v, err := m.secrets.Get(ctx, srv.SecretRef)
		if err != nil {
			return errs.Wrap(errs.CodeUnavailable, err, "credential of MCP server %q is not readable", srv.Name)
		}
		sec = v
	}
	dial := m.opts.Dial
	if dial == nil {
		dial = m.dial
	}
	tr, err := dial(ctx, srv, sec)
	if err != nil {
		return err
	}
	// The session outlives the request that opened it (SSE ties its stream to
	// the connect context), so connect on a detached context and enforce the
	// handshake timeout and caller cancellation by hand.
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	timer := time.AfterFunc(m.opts.ConnectTimeout, cancel)
	stop := context.AfterFunc(ctx, cancel)
	cs, err := m.client.Connect(sctx, tr, nil)
	expired := !timer.Stop()
	stop()
	if err != nil || expired || ctx.Err() != nil {
		cancel()
		if cs != nil {
			_ = cs.Close()
		}
		if err == nil {
			err = context.DeadlineExceeded
		}
		return errs.Wrap(errs.CodeUnavailable, scrub(err, sec), "cannot connect to MCP server %q", srv.Name)
	}
	dead := make(chan struct{})
	go func() { _ = cs.Wait(); close(dead) }()
	c.cs, c.cancel, c.dead = cs, cancel, dead
	return nil
}

func (m *Manager) closeConn(c *conn) {
	if c.cs != nil {
		_ = c.cs.Close()
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.cs, c.cancel, c.dead, c.fingerprint = nil, nil, nil, ""
}

// dial builds the real transports.
func (m *Manager) dial(_ context.Context, srv *types.MCPServer, sec string) (sdk.Transport, error) {
	switch srv.Transport {
	case types.MCPStdio:
		if len(srv.Command) == 0 {
			return nil, errs.Invalid("stdio MCP server %q has no command", srv.Name)
		}
		cmd := exec.Command(srv.Command[0], srv.Command[1:]...)
		// A cleared environment: the child sees only what the server
		// configuration declares, never the platform's own variables.
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
		for k, v := range srv.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if sec != "" {
			cmd.Env = append(cmd.Env, stdioSecretEnv(srv)+"="+sec)
		}
		return &sdk.CommandTransport{Command: cmd}, nil
	case types.MCPStreamable, types.MCPSSE:
		if srv.URL == "" {
			return nil, errs.Invalid("MCP server %q has no URL", srv.Name)
		}
		hc := m.httpClient(srv, sec)
		if srv.Transport == types.MCPSSE {
			return &sdk.SSEClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil
		}
		return &sdk.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, DisableStandaloneSSE: true}, nil
	}
	return nil, errs.Invalid("unknown MCP transport %q", srv.Transport)
}

func (m *Manager) httpClient(srv *types.MCPServer, sec string) *http.Client {
	base := http.DefaultTransport
	timeout := time.Duration(0)
	if m.opts.HTTPClient != nil {
		if m.opts.HTTPClient.Transport != nil {
			base = m.opts.HTTPClient.Transport
		}
		timeout = m.opts.HTTPClient.Timeout
	}
	if sec == "" {
		return &http.Client{Transport: base, Timeout: timeout}
	}
	name, value := AuthHeader(srv.AuthHeader, sec)
	return &http.Client{Transport: &authTransport{base: base, name: name, value: value}, Timeout: timeout}
}

// AuthHeader returns the header name and value carrying sec. The configured
// form is "Name: Prefix" (default "Authorization: Bearer") or a bare header
// name, which carries the raw secret.
func AuthHeader(conf, sec string) (string, string) {
	conf = strings.TrimSpace(conf)
	if conf == "" {
		conf = "Authorization: Bearer"
	}
	name, prefix, ok := strings.Cut(conf, ":")
	name, prefix = strings.TrimSpace(name), strings.TrimSpace(prefix)
	if !ok || prefix == "" {
		return name, sec
	}
	return name, prefix + " " + sec
}

// stdioSecretEnv names the environment variable carrying a stdio server's
// secret: the configured auth header if it is a bare name, else MCP_SECRET.
func stdioSecretEnv(srv *types.MCPServer) string {
	h := strings.TrimSpace(srv.AuthHeader)
	if h != "" && !strings.ContainsAny(h, ": ") {
		return h
	}
	return "MCP_SECRET"
}

type authTransport struct {
	base        http.RoundTripper
	name, value string
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(t.name, t.value)
	return t.base.RoundTrip(r)
}

// fingerprint covers every field that affects the connection; changing any
// of them makes the next use reconnect even without an explicit Invalidate.
func fingerprint(srv *types.MCPServer) string {
	h := sha256.New()
	keys := make([]string, 0, len(srv.Env))
	for k := range srv.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(h, "%s\x00%s\x00%q\x00%s\x00%s\x00", srv.Transport, srv.URL, srv.Command, srv.SecretRef, srv.AuthHeader)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, srv.Env[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// transportGone reports errors after which the session is unusable. Besides
// the SDK sentinels, a server restart surfaces from the transport as a bare
// EOF or a closed pipe, which carry no sentinel at all.
func transportGone(err error) bool {
	return errors.Is(err, sdk.ErrConnectionClosed) || errors.Is(err, sdk.ErrSessionMissing) ||
		notDelivered(err) || errors.Is(err, io.ErrClosedPipe)
}

// notDelivered reports errors that prove the request never reached the
// server: the connection failed while being used.
func notDelivered(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
}

// retryable reports errors that prove the server never executed the request:
// it did not recognise the session at all.
func retryable(err error) bool { return errors.Is(err, sdk.ErrSessionMissing) }

// safeErr turns a client error into a coded one without leaking anything the
// transport may have echoed back.
func safeErr(srv *types.MCPServer, err error) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(errs.CodeUnavailable, err, "MCP server %q did not answer in time", srv.Name)
	}
	return errs.Wrap(errs.CodeUnavailable, err, "MCP server %q call failed", srv.Name)
}

// scrub removes a secret from an error message, in case a transport echoes
// request headers or the command environment.
func scrub(err error, sec string) error {
	if err == nil || sec == "" || !strings.Contains(err.Error(), sec) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), sec, "[redacted]"))
}

func specOf(t *sdk.Tool) types.ToolSpec {
	schema, err := json.Marshal(t.InputSchema)
	if err != nil || string(schema) == "null" {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	desc := t.Description
	if desc == "" {
		desc = t.Title
	}
	return types.ToolSpec{Name: t.Name, Description: desc, InputSchema: schema}
}

// flatten concatenates text content; other content kinds are summarised so
// the model knows something was returned that it cannot see.
func flatten(res *sdk.CallToolResult) CallResult {
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *sdk.TextContent:
			parts = append(parts, v.Text)
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s omitted]", v.MIMEType))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s omitted]", v.MIMEType))
		case *sdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource %s]", v.URI))
		case *sdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				parts = append(parts, v.Resource.Text)
			} else if v.Resource != nil {
				parts = append(parts, fmt.Sprintf("[resource %s]", v.Resource.URI))
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return CallResult{Text: strings.Join(parts, "\n"), IsError: res.IsError}
}
