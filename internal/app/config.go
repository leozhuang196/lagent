// Package app is the composition root: it reads configuration, builds the
// chosen implementations of every infra interface and starts the roles.
// It is the only package that imports implementation sub-packages (R-3).
package app

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Role is a process role.
type Role string

const (
	RoleAPI    Role = "api"
	RoleEngine Role = "engine"
	RoleWorker Role = "worker"
	RoleFeishu Role = "feishu"
)

// Config is the process configuration (configs/lagent.example.yaml).
type Config struct {
	Roles []Role `yaml:"roles"`
	// BootstrapFile installs missing integrations and tenant roles after built-in seeds.
	BootstrapFile string `yaml:"bootstrap_file"`
	HTTP          struct {
		Addr         string `yaml:"addr"`
		BaseURL      string `yaml:"base_url"`
		CookieSecret string `yaml:"cookie_secret"`
		StaticDir    string `yaml:"static_dir"` // built console; empty = embedded
	} `yaml:"http"`
	Auth struct {
		BootstrapKey    string `yaml:"bootstrap_key"`
		BootstrapTenant string `yaml:"bootstrap_tenant"`
	} `yaml:"auth"`
	Store struct {
		Driver   string `yaml:"driver"` // memory, mongo, postgres
		URI      string `yaml:"uri"`
		Database string `yaml:"database"`
	} `yaml:"store"`
	Redis struct {
		Addr     string `yaml:"addr"` // empty = in-process coordination (single process only)
		Password string `yaml:"password"`
		DB       int    `yaml:"db"`
	} `yaml:"redis"`
	Object struct {
		Driver   string `yaml:"driver"` // local, s3
		Root     string `yaml:"root"`
		Bucket   string `yaml:"bucket"`
		Region   string `yaml:"region"`
		Endpoint string `yaml:"endpoint"`
		Secret   string `yaml:"secret"` // signing key for local signed URLs
	} `yaml:"object"`
	Secret struct {
		Driver string `yaml:"driver"` // local
		Path   string `yaml:"path"`
		Key    string `yaml:"key"`
	} `yaml:"secret"`
	Sandbox struct {
		Root        string `yaml:"root"`
		RunAsUID    int    `yaml:"run_as_uid"`
		RunAsGID    int    `yaml:"run_as_gid"`
		EgressProxy string `yaml:"egress_proxy"`
		CPUSeconds  int    `yaml:"cpu_seconds"`
		MemoryMB    int    `yaml:"memory_mb"`
	} `yaml:"sandbox"`
	LLM struct {
		Default   string                 `yaml:"default"`
		Providers map[string]LLMProvider `yaml:"providers"`
	} `yaml:"llm"`
	Limits struct {
		Workers         int           `yaml:"workers"`
		TurnTimeout     time.Duration `yaml:"turn_timeout"`
		ModelCall       time.Duration `yaml:"model_call"`
		Step            time.Duration `yaml:"step"`
		Flow            time.Duration `yaml:"flow"`
		LeaseTTL        time.Duration `yaml:"lease_ttl"`
		EventTTL        time.Duration `yaml:"event_ttl"`
		SubtaskParallel int           `yaml:"subtask_parallel"`
		FanoutParallel  int           `yaml:"fanout_parallel"`
		ContextLimit    int           `yaml:"context_limit"`
	} `yaml:"limits"`
	Feishu struct {
		AppID     string `yaml:"app_id"`
		AppSecret string `yaml:"app_secret"`
		Domain    string `yaml:"domain"`
	} `yaml:"feishu"`
	RecordLLMCalls bool   `yaml:"record_llm_calls"`
	LogLevel       string `yaml:"log_level"`
	DefaultRole    string `yaml:"default_role"`
}

// LLMProvider configures one model provider.
type LLMProvider struct {
	APIKey      string `yaml:"api_key"`
	BaseURL     string `yaml:"base_url"`
	Concurrency int    `yaml:"concurrency"`
}

var envRef = regexp.MustCompile(`\$\{([A-Z0-9_]+)(:-([^}]*))?\}`)

// Load reads a YAML file, expanding ${VAR} and ${VAR:-default}.
func Load(path string) (*Config, error) {
	raw := []byte{}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	expanded := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		g := envRef.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(g[1]); ok {
			return v
		}
		return g[3]
	})
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.defaults()
	return cfg, cfg.Validate()
}

