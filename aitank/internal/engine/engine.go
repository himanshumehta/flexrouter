// Package engine refreshes accounts and builds the view every display
// command renders. Refresh is the only part that touches the network.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/forecast"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/recommend"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// RefreshOptions control a refresh run.
type RefreshOptions struct {
	Only  string // account id; empty means all
	Force bool   // ignore backoff (never ignores a vendor Retry-After)
}

// Outcome is the result of reading one account.
type Outcome struct {
	Account string
	Status  model.Status
	Err     error
	Skipped string // why the read was skipped, if it was
}

// maxBackoff caps the delay after repeated errors.
const maxBackoff = time.Hour

// Backoff returns the wait after n consecutive failures (FR-6.8): the refresh
// interval doubled per failure, capped at an hour.
func Backoff(interval time.Duration, failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := float64(interval) * math.Pow(2, float64(failures-1))
	if d > float64(maxBackoff) {
		return maxBackoff
	}
	return time.Duration(d)
}

// Refresh reads accounts and updates the cache, history and read log. Reads
// run concurrently; the cache is updated under the lock.
func Refresh(ctx context.Context, cfg *config.Config, env *provider.Env, opt RefreshOptions) ([]Outcome, error) {
	now := env.Now()
	cache := state.LoadCache()
	type job struct {
		acct *config.Account
		p    provider.Provider
	}
	var jobs []job
	var out []Outcome
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if opt.Only != "" && a.ID != opt.Only {
			continue
		}
		p, ok := provider.Get(a.Provider)
		if !ok {
			out = append(out, Outcome{Account: a.ID, Status: model.StatusUnsupported, Err: errors.New("unknown provider " + a.Provider)})
			continue
		}
		e := cache.Get(a.ID)
		if now.Before(e.NextAttempt) {
			rateLimited := e.LastStatus == model.StatusRateLimited
			if !opt.Force || rateLimited {
				out = append(out, Outcome{Account: a.ID, Status: e.LastStatus, Skipped: "backing off until " + e.NextAttempt.Local().Format("15:04")})
				continue
			}
		}
		jobs = append(jobs, job{a, p})
	}

	type result struct {
		acct    *config.Account
		reading *model.Reading
		err     error
		dur     time.Duration
		ver     string
	}
	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			jenv := *env
			ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			start := time.Now()
			r, err := j.p.Read(ctx, &jenv, j.acct)
			results[i] = result{j.acct, r, err, time.Since(start), jenv.VersionSeen}
		}(i, j)
	}
	wg.Wait()

	err := paths.WithLock(func() error {
		cache = state.LoadCache() // re-read: `aitank status` may have ingested meanwhile
		for _, res := range results {
			a := res.acct
			e := cache.Get(a.ID)
			e.LastAttempt = now
			if res.ver != "" {
				e.VendorVer = res.ver
			}
			entry := state.LogEntry{T: now, Account: a.ID, Provider: a.Provider, Millis: res.dur.Milliseconds()}
			if res.err == nil && res.reading != nil {
				r := res.reading
				r.AccountID, r.Provider = a.ID, a.Provider
				if r.FetchedAt.IsZero() {
					r.FetchedAt = now
				}
				e.LastGood = r
				e.LastStatus = model.StatusOK
				e.LastError = ""
				e.Failures = 0
				e.NextAttempt = time.Time{}
				if r.Plan != "" && r.Plan != a.Plan {
					a.Plan = r.Plan
				}
				if r.Identity != "" && a.Identity == "" {
					a.Identity = r.Identity
				}
				_ = state.AppendHistory(r)
				entry.Status, entry.Source = model.StatusOK, r.Source
				out = append(out, Outcome{Account: a.ID, Status: model.StatusOK})
			} else {
				if res.err == nil {
					res.err = errors.New("provider returned no reading")
				}
				st, retry := provider.StatusOf(res.err)
				// Keep the last good values (FR-6.5); record the error.
				e.LastStatus = st
				e.LastError = state.Redact(res.err.Error())
				e.Failures++
				wait := Backoff(cfg.RefreshInterval.Duration, e.Failures)
				if retry > wait {
					wait = retry
				}
				if st == model.StatusRateLimited && retry == 0 && wait < 15*time.Minute {
					wait = 15 * time.Minute
				}
				e.NextAttempt = now.Add(wait)
				entry.Status, entry.Error = st, res.err.Error()
				out = append(out, Outcome{Account: a.ID, Status: st, Err: res.err})
			}
			_ = state.AppendLog(entry)
		}
		if opt.Only == "" {
			cache.RefreshedAt = now
		}
		// Drop cache entries for removed accounts.
		for id := range cache.Accounts {
			if _, err := cfg.Account(id); err != nil {
				delete(cache.Accounts, id)
			}
		}
		_ = state.TrimLog()
		_ = state.PruneHistory(now)
		return cache.Save()
	})
	return out, err
}

// Row is one account as displays show it.
type Row struct {
	Account   *config.Account
	Status    model.Status
	Reading   *model.Reading // last good reading, may be nil
	Left      *float64
	Age       time.Duration // age of the reading
	Stale     bool
	Error     string
	Forecasts []forecast.Forecast
	Next      bool // this row is the overall "use next"
	Active    bool // this row is the current session's account
	VendorVer string
}

// View is everything a display needs, built from local files only.
type View struct {
	Now         time.Time
	Rows        []*Row
	Rec         recommend.Result
	RefreshedAt time.Time
	Config      *config.Config
}

