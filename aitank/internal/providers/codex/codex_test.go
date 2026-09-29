package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

// sampleRateLimits is the §2 account/rateLimits/read result.
const sampleRateLimits = `{
  "ordinaryUsageAllowed": false,
  "accountId": "account-123",
  "rateLimits": {
    "limitId":"codex","limitName":null,"normalModelSlug":null,
    "primary":   {"usedPercent":42,"windowDurationMins":300,"resetsAt":1735693200},
    "secondary": {"usedPercent":5, "windowDurationMins":10080,"resetsAt":1736200000},
    "credits":   {"hasCredits":true,"unlimited":false,"balance":"12.50"},
    "individualLimit": {"limit":"25000","used":"8000","remainingPercent":68,"resetsAt":1737000000},
    "spendControlReached": false,
    "planType":"pro",
    "rateLimitReachedType": null
  },
  "rateLimitsByLimitId": {
    "codex": {"limitId":"codex","primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1735693200}},
    "codex_other": {"limitId":"codex_other","limitName":"GPT-5 Pro","primary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1736200000}}
  },
  "rateLimitResetCredits": {"availableCount":3,"credits":null},
  "rateLimitUpsell": null
}`

const sampleAccount = `{"account":{"type":"chatgpt","email":"a@b.com","planType":"pro"},"requiresOpenaiAuth":true}`

func findKind(ws []model.Window, kind model.WindowKind, name string) *model.Window {
	for i := range ws {
		if ws[i].Kind == kind && (name == "" || ws[i].Name == name) {
			return &ws[i]
		}
	}
	return nil
}

func TestNormalizeSample(t *testing.T) {
	var rl RateLimits
	if err := json.Unmarshal([]byte(sampleRateLimits), &rl); err != nil {
		t.Fatal(err)
	}
	var ar accountRead
	if err := json.Unmarshal([]byte(sampleAccount), &ar); err != nil {
		t.Fatal(err)
	}
	r := Normalize(&rl, &ar)

	if r.Identity != "a@b.com" || r.Plan != "ChatGPT Pro" {
		t.Errorf("identity/plan = %q/%q", r.Identity, r.Plan)
	}
	tests := []struct {
		name    string
		kind    model.WindowKind
		wname   string
		pct     float64
		resetAt int64
	}{
		{"primary is five_hour", model.FiveHour, "5-hour", 42, 1735693200},
		{"secondary is weekly", model.Weekly, "Weekly", 5, 1736200000},
		{"individual limit", model.Monthly, "Workspace credit limit", 32, 1737000000},
		{"extra limit by id", model.ModelWeekly, "Weekly (GPT-5 Pro)", 20, 1736200000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := findKind(r.Windows, tc.kind, tc.wname)
			if w == nil {
				t.Fatalf("no %s/%s window in %+v", tc.kind, tc.wname, r.Windows)
			}
			if p := w.Pct(); p == nil || *p != tc.pct {
				t.Errorf("pct = %v, want %v", p, tc.pct)
			}
			if w.ResetsAt == nil || w.ResetsAt.Unix() != tc.resetAt {
				t.Errorf("reset = %v, want %d", w.ResetsAt, tc.resetAt)
			}
		})
	}
	if len(r.Windows) != 4 {
		t.Errorf("want 4 windows (codex duplicate skipped), got %d", len(r.Windows))
	}
	il := findKind(r.Windows, model.Monthly, "")
	if il != nil && (il.Used == nil || *il.Used != 8000 || il.Limit == nil || *il.Limit != 25000) {
		t.Errorf("individual limit used/limit = %v/%v", il.Used, il.Limit)
	}
	if len(r.Balances) != 1 || r.Balances[0].Amount == nil || *r.Balances[0].Amount != 12.5 || r.Balances[0].Currency != "credits" {
		t.Errorf("balances = %+v", r.Balances)
	}
	if !r.UsagePaused {
		t.Error("ordinaryUsageAllowed=false should set UsagePaused")
	}
	if l := r.Left(false); l == nil || *l != 0 {
		t.Errorf("paused account should have 0 left, got %v", l)
	}
	found := false
	for _, n := range r.Notes {
		if strings.Contains(n, "3 saved reset") {
			found = true
		}
	}
	if !found {
		t.Errorf("reset-credits note missing: %v", r.Notes)
	}
}

