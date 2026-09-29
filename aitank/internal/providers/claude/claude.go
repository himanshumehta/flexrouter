// Package claude reads Claude Pro/Max usage (FR-4.1) through the local
// Claude Code install: it reads Claude Code's stored sign-in in place and
// asks Anthropic's usage endpoint for the plan's windows. It sends no
// prompts and makes no model requests, and it never renews the sign-in:
// when the stored token has expired, the account shows "auth needed" until
// the user opens Claude Code, which renews it itself.
//
// These interfaces are undocumented, so reads are gated on the Claude Code
// version (see docs/VENDORS.md §1).
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

// UsageURL is the usage endpoint; tests point it at a fake server.
var UsageURL = "https://api.anthropic.com/api/oauth/usage"

// P is the Claude provider.
type P struct{}

func (P) ID() string   { return "claude" }
func (P) Name() string { return "Claude (Pro/Max)" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyClaude, Auth: []provider.AuthMethod{provider.AuthLocal},
		Windows: true, Launch: true, Profiles: true,
		FilesRead: []string{
			"~/.claude.json (signed-in email, default profile)",
			"<profile>/.claude.json (signed-in email, other profiles)",
			"~/.claude-*/.claude.json (other profile folders, discovery only)",
			`Keychain item "Claude Code-credentials[-<hash>]" (read in place, never changed)`,
			"<profile>/.credentials.json (only where Claude Code stores its sign-in in a file)",
		},
		Endpoints:  []string{UsageURL},
		Commands:   []string{"claude --version", "/usr/bin/security find-generic-password"},
		VendorTool: "claude", MinVersion: "2.0.0", TestedMax: "2.1.284",
	}
}

// LaunchSpec binds the account to its profile folder via CLAUDE_CONFIG_DIR.
func (P) LaunchSpec(a *config.Account) (string, string, string) {
	return "claude", "CLAUDE_CONFIG_DIR", a.ProfileDir
}

