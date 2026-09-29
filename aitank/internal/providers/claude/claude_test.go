package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// sampleUsage is the §1b response shape from docs/VENDORS.md.
const sampleUsage = `{
  "five_hour":  { "utilization": 11.0, "resets_at": "2026-07-03T00:30:00.282668+00:00" },
  "seven_day":  { "utilization": 9.0,  "resets_at": "2026-07-08T09:00:00.282694+00:00" },
  "seven_day_opus": null,
  "seven_day_sonnet": null,
  "seven_day_oauth_apps": null,
  "seven_day_routines": { "utilization": 18, "resets_at": "2026-07-08T09:00:00Z" },
  "extra_usage": { "is_enabled": true, "monthly_limit": 2050, "used_credits": 325, "utilization": 15.8, "currency": "USD" },
  "limits": [
    { "kind": "session",       "group": "session", "percent": 11, "resets_at": "2026-07-03T00:30:00Z", "scope": null, "is_active": true },
    { "kind": "weekly_all",    "group": "weekly",  "percent": 9,  "resets_at": "2026-07-08T09:00:00Z", "scope": null, "is_active": false },
    { "kind": "weekly_scoped", "group": "weekly",  "percent": 5,  "resets_at": "2026-07-08T09:00:00Z",
      "scope": { "model": { "id": null, "display_name": "Fable" }, "surface": null }, "is_active": false }
  ]
}`

func decode(t *testing.T, s string) *Usage {
	t.Helper()
	var u Usage
	if err := json.Unmarshal([]byte(s), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &u
}

func findWindow(ws []model.Window, key string) *model.Window {
	for i := range ws {
		if ws[i].Key() == key {
			return &ws[i]
		}
	}
	return nil
}

func TestNormalizeSample(t *testing.T) {
	ws, bals, spend := Normalize(decode(t, sampleUsage))
	if len(ws) != 3 {
		t.Fatalf("want 3 windows (5h, weekly, Fable), got %d: %+v", len(ws), ws)
	}
	tests := []struct {
		key   string
		kind  model.WindowKind
		pct   float64
		model string
		reset string
	}{
		{"five_hour:5-hour", model.FiveHour, 11, "", "2026-07-03T00:30:00Z"},
		{"weekly:weekly", model.Weekly, 9, "", "2026-07-08T09:00:00Z"},
		{"model_weekly:fable", model.ModelWeekly, 5, "Fable", "2026-07-08T09:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			w := findWindow(ws, tc.key)
			if w == nil {
				t.Fatalf("window %s missing", tc.key)
			}
			if w.Kind != tc.kind || w.Model != tc.model {
				t.Errorf("kind/model = %s/%q", w.Kind, w.Model)
			}
			if w.UsedPct == nil || *w.UsedPct != tc.pct {
				t.Errorf("pct = %v, want %v", w.UsedPct, tc.pct)
			}
			want, _ := time.Parse(time.RFC3339, tc.reset)
			if w.ResetsAt == nil || !w.ResetsAt.Truncate(time.Second).Equal(want) {
				t.Errorf("reset = %v, want %v", w.ResetsAt, want)
			}
		})
	}
	if spend == nil || spend.Amount == nil || *spend.Amount != 3.25 || spend.Currency != "USD" {
		t.Errorf("spend = %+v, want $3.25", spend)
	}
	if len(bals) != 1 || bals[0].Amount == nil || *bals[0].Amount != 17.25 {
		t.Errorf("balances = %+v, want $17.25 left", bals)
	}
}

