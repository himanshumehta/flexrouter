package all_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/anthropicapi"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/deepseek"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/moonshot"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/ollama"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/openaiapi"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/openrouter"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/xai"
	"github.com/himanshumehta/flexrouter/aitank/internal/secrets"
)

// route is one canned response.
type route struct {
	status int
	body   string
}

type apiCase struct {
	name       string
	provider   string
	setBase    func(string) func() // points the provider at the fake server, returns restore
	options    map[string]string
	noKey      bool
	routes     map[string]route // path → response; missing paths give 404
	authHeader string           // header name that must carry the key
	authValue  string
	wantPaths  []string // paths that must be requested, in order
	wantStatus model.Status
	check      func(t *testing.T, r *model.Reading)
}

func setVar(p *string) func(string) func() {
	return func(u string) func() {
		old := *p
		*p = u
		return func() { *p = old }
	}
}

func amount(t *testing.T, m *model.Money, want float64) {
	t.Helper()
	if m == nil || m.Amount == nil {
		t.Fatalf("money = %+v, want %v", m, want)
	}
	if d := *m.Amount - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("amount = %v, want %v", *m.Amount, want)
	}
}

func TestAPIKeyProviders(t *testing.T) {
	const key = "sk-test-123"
	tests := []apiCase{
		// OpenRouter: a management key reads /credits.
		{
			name: "openrouter management key", provider: "openrouter", setBase: setVar(&openrouter.Base),
			routes:     map[string]route{"/credits": {200, `{"data":{"total_credits":5.0,"total_usage":3.1}}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			wantPaths: []string{"/credits"},
			check: func(t *testing.T, r *model.Reading) {
				amount(t, &r.Balances[0], 1.9)
				if r.Plan != "OpenRouter credits" || len(r.Windows) != 1 || *r.Windows[0].Limit != 5 {
					t.Errorf("reading = %+v", r)
				}
			},
		},
		// A regular key gets 403 on /credits, then reads /key.
		{
			name: "openrouter regular key", provider: "openrouter", setBase: setVar(&openrouter.Base),
			routes: map[string]route{
				"/credits": {403, `{"error":"management key required"}`},
				"/key":     {200, `{"data":{"label":"sk-or-v1-abc","limit":30,"limit_remaining":28.5,"limit_reset":"monthly","usage":1.5,"is_free_tier":false}}`},
			},
			authHeader: "Authorization", authValue: "Bearer " + key,
			wantPaths: []string{"/credits", "/key"},
			check: func(t *testing.T, r *model.Reading) {
				amount(t, r.Spend, 1.5)
				amount(t, &r.Balances[0], 28.5)
				w := r.Windows[0]
				if w.Name != "Key limit (monthly)" || w.Kind != model.Monthly || *w.Limit != 30 || w.ResetsAt != nil {
					t.Errorf("window = %+v", w)
				}
			},
		},
		{
			name: "openrouter key without limit", provider: "openrouter", setBase: setVar(&openrouter.Base),
			routes: map[string]route{
				"/credits": {403, `{}`},
				"/key":     {200, `{"data":{"limit":null,"usage":0.25,"is_free_tier":true}}`},
			},
			check: func(t *testing.T, r *model.Reading) {
				if r.Plan != "OpenRouter free tier" || len(r.Windows) != 0 || len(r.Notes) != 1 {
					t.Errorf("reading = %+v", r)
				}
				amount(t, r.Spend, 0.25)
			},
		},
		{
			name: "openrouter key rejected", provider: "openrouter", setBase: setVar(&openrouter.Base),
			routes:     map[string]route{"/credits": {401, `{}`}, "/key": {401, `{}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "openrouter missing key", provider: "openrouter", setBase: setVar(&openrouter.Base), noKey: true,
			wantStatus: model.StatusAuthNeeded,
		},
		// DeepSeek: string amounts.
		{
			name: "deepseek", provider: "deepseek", setBase: setVar(&deepseek.Base),
			routes:     map[string]route{"/user/balance": {200, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"50.00","granted_balance":"10.00","topped_up_balance":"40.00"},{"currency":"CNY","total_balance":"7.5"}]}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			check: func(t *testing.T, r *model.Reading) {
				if len(r.Balances) != 2 || r.Balances[0].Currency != "USD" || r.Balances[1].Currency != "CNY" {
					t.Fatalf("balances = %+v", r.Balances)
				}
				amount(t, &r.Balances[0], 50)
				amount(t, &r.Balances[1], 7.5)
				if len(r.Notes) != 0 {
					t.Errorf("notes = %v", r.Notes)
				}
			},
		},
		{
			name: "deepseek unavailable", provider: "deepseek", setBase: setVar(&deepseek.Base),
			routes: map[string]route{"/user/balance": {200, `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"0.00"}]}`}},
			check: func(t *testing.T, r *model.Reading) {
				amount(t, &r.Balances[0], 0)
				if len(r.Notes) != 1 {
					t.Errorf("notes = %v", r.Notes)
				}
			},
		},
		{
			name: "deepseek 401", provider: "deepseek", setBase: setVar(&deepseek.Base),
			routes:     map[string]route{"/user/balance": {401, `{}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		// Moonshot: international (USD) and China (CNY).
		{
			name: "moonshot", provider: "moonshot", setBase: setVar(&moonshot.Base),
			routes:     map[string]route{"/users/me/balance": {200, `{"code":0,"data":{"available_balance":49.58,"voucher_balance":46.58,"cash_balance":3.00},"scode":"0x0","status":true}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			check: func(t *testing.T, r *model.Reading) {
				if len(r.Balances) != 2 || r.Balances[0].Currency != "USD" {
					t.Fatalf("balances = %+v", r.Balances)
				}
				amount(t, &r.Balances[0], 49.58)
				amount(t, &r.Balances[1], 46.58)
			},
		},
		{
			name: "moonshot cn", provider: "moonshot", setBase: setVar(&moonshot.BaseCN), options: map[string]string{"region": "cn"},
			routes: map[string]route{"/users/me/balance": {200, `{"data":{"available_balance":10,"voucher_balance":0}}`}},
			check: func(t *testing.T, r *model.Reading) {
				if len(r.Balances) != 1 || r.Balances[0].Currency != "CNY" {
					t.Errorf("balances = %+v", r.Balances)
				}
			},
		},
		// xAI: key check only.
		{
			name: "xai ok", provider: "xai", setBase: setVar(&xai.Base),
			routes:     map[string]route{"/api-key": {200, `{"name":"k","api_key_blocked":false,"api_key_disabled":false,"team_blocked":false}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			check: func(t *testing.T, r *model.Reading) {
				if len(r.Balances) != 1 || r.Balances[0].Amount != nil {
					t.Errorf("xAI balance must be unknown, got %+v", r.Balances)
				}
			},
		},
		{
			name: "xai blocked", provider: "xai", setBase: setVar(&xai.Base),
			routes:     map[string]route{"/api-key": {200, `{"api_key_blocked":true}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "xai team blocked", provider: "xai", setBase: setVar(&xai.Base),
			routes:     map[string]route{"/api-key": {200, `{"team_blocked":true}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "xai 401", provider: "xai", setBase: setVar(&xai.Base),
			routes:     map[string]route{"/api-key": {401, `{}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "xai missing key", provider: "xai", setBase: setVar(&xai.Base), noKey: true,
			wantStatus: model.StatusAuthNeeded,
		},
		// Anthropic Admin API: cents as decimal strings, paginated.
		{
			name: "anthropic api", provider: "anthropic-api", setBase: setVar(&anthropicapi.Base),
			routes:     map[string]route{"/organizations/cost_report": {200, `{"data":[{"results":[{"currency":"USD","amount":"123.45"},{"currency":"USD","amount":"76.55"}]}],"has_more":false,"next_page":null}`}},
			authHeader: "x-api-key", authValue: key,
			check: func(t *testing.T, r *model.Reading) {
				amount(t, r.Spend, 2.00) // (123.45 + 76.55) cents
				if r.Spend.Currency != "USD" || r.Spend.Label != "Spend Sep" {
					t.Errorf("spend = %+v", r.Spend)
				}
			},
		},
		{
			name: "anthropic api 403", provider: "anthropic-api", setBase: setVar(&anthropicapi.Base),
			routes:     map[string]route{"/organizations/cost_report": {403, `{}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "anthropic api 429", provider: "anthropic-api", setBase: setVar(&anthropicapi.Base),
			routes:     map[string]route{"/organizations/cost_report": {429, `{}`}},
			wantStatus: model.StatusRateLimited,
		},
		// OpenAI Admin API: dollars as floats.
		{
			name: "openai api", provider: "openai-api", setBase: setVar(&openaiapi.Base),
			routes:     map[string]route{"/organization/costs": {200, `{"object":"page","data":[{"object":"bucket","results":[{"amount":{"value":0.06,"currency":"usd"}},{"amount":{"value":1.94,"currency":"usd"}}]}],"has_more":false,"next_page":null}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			check: func(t *testing.T, r *model.Reading) {
				amount(t, r.Spend, 2.00)
				if r.Spend.Currency != "USD" {
					t.Errorf("currency = %q", r.Spend.Currency)
				}
			},
		},
		{
			name: "openai api missing key", provider: "openai-api", setBase: setVar(&openaiapi.Base), noKey: true,
			wantStatus: model.StatusAuthNeeded,
		},
		// Ollama: key check; 400 on an empty query means the key was accepted.
		{
			name: "ollama accepted", provider: "ollama", setBase: setVar(&ollama.URL),
			routes:     map[string]route{"": {400, `{"error":"query is required"}`}},
			authHeader: "Authorization", authValue: "Bearer " + key,
			check: func(t *testing.T, r *model.Reading) {
				if len(r.Windows) != 2 || r.Windows[0].Pct() != nil {
					t.Errorf("ollama windows must be unknown: %+v", r.Windows)
				}
			},
		},
		{
			name: "ollama rejected", provider: "ollama", setBase: setVar(&ollama.URL),
			routes:     map[string]route{"": {401, `{}`}},
			wantStatus: model.StatusAuthNeeded,
		},
		{
			name: "ollama server error", provider: "ollama", setBase: setVar(&ollama.URL),
			routes:     map[string]route{"": {500, `{}`}},
			wantStatus: model.StatusError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := provider.Get(tc.provider)
			if !ok {
				t.Fatalf("provider %s not registered", tc.provider)
			}
			var mu sync.Mutex
			var paths []string
			var gotAuth []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				if tc.authHeader != "" {
					gotAuth = append(gotAuth, r.Header.Get(tc.authHeader))
				}
				mu.Unlock()
				rt, ok := tc.routes[r.URL.Path]
				if !ok {
					rt, ok = tc.routes[""]
				}
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(rt.status)
				w.Write([]byte(rt.body))
			}))
			defer srv.Close()
			defer tc.setBase(srv.URL)()

			store := secrets.NewMemory()
			if !tc.noKey {
				store.Set("acct-1", "label", key)
			}
			now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			env := &provider.Env{Home: t.TempDir(), HTTP: srv.Client(), Secrets: store, Now: func() time.Time { return now }, Getenv: func(string) string { return "" }}
			acct := &config.Account{ID: "acct-1", Provider: tc.provider, Auth: "key", Options: tc.options}
			r, err := p.Read(context.Background(), env, acct)
			if tc.wantStatus != "" {
				if err == nil {
					t.Fatalf("want %s, got %+v", tc.wantStatus, r)
				}
				if st, _ := provider.StatusOf(err); st != tc.wantStatus {
					t.Fatalf("status = %s, want %s (%v)", st, tc.wantStatus, err)
				}
				if tc.noKey && len(paths) != 0 {
					t.Errorf("no request should be made without a key, got %v", paths)
				}
				return
			}
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, a := range gotAuth {
				if a != tc.authValue {
					t.Errorf("%s = %q, want %q", tc.authHeader, a, tc.authValue)
				}
			}
			if tc.authHeader != "" && len(gotAuth) == 0 {
				t.Error("no request made")
			}
			if tc.wantPaths != nil && strings.Join(paths, ",") != strings.Join(tc.wantPaths, ",") {
				t.Errorf("paths = %v, want %v", paths, tc.wantPaths)
			}
			if !r.FetchedAt.Equal(now) {
				t.Errorf("FetchedAt = %v", r.FetchedAt)
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

// TestCostReportPagination checks the Anthropic and OpenAI readers follow
// next_page and query the current month.
func TestCostReportPagination(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		setBase  func(string) func()
		pages    []string
		want     float64
		startKey string
		startVal string
	}{
		{
			name: "anthropic", provider: "anthropic-api", setBase: setVar(&anthropicapi.Base),
			pages: []string{
				`{"data":[{"results":[{"currency":"USD","amount":"100"}]}],"has_more":true,"next_page":"p2"}`,
				`{"data":[{"results":[{"currency":"USD","amount":"250"}]}],"has_more":false}`,
			},
			want: 3.5, startKey: "starting_at", startVal: "2026-09-01T00:00:00Z",
		},
		{
			name: "openai", provider: "openai-api", setBase: setVar(&openaiapi.Base),
			pages: []string{
				`{"data":[{"results":[{"amount":{"value":1.25,"currency":"usd"}}]}],"has_more":true,"next_page":"p2"}`,
				`{"data":[{"results":[{"amount":{"value":2.25,"currency":"usd"}}]}],"has_more":false}`,
			},
			want: 3.5, startKey: "start_time", startVal: "1788220800",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var pagesSeen []string
			var starts []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				pg := r.URL.Query().Get("page")
				pagesSeen = append(pagesSeen, pg)
				starts = append(starts, r.URL.Query().Get(tc.startKey))
				if pg == "" {
					w.Write([]byte(tc.pages[0]))
				} else {
					w.Write([]byte(tc.pages[1]))
				}
			}))
			defer srv.Close()
			defer tc.setBase(srv.URL)()
			p, _ := provider.Get(tc.provider)
			store := secrets.NewMemory()
			store.Set("a", "", "k")
			now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			env := &provider.Env{HTTP: srv.Client(), Secrets: store, Now: func() time.Time { return now }}
			r, err := p.Read(context.Background(), env, &config.Account{ID: "a", Provider: tc.provider})
			if err != nil {
				t.Fatal(err)
			}
			amount(t, r.Spend, tc.want)
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(pagesSeen, ",") != ",p2" {
				t.Errorf("pages = %v", pagesSeen)
			}
			if starts[0] != tc.startVal {
				t.Errorf("%s = %q, want %q", tc.startKey, starts[0], tc.startVal)
			}
		})
	}
}
