package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

const cookieName = "lagent_session"

// KeyLookup finds API keys across tenants (implemented by the app wiring).
type KeyLookup interface {
	ByPrefix(ctx context.Context, prefix string) (*types.APIKey, error)
	ByID(ctx context.Context, id string) (*types.APIKey, error)
}

// Auth resolves API keys and console cookies into principals.
type Auth struct {
	Keys         KeyLookup
	CookieSecret []byte
	CookieTTL    time.Duration
	// Bootstrap is an optional platform-admin key from configuration, used to
	// create the first tenants and keys.
	BootstrapKey    string
	BootstrapTenant string
	Now             func() time.Time
}

// NewKey generates a plaintext key "lk_<prefix>_<secret>" and its hash.
func NewKey() (plain, prefix, hash string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	p := make([]byte, 6)
	_, _ = rand.Read(p)
	prefix = hex.EncodeToString(p)
	plain = "lk_" + prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
	return plain, prefix, HashKey(plain)
}

// HashKey hashes a plaintext key.
func HashKey(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Principal resolves a plaintext key.
func (a *Auth) Principal(ctx context.Context, plain string) (tenant.Principal, error) {
	if a.BootstrapKey != "" && hmac.Equal([]byte(plain), []byte(a.BootstrapKey)) {
		return tenant.Principal{Kind: tenant.KindAPIKey, ID: "bootstrap", TenantID: a.BootstrapTenant, Role: tenant.RolePlatformAdmin, Name: "bootstrap"}, nil
	}
	parts := strings.SplitN(plain, "_", 3)
	if len(parts) != 3 || parts[0] != "lk" {
		return tenant.Principal{}, errs.New(errs.CodeUnauthorized, "malformed API key")
	}
	k, err := a.Keys.ByPrefix(ctx, parts[1])
	if err != nil || k.Disabled || !hmac.Equal([]byte(k.Hash), []byte(HashKey(plain))) {
		return tenant.Principal{}, errs.New(errs.CodeUnauthorized, "invalid API key")
	}
	return keyPrincipal(k), nil
}

func keyPrincipal(k *types.APIKey) tenant.Principal {
	return tenant.Principal{Kind: tenant.KindAPIKey, ID: k.ID, TenantID: k.TenantID, Role: k.Role, Name: k.Name, MaxPermissionMode: string(k.MaxPermissionMode)}
}

// Middleware authenticates Authorization: Bearer <key> or the console cookie.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p tenant.Principal
		var err error
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			p, err = a.Principal(r.Context(), strings.TrimPrefix(h, "Bearer "))
		} else if c, cerr := r.Cookie(cookieName); cerr == nil {
			p, err = a.fromCookie(r.Context(), c.Value)
		} else {
			err = errs.New(errs.CodeUnauthorized, "missing credentials")
		}
		if err != nil {
			WriteError(w, err)
			return
		}
		// Platform admins may act inside another tenant explicitly.
		if t := r.Header.Get("X-Lagent-Tenant"); t != "" && t != p.TenantID {
			if p.Role != tenant.RolePlatformAdmin {
				WriteError(w, errs.New(errs.CodeForbidden, "cannot switch tenant"))
				return
			}
			p.TenantID = t
		}
		next.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), p)))
	})
}

// cookie value: keyID.exp.sig — the key must still exist and be enabled.
func (a *Auth) cookieValue(keyID string, exp int64) string {
	m := hmac.New(sha256.New, a.CookieSecret)
	fmt.Fprintf(m, "%s.%d", keyID, exp)
	return fmt.Sprintf("%s.%d.%s", keyID, exp, hex.EncodeToString(m.Sum(nil)))
}

func (a *Auth) fromCookie(ctx context.Context, v string) (tenant.Principal, error) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return tenant.Principal{}, errs.New(errs.CodeUnauthorized, "bad session")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.now().Unix() > exp || !hmac.Equal([]byte(a.cookieValue(parts[0], exp)), []byte(v)) {
		return tenant.Principal{}, errs.New(errs.CodeUnauthorized, "session expired")
	}
	if parts[0] == "bootstrap" && a.BootstrapKey != "" {
		return tenant.Principal{Kind: tenant.KindAPIKey, ID: "bootstrap", TenantID: a.BootstrapTenant, Role: tenant.RolePlatformAdmin, Name: "bootstrap"}, nil
	}
	k, err := a.Keys.ByID(ctx, parts[0])
	if err != nil || k.Disabled {
		return tenant.Principal{}, errs.New(errs.CodeUnauthorized, "key revoked")
	}
	return keyPrincipal(k), nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, err)
		return
	}
	p, err := s.Auth.Principal(r.Context(), body.Key)
	if err != nil {
		WriteError(w, err)
		return
	}
	ttl := s.Auth.CookieTTL
	if ttl == 0 {
		ttl = 12 * time.Hour
	}
	exp := s.Auth.now().Add(ttl).Unix()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.Auth.cookieValue(p.ID, exp), Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, Expires: time.Unix(exp, 0),
	})
	WriteJSON(w, http.StatusOK, p)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.From(r.Context())
	WriteJSON(w, http.StatusOK, p)
}

// RequireRole wraps a handler with a minimum role.
func RequireRole(min tenant.Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := tenant.From(r.Context())
		ok := false
		switch min {
		case tenant.RoleMember:
			ok = true
		case tenant.RoleTenantAdmin:
			ok = p.IsAdmin()
		case tenant.RolePlatformAdmin:
			ok = p.Role == tenant.RolePlatformAdmin
		}
		if !ok {
			WriteError(w, errs.New(errs.CodeForbidden, "requires %s", min))
			return
		}
		h(w, r)
	}
}
