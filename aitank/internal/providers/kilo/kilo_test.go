package kilo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/secrets"
)

const batchSample = `[
  {"result":{"data":{"json":{"creditBlocks":[
      {"id":"a","amount_mUsd":20000000,"balance_mUsd":5000000},
      {"id":"b","amount_mUsd":"10000000","balance_mUsd":"2500000"}
    ],"totalBalance_mUsd":7500000}}}},
  {"result":{"data":{"json":{"subscription":{"tier":"Pro","currentPeriodUsageUsd":12.5,
      "currentPeriodBaseCreditsUsd":19,"currentPeriodBonusCreditsUsd":6,"nextBillingAt":"2026-10-15T00:00:00Z"}}}}},
  {"result":{"data":{"json":null}}}
]`

func decode(t *testing.T, s string) []map[string]any {
	t.Helper()
	var b []map[string]any
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNormalize(t *testing.T) {
	r := Normalize(decode(t, batchSample))
	if r.Plan != "Kilo Pass Pro" {
		t.Errorf("plan = %q", r.Plan)
	}
	if len(r.Balances) != 1 || *r.Balances[0].Amount != 7.5 {
		t.Errorf("balances = %+v, want $7.50", r.Balances)
	}
	if len(r.Windows) != 2 {
		t.Fatalf("windows = %+v", r.Windows)
	}
	cr := r.Windows[0]
	if cr.Kind != model.Prepaid || *cr.Limit != 30 || *cr.Used != 22.5 {
		t.Errorf("credits window = %+v used %v limit %v", cr, *cr.Used, *cr.Limit)
	}
	pass := r.Windows[1]
	if pass.Kind != model.BillingCycle || *pass.Limit != 25 || *pass.Used != 12.5 {
		t.Errorf("pass window = %+v", pass)
	}
	if pass.ResetsAt == nil || !pass.ResetsAt.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("pass reset = %v", pass.ResetsAt)
	}
}

func TestNormalizeVariants(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		plan    string
		balance *float64
		windows int
	}{
		{"total only", `[{"result":{"data":{"json":{"totalBalance_mUsd":1234567}}}}]`, "Kilo Code", model.F(1.234567), 0},
		{"no json wrapper", `[{"result":{"data":{"creditBlocks":[{"amount_mUsd":1000000,"balance_mUsd":1000000}]}}}]`, "Kilo Code", model.F(1), 1},
		{"pass without subscription wrapper", `[{},{"result":{"data":{"json":{"currentPeriodBaseCreditsUsd":19,"nextRenewalAt":"2026-10-01"}}}}]`, "Kilo Pass", nil, 1},
		{"empty", `[]`, "Kilo Code", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Normalize(decode(t, tc.body))
			if r.Plan != tc.plan || len(r.Windows) != tc.windows {
				t.Errorf("plan=%q windows=%+v", r.Plan, r.Windows)
			}
			if tc.balance == nil {
				if len(r.Balances) != 0 {
					t.Errorf("balances = %+v", r.Balances)
				}
			} else if len(r.Balances) != 1 || *r.Balances[0].Amount != *tc.balance {
				t.Errorf("balances = %+v, want %v", r.Balances, *tc.balance)
			}
		})
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name       string
		auth       string
		key        string
		localTok   string
		wantAuth   string
		wantStatus model.Status
	}{
		{name: "local sign-in", auth: "local", localTok: "kilo-local", wantAuth: "Bearer kilo-local"},
		{name: "pasted key", auth: "key", key: "kilo-key", wantAuth: "Bearer kilo-key"},
		{name: "local missing", auth: "local", wantStatus: model.StatusAuthNeeded},
		{name: "key missing", auth: "key", wantStatus: model.StatusAuthNeeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.localTok != "" {
				p := authFile(home)
				os.MkdirAll(filepath.Dir(p), 0o700)
				os.WriteFile(p, []byte(`{"kilo":{"type":"oauth","access":"`+tc.localTok+`"}}`), 0o600)
			}
			var gotAuth, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/trpc/") {
					gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.Query().Get("input")
					w.Write([]byte(batchSample))
					return
				}
				w.Write([]byte(`{"user":{"email":"k@kilo.test"}}`))
			}))
			defer srv.Close()
			oldT, oldP := TRPC, Profile
			TRPC, Profile = srv.URL+"/trpc", srv.URL+"/profile"
			defer func() { TRPC, Profile = oldT, oldP }()
			store := secrets.NewMemory()
			if tc.key != "" {
				store.Set("kilo-1", "", tc.key)
			}
			env := &provider.Env{Home: home, HTTP: srv.Client(), Secrets: store, Now: time.Now}
			r, err := P{}.Read(context.Background(), env, &config.Account{ID: "kilo-1", Provider: "kilo", Auth: tc.auth})
			if tc.wantStatus != "" {
				if st, _ := provider.StatusOf(err); err == nil || st != tc.wantStatus {
					t.Fatalf("status %v (%v), want %s", st, err, tc.wantStatus)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotAuth != tc.wantAuth || !strings.Contains(gotQuery, `"0":{"json":null}`) {
				t.Errorf("auth=%q input=%q", gotAuth, gotQuery)
			}
			if r.Identity != "k@kilo.test" {
				t.Errorf("identity = %q", r.Identity)
			}
		})
	}
}
