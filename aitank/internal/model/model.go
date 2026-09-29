// Package model holds the normalised reading shape every provider produces
// (FR-5.1) and the maths built on it.
package model

import (
	"math"
	"strings"
	"time"
)

// SchemaVersion is the version of every --json document aitank prints
// (FR-9.5). Bump it only for breaking changes; adding fields is not breaking.
const SchemaVersion = "aitank/v1"

// WindowKind is one of the window types in FR-5.2.
type WindowKind string

const (
	FiveHour     WindowKind = "five_hour"
	Weekly       WindowKind = "weekly"
	ModelWeekly  WindowKind = "model_weekly"
	Monthly      WindowKind = "monthly"
	BillingCycle WindowKind = "billing_cycle"
	Prepaid      WindowKind = "prepaid" // no reset
)

// Window is one usage limit. Nil pointers mean the vendor did not say, and
// are shown as "—", never 0 (FR-5.4).
type Window struct {
	Kind     WindowKind `json:"kind"`
	Name     string     `json:"name"`
	Model    string     `json:"model,omitempty"`
	UsedPct  *float64   `json:"used_pct"`
	Used     *float64   `json:"used"`
	Limit    *float64   `json:"limit"`
	Unit     string     `json:"unit,omitempty"`
	ResetsAt *time.Time `json:"resets_at"` // vendor-reported only (FR-5.5)
}

// Key identifies a window across readings, for history and alerts.
func (w Window) Key() string {
	if w.Model != "" {
		return string(w.Kind) + ":" + strings.ToLower(w.Model)
	}
	if w.Kind == "" {
		return strings.ToLower(w.Name)
	}
	return string(w.Kind) + ":" + strings.ToLower(w.Name)
}

// Used returns percent used, deriving it from used/limit when needed.
func (w Window) Pct() *float64 {
	if w.UsedPct != nil {
		v := clamp(*w.UsedPct, 0, 100)
		return &v
	}
	if w.Used != nil && w.Limit != nil && *w.Limit > 0 {
		v := clamp(*w.Used / *w.Limit * 100, 0, 100)
		return &v
	}
	return nil
}

// Money is an amount in a currency. Amount nil means unknown.
type Money struct {
	Amount   *float64 `json:"amount"`
	Currency string   `json:"currency"`
	Label    string   `json:"label,omitempty"`
}

// Reading is one normalised read of one account (FR-5.1).
type Reading struct {
	AccountID string    `json:"account"`
	Provider  string    `json:"provider"`
	Plan      string    `json:"plan"`
	Identity  string    `json:"identity,omitempty"`
	Windows   []Window  `json:"windows"`
	Balances  []Money   `json:"balances"`
	Spend     *Money    `json:"spend,omitempty"` // current-period spend (API orgs, overage)
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"`
	// UsagePaused is set when the vendor says included usage is paused
	// (Codex, FR-4.2); such an account is treated as exhausted.
	UsagePaused bool     `json:"usage_paused,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// Status is an account's display status (FR-18.1).
type Status string

const (
	StatusOK          Status = "ok"
	StatusStale       Status = "stale"
	StatusAuthNeeded  Status = "auth_needed"
	StatusToolMissing Status = "tool_missing"
	StatusRateLimited Status = "rate_limited"
	StatusUnsupported Status = "unsupported"
	StatusError       Status = "error"
	StatusPaused      Status = "paused"
	StatusNeverRead   Status = "not_read"
)

// Label is the human text for a status.
func (s Status) Label() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusStale:
		return "stale"
	case StatusAuthNeeded:
		return "auth needed"
	case StatusToolMissing:
		return "vendor tool missing"
	case StatusRateLimited:
		return "rate-limited"
	case StatusUnsupported:
		return "unsupported"
	case StatusPaused:
		return "paused"
	case StatusNeverRead:
		return "not read yet"
	default:
		return "error"
	}
}

// Binding returns the tightest window (FR-5.3) and its percent left. When
// ignoreModel is true, per-model weekly windows are skipped (FR-7.3). ok is
// false when no window has a known percentage.
func (r *Reading) Binding(ignoreModel bool) (w Window, left float64, ok bool) {
	left = math.Inf(1)
	for _, win := range r.Windows {
		if ignoreModel && win.Kind == ModelWeekly {
			continue
		}
		p := win.Pct()
		if p == nil {
			continue
		}
		l := 100 - *p
		if l < left || (l == left && earlier(win.ResetsAt, w.ResetsAt)) {
			left, w, ok = l, win, true
		}
	}
	if !ok {
		return Window{}, 0, false
	}
	return w, left, true
}

// Left is the account's percent left, or nil when unknown.
func (r *Reading) Left(ignoreModel bool) *float64 {
	if r == nil {
		return nil
	}
	_, l, ok := r.Binding(ignoreModel)
	if !ok {
		return nil
	}
	if r.UsagePaused {
		l = 0
	}
	return &l
}

// SoonestReset is the earliest vendor-reported reset among windows.
func (r *Reading) SoonestReset() *time.Time {
	var best *time.Time
	for _, w := range r.Windows {
		if w.ResetsAt != nil && (best == nil || w.ResetsAt.Before(*best)) {
			t := *w.ResetsAt
			best = &t
		}
	}
	return best
}

func earlier(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	return b == nil || a.Before(*b)
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// F is a helper for building optional floats.
func F(v float64) *float64 { return &v }

// T is a helper for building optional times.
func T(t time.Time) *time.Time { return &t }