// identityFile is where Claude Code keeps the signed-in account: ~/.claude.json
// for the default profile, <dir>/.claude.json when CLAUDE_CONFIG_DIR is set.
func identityFile(home, dir string) string {
	if dir == "" {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(dir, ".claude.json")
}

func readIdentity(home, dir string) (email, org string, ok bool) {
	m, err := provider.ReadJSONFile(identityFile(home, dir))
	if err != nil {
		return "", "", false
	}
	acct, _ := m["oauthAccount"].(map[string]any)
	if acct == nil {
		return "", "", false
	}
	return provider.Str(acct, "emailAddress"), provider.Str(acct, "organizationName"), true
}

// Detect finds the default profile and any ~/.claude-* profile folders.
func (P) Detect(env *provider.Env) []provider.Detection {
	var out []provider.Detection
	if email, _, ok := readIdentity(env.Home, ""); ok {
		out = append(out, provider.Detection{Provider: "claude", Identity: email, Source: "~/.claude.json"})
	}
	dirs, _ := filepath.Glob(filepath.Join(env.Home, ".claude-*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		if email, _, ok := readIdentity(env.Home, d); ok {
			out = append(out, provider.Detection{Provider: "claude", Identity: email, ProfileDir: d, Source: d + "/.claude.json"})
		}
	}
	return out
}

// KeychainService is the Keychain item Claude Code stores its sign-in
// under: unsuffixed only when CLAUDE_CONFIG_DIR is unset; otherwise
// suffixed with the first 8 hex digits of sha256 of the exact
// (NFC-normalised) folder string.
func KeychainService(dir string) string {
	if dir == "" {
		return "Claude Code-credentials"
	}
	sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

type oauthCreds struct {
	ClaudeAiOauth *struct {
		AccessToken      string   `json:"accessToken"`
		ExpiresAt        float64  `json:"expiresAt"` // epoch ms
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
		RateLimitTier    string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// credentials reads Claude Code's stored sign-in without changing it.
func credentials(ctx context.Context, env *provider.Env, dir string) (*oauthCreds, error) {
	var raw []byte
	if runtime.GOOS == "darwin" && env.Getenv("AITANK_CLAUDE_CREDS_FILE_ONLY") == "" {
		out, err := env.Run(ctx, nil, "/usr/bin/security", "find-generic-password", "-s", KeychainService(dir), "-w")
		if err == nil {
			raw = out
		}
	}
	if raw == nil {
		base := dir
		if base == "" {
			base = filepath.Join(env.Home, ".claude")
		}
		b, err := os.ReadFile(filepath.Join(base, ".credentials.json"))
		if err != nil {
			return nil, provider.Errf(model.StatusAuthNeeded, "no Claude Code sign-in found for this profile; run `aitank claude <id>` and sign in")
		}
		raw = b
	}
	var c oauthCreds
	if err := json.Unmarshal(raw, &c); err != nil || c.ClaudeAiOauth == nil || c.ClaudeAiOauth.AccessToken == "" {
		return nil, provider.Errf(model.StatusAuthNeeded, "Claude Code is not signed in to a Claude subscription in this profile")
	}
	return &c, nil
}

// PlanName turns the stored subscription fields into "Max 20x" etc.
func PlanName(sub, tier string) string {
	t := strings.ToLower(tier)
	switch {
	case strings.Contains(t, "max_20x"):
		return "Max 20x"
	case strings.Contains(t, "max_5x"):
		return "Max 5x"
	}
	switch strings.ToLower(sub) {
	case "max":
		return "Max"
	case "pro":
		return "Pro"
	case "team":
		return "Team"
	case "enterprise":
		return "Enterprise"
	case "":
		return ""
	}
	return strings.ToUpper(sub[:1]) + sub[1:]
}

type usageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type limitEntry struct {
	Kind     string   `json:"kind"`
	Group    string   `json:"group"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// Usage is the usage endpoint's response (docs/VENDORS.md §1b).
type Usage struct {
	FiveHour       *usageWindow `json:"five_hour"`
	SevenDay       *usageWindow `json:"seven_day"`
	SevenDayOpus   *usageWindow `json:"seven_day_opus"`
	SevenDaySonnet *usageWindow `json:"seven_day_sonnet"`
	ExtraUsage     *struct {
		IsEnabled    bool     `json:"is_enabled"`
		MonthlyLimit *float64 `json:"monthly_limit"` // cents
		UsedCredits  *float64 `json:"used_credits"`  // cents
		Currency     string   `json:"currency"`
	} `json:"extra_usage"`
	Limits []limitEntry `json:"limits"`
}

// Normalize converts the usage response to windows.
func Normalize(u *Usage) (windows []model.Window, balances []model.Money, spend *model.Money) {
	add := func(kind model.WindowKind, name, mdl string, w *usageWindow) {
		if w == nil || w.Utilization == nil {
			return
		}
		windows = append(windows, model.Window{Kind: kind, Name: name, Model: mdl, UsedPct: w.Utilization, ResetsAt: provider.ParseTime(w.ResetsAt), Unit: "%"})
	}
	add(model.FiveHour, "5-hour", "", u.FiveHour)
	add(model.Weekly, "Weekly", "", u.SevenDay)
	add(model.ModelWeekly, "Weekly (Opus)", "Opus", u.SevenDayOpus)
	add(model.ModelWeekly, "Weekly (Sonnet)", "Sonnet", u.SevenDaySonnet)
	have := map[string]bool{}
	for _, w := range windows {
		have[w.Key()] = true
	}
	// Newer accounts report per-model weekly caps in limits[].
	for _, l := range u.Limits {
		if l.Percent == nil {
			continue
		}
		var w model.Window
		switch {
		case l.Kind == "session" && !have[string(model.FiveHour)+":5-hour"]:
			w = model.Window{Kind: model.FiveHour, Name: "5-hour"}
		case l.Kind == "weekly_all" && !have[string(model.Weekly)+":weekly"]:
			w = model.Window{Kind: model.Weekly, Name: "Weekly"}
		case l.Kind == "weekly_scoped" && l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "":
			m := l.Scope.Model.DisplayName
			w = model.Window{Kind: model.ModelWeekly, Name: "Weekly (" + m + ")", Model: m}
		default:
			continue
		}
		if have[w.Key()] {
			continue
		}
		w.UsedPct, w.ResetsAt, w.Unit = l.Percent, provider.ParseTime(l.ResetsAt), "%"
		have[w.Key()] = true
		windows = append(windows, w)
	}
	if x := u.ExtraUsage; x != nil && x.IsEnabled {
		cur := x.Currency
		if cur == "" {
			cur = "USD"
		}
		if x.UsedCredits != nil {
			v := *x.UsedCredits / 100
			spend = &model.Money{Amount: &v, Currency: cur, Label: "Usage credits used"}
		}
		if x.MonthlyLimit != nil && x.UsedCredits != nil {
			v := (*x.MonthlyLimit - *x.UsedCredits) / 100
			balances = append(balances, model.Money{Amount: &v, Currency: cur, Label: "Usage credits left"})
		}
	}
	return windows, balances, spend
}

func (p P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	caps := p.Capabilities()
	bin, err := env.LookPath("claude")
	if err != nil {
		return nil, provider.Errf(model.StatusToolMissing, "Claude Code (`claude`) is not installed or not on PATH")
	}
	gate, err := env.Gate(ctx, caps, bin)
	if err != nil {
		return nil, err
	}
	creds, err := credentials(ctx, env, acct.ProfileDir)
	if err != nil {
		return nil, err
	}
	o := creds.ClaudeAiOauth
	if o.ExpiresAt > 0 && env.Now().After(time.UnixMilli(int64(o.ExpiresAt))) {
		return nil, provider.Errf(model.StatusAuthNeeded, "Claude Code's sign-in has expired; open Claude Code in this profile once (`aitank claude %s`) and it renews it. aitank never renews sign-ins", acct.ID)
	}
	if len(o.Scopes) > 0 && !contains(o.Scopes, "user:profile") {
		return nil, provider.Errf(model.StatusAuthNeeded, "this profile signed in with a long-lived token that cannot read usage; sign in with /login instead")
	}
	var u Usage
	err = env.JSON(ctx, provider.Request{URL: UsageURL, Headers: map[string]string{
		"Authorization":  "Bearer " + o.AccessToken,
		"anthropic-beta": "oauth-2025-04-20",
	}}, &u)
	if err != nil {
		if st, _ := provider.StatusOf(err); gate.Untested && st == model.StatusUnsupported {
			return nil, provider.Errf(model.StatusUnsupported, "%v (Claude Code %s is newer than the %s this reader was checked against)", err, gate.Version, caps.TestedMax)
		}
		return nil, err
	}
	r := &model.Reading{Plan: PlanName(o.SubscriptionType, o.RateLimitTier), Source: "claude code sign-in + usage endpoint", FetchedAt: env.Now()}
	if email, _, ok := readIdentity(env.Home, acct.ProfileDir); ok {
		r.Identity = email
	}
	r.Windows, r.Balances, r.Spend = Normalize(&u)
	if len(r.Windows) == 0 {
		return nil, provider.Errf(model.StatusUnsupported, "usage response had no windows; Claude Code %s may have changed the format", gate.Version)
	}
	if gate.Untested {
		r.Notes = append(r.Notes, fmt.Sprintf("Claude Code %s is newer than the version this reader was checked against (%s).", gate.Version, caps.TestedMax))
	}
	return r, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// NewProfileDir is the folder aitank creates for an extra Claude account.
func NewProfileDir(id string) string { return paths.Profile(id) }
