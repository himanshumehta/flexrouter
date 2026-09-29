// Package copilot reads GitHub Copilot's monthly allowance, overage and
// free-tier counts (FR-4.4) with one request per read, using the GitHub
// CLI's sign-in (`gh auth token`). The endpoint is GitHub's internal
// Copilot endpoint (docs/VENDORS.md §4).
package copilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

var URL = "https://api.github.com/copilot_internal/user"

// P is the Copilot provider.
type P struct{}

func (P) ID() string   { return "copilot" }
func (P) Name() string { return "GitHub Copilot" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthLocal}, Windows: true,
		FilesRead: []string{"~/.config/gh/hosts.yml (signed-in username; discovery only)"},
		Endpoints: []string{URL},
		Commands:  []string{"gh auth token"},
	}
}

// Detect reads the gh username from hosts.yml.
func (P) Detect(env *provider.Env) []provider.Detection {
	dir := env.Getenv("GH_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(env.Home, ".config", "gh")
	}
	b, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return nil
	}
	user := ""
	inGitHub := false
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inGitHub = strings.HasPrefix(line, "github.com:")
			continue
		}
		t := strings.TrimSpace(line)
		if inGitHub && strings.HasPrefix(t, "user:") && user == "" {
			user = strings.TrimSpace(strings.TrimPrefix(t, "user:"))
		}
	}
	if !inGitHubSeen(string(b)) {
		return nil
	}
	return []provider.Detection{{Provider: "copilot", Identity: user, Source: "~/.config/gh/hosts.yml"}}
}

func inGitHubSeen(s string) bool { return strings.Contains(s, "github.com:") }

type quota struct {
	Entitlement      *float64 `json:"entitlement"`
	Remaining        *float64 `json:"remaining"`
	PercentRemaining *float64 `json:"percent_remaining"`
	Unlimited        bool     `json:"unlimited"`
	OverageCount     *float64 `json:"overage_count"`
	OveragePermitted bool     `json:"overage_permitted"`
}

// User is the copilot_internal/user response.
type User struct {
	Plan           string           `json:"copilot_plan"`
	QuotaResetDate string           `json:"quota_reset_date"`
	LimitedReset   string           `json:"limited_user_reset_date"`
	Snapshots      map[string]quota `json:"quota_snapshots"`
	MonthlyQuotas  map[string]any   `json:"monthly_quotas"`
	LimitedQuotas  map[string]any   `json:"limited_user_quotas"`
}

var names = map[string]string{"premium_interactions": "Premium requests", "chat": "Chat", "completions": "Completions"}

// Normalize converts the response to a reading.
func Normalize(u *User) *model.Reading {
	r := &model.Reading{Source: "gh sign-in + copilot api"}
	switch strings.ToLower(u.Plan) {
	case "individual":
		r.Plan = "Copilot Pro"
	case "individual_pro":
		r.Plan = "Copilot Pro+"
	case "business":
		r.Plan = "Copilot Business"
	case "enterprise":
		r.Plan = "Copilot Enterprise"
	case "free", "":
		if u.Plan != "" || u.MonthlyQuotas != nil {
			r.Plan = "Copilot Free"
		}
	default:
		r.Plan = "Copilot " + u.Plan
	}
	reset := provider.ParseTime(u.QuotaResetDate)
	for _, k := range []string{"premium_interactions", "chat", "completions"} {
		q, ok := u.Snapshots[k]
		if !ok || q.Unlimited {
			continue
		}
		// entitlement 0 / remaining 0 / 100% is a placeholder, not "0% used".
		if q.Entitlement == nil || *q.Entitlement <= 0 {
			continue
		}
		w := model.Window{Kind: model.Monthly, Name: names[k], Limit: q.Entitlement, ResetsAt: reset, Unit: "requests"}
		if q.Remaining != nil {
			used := *q.Entitlement - *q.Remaining
			if used < 0 {
				used = 0
			}
			w.Used = &used
		}
		if q.PercentRemaining != nil {
			p := 100 - *q.PercentRemaining
			w.UsedPct = &p
		}
		r.Windows = append(r.Windows, w)
		if q.OverageCount != nil && *q.OverageCount > 0 {
			r.Notes = append(r.Notes, fmt.Sprintf("%s overage: %.0f billed beyond the allowance.", names[k], *q.OverageCount))
		}
		if k == "premium_interactions" && q.OveragePermitted {
			r.Notes = append(r.Notes, "Overage billing is on for premium requests.")
		}
	}
	// Free tier: monthly_quotas is the entitlement, limited_user_quotas what remains.
	if len(r.Windows) == 0 && u.MonthlyQuotas != nil {
		freeReset := provider.ParseTime(u.LimitedReset)
		if freeReset == nil {
			freeReset = reset
		}
		for _, k := range []string{"chat", "completions"} {
			total, left := provider.Num(u.MonthlyQuotas[k]), provider.Num(u.LimitedQuotas[k])
			if total == nil || *total <= 0 {
				continue
			}
			w := model.Window{Kind: model.Monthly, Name: names[k], Limit: total, ResetsAt: freeReset, Unit: "requests"}
			if left != nil {
				used := *total - *left
				w.Used = &used
			}
			r.Windows = append(r.Windows, w)
		}
	}
	return r
}

func (P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	gh, err := env.LookPath("gh")
	if err != nil {
		return nil, provider.Errf(model.StatusToolMissing, "GitHub CLI (`gh`) is not installed or not on PATH")
	}
	out, err := env.Run(ctx, nil, gh, "auth", "token")
	tok := strings.TrimSpace(string(out))
	if err != nil || tok == "" {
		return nil, provider.Errf(model.StatusAuthNeeded, "gh is not signed in; run `gh auth login`")
	}
	var u User
	err = env.JSON(ctx, provider.Request{URL: URL, Headers: map[string]string{
		"Authorization":        "token " + tok,
		"X-Github-Api-Version": "2025-04-01",
	}}, &u)
	if err != nil {
		if st, _ := provider.StatusOf(err); st == model.StatusAuthNeeded {
			return nil, provider.Errf(model.StatusAuthNeeded, "GitHub rejected the gh token for Copilot usage; make sure this GitHub account has Copilot (%v)", err)
		}
		return nil, err
	}
	r := Normalize(&u)
	r.FetchedAt = env.Now()
	if len(r.Windows) == 0 {
		r.Notes = append(r.Notes, "Copilot reports no metered allowance for this seat (unlimited or billed by usage).")
	}
	return r, nil
}
