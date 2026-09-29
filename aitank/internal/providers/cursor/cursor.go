// Package cursor reads Cursor's monthly usage pools, on-demand spend and
// billing cycle (FR-4.3) from the Cursor app's existing session, read-only.
// The session lives in Cursor's state.vscdb SQLite file; aitank opens it
// read-only with the system sqlite3 and never writes to it. The format is
// undocumented (docs/VENDORS.md §3).
package cursor

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

var Base = "https://cursor.com"

// P is the Cursor provider.
type P struct{}

func (P) ID() string   { return "cursor" }
func (P) Name() string { return "Cursor" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthLocal}, Windows: true, Balance: true,
		FilesRead: []string{"~/Library/Application Support/Cursor/User/globalStorage/state.vscdb (keys cursorAuth/*, opened read-only)"},
		Endpoints: []string{Base + "/api/usage-summary", Base + "/api/auth/me (only until the email is known)"},
		Commands:  []string{"/usr/bin/sqlite3 -readonly"},
	}
}

// DBPath is Cursor's state database.
func DBPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
}

// SettingsPath is Cursor's user settings.json.
func SettingsPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json")
}

func sqlite() string {
	if _, err := os.Stat("/usr/bin/sqlite3"); err == nil {
		return "/usr/bin/sqlite3"
	}
	return "sqlite3"
}

func dbValue(ctx context.Context, env *provider.Env, key string) (string, error) {
	db := DBPath(env.Home)
	if _, err := os.Stat(db); err != nil {
		return "", provider.Errf(model.StatusToolMissing, "Cursor's session database was not found; is Cursor installed and signed in?")
	}
	q := fmt.Sprintf("SELECT value FROM ItemTable WHERE key = '%s';", strings.ReplaceAll(key, "'", "''"))
	out, err := env.Run(ctx, nil, sqlite(), "-readonly", "-noheader", db, q)
	if err != nil {
		return "", provider.Errf(model.StatusError, "could not read Cursor's session database: %v", err)
	}
	return strings.Trim(strings.TrimSpace(string(out)), "\""), nil
}

// Detect reads the cached email from Cursor's database. It runs the local
// sqlite3 tool only; no network, no Cursor process.
func (P) Detect(env *provider.Env) []provider.Detection {
	ctx := context.Background()
	tok, err := dbValue(ctx, env, "cursorAuth/accessToken")
	if err != nil || tok == "" {
		return nil
	}
	email, _ := dbValue(ctx, env, "cursorAuth/cachedEmail")
	plan, _ := dbValue(ctx, env, "cursorAuth/stripeMembershipType")
	return []provider.Detection{{Provider: "cursor", Identity: email, Plan: planName(plan), Source: "Cursor state.vscdb"}}
}

func planName(s string) string {
	switch strings.ToLower(s) {
	case "":
		return ""
	case "free", "hobby":
		return "Cursor Hobby"
	case "pro":
		return "Cursor Pro"
	case "pro_plus", "pro+":
		return "Cursor Pro+"
	case "ultra":
		return "Cursor Ultra"
	case "business", "team", "enterprise":
		return "Cursor " + strings.ToUpper(s[:1]) + s[1:]
	}
	return "Cursor " + s
}

type pool struct {
	Enabled          *bool    `json:"enabled"`
	Used             *float64 `json:"used"` // cents
	Limit            *float64 `json:"limit"`
	Remaining        *float64 `json:"remaining"`
	AutoPercentUsed  *float64 `json:"autoPercentUsed"`
	APIPercentUsed   *float64 `json:"apiPercentUsed"`
	TotalPercentUsed *float64 `json:"totalPercentUsed"`
}

// Summary is /api/usage-summary.
type Summary struct {
	BillingCycleStart string `json:"billingCycleStart"`
	BillingCycleEnd   string `json:"billingCycleEnd"`
	MembershipType    string `json:"membershipType"`
	IsUnlimited       bool   `json:"isUnlimited"`
	IndividualUsage   *struct {
		Plan     *pool `json:"plan"`
		OnDemand *pool `json:"onDemand"`
	} `json:"individualUsage"`
	TeamUsage *struct {
		OnDemand *pool `json:"onDemand"`
		Pooled   *pool `json:"pooled"`
	} `json:"teamUsage"`
}

func dollars(c *float64) *float64 {
	if c == nil {
		return nil
	}
	v := *c / 100
	return &v
}

// Normalize converts the usage summary to a reading.
func Normalize(s *Summary) *model.Reading {
	r := &model.Reading{Plan: planName(s.MembershipType), Source: "cursor app session"}
	end := provider.ParseTime(s.BillingCycleEnd)
	if iu := s.IndividualUsage; iu != nil {
		if p := iu.Plan; p != nil && (p.Enabled == nil || *p.Enabled) {
			w := model.Window{Kind: model.BillingCycle, Name: "Included usage", Used: dollars(p.Used), Limit: dollars(p.Limit), Unit: "USD", ResetsAt: end, UsedPct: p.TotalPercentUsed}
			if w.Pct() != nil {
				r.Windows = append(r.Windows, w)
			}
			for _, sub := range []struct {
				name string
				v    *float64
			}{{"Auto pool", p.AutoPercentUsed}, {"API pool", p.APIPercentUsed}} {
				if sub.v != nil {
					r.Windows = append(r.Windows, model.Window{Kind: model.BillingCycle, Name: sub.name, UsedPct: sub.v, ResetsAt: end, Unit: "%"})
				}
			}
		}
		if od := iu.OnDemand; od != nil && od.Enabled != nil && *od.Enabled {
			r.Spend = &model.Money{Amount: dollars(od.Used), Currency: "USD", Label: "On-demand spend"}
			if od.Limit != nil && *od.Limit > 0 {
				r.Balances = append(r.Balances, model.Money{Amount: dollars(od.Remaining), Currency: "USD", Label: "On-demand left"})
			}
		}
	}
	if s.IsUnlimited {
		r.Notes = append(r.Notes, "Cursor reports this plan as unlimited.")
	}
	return r
}

func (P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	tok, err := dbValue(ctx, env, "cursorAuth/accessToken")
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, provider.Errf(model.StatusAuthNeeded, "Cursor is not signed in; sign in in the Cursor app")
	}
	claims := provider.JWTClaims(tok)
	sub := provider.Str(claims, "sub")
	if i := strings.LastIndex(sub, "|"); i >= 0 {
		sub = sub[i+1:]
	}
	if sub == "" {
		return nil, provider.Errf(model.StatusUnsupported, "Cursor's stored session has an unexpected format")
	}
	if exp := provider.Num(claims["exp"]); exp != nil && env.Now().Unix() > int64(*exp) {
		return nil, provider.Errf(model.StatusAuthNeeded, "Cursor's session has expired; open Cursor to renew it")
	}
	h := map[string]string{"Cookie": "WorkosCursorSessionToken=" + sub + "%3A%3A" + tok}
	var s Summary
	if err := env.JSON(ctx, provider.Request{URL: Base + "/api/usage-summary", Headers: h}, &s); err != nil {
		return nil, err
	}
	r := Normalize(&s)
	r.FetchedAt = env.Now()
	if acct.Identity == "" {
		var me struct {
			Email string `json:"email"`
		}
		if env.JSON(ctx, provider.Request{URL: Base + "/api/auth/me", Headers: h}, &me) == nil {
			r.Identity = me.Email
		}
	}
	return r, nil
}