func (c *Config) defaults() {
	if len(c.Roles) == 0 {
		c.Roles = []Role{RoleAPI, RoleEngine, RoleWorker}
	}
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = ":8080"
	}
	if c.HTTP.BaseURL == "" {
		c.HTTP.BaseURL = "http://localhost" + c.HTTP.Addr
	}
	if c.HTTP.CookieSecret == "" {
		c.HTTP.CookieSecret = "dev-cookie-secret-change-me"
	}
	if c.Auth.BootstrapTenant == "" {
		c.Auth.BootstrapTenant = "default"
	}
	if c.Store.Driver == "" {
		c.Store.Driver = "memory"
	}
	if c.Store.Database == "" {
		c.Store.Database = "lagent"
	}
	if c.Object.Driver == "" {
		c.Object.Driver = "local"
	}
	if c.Object.Root == "" {
		c.Object.Root = "./data/objects"
	}
	if c.Object.Secret == "" {
		c.Object.Secret = c.HTTP.CookieSecret
	}
	if c.Secret.Driver == "" {
		c.Secret.Driver = "local"
	}
	if c.Secret.Key == "" {
		c.Secret.Key = "dev-secret-key-change-me"
	}
	if c.Sandbox.Root == "" {
		c.Sandbox.Root = "./data/workspaces"
	}
	if c.LLM.Default == "" {
		c.LLM.Default = "anthropic/claude-opus-5-5"
	}
	l := &c.Limits
	if l.Workers == 0 {
		l.Workers = 16
	}
	if l.TurnTimeout == 0 {
		l.TurnTimeout = 30 * time.Minute
	}
	if l.ModelCall == 0 {
		l.ModelCall = 5 * time.Minute
	}
	if l.Step == 0 {
		l.Step = 30 * time.Minute
	}
	if l.Flow == 0 {
		l.Flow = 6 * time.Hour
	}
	if l.LeaseTTL == 0 {
		l.LeaseTTL = 30 * time.Second
	}
	if l.EventTTL == 0 {
		l.EventTTL = 24 * time.Hour
	}
	if l.SubtaskParallel == 0 {
		l.SubtaskParallel = 4
	}
	if l.FanoutParallel == 0 {
		l.FanoutParallel = 8
	}
	if l.ContextLimit == 0 {
		l.ContextLimit = 150_000
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.DefaultRole == "" {
		c.DefaultRole = "general"
	}
}

// Validate refuses configurations that would misbehave at run time (§7.4):
// the three timeouts must strictly increase, otherwise a timed-out step leaves
// an orphan model call writing into a finished step.
func (c *Config) Validate() error {
	l := c.Limits
	if !(l.ModelCall < l.Step && l.Step < l.Flow) {
		return fmt.Errorf("config: timeouts must satisfy model_call (%s) < step (%s) < flow (%s)", l.ModelCall, l.Step, l.Flow)
	}
	if l.ModelCall >= l.TurnTimeout {
		return fmt.Errorf("config: model_call (%s) must be shorter than turn_timeout (%s)", l.ModelCall, l.TurnTimeout)
	}
	if l.LeaseTTL < 3*time.Second {
		return fmt.Errorf("config: lease_ttl too short")
	}
	for _, r := range c.Roles {
		switch r {
		case RoleAPI, RoleEngine, RoleWorker, RoleFeishu:
		default:
			return fmt.Errorf("config: unknown role %q", r)
		}
	}
	multi := len(c.Roles) < 4 && !(c.Has(RoleAPI) && c.Has(RoleEngine) && c.Has(RoleWorker))
	if multi && c.Redis.Addr == "" {
		return fmt.Errorf("config: roles split across processes need redis.addr")
	}
	if multi && c.Store.Driver == "memory" {
		return fmt.Errorf("config: roles split across processes need a shared store")
	}
	return nil
}

// Has reports whether the process runs role r.
func (c *Config) Has(r Role) bool {
	for _, x := range c.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// ParseRoles parses "api,engine".
func ParseRoles(s string) []Role {
	var out []Role
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, Role(p))
		}
	}
	return out
}
