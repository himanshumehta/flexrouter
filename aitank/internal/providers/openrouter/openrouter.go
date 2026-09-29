// Package openrouter reads OpenRouter balance or per-key spend (FR-4.7).
package openrouter

import (
	"context"
	"fmt"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(p{}) }

// Base is the API root; tests point it at a fake server.
var Base = "https://openrouter.ai/api/v1"

type p struct{}

func (p) ID() string   { return "openrouter" }
func (p) Name() string { return "OpenRouter" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Balance: true, Windows: true,
		Endpoints: []string{Base + "/credits (management key)", Base + "/key (any key)"},
		KeyHelp:   "a management key (account balance) or a regular API key (that key's spend and limit), from openrouter.ai/settings/keys",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type credits struct {
	Data struct {
		TotalCredits *float64 `json:"total_credits"`
		TotalUsage   *float64 `json:"total_usage"`
	} `json:"data"`
}

type keyInfo struct {
	Data struct {
		Label          string   `json:"label"`
		Usage          *float64 `json:"usage"`
		Limit          *float64 `json:"limit"`
		LimitRemaining *float64 `json:"limit_remaining"`
		LimitReset     string   `json:"limit_reset"`
		IsFreeTier     bool     `json:"is_free_tier"`
	} `json:"data"`
}

func (p) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Authorization": "Bearer " + key}
	r := &model.Reading{Plan: "OpenRouter", Source: "openrouter api", FetchedAt: env.Now()}
	// A management key can read the account balance.
	var c credits
	cerr := env.JSON(ctx, provider.Request{URL: Base + "/credits", Headers: h}, &c)
	if cerr == nil && c.Data.TotalCredits != nil && c.Data.TotalUsage != nil {
		bal := *c.Data.TotalCredits - *c.Data.TotalUsage
		r.Balances = append(r.Balances, model.Money{Amount: &bal, Currency: "USD", Label: "Balance"})
		r.Windows = append(r.Windows, model.Window{Kind: model.Prepaid, Name: "Credits", Used: c.Data.TotalUsage, Limit: c.Data.TotalCredits, Unit: "USD"})
		r.Plan = "OpenRouter credits"
		return r, nil
	}
	if cerr != nil {
		if st, _ := provider.StatusOf(cerr); st != model.StatusAuthNeeded {
			return nil, cerr
		}
	}
	// A regular key reads its own spend and limit.
	var k keyInfo
	if err := env.JSON(ctx, provider.Request{URL: Base + "/key", Headers: h}, &k); err != nil {
		return nil, err
	}
	if k.Data.IsFreeTier {
		r.Plan = "OpenRouter free tier"
	}
	r.Spend = &model.Money{Amount: k.Data.Usage, Currency: "USD", Label: "Key spend"}
	if k.Data.Limit != nil && *k.Data.Limit > 0 {
		w := model.Window{Kind: model.Prepaid, Name: "Key limit", Used: k.Data.Usage, Limit: k.Data.Limit, Unit: "USD"}
		switch k.Data.LimitReset {
		case "daily", "weekly", "monthly":
			w.Name = fmt.Sprintf("Key limit (%s)", k.Data.LimitReset)
			w.Kind = model.Monthly
			// OpenRouter does not report the reset time; leave it unknown
			// rather than estimating it (FR-5.5).
		}
		r.Windows = append(r.Windows, w)
		if k.Data.LimitRemaining != nil {
			r.Balances = append(r.Balances, model.Money{Amount: k.Data.LimitRemaining, Currency: "USD", Label: "Key limit left"})
		}
	} else {
		r.Notes = append(r.Notes, "Key has no spend limit; add a management key to see the account balance.")
	}
	return r, nil
}
