// Package openaiapi reads current-month organisation spend with an OpenAI
// admin key (FR-4.10).
package openaiapi

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(p{}) }

var Base = "https://api.openai.com/v1"

type p struct{}

func (p) ID() string   { return "openai-api" }
func (p) Name() string { return "OpenAI API (org)" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Balance: true,
		Endpoints: []string{Base + "/organization/costs"},
		KeyHelp:   "an Admin key (sk-admin-…) from platform.openai.com/settings/organization/admin-keys",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type costs struct {
	Data []struct {
		Results []struct {
			Amount struct {
				Value    float64 `json:"value"`
				Currency string  `json:"currency"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func (p) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	now := env.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	total, cur := 0.0, "USD"
	page := ""
	for i := 0; i < 10; i++ {
		q := url.Values{"start_time": {strconv.FormatInt(start.Unix(), 10)}, "bucket_width": {"1d"}, "limit": {"31"}}
		if page != "" {
			q.Set("page", page)
		}
		var c costs
		if err := env.JSON(ctx, provider.Request{URL: Base + "/organization/costs?" + q.Encode(), Headers: map[string]string{"Authorization": "Bearer " + key}}, &c); err != nil {
			return nil, err
		}
		for _, b := range c.Data {
			for _, res := range b.Results {
				total += res.Amount.Value
				if res.Amount.Currency != "" {
					cur = res.Amount.Currency
				}
			}
		}
		if !c.HasMore || c.NextPage == "" {
			break
		}
		page = c.NextPage
	}
	return &model.Reading{
		Plan: "OpenAI API", Source: "openai admin api", FetchedAt: env.Now(),
		Spend: &model.Money{Amount: &total, Currency: normalizeCur(cur), Label: "Spend " + start.Format("Jan")},
	}, nil
}

func normalizeCur(c string) string {
	if c == "usd" {
		return "USD"
	}
	return c
}
