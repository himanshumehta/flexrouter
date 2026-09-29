// Package anthropicapi reads current-month organisation spend with an
// Anthropic Admin API key (FR-4.10).
package anthropicapi

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

var Base = "https://api.anthropic.com/v1"

type p struct{}

func (p) ID() string   { return "anthropic-api" }
func (p) Name() string { return "Anthropic API (org)" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Balance: true,
		Endpoints: []string{Base + "/organizations/cost_report"},
		KeyHelp:   "an Admin API key (sk-ant-admin…) from console.anthropic.com/settings/admin-keys",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type costReport struct {
	Data []struct {
		Results []struct {
			Amount   string `json:"amount"` // cents, as a decimal string
			Currency string `json:"currency"`
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
		q := url.Values{"starting_at": {start.Format(time.RFC3339)}, "bucket_width": {"1d"}, "limit": {"31"}}
		if page != "" {
			q.Set("page", page)
		}
		var cr costReport
		err := env.JSON(ctx, provider.Request{URL: Base + "/organizations/cost_report?" + q.Encode(), Headers: map[string]string{
			"x-api-key": key, "anthropic-version": "2023-06-01",
		}}, &cr)
		if err != nil {
			return nil, err
		}
		for _, b := range cr.Data {
			for _, res := range b.Results {
				if v, err := strconv.ParseFloat(res.Amount, 64); err == nil {
					total += v / 100 // amounts are in cents
				}
				if res.Currency != "" {
					cur = res.Currency
				}
			}
		}
		if !cr.HasMore || cr.NextPage == "" {
			break
		}
		page = cr.NextPage
	}
	next := start.AddDate(0, 1, 0)
	return &model.Reading{
		Plan: "Anthropic API", Source: "anthropic admin api", FetchedAt: env.Now(),
		Spend: &model.Money{Amount: &total, Currency: cur, Label: "Spend " + start.Format("Jan")},
		Notes: []string{"Month-to-date spend; the month rolls over " + next.Format("2 Jan") + " (UTC)."},
	}, nil
}
