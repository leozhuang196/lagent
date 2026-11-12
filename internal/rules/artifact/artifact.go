// Package artifact names archived deliverables. Outputs are archived as soon
// as they are produced (before the workspace may be cleaned), and names never
// overwrite: each run/step or turn gets its own prefix.
package artifact

import (
	"path"
	"strings"
	"time"
)

// Owner identifies who produced an artifact.
type Owner struct {
	TenantID  string
	SessionID string
	TurnID    string
	RunID     string
	StepRunID string
}

// Key returns the object key: runs/{run}/{step}/{name} for workflow steps,
// sessions/{session}/{turn}/{name} otherwise.
func Key(o Owner, name string) string {
	clean := SafeName(name)
	base := "tenants/" + o.TenantID + "/artifacts/"
	if o.RunID != "" {
		return base + "runs/" + o.RunID + "/" + o.StepRunID + "/" + clean
	}
	return base + "sessions/" + o.SessionID + "/" + o.TurnID + "/" + clean
}

// SafeName strips directories and unsafe characters from a file name.
func SafeName(name string) string {
	n := path.Base(strings.ReplaceAll(name, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, n)
	if n == "." || n == "/" || n == "" {
		n = "file"
	}
	return n
}

// DefaultRetentionDays applies when the tenant sets none.
const DefaultRetentionDays = 180

// SignedURLTTL is the lifetime of download links.
const SignedURLTTL = 15 * time.Minute

// Expired reports whether an artifact outlived its retention. Artifacts
// referenced by a workflow run live as long as the run record (§13.3), so the
// caller passes referenced=true for those.
func Expired(created, now time.Time, retentionDays int, referenced bool) bool {
	if referenced {
		return false
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return now.Sub(created) > time.Duration(retentionDays)*24*time.Hour
}
