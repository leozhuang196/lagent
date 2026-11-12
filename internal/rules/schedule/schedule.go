// Package schedule computes firing times and overlap decisions. It uses the
// pure robfig/cron parser (no goroutines, no clock reads).
//
// DST rules (§9.11): a wall time that occurs twice fires once (the first
// occurrence); a wall time that does not exist moves to the next valid instant.
package schedule

import (
	"time"

	"github.com/robfig/cron/v3"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Validate checks the expression and time zone.
func Validate(expr, tz string) error {
	if _, err := parser.Parse(expr); err != nil {
		return errs.Invalid("bad cron %q: %v", expr, err)
	}
	if _, err := time.LoadLocation(tz); err != nil || tz == "" {
		return errs.Invalid("bad time zone %q", tz)
	}
	return nil
}

// Next returns the first firing strictly after `after`.
//
// The cron is evaluated on wall-clock time (as if the zone had no DST) and the
// result is mapped back into the zone: a repeated wall time therefore occurs
// once, and a non-existent one is normalised forward by time.Date.
func Next(expr, tz string, after time.Time) (time.Time, error) {
	sched, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, errs.Invalid("bad cron %q: %v", expr, err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, errs.Invalid("bad time zone %q", tz)
	}
	l := after.In(loc)
	wall := time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
	for i := 0; i < 4; i++ {
		nw := sched.Next(wall)
		if nw.IsZero() {
			return time.Time{}, errs.Invalid("cron %q never fires", expr)
		}
		real := time.Date(nw.Year(), nw.Month(), nw.Day(), nw.Hour(), nw.Minute(), 0, 0, loc)
		if real.After(after) {
			return real.UTC(), nil
		}
		wall = nw
	}
	return time.Time{}, errs.Invalid("cron %q: no firing found", expr)
}

// Upcoming lists the next n firings after t (for the console preview).
func Upcoming(expr, tz string, after time.Time, n int) ([]time.Time, error) {
	var out []time.Time
	t := after
	for i := 0; i < n; i++ {
		nx, err := Next(expr, tz, t)
		if err != nil {
			return nil, err
		}
		out = append(out, nx)
		t = nx
	}
	return out, nil
}

// Grace is how late a firing may still run; later ones count as missed
// (no catch-up after downtime, §9.11).
const Grace = 5 * time.Minute

// DueState says what to do about a schedule at now.
type DueState int

const (
	NotDue DueState = iota
	Due
	Missed
)

// Check classifies a planned time.
func Check(planned, now time.Time) DueState {
	switch {
	case now.Before(planned):
		return NotDue
	case now.Sub(planned) > Grace:
		return Missed
	default:
		return Due
	}
}

// OverlapAction is what to do when the previous run may still be active.
type OverlapAction int

const (
	RunNow OverlapAction = iota
	SkipThis
	QueueAfter
)

// Overlap decides what to do given whether the previous run is still active.
func Overlap(policy types.OverlapPolicy, previousActive bool) (OverlapAction, string) {
	if !previousActive {
		return RunNow, ""
	}
	switch policy {
	case types.OverlapParallel:
		return RunNow, ""
	case types.OverlapQueue:
		return QueueAfter, "previous run still active; queued"
	default:
		return SkipThis, "skipped because the previous run had not finished"
	}
}
