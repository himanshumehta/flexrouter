// Package alerts decides which notifications to send (FR-13) and delivers
// them through macOS Notification Center.
package alerts

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/bills"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// Notice is one notification to deliver.
type Notice struct {
	Key     string // de-duplication key; one per window cycle (FR-13.4)
	Kind    string // threshold | forecast | limit | refilled | bill
	Title   string
	Message string
}

// Evaluate returns the notices that should fire now and records them in st
// so they do not fire again in the same window cycle.
func Evaluate(v *engine.View, st *state.Alerts, now time.Time) []Notice {
	cfg := v.Config
	var out []Notice
	fire := func(n Notice) {
		if _, done := st.Fired[n.Key]; done {
			return
		}
		st.Fired[n.Key] = now
		out = append(out, n)
	}
	switchHint := ""
	if p := v.Rec.Overall; p != nil {
		switchHint = fmt.Sprintf(" Use %s (%.0f%% left): aitank %s %s", p.Label, p.Left, cmdFor(p.Family), p.AccountID)
	}
	for _, r := range v.Rows {
		a := r.Account
		if r.Reading == nil || r.Status != model.StatusOK {
			continue
		}
		w, left, ok := r.Reading.Binding(cfg.Recommend.IgnoreModelLimits)
		if !ok {
			continue
		}
		cycle := "none"
		if w.ResetsAt != nil {
			cycle = strconv.FormatInt(w.ResetsAt.Unix()/600, 10) // tolerate small reset jitter
		}
		base := a.ID + ":" + w.Key() + ":" + cycle
		prevKey := a.ID + ":" + w.Key()
		prev, hadPrev := st.LastLeft[prevKey]
		st.LastLeft[prevKey] = left

		if !a.Alerts.Enabled || (!a.QuietUntil.IsZero() && now.Before(a.QuietUntil)) {
			continue
		}
		hint := switchHint
		if v.Rec.Overall != nil && v.Rec.Overall.AccountID == a.ID {
			hint = ""
		}
		at := a.Alerts.At
		if at <= 0 {
			at = cfg.Alerts.DefaultAt
		}
		switch {
		case left <= 0:
			fire(Notice{Key: "limit:" + base, Kind: "limit", Title: a.Label() + " hit its limit",
				Message: fmt.Sprintf("%s limit reached; resets in %s.%s", w.Name, until(w.ResetsAt, now), hint)})
		case left <= at:
			fire(Notice{Key: "threshold:" + base, Kind: "threshold", Title: fmt.Sprintf("%s is at %.0f%% left", a.Label(), left),
				Message: fmt.Sprintf("%s window resets in %s.%s", w.Name, until(w.ResetsAt, now), hint)})
		}
		if cfg.Alerts.Forecast {
			for _, f := range r.Forecasts {
				if f.Warn {
					fire(Notice{Key: "forecast:" + a.ID + ":" + f.Window + ":" + cycle, Kind: "forecast", Title: a.Label(), Message: f.Message() + "." + hint})
				}
			}
		}
		if cfg.Alerts.Refilled && hadPrev && prev <= at && left >= 90 {
			fire(Notice{Key: "refilled:" + base, Kind: "refilled", Title: a.Label() + " refilled",
				Message: fmt.Sprintf("%s is back to %.0f%% left.", w.Name, left)})
		}
	}
	if cfg.Alerts.BillDaysBefore > 0 {
		for _, b := range bills.Due(cfg.Bills, now, cfg.Alerts.BillDaysBefore) {
			next := bills.NextRenewal(b, now)
			fire(Notice{Key: "bill:" + b.ID + ":" + next.Format("2006-01-02"), Kind: "bill", Title: b.Name + " renews soon",
				Message: fmt.Sprintf("%.2f %s on %s.", b.Price, b.Currency, next.Format("Mon 2 Jan"))})
		}
	}
	return out
}

func cmdFor(family string) string {
	if family == "codex" {
		return "codex"
	}
	if family == "claude" {
		return "claude"
	}
	return "cmd"
}

func until(t *time.Time, now time.Time) string {
	if t == nil {
		return "an unknown time"
	}
	d := t.Sub(now).Round(time.Minute)
	if d <= 0 {
		return "moments"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 24 {
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// Deliver shows a notice in Notification Center via osascript (FR-13.3).
// Text is passed as arguments, never spliced into the script.
func Deliver(n Notice, sound bool) error {
	script := []string{
		"-e", "on run argv",
		"-e", `display notification (item 2 of argv) with title "aitank" subtitle (item 1 of argv)` + soundClause(sound),
		"-e", "end run",
		n.Title, n.Message,
	}
	return exec.Command("/usr/bin/osascript", script...).Run()
}

func soundClause(on bool) string {
	if on {
		return ` sound name "Glass"`
	}
	return ""
}