func TestNormalizeVariants(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantKeys  []string
		wantSpend bool
	}{
		{"null windows", `{"five_hour":null,"seven_day":null,"seven_day_opus":null}`, nil, false},
		{"null utilization", `{"five_hour":{"utilization":null,"resets_at":null}}`, nil, false},
		{"only seven_day", `{"five_hour":null,"seven_day":{"utilization":40,"resets_at":"2026-07-08T09:00:00Z"}}`, []string{"weekly:weekly"}, false},
		{"legacy opus field", `{"seven_day_opus":{"utilization":70,"resets_at":"2026-07-08T09:00:00Z"}}`, []string{"model_weekly:opus"}, false},
		{"limits fill missing windows", `{"limits":[{"kind":"session","percent":3},{"kind":"weekly_all","percent":4}]}`, []string{"five_hour:5-hour", "weekly:weekly"}, false},
		{"limits null percent skipped", `{"limits":[{"kind":"session","percent":null}]}`, nil, false},
		{"scoped without model skipped", `{"limits":[{"kind":"weekly_scoped","percent":5,"scope":null}]}`, nil, false},
		{"duplicate legacy and scoped", `{"seven_day_opus":{"utilization":70},"limits":[{"kind":"weekly_scoped","percent":71,"scope":{"model":{"display_name":"Opus"}}}]}`, []string{"model_weekly:opus"}, false},
		{"extra usage disabled", `{"extra_usage":{"is_enabled":false,"monthly_limit":100,"used_credits":10}}`, nil, false},
		{"extra usage no limit", `{"extra_usage":{"is_enabled":true,"used_credits":10}}`, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws, _, spend := Normalize(decode(t, tc.body))
			if len(ws) != len(tc.wantKeys) {
				t.Fatalf("got %d windows %+v, want %v", len(ws), ws, tc.wantKeys)
			}
			for _, k := range tc.wantKeys {
				if findWindow(ws, k) == nil {
					t.Errorf("missing %s", k)
				}
			}
			if (spend != nil) != tc.wantSpend {
				t.Errorf("spend = %+v, want present=%v", spend, tc.wantSpend)
			}
		})
	}
	// duplicate case keeps the legacy value.
	ws, _, _ := Normalize(decode(t, tests[7].body))
	if *ws[0].UsedPct != 70 {
		t.Errorf("legacy opus value should win, got %v", *ws[0].UsedPct)
	}
}