// BuildView reads the cache and history and computes statuses, forecasts
// and recommendations. It makes no network calls (FR-6.3).
func BuildView(cfg *config.Config, now time.Time) *View {
	cache := state.LoadCache()
	history := state.LoadHistory(now.Add(-cfg.ForecastWindow.Duration - time.Minute))
	v := &View{Now: now, Config: cfg, RefreshedAt: cache.RefreshedAt}
	var cands []recommend.Candidate
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		e := cache.Accounts[a.ID]
		row := &Row{Account: a}
		row.Status = e.Status(now, cfg.StaleAfter.Duration, a.Paused)
		if e != nil {
			row.Reading = e.LastGood
			row.Error = e.LastError
			row.VendorVer = e.VendorVer
			if e.LastGood != nil {
				row.Age = now.Sub(e.LastGood.FetchedAt)
				row.Stale = row.Age > cfg.StaleAfter.Duration
				row.Left = e.LastGood.Left(cfg.Recommend.IgnoreModelLimits)
				row.Forecasts = forecast.Compute(e.LastGood, history, now, forecast.Options{Pace: cfg.ForecastWindow.Duration, Warn: cfg.ForecastWarn.Duration})
			}
		}
		// A paused account that has refilled rejoins automatically (FR-3.6):
		// that is handled by `pause` storing the reset time; see Unpause.
		v.Rows = append(v.Rows, row)
		c := recommend.Candidate{AccountID: a.ID, Label: a.Label(), Family: string(provider.FamilyOf(a.Provider)), Status: row.Status, Reading: row.Reading}
		if row.Reading != nil {
			if w, _, ok := row.Reading.Binding(cfg.Recommend.IgnoreModelLimits); ok {
				for _, f := range row.Forecasts {
					if f.Window == w.Key() {
						c.PctPerHour = f.PctPerHour
					}
				}
			}
		}
		cands = append(cands, c)
	}
	v.Rec = recommend.Recommend(cands, now, recommend.Options{IgnoreModelLimits: cfg.Recommend.IgnoreModelLimits, CloseMargin: cfg.Recommend.CloseMargin})
	if v.Rec.Overall != nil {
		for _, r := range v.Rows {
			r.Next = r.Account.ID == v.Rec.Overall.AccountID
		}
	}
	// Mark the active session's account (FR-10.2).
	markActiveSession(v.Rows)
	return v
}

// markActiveSession sets Active=true on the account matching the current
// session's env vars (CLAUDE_CONFIG_DIR for Claude, CODEX_HOME for Codex).
func markActiveSession(rows []*Row) {
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	codexHome := os.Getenv("CODEX_HOME")
	home := os.Getenv("HOME")

	// For Claude, detect identity from CLAUDE_CONFIG_DIR or fall back to default.
	claudeActiveEmail := detectClaudeIdentity(claudeDir, home)

	for _, r := range rows {
		switch r.Account.Provider {
		case "claude":
			// Match by identity (email) if we found one
			if claudeActiveEmail != "" && r.Account.Identity == claudeActiveEmail {
				r.Active = true
			} else if claudeDir == "" && r.Account.ProfileDir == "" {
				// No config dir set and no profile = default profile active
				r.Active = true
			} else if claudeDir != "" && r.Account.ProfileDir != "" &&
				filepath.Clean(claudeDir) == filepath.Clean(r.Account.ProfileDir) {
				r.Active = true
			}
		case "codex":
			if codexHome == "" && r.Account.ProfileDir == "" {
				r.Active = true
			} else if codexHome != "" && r.Account.ProfileDir != "" &&
				filepath.Clean(codexHome) == filepath.Clean(r.Account.ProfileDir) {
				r.Active = true
			}
		}
	}
}

// detectClaudeIdentity finds the signed-in email for the current Claude session.
// If CLAUDE_CONFIG_DIR has a .claude.json, use that. Otherwise fall back to ~/.claude.json.
func detectClaudeIdentity(configDir, home string) string {
	// Try the config dir first (works when it's a profile dir)
	if configDir != "" {
		if email := readClaudeIdentityFile(filepath.Join(configDir, ".claude.json")); email != "" {
			return email
		}
	}
	// Fall back to default profile
	if home != "" {
		return readClaudeIdentityFile(filepath.Join(home, ".claude.json"))
	}
	return ""
}

// readClaudeIdentityFile reads the signed-in email from a .claude.json file.
func readClaudeIdentityFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if acct, ok := m["oauthAccount"].(map[string]any); ok {
		if email, ok := acct["emailAddress"].(string); ok {
			return email
		}
	}
	return ""
}

// Row returns the row for an account id.
func (v *View) Row(id string) *Row {
	for _, r := range v.Rows {
		if r.Account.ID == id {
			return r
		}
	}
	return nil
}

// AutoResume clears pauses whose "until refilled" time has passed (FR-3.6).
// It returns true when the config changed.
func AutoResume(cfg *config.Config, now time.Time) bool {
	changed := false
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Paused && a.Options != nil {
			if until, err := time.Parse(time.RFC3339, a.Options["paused_until"]); err == nil && now.After(until) {
				a.Paused = false
				delete(a.Options, "paused_until")
				changed = true
			}
		}
		if !a.QuietUntil.IsZero() && now.After(a.QuietUntil) {
			a.QuietUntil = time.Time{}
			changed = true
		}
	}
	return changed
}