func TestWindowDurations(t *testing.T) {
	tests := []struct {
		mins     float64
		kind     model.WindowKind
		wantName string
	}{
		{300, model.FiveHour, "5-hour"},
		{10080, model.Weekly, "Weekly"},
		{43200, model.Monthly, "Monthly"},
		{1440, model.Weekly, "1-day"},
		{120, model.FiveHour, "2-hour"},
		{0, model.Weekly, "Window"},
	}
	for _, tc := range tests {
		m := tc.mins
		w := windowFrom(&window{UsedPercent: model.F(1), WindowDurationMins: &m}, "")
		if w.Kind != tc.kind || w.Name != tc.wantName {
			t.Errorf("%v mins → %s/%s, want %s/%s", tc.mins, w.Kind, w.Name, tc.kind, tc.wantName)
		}
	}
	if windowFrom(nil, "") != nil || windowFrom(&window{}, "") != nil {
		t.Error("nil window or nil usedPercent should give nil")
	}
}

func TestNormalizeNullsAndCredits(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantWindows  int
		wantBalances int
		paused       bool
	}{
		{"all null", `{"rateLimits":{"primary":null,"secondary":null}}`, 0, 0, false},
		{"unlimited credits ignored", `{"rateLimits":{"credits":{"hasCredits":true,"unlimited":true,"balance":"5"}}}`, 0, 0, false},
		{"no credits", `{"rateLimits":{"credits":{"hasCredits":false,"unlimited":false,"balance":null}}}`, 0, 0, false},
		{"ordinary allowed", `{"ordinaryUsageAllowed":true,"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":300}}}`, 1, 0, false},
		{"no rateLimits", `{}`, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rl RateLimits
			if err := json.Unmarshal([]byte(tc.body), &rl); err != nil {
				t.Fatal(err)
			}
			r := Normalize(&rl, nil)
			if len(r.Windows) != tc.wantWindows || len(r.Balances) != tc.wantBalances || r.UsagePaused != tc.paused {
				t.Errorf("got %d windows, %d balances, paused %v", len(r.Windows), len(r.Balances), r.UsagePaused)
			}
		})
	}
}

// fakeAppServer answers app-server requests in-process over io.Pipe.
type fakeAppServer struct {
	account    string // JSON for account/read result
	rateLimits string // JSON result, or "" when rlErr is used
	rlErr      string // JSON error object for account/rateLimits/read

	mu       sync.Mutex
	received []map[string]any
}

func (f *fakeAppServer) start(ctx context.Context, bin string, env []string) (*Server, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	replies := make(chan string, 16)
	go func() {
		defer close(replies)
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			f.mu.Lock()
			f.received = append(f.received, m)
			f.mu.Unlock()
			id, hasID := m["id"]
			if !hasID {
				continue // notification
			}
			idj, _ := json.Marshal(id)
			compact := func(s string) string {
				var b bytes.Buffer
				if err := json.Compact(&b, []byte(s)); err != nil {
					return s
				}
				return b.String()
			}
			switch m["method"] {
			case "initialize":
				// a notification first, which the client must ignore
				replies <- `{"method":"account/updated","params":{}}`
				replies <- `{"id":` + string(idj) + `,"result":{"userAgent":"codex/0.150.0","codexHome":"/x/.codex","platformFamily":"unix","platformOs":"macos"}}`
			case "account/read":
				replies <- `{"id":` + string(idj) + `,"result":` + compact(f.account) + `}`
			case "account/rateLimits/read":
				if f.rlErr != "" {
					replies <- `{"id":` + string(idj) + `,"error":` + compact(f.rlErr) + `}`
				} else {
					replies <- `{"method":"account/rateLimits/updated","params":{}}`
					replies <- `{"id":` + string(idj) + `,"result":` + compact(f.rateLimits) + `}`
				}
			}
		}
	}()
	go func() {
		for r := range replies {
			if _, err := io.WriteString(outW, r+"\n"); err != nil {
				return
			}
		}
	}()
	return &Server{In: inW, Out: outR, Stop: func() {
		inW.Close()
		outW.Close()
		outR.Close()
	}}, nil
}

