// Package claudecode integrates with Claude Code: the statusLine command
// (FR-12.1–12.3), settings.json setup (FR-12.4) and the limit-hit hook
// (FR-12.5).
package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// StatusInput is the part of Claude Code's statusLine stdin aitank uses
// (docs/VENDORS.md §1a).
type StatusInput struct {
	SessionID  string `json:"session_id"`
	RateLimits *struct {
		FiveHour   *limit `json:"five_hour"`
		SevenDay   *limit `json:"seven_day"`
		SpendLimit *limit `json:"spend_limit"`
	} `json:"rate_limits"`
}

type limit struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"` // epoch seconds
}

// SessionAccount finds the tracked Claude account the running session
// belongs to, from CLAUDE_CONFIG_DIR (unset means the default profile).
func SessionAccount(cfg *config.Config, configDir string) *config.Account {
	want := filepath.Clean(configDir)
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Provider != "claude" {
			continue
		}
		if configDir == "" && a.ProfileDir == "" {
			return a
		}
		if configDir != "" && a.ProfileDir != "" && filepath.Clean(a.ProfileDir) == want {
			return a
		}
	}
	return nil
}

// Windows converts the statusLine rate limits to windows.
func (in *StatusInput) Windows() []model.Window {
	if in == nil || in.RateLimits == nil {
		return nil
	}
	var out []model.Window
	add := func(kind model.WindowKind, name string, l *limit) {
		if l == nil || l.UsedPercentage == nil {
			return
		}
		w := model.Window{Kind: kind, Name: name, UsedPct: l.UsedPercentage, Unit: "%"}
		if l.ResetsAt != nil && *l.ResetsAt > 0 {
			t := time.Unix(int64(*l.ResetsAt), 0).UTC()
			w.ResetsAt = &t
		}
		out = append(out, w)
	}
	add(model.FiveHour, "5-hour", in.RateLimits.FiveHour)
	add(model.Weekly, "Weekly", in.RateLimits.SevenDay)
	add(model.Monthly, "Spend limit", in.RateLimits.SpendLimit)
	return out
}

// Merge folds statusLine windows into the last full reading: matching
// windows are replaced, others (such as per-model limits) are kept.
func Merge(prev *model.Reading, acct *config.Account, ws []model.Window, now time.Time) *model.Reading {
	r := &model.Reading{AccountID: acct.ID, Provider: acct.Provider, Plan: acct.Plan, Source: "claude code statusline"}
	if prev != nil {
		cp := *prev
		r = &cp
		r.Windows = append([]model.Window(nil), prev.Windows...)
		r.Source = "claude code statusline"
	}
	for _, w := range ws {
		replaced := false
		for i := range r.Windows {
			if r.Windows[i].Kind == w.Kind && r.Windows[i].Model == "" {
				r.Windows[i].UsedPct, r.Windows[i].ResetsAt = w.UsedPct, w.ResetsAt
				r.Windows[i].Used, r.Windows[i].Limit = nil, nil
				replaced = true
				break
			}
		}
		if !replaced {
			r.Windows = append(r.Windows, w)
		}
	}
	r.FetchedAt = now
	return r
}

// Ingest stores statusLine limits for the session's account (FR-12.3). It
// skips the write when nothing changed and the cache is younger than a
// minute, keeping the statusLine fast.
func Ingest(cfg *config.Config, acct *config.Account, in *StatusInput, now time.Time) error {
	ws := in.Windows()
	if acct == nil || len(ws) == 0 {
		return nil
	}
	return paths.WithLock(func() error {
		cache := state.LoadCache()
		e := cache.Get(acct.ID)
		if e.LastGood != nil && now.Sub(e.LastGood.FetchedAt) < time.Minute && sameWindows(e.LastGood, ws) {
			return nil
		}
		r := Merge(e.LastGood, acct, ws, now)
		e.LastGood = r
		if e.LastStatus != model.StatusRateLimited {
			e.LastStatus, e.LastError = model.StatusOK, ""
		}
		if err := state.AppendHistory(r); err != nil {
			return err
		}
		return cache.Save()
	})
}

func sameWindows(r *model.Reading, ws []model.Window) bool {
	for _, w := range ws {
		found := false
		for _, x := range r.Windows {
			if x.Kind == w.Kind && x.Model == "" {
				p := x.Pct()
				found = p != nil && w.UsedPct != nil && *p == *w.UsedPct
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SettingsPath is the settings.json of a Claude Code profile.
func SettingsPath(home, profileDir string) string {
	if profileDir == "" {
		profileDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(profileDir, "settings.json")
}

// Blocks are the settings aitank needs: a statusLine command and a
// StopFailure hook that fires when a session hits a rate limit.
func Blocks(binary string) map[string]any {
	return map[string]any{
		"statusLine": map[string]any{"type": "command", "command": binary + " status", "padding": 0},
		"hooks": map[string]any{
			"StopFailure": []any{
				map[string]any{
					"matcher": "rate_limit",
					"hooks":   []any{map[string]any{"type": "command", "command": binary + " hook limit-hit"}},
				},
			},
		},
	}
}

// ApplyResult says what `setup claude-code --apply` did.
type ApplyResult struct {
	Path    string
	Backup  string
	Changed bool
}

// Apply merges the blocks into settings.json, backing it up first. An
// existing statusLine that is not aitank's is replaced only when force is
// set; existing hooks are kept and aitank's hook is appended once.
func Apply(path, binary string, force bool) (ApplyResult, error) {
	res := ApplyResult{Path: path}
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return res, fmt.Errorf("%s is not valid JSON; fix it or merge the blocks by hand: %v", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return res, err
	}
	blocks := Blocks(binary)
	if cur, ok := settings["statusLine"].(map[string]any); ok {
		cmd, _ := cur["command"].(string)
		if !strings.Contains(cmd, "aitank") && !force {
			return res, fmt.Errorf("settings.json already has a statusLine (%q); rerun with --force to replace it", cmd)
		}
	}
	if !equalJSON(settings["statusLine"], blocks["statusLine"]) {
		settings["statusLine"] = blocks["statusLine"]
		res.Changed = true
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	list, _ := hooks["StopFailure"].([]any)
	if !hasAitankHook(list) {
		list = append(list, blocks["hooks"].(map[string]any)["StopFailure"].([]any)...)
		hooks["StopFailure"] = list
		settings["hooks"] = hooks
		res.Changed = true
	}
	if !res.Changed {
		return res, nil
	}
	if data != nil {
		if res.Backup, err = paths.Backup(path, "claude-settings.json"); err != nil {
			return res, fmt.Errorf("backup failed, nothing changed: %v", err)
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return res, err
	}
	return res, os.WriteFile(path, append(out, '\n'), 0o600)
}

func hasAitankHook(list []any) bool {
	for _, m := range list {
		mm, _ := m.(map[string]any)
		hs, _ := mm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); strings.Contains(c, "aitank") {
				return true
			}
		}
	}
	return false
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// HookInput is the StopFailure payload (docs/VENDORS.md §1e).
type HookInput struct {
	HookEventName string `json:"hook_event_name"`
	Error         string `json:"error"`
	ErrorDetails  string `json:"error_details"`
}

// IsLimitHit reports whether a hook payload means the session hit a limit.
func (h HookInput) IsLimitHit() bool {
	if h.Error == "rate_limit" {
		return true
	}
	d := strings.ToLower(h.ErrorDetails)
	return strings.Contains(d, "usage limit") || strings.Contains(d, "limit reached")
}
