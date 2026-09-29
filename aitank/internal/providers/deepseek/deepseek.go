// Package deepseek reads the DeepSeek account balance (FR-4.8).
package deepseek

import (
	"context"
	"strconv"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(p{}) }

var Base = "https://api.deepseek.com"

type p struct{}

func (p) ID() string   { return "deepseek" }
func (p) Name() string { return "DeepSeek" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Balance: true,
		Endpoints: []string{Base + "/user/balance"},
		KeyHelp:   "an API key from platform.deepseek.com/api_keys",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type balance struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency       string `json:"currency"`
		TotalBalance   string `json:"total_balance"`
		GrantedBalance string `json:"granted_balance"`
		ToppedUp       string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

func (p) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	var b balance
	if err := env.JSON(ctx, provider.Request{URL: Base + "/user/balance", Headers: map[string]string{"Authorization": "Bearer " + key}}, &b); err != nil {
		return nil, err
	}
	r := &model.Reading{Plan: "DeepSeek API", Source: "deepseek api", FetchedAt: env.Now()}
	for _, bi := range b.BalanceInfos {
		r.Balances = append(r.Balances, model.Money{Amount: parse(bi.TotalBalance), Currency: bi.Currency, Label: "Balance"})
	}
	if !b.IsAvailable {
		r.Notes = append(r.Notes, "DeepSeek reports the balance is not enough for API calls.")
	}
	return r, nil
}

func parse(s string) *float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}