func TestKeychainService(t *testing.T) {
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
	}
	tests := []struct {
		dir, want string
	}{
		{"", "Claude Code-credentials"},
		{"/Users/x/.claude-work", hash("/Users/x/.claude-work")},
		{"/Users/x/.claude-work/", hash("/Users/x/.claude-work/")},
		{"~/.claude", hash("~/.claude")},
	}
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			if got := KeychainService(tc.dir); got != tc.want {
				t.Errorf("KeychainService(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
	if KeychainService("/a") == KeychainService("/a/") {
		t.Error("trailing slash must change the hash")
	}
}

func TestPlanName(t *testing.T) {
	tests := []struct{ sub, tier, want string }{
		{"max", "default_claude_max_20x", "Max 20x"},
		{"max", "default_claude_max_5x", "Max 5x"},
		{"", "default_claude_max_5x", "Max 5x"},
		{"max", "", "Max"},
		{"pro", "", "Pro"},
		{"PRO", "default", "Pro"},
		{"team", "", "Team"},
		{"enterprise", "", "Enterprise"},
		{"", "", ""},
		{"student", "", "Student"},
	}
	for _, tc := range tests {
		if got := PlanName(tc.sub, tc.tier); got != tc.want {
			t.Errorf("PlanName(%q,%q) = %q, want %q", tc.sub, tc.tier, got, tc.want)
		}
	}
}

type fakeServer struct {
	status     int
	retryAfter string
	body       string
	gotAuth    string
	gotBeta    string
}

func (f *fakeServer) handler(w http.ResponseWriter, r *http.Request) {
	f.gotAuth = r.Header.Get("Authorization")
	f.gotBeta = r.Header.Get("anthropic-beta")
	if f.retryAfter != "" {
		w.Header().Set("Retry-After", f.retryAfter)
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	fmt.Fprint(w, f.body)
}

func TestRead(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	future := float64(now.Add(time.Hour).UnixMilli())
	past := float64(now.Add(-time.Hour).UnixMilli())

	type creds map[string]any
	good := func(mut func(m creds)) creds {
		m := creds{
			"accessToken":      "sk-ant-oat01-test",
			"expiresAt":        future,
			"scopes":           []string{"user:inference", "user:profile"},
			"subscriptionType": "max",
			"rateLimitTier":    "default_claude_max_20x",
		}
		if mut != nil {
			mut(m)
		}
		return m
	}

	tests := []struct {
		name       string
		version    string
		creds      creds // nil = no file
		server     fakeServer
		wantStatus model.Status // "" = success
		wantRetry  time.Duration
	}{
		{"ok", "2.1.200 (Claude Code)", good(nil), fakeServer{body: sampleUsage}, "", 0},
		{"expired token", "2.1.200 (Claude Code)", good(func(m creds) { m["expiresAt"] = past }), fakeServer{body: sampleUsage}, model.StatusAuthNeeded, 0},
		{"missing profile scope", "2.1.200 (Claude Code)", good(func(m creds) { m["scopes"] = []string{"user:inference"} }), fakeServer{body: sampleUsage}, model.StatusAuthNeeded, 0},
		{"old version", "1.9.9 (Claude Code)", good(nil), fakeServer{body: sampleUsage}, model.StatusUnsupported, 0},
		{"rate limited", "2.1.200 (Claude Code)", good(nil), fakeServer{status: 429, retryAfter: "120", body: `{}`}, model.StatusRateLimited, 120 * time.Second},
		{"401", "2.1.200 (Claude Code)", good(nil), fakeServer{status: 401, body: `{}`}, model.StatusAuthNeeded, 0},
		{"no credentials", "2.1.200 (Claude Code)", nil, fakeServer{body: sampleUsage}, model.StatusAuthNeeded, 0},
		{"no windows", "2.1.200 (Claude Code)", good(nil), fakeServer{body: `{"five_hour":null}`}, model.StatusUnsupported, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("AITANK_HOME", t.TempDir())
			t.Setenv("AITANK_TEST_HOME", home)
			if tc.creds != nil {
				dir := filepath.Join(home, ".claude")
				os.MkdirAll(dir, 0o700)
				b, _ := json.Marshal(map[string]any{"claudeAiOauth": tc.creds})
				if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"me@example.com"}}`), 0o600)

			fs := tc.server
			srv := httptest.NewServer(http.HandlerFunc(fs.handler))
			defer srv.Close()
			old := UsageURL
			UsageURL = srv.URL + "/api/oauth/usage"
			defer func() { UsageURL = old }()

			env := &provider.Env{
				Home:     home,
				HTTP:     srv.Client(),
				Secrets:  secrets.NewMemory(),
				Now:      func() time.Time { return now },
				Getenv:   func(k string) string { return map[string]string{"AITANK_CLAUDE_CREDS_FILE_ONLY": "1"}[k] },
				LookPath: func(string) (string, error) { return "/fake/bin/claude", nil },
				Run: func(ctx context.Context, e []string, name string, args ...string) ([]byte, error) {
					if name == "/fake/bin/claude" && len(args) == 1 && args[0] == "--version" {
						return []byte(tc.version + "\n"), nil
					}
					return nil, errors.New("unexpected command " + name + " " + strings.Join(args, " "))
				},
			}
			r, err := P{}.Read(context.Background(), env, &config.Account{ID: "claude-1", Provider: "claude"})
			if tc.wantStatus == "" {
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				if r.Plan != "Max 20x" || r.Identity != "me@example.com" || len(r.Windows) != 3 {
					t.Errorf("reading = %+v", r)
				}
				if fs.gotAuth != "Bearer sk-ant-oat01-test" || fs.gotBeta != "oauth-2025-04-20" {
					t.Errorf("headers auth=%q beta=%q", fs.gotAuth, fs.gotBeta)
				}
				if env.VersionSeen != "2.1.200" {
					t.Errorf("VersionSeen = %q", env.VersionSeen)
				}
				return
			}
			if err == nil {
				t.Fatalf("want %s, got reading %+v", tc.wantStatus, r)
			}
			st, ra := provider.StatusOf(err)
			if st != tc.wantStatus || ra != tc.wantRetry {
				t.Errorf("status = %s retry %v, want %s retry %v (err %v)", st, ra, tc.wantStatus, tc.wantRetry, err)
			}
		})
	}
}

func TestReadToolMissing(t *testing.T) {
	env := &provider.Env{
		Home:     t.TempDir(),
		LookPath: func(n string) (string, error) { return "", errors.New("not found") },
	}
	_, err := P{}.Read(context.Background(), env, &config.Account{ID: "c"})
	if st, _ := provider.StatusOf(err); st != model.StatusToolMissing {
		t.Errorf("status = %s", st)
	}
}