func TestReadEndToEnd(t *testing.T) {
	tests := []struct {
		name       string
		srv        *fakeAppServer
		wantStatus model.Status
	}{
		{"ok", &fakeAppServer{account: sampleAccount, rateLimits: sampleRateLimits}, ""},
		{"method not found", &fakeAppServer{account: sampleAccount, rlErr: `{"code":-32601,"message":"unknown request"}`}, model.StatusUnsupported},
		{"account null", &fakeAppServer{account: `{"account":null,"requiresOpenaiAuth":true}`, rateLimits: sampleRateLimits}, model.StatusAuthNeeded},
		{"api key account", &fakeAppServer{account: `{"account":{"type":"apiKey"}}`, rateLimits: sampleRateLimits}, model.StatusUnsupported},
		{"no windows", &fakeAppServer{account: sampleAccount, rateLimits: `{"rateLimits":{"primary":null}}`}, model.StatusUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := Start
			var gotEnv []string
			Start = func(ctx context.Context, bin string, env []string) (*Server, error) {
				gotEnv = env
				return tc.srv.start(ctx, bin, env)
			}
			defer func() { Start = old }()

			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			env := &provider.Env{
				Home:     t.TempDir(),
				Now:      func() time.Time { return now },
				Getenv:   func(string) string { return "" },
				LookPath: func(string) (string, error) { return "/fake/codex", nil },
				Run: func(ctx context.Context, e []string, name string, args ...string) ([]byte, error) {
					if len(args) == 1 && args[0] == "--version" {
						return []byte("codex-cli 0.150.0\n"), nil
					}
					return nil, errors.New("unexpected")
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := P{}.Read(ctx, env, &config.Account{ID: "codex-2", Provider: "codex", ProfileDir: "/p/.codex-work"})
			if len(gotEnv) != 1 || gotEnv[0] != "CODEX_HOME=/p/.codex-work" {
				t.Errorf("start env = %v", gotEnv)
			}
			if tc.wantStatus == "" {
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				if r.Identity != "a@b.com" || len(r.Windows) != 4 || !r.FetchedAt.Equal(now) {
					t.Errorf("reading = %+v", r)
				}
				// Messages carry no jsonrpc field; initialized is a notification.
				tc.srv.mu.Lock()
				defer tc.srv.mu.Unlock()
				var methods []string
				for _, m := range tc.srv.received {
					if _, ok := m["jsonrpc"]; ok {
						t.Errorf("message has jsonrpc field: %v", m)
					}
					methods = append(methods, m["method"].(string))
					if m["method"] == "initialized" {
						if _, ok := m["id"]; ok {
							t.Error("initialized must be a notification")
						}
					}
				}
				want := "initialize,initialized,account/read,account/rateLimits/read"
				if strings.Join(methods, ",") != want {
					t.Errorf("methods = %v, want %s", methods, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("want %s, got %+v", tc.wantStatus, r)
			}
			if st, _ := provider.StatusOf(err); st != tc.wantStatus {
				t.Errorf("status = %s, want %s (%v)", st, tc.wantStatus, err)
			}
		})
	}
}

func TestPlanName(t *testing.T) {
	tests := map[string]string{"pro": "ChatGPT Pro", "pro_lite": "ChatGPT Pro Lite", "": "", "ent26": "ChatGPT ent26", "edu_plus": "ChatGPT edu plus"}
	for in, want := range tests {
		if got := PlanName(in); got != want {
			t.Errorf("PlanName(%q) = %q, want %q", in, got, want)
		}
	}
}
