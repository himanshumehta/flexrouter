// Package xai validates an xAI key; usage is shown as unknown (FR-4.9).
package xai

import (
	"context"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(p{}) }

var Base = "https://api.x.ai/v1"

type p struct{}

func (p) ID() string   { return "xai" }
func (p) Name() string { return "xAI" }
func (p) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey},
		Endpoints: []string{Base + "/api-key"},
		KeyHelp:   "an API key from console.x.ai",
	}
}
func (p) Detect(*provider.Env) []provider.Detection { return nil }

type keyInfo struct {
	Name           string `json:"name"`
	APIKeyBlocked  bool   `json:"api_key_blocked"`
	APIKeyDisabled bool   `json:"api_key_disabled"`
	TeamBlocked    bool   `json:"team_blocked"`
}

func (p) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	var k keyInfo
	if err := env.JSON(ctx, provider.Request{URL: Base + "/api-key", Headers: map[string]string{"Authorization": "Bearer " + key}}, &k); err != nil {
		return nil, err
	}
	if k.APIKeyBlocked || k.APIKeyDisabled || k.TeamBlocked {
		return nil, provider.Errf(model.StatusAuthNeeded, "xAI reports this key or its team is blocked or disabled")
	}
	// xAI exposes no balance or usage for a key: keep it unknown, never 0.
	return &model.Reading{
		Plan: "xAI API", Source: "xai api", FetchedAt: env.Now(),
		Balances: []model.Money{{Currency: "USD", Label: "Balance"}},
		Notes:    []string{"Key works. xAI does not expose usage or balance through the API."},
	}, nil
}
