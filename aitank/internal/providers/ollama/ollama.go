// Package ollama is the experimental Ollama Cloud reader (FR-4.5). Ollama
// has no usage API for keys (docs/VENDORS.md §6): the key is validated and
// the session, weekly and monthly limits are shown as unknown ("—") rather
// than guessed.
package ollama

import (
	"context"
	"strings"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

var URL = "https://ollama.com/api/web_search"

// P is the Ollama Cloud provider.
type P struct{}

func (P) ID() string   { return "ollama" }
func (P) Name() string { return "Ollama Cloud (experimental)" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyOther, Auth: []provider.AuthMethod{provider.AuthKey}, Windows: true, Experimental: true,
		Endpoints: []string{URL + " (key check with an empty query; no search is run)"},
		KeyHelp:   "an API key from ollama.com/settings/keys",
	}
}
func (P) Detect(*provider.Env) []provider.Detection { return nil }

func (P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	key, err := env.Key(acct)
	if err != nil {
		return nil, err
	}
	err = env.JSON(ctx, provider.Request{Method: "POST", URL: URL, Body: strings.NewReader(`{"query":""}`), Headers: map[string]string{
		"Authorization": "Bearer " + key, "Content-Type": "application/json",
	}}, nil)
	if err != nil {
		// An empty query is rejected as a bad request once the key is
		// accepted; only 401/403 mean the key is wrong.
		if st, _ := provider.StatusOf(err); st != model.StatusError || !strings.Contains(err.Error(), "HTTP 4") {
			return nil, err
		}
	}
	return &model.Reading{
		Plan: "Ollama Cloud", Source: "ollama key check", FetchedAt: env.Now(),
		Windows: []model.Window{
			{Kind: model.FiveHour, Name: "Session"},
			{Kind: model.Weekly, Name: "Weekly"},
		},
		Notes: []string{"Key works. Ollama exposes no usage API for keys, so limits show as unknown."},
	}, nil
}
