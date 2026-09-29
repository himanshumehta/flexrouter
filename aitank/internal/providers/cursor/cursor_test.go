package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

const sampleSummary = `{
  "billingCycleStart": "2026-09-01T00:00:00.000Z",
  "billingCycleEnd": "2026-10-01T00:00:00.000Z",
  "membershipType": "pro",
  "limitType": "user",
  "isUnlimited": false,
  "individualUsage": {
    "plan": {"enabled": true, "used": 1500, "limit": 2000, "remaining": 500,
             "breakdown": {"included": 2000, "bonus": 0, "total": 2000},
             "autoPercentUsed": 40, "apiPercentUsed": 90, "totalPercentUsed": 75},
    "onDemand": {"enabled": true, "used": 1234, "limit": 5000, "remaining": 3766},
    "overall": {"enabled": true, "used": 2734, "limit": 7000, "remaining": 4266}
  },
  "teamUsage": {}
}`

func TestNormalize(t *testing.T) {
	var s Summary
	if err := json.Unmarshal([]byte(sampleSummary), &s); err != nil {
		t.Fatal(err)
	}
	r := Normalize(&s)
	if r.Plan != "Cursor Pro" {
		t.Errorf("plan = %q", r.Plan)
	}
	end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		pct        float64
		used, limt *float64
	}{
		{"Included usage", 75, model.F(15), model.F(20)},
		{"Auto pool", 40, nil, nil},
		{"API pool", 90, nil, nil},
	}
	if len(r.Windows) != len(tests) {
		t.Fatalf("windows = %+v", r.Windows)
	}
	for i, tc := range tests {
		w := r.Windows[i]
		if w.Name != tc.name || w.Kind != model.BillingCycle {
			t.Errorf("[%d] name/kind = %s/%s", i, w.Name, w.Kind)
		}
		if p := w.Pct(); p == nil || *p != tc.pct {
			t.Errorf("%s pct = %v want %v", tc.name, p, tc.pct)
		}
		if w.ResetsAt == nil || !w.ResetsAt.Equal(end) {
			t.Errorf("%s reset = %v", tc.name, w.ResetsAt)
		}
		if tc.used != nil && (w.Used == nil || *w.Used != *tc.used || *w.Limit != *tc.limt) {
			t.Errorf("%s used/limit = %v/%v (cents must become dollars)", tc.name, w.Used, w.Limit)
		}
	}
	if r.Spend == nil || *r.Spend.Amount != 12.34 {
		t.Errorf("spend = %+v, want $12.34", r.Spend)
	}
	if len(r.Balances) != 1 || *r.Balances[0].Amount != 37.66 {
		t.Errorf("balances = %+v", r.Balances)
	}
}

func TestNormalizeVariants(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		windows  int
		spend    bool
		balances int
	}{
		{"plan disabled", `{"individualUsage":{"plan":{"enabled":false,"totalPercentUsed":10}}}`, 0, false, 0},
		{"used/limit only", `{"individualUsage":{"plan":{"used":50,"limit":200}}}`, 1, false, 0},
		{"on-demand disabled", `{"individualUsage":{"onDemand":{"enabled":false,"used":100,"limit":1000}}}`, 0, false, 0},
		{"on-demand no limit", `{"individualUsage":{"onDemand":{"enabled":true,"used":100,"limit":0}}}`, 0, true, 0},
		{"empty", `{}`, 0, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s Summary
			if err := json.Unmarshal([]byte(tc.body), &s); err != nil {
				t.Fatal(err)
			}
			r := Normalize(&s)
			if len(r.Windows) != tc.windows || (r.Spend != nil) != tc.spend || len(r.Balances) != tc.balances {
				t.Errorf("got windows=%d spend=%v balances=%d", len(r.Windows), r.Spend, len(r.Balances))
			}
		})
	}
}

func jwt(claims map[string]any) string {
	enc := base64.RawURLEncoding
	h := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	b, _ := json.Marshal(claims)
	return h + "." + enc.EncodeToString(b) + "." + enc.EncodeToString([]byte("sig"))
}

