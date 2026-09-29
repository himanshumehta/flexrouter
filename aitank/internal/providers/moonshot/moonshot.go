// Package moonshot reads the Moonshot / Kimi account balance (FR-4.8).
package moonshot

import (
	"context"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(p{}) }

// Base is the international endpoint; accounts created on the China site
// set option region=cn to use api.moonshot.cn.
var (
	Base   = "https://api.moonshot.ai/v1"
	BaseCN = "https://api.moonshot.cn/v1"
)

type p struct{}

func (p) ID() string   { return "moonshot" }
func (p) Name() string { return "Moonshot / Kimi" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Balance: true,
		Endpoints: []string{Base + "/users/me/balance", BaseCN + "/users/me/balance (region=cn)"},
		KeyHelp:   "an API key from platform.moonshot.ai (use --option region=cn for platform.moonshot.cn keys)",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type balance struct {
	Data struct {
		Available *float64 `json:"available_balance"`
		Voucher   *float64 `json:"voucher_balance"`
		Cash      *float64 `json:"cash_balance"`
	} `json:"data"`
}

func (p) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	base, cur := Base, "USD"
	if acct.Options["region"] == "cn" {
		base, cur = BaseCN, "CNY"
	}
	var b balance
	if err := env.JSON(ctx, provider.Request{URL: base + "/users/me/balance", Headers: map[string]string{"Authorization": "Bearer " + key}}, &b); err != nil {
		return nil, err
	}
	r := &model.Reading{Plan: "Moonshot API", Source: "moonshot api", FetchedAt: env.Now()}
	r.Balances = append(r.Balances, model.Money{Amount: b.Data.Available, Currency: cur, Label: "Balance"})
	if b.Data.Voucher != nil && *b.Data.Voucher != 0 {
		r.Balances = append(r.Balances, model.Money{Amount: b.Data.Voucher, Currency: cur, Label: "Voucher"})
	}
	return r, nil
}
