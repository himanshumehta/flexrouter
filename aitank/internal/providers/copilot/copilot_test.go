package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

const paidSample = `{ "copilot_plan": "individual", "assigned_date": "2025-01-01", "quota_reset_date": "2025-02-01",
  "token_based_billing": false,
  "quota_snapshots": {
    "premium_interactions": { "entitlement": 300, "remaining": 250, "percent_remaining": 83.3,
                              "quota_id": "premium_interactions", "unlimited": false,
                              "overage_count": 0, "overage_permitted": false, "credits_used": 0 },
    "chat":        { "entitlement": 0, "remaining": 0, "percent_remaining": 100, "unlimited": true, "quota_id":"chat" },
    "completions": { "entitlement": 0, "remaining": 0, "percent_remaining": 100, "unlimited": false, "quota_id":"completions" } } }`

func decode(t *testing.T, s string) *User {
	t.Helper()
	var u User
	if err := json.Unmarshal([]byte(s), &u); err != nil {
		t.Fatal(err)
	}
	return &u
}

func TestNormalizePaid(t *testing.T) {
	r := Normalize(decode(t, paidSample))
	if r.Plan != "Copilot Pro" {
		t.Errorf("plan = %q", r.Plan)
	}
	// chat is unlimited and completions is the 0/0/100 placeholder: both ignored.
	if len(r.Windows) != 1 {
		t.Fatalf("windows = %+v", r.Windows)
	}
	w := r.Windows[0]
	if w.Name != "Premium requests" || w.Kind != model.Monthly {
		t.Errorf("window = %+v", w)
	}
	if *w.Limit != 300 || *w.Used != 50 {
		t.Errorf("used/limit = %v/%v", *w.Used, *w.Limit)
	}
	if p := w.Pct(); p == nil || *p < 16.6 || *p > 16.8 {
		t.Errorf("pct = %v", p)
	}
	if w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("reset = %v", w.ResetsAt)
	}
}

func TestNormalizeVariants(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		plan    string
		windows map[string][2]float64 // name → used, limit
		notes   int
	}{
		{
			name:    "free tier numbers",
			body:    `{"copilot_plan":"free","monthly_quotas":{"chat":500,"completions":4000},"limited_user_quotas":{"chat":125,"completions":75},"limited_user_reset_date":"2025-03-01"}`,
			plan:    "Copilot Free",
			windows: map[string][2]float64{"Chat": {375, 500}, "Completions": {3925, 4000}},
		},
		{
			name:    "free tier strings",
			body:    `{"monthly_quotas":{"chat":"500","completions":"4000"},"limited_user_quotas":{"chat":"125","completions":"75"}}`,
			plan:    "Copilot Free",
			windows: map[string][2]float64{"Chat": {375, 500}, "Completions": {3925, 4000}},
		},
		{
			name:    "all placeholders",
			body:    `{"copilot_plan":"business","quota_snapshots":{"premium_interactions":{"entitlement":0,"remaining":0,"percent_remaining":100}}}`,
			plan:    "Copilot Business",
			windows: map[string][2]float64{},
		},
		{
			name:    "unlimited premium",
			body:    `{"copilot_plan":"enterprise","quota_snapshots":{"premium_interactions":{"entitlement":1000,"remaining":1000,"unlimited":true}}}`,
			plan:    "Copilot Enterprise",
			windows: map[string][2]float64{},
		},
		{
			name:    "overage",
			body:    `{"copilot_plan":"individual_pro","quota_snapshots":{"premium_interactions":{"entitlement":1500,"remaining":0,"percent_remaining":0,"overage_count":12,"overage_permitted":true}}}`,
			plan:    "Copilot Pro+",
			windows: map[string][2]float64{"Premium requests": {1500, 1500}},
			notes:   2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Normalize(decode(t, tc.body))
			if r.Plan != tc.plan {
				t.Errorf("plan = %q, want %q", r.Plan, tc.plan)
			}
			if len(r.Windows) != len(tc.windows) {
				t.Fatalf("windows = %+v", r.Windows)
			}
			for _, w := range r.Windows {
				want, ok := tc.windows[w.Name]
				if !ok {
					t.Errorf("unexpected window %s", w.Name)
					continue
				}
				if w.Used == nil || *w.Used != want[0] || *w.Limit != want[1] {
					t.Errorf("%s used/limit = %v/%v, want %v", w.Name, w.Used, w.Limit, want)
				}
			}
			if len(r.Notes) != tc.notes {
				t.Errorf("notes = %v", r.Notes)
			}
		})
	}
}

func TestRead(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(paidSample))
	}))
	defer srv.Close()
	old := URL
	URL = srv.URL
	defer func() { URL = old }()
	env := &provider.Env{
		Home: t.TempDir(), HTTP: srv.Client(), Now: time.Now, Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) { return "/fake/gh", nil },
		Run: func(ctx context.Context, e []string, name string, args ...string) ([]byte, error) {
			return []byte("gho_abc\n"), nil
		},
	}
	r, err := P{}.Read(context.Background(), env, &config.Account{ID: "copilot-1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "token gho_abc" || len(r.Windows) != 1 {
		t.Errorf("auth = %q, reading = %+v", gotAuth, r)
	}
}