func TestRead(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	good := jwt(map[string]any{"sub": "auth0|user_ABC", "exp": now.Add(time.Hour).Unix()})
	expired := jwt(map[string]any{"sub": "auth0|user_ABC", "exp": now.Add(-time.Hour).Unix()})
	noSub := jwt(map[string]any{"exp": now.Add(time.Hour).Unix()})

	tests := []struct {
		name       string
		noDB       bool
		dbOut      string
		status     int
		identity   string
		wantStatus model.Status
		wantCookie string
		wantEmail  string
	}{
		{name: "ok", dbOut: good + "\n", wantCookie: "WorkosCursorSessionToken=user_ABC%3A%3A" + good, wantEmail: "me@cursor.test"},
		{name: "known identity skips /me", dbOut: good + "\n", identity: "known@x", wantCookie: "WorkosCursorSessionToken=user_ABC%3A%3A" + good},
		{name: "no db", noDB: true, wantStatus: model.StatusToolMissing},
		{name: "signed out", dbOut: "\n", wantStatus: model.StatusAuthNeeded},
		{name: "expired", dbOut: expired, wantStatus: model.StatusAuthNeeded},
		{name: "no sub", dbOut: noSub, wantStatus: model.StatusUnsupported},
		{name: "401", dbOut: good, status: 401, wantStatus: model.StatusAuthNeeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("AITANK_HOME", t.TempDir())
			t.Setenv("AITANK_TEST_HOME", home)
			if !tc.noDB {
				db := DBPath(home)
				os.MkdirAll(filepath.Dir(db), 0o700)
				os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600)
			}
			var mu sync.Mutex
			cookies := map[string]string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				cookies[r.URL.Path] = r.Header.Get("Cookie")
				mu.Unlock()
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				switch r.URL.Path {
				case "/api/usage-summary":
					w.Write([]byte(sampleSummary))
				case "/api/auth/me":
					w.Write([]byte(`{"email":"me@cursor.test","sub":"auth0|user_ABC"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			old := Base
			Base = srv.URL
			defer func() { Base = old }()

			var ranArgs []string
			env := &provider.Env{
				Home:   home,
				HTTP:   srv.Client(),
				Now:    func() time.Time { return now },
				Getenv: func(string) string { return "" },
				Run: func(ctx context.Context, e []string, name string, args ...string) ([]byte, error) {
					ranArgs = append([]string{name}, args...)
					return []byte(tc.dbOut), nil
				},
			}
			r, err := P{}.Read(context.Background(), env, &config.Account{ID: "cursor-1", Provider: "cursor", Identity: tc.identity})
			if tc.wantStatus != "" {
				if st, _ := provider.StatusOf(err); err == nil || st != tc.wantStatus {
					t.Fatalf("status = %v (%v), want %s", st, err, tc.wantStatus)
				}
				return
			}
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(ranArgs) == 0 || !strings.Contains(strings.Join(ranArgs, " "), "-readonly") || !strings.Contains(strings.Join(ranArgs, " "), "cursorAuth/accessToken") {
				t.Errorf("sqlite call = %v", ranArgs)
			}
			mu.Lock()
			defer mu.Unlock()
			if got := cookies["/api/usage-summary"]; got != tc.wantCookie {
				t.Errorf("Cookie = %q\nwant     %q", got, tc.wantCookie)
			}
			if _, called := cookies["/api/auth/me"]; called != (tc.identity == "") {
				t.Errorf("/api/auth/me called = %v", called)
			}
			if r.Identity != tc.wantEmail {
				t.Errorf("identity = %q", r.Identity)
			}
			if len(r.Windows) != 3 || !r.FetchedAt.Equal(now) {
				t.Errorf("reading = %+v", r)
			}
		})
	}
}

func TestPlanName(t *testing.T) {
	tests := map[string]string{"": "", "free": "Cursor Hobby", "pro": "Cursor Pro", "pro_plus": "Cursor Pro+", "ultra": "Cursor Ultra", "business": "Cursor Business", "weird": "Cursor weird"}
	for in, want := range tests {
		if got := planName(in); got != want {
			t.Errorf("planName(%q) = %q, want %q", in, got, want)
		}
	}
}
