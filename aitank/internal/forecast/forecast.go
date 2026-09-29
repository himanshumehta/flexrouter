// Package forecast computes burn rates from reading history (FR-8).
package forecast

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// Forecast is the pace of one window.
type Forecast struct {
	Account     string        `json:"account"`
	Window      string        `json:"window"`
	WindowName  string        `json:"window_name"`
	PctPerHour  float64       `json:"pct_per_hour"`
	EmptyIn     time.Duration `json:"-"`
	EmptyInMins *float64      `json:"empty_in_minutes"` // nil unless a warning applies
	Warn        bool          `json:"warn"`
}

// Message is the warning text, e.g. "5h limit runs out in ~55 min at this pace".
func (f Forecast) Message() string {
	return fmt.Sprintf("%s limit runs out in ~%d min at this pace", f.WindowName, int(math.Round(f.EmptyIn.Minutes())))
}

// Options tune the forecast.
type Options struct {
	Pace time.Duration // how far back to measure (default 60 min, FR-8.2)
	Warn time.Duration // warn when empty sooner than this (default 60 min, FR-8.3)
}

// Compute returns the pace for each window of r that has enough history.
// Samples from before the window's last reset are ignored, the pace must be
// positive, and the latest sample must be recent: when usage stops or slows
// the warning disappears (FR-8.4). A forecast never reaches further than the
// warn horizon, so it never projects days ahead.
func Compute(r *model.Reading, history []state.Point, now time.Time, opt Options) []Forecast {
	if opt.Pace <= 0 {
		opt.Pace = time.Hour
	}
	if opt.Warn <= 0 {
		opt.Warn = time.Hour
	}
	var out []Forecast
	for _, w := range r.Windows {
		pct := w.Pct()
		if pct == nil {
			continue
		}
		pts := windowPoints(history, r.AccountID, w.Key(), now.Add(-opt.Pace), w.ResetsAt)
		// Include the current reading if it is newer than the history.
		if len(pts) == 0 || r.FetchedAt.After(pts[len(pts)-1].T) {
			pts = append(pts, state.Point{T: r.FetchedAt, UsedPct: *pct, ResetsAt: w.ResetsAt})
		}
		if len(pts) < 2 {
			continue
		}
		first, last := pts[0], pts[len(pts)-1]
		span := last.T.Sub(first.T)
		// Need at least 10 minutes of data and a sample from the last 15.
		if span < 10*time.Minute || now.Sub(last.T) > 15*time.Minute {
			continue
		}
		rate := slope(pts) // percent per minute
		if rate <= 0 {
			continue
		}
		f := Forecast{Account: r.AccountID, Window: w.Key(), WindowName: shortName(w), PctPerHour: rate * 60}
		remaining := 100 - last.UsedPct
		f.EmptyIn = time.Duration(remaining/rate*float64(time.Minute)) - now.Sub(last.T)
		if f.EmptyIn < 0 {
			f.EmptyIn = 0
		}
		resetsFirst := w.ResetsAt != nil && w.ResetsAt.Before(now.Add(f.EmptyIn))
		if f.EmptyIn <= opt.Warn && !resetsFirst && remaining > 0 {
			f.Warn = true
			m := f.EmptyIn.Minutes()
			f.EmptyInMins = &m
		}
		out = append(out, f)
	}
	return out
}

// Warnings returns only the forecasts that warn.
func Warnings(fs []Forecast) []Forecast {
	var out []Forecast
	for _, f := range fs {
		if f.Warn {
			out = append(out, f)
		}
	}
	return out
}

// windowPoints returns the account/window samples after since that belong
// to the current window cycle.
func windowPoints(h []state.Point, account, window string, since time.Time, resets *time.Time) []state.Point {
	var pts []state.Point
	for _, p := range h {
		if p.Account != account || p.Window != window || p.T.Before(since) {
			continue
		}
		pts = append(pts, p)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].T.Before(pts[j].T) })
	// Drop everything before a reset: a drop in usage or a changed reset time.
	start := 0
	for i := 1; i < len(pts); i++ {
		if pts[i].UsedPct < pts[i-1].UsedPct-0.5 || differentCycle(pts[i].ResetsAt, pts[i-1].ResetsAt) {
			start = i
		}
	}
	pts = pts[start:]
	if resets != nil && len(pts) > 0 && differentCycle(resets, pts[len(pts)-1].ResetsAt) {
		return nil
	}
	return pts
}

func differentCycle(a, b *time.Time) bool {
	if a == nil || b == nil {
		return false
	}
	d := a.Sub(*b)
	return d > 5*time.Minute || d < -5*time.Minute
}

// slope is a least-squares fit of used % against minutes.
func slope(pts []state.Point) float64 {
	t0 := pts[0].T
	var n, sx, sy, sxx, sxy float64
	for _, p := range pts {
		x := p.T.Sub(t0).Minutes()
		y := p.UsedPct
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func shortName(w model.Window) string {
	switch w.Kind {
	case model.FiveHour:
		return "5h"
	case model.Weekly:
		return "Weekly"
	case model.ModelWeekly:
		return "Weekly " + w.Model
	case model.Monthly:
		return "Monthly"
	}
	if w.Name != "" {
		return w.Name
	}
	return string(w.Kind)
}
