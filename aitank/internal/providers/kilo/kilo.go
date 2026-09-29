// Package kilo reads Kilo Code credit balance, Kilo Pass credits and the
// next billing date (FR-4.6) using the Kilo CLI's sign-in or a pasted key.
// The tRPC endpoint is internal to kilo.ai (docs/VENDORS.md §5); parsing is
// deliberately tolerant.
package kilo

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

var (
	TRPC    = "https://app.kilo.ai/api/trpc"
	Profile = "https://api.kilo.ai/api/profile"
)

// P is the Kilo provider.
type P struct{}

func (P) ID() string   { return "kilo" }
func (P) Name() string { return "Kilo Code" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthLocal, provider.AuthKey}, Balance: true, Windows: true,
		FilesRead: []string{"~/.local/share/kilo/auth.json (Kilo CLI sign-in, read in place)"},
		Endpoints: []string{TRPC + "/user.getCreditBlocks,kiloPass.getState", Profile + " (only until the email is known)"},
		KeyHelp:   "a Kilo API key (KILO_API_KEY) from app.kilo.ai",
	}
}

func authFile(home string) string { return filepath.Join(home, ".local", "share", "kilo", "auth.json") }

func localToken(env *provider.Env) string {
	m, err := provider.ReadJSONFile(authFile(env.Home))
	if err != nil {
		return ""
	}
	return provider.Str(m, "kilo", "access")
}

func (P) Detect(env *provider.Env) []provider.Detection {
	if localToken(env) == "" {
		return nil
	}
	return []provider.Detection{{Provider: "kilo", Source: "~/.local/share/kilo/auth.json", Note: "email shows after the first read"}}
}

// Normalize reads the tRPC batch response.
func Normalize(batch []map[string]any) *model.Reading {
	r := &model.Reading{Plan: "Kilo Code", Source: "kilo sign-in + kilo.ai"}
	payload := func(i int) map[string]any {
		if i >= len(batch) {
			return nil
		}
		res, _ := batch[i]["result"].(map[string]any)
		data, _ := res["data"].(map[string]any)
		if j, ok := data["json"].(map[string]any); ok {
			return j
		}
		return data
	}
	if credits := payload(0); credits != nil {
		var total, left float64
		saw := false
		if blocks, ok := credits["creditBlocks"].([]any); ok {
			for _, b := range blocks {
				bm, _ := b.(map[string]any)
				if v := provider.Num(bm["amount_mUsd"]); v != nil {
					total += *v / 1e6
					saw = true
				}
				if v := provider.Num(bm["balance_mUsd"]); v != nil {
					left += *v / 1e6
				}
			}
		}
		if !saw {
			if v := provider.Num(credits["totalBalance_mUsd"]); v != nil {
				left = *v / 1e6
				r.Balances = append(r.Balances, model.Money{Amount: &left, Currency: "USD", Label: "Credit balance"})
			}
		} else {
			r.Balances = append(r.Balances, model.Money{Amount: &left, Currency: "USD", Label: "Credit balance"})
			used := total - left
			r.Windows = append(r.Windows, model.Window{Kind: model.Prepaid, Name: "Credits", Used: &used, Limit: &total, Unit: "USD"})
		}
	}
	if pass := payload(1); pass != nil {
		sub, _ := pass["subscription"].(map[string]any)
		if sub == nil {
			sub = pass
		}
		used := provider.Num(sub["currentPeriodUsageUsd"])
		base := provider.Num(sub["currentPeriodBaseCreditsUsd"])
		if base != nil {
			total := *base
			if b := provider.Num(sub["currentPeriodBonusCreditsUsd"]); b != nil {
				total += *b
			}
			var resets = provider.ParseTime(sub["nextBillingAt"])
			if resets == nil {
				resets = provider.ParseTime(sub["nextRenewalAt"])
			}
			r.Windows = append(r.Windows, model.Window{Kind: model.BillingCycle, Name: "Kilo Pass", Used: used, Limit: &total, Unit: "USD", ResetsAt: resets})
			r.Plan = "Kilo Pass"
			if t, ok := sub["tier"].(string); ok && t != "" {
				r.Plan = "Kilo Pass " + t
			}
		}
	}
	return r
}

func (P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	tok := ""
	if acct.Auth == string(provider.AuthKey) {
		k, err := env.Key(acct)
		if err != nil {
			return nil, err
		}
		tok = k
	} else if tok = localToken(env); tok == "" {
		return nil, provider.Errf(model.StatusAuthNeeded, "Kilo CLI is not signed in; run `kilo auth login` or add a key with `aitank add kilo --key`")
	}
	h := map[string]string{"Authorization": "Bearer " + tok}
	input, _ := json.Marshal(map[string]any{"0": map[string]any{"json": nil}, "1": map[string]any{"json": nil}})
	u := TRPC + "/user.getCreditBlocks,kiloPass.getState?batch=1&input=" + url.QueryEscape(string(input))
	var batch []map[string]any
	if err := env.JSON(ctx, provider.Request{URL: u, Headers: h}, &batch); err != nil {
		return nil, err
	}
	r := Normalize(batch)
	r.FetchedAt = env.Now()
	if len(r.Balances) == 0 && len(r.Windows) == 0 {
		return nil, provider.Errf(model.StatusUnsupported, "Kilo's response had no credit data; the format may have changed")
	}
	if acct.Identity == "" {
		var p map[string]any
		if env.JSON(ctx, provider.Request{URL: Profile, Headers: h}, &p) == nil {
			r.Identity = provider.Str(p, "user", "email")
			if r.Identity == "" {
				r.Identity = provider.Str(p, "email")
			}
		}
	}
	return r, nil
}
