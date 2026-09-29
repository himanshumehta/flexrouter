// Package codex reads ChatGPT/Codex plan usage (FR-4.2) through the user's
// own `codex app-server` (JSON-RPC over stdio), started read-only in the
// account's CODEX_HOME. The app-server does the network call with Codex's
// own sign-in; aitank never sees the token.
//
// The app-server protocol is not a stable public API, so reads are gated on
// the Codex version (docs/VENDORS.md §2).
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

// P is the Codex provider.
type P struct{}

func (P) ID() string   { return "codex" }
func (P) Name() string { return "ChatGPT / Codex" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family: provider.FamilyCodex, Auth: []provider.AuthMethod{provider.AuthLocal},
		Windows: true, Balance: true, Launch: true, Profiles: true,
		FilesRead: []string{
			"~/.codex/auth.json (signed-in email and plan from the stored ID token; discovery only)",
			"~/.codex-*/auth.json (other profile folders, discovery only)",
		},
		Endpoints:  []string{"none directly; `codex app-server` contacts chatgpt.com/backend-api/wham/usage with Codex's own sign-in"},
		Commands:   []string{"codex --version", "codex -s read-only -a never app-server"},
		VendorTool: "codex", TestedMax: "0.159.0",
	}
}

func (P) LaunchSpec(a *config.Account) (string, string, string) {
	return "codex", "CODEX_HOME", a.ProfileDir
}

func identity(dir string) (email, plan string, ok bool) {
	m, err := provider.ReadJSONFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		return "", "", false
	}
	tok := provider.Str(m, "tokens", "id_token")
	if tok == "" {
		if k, _ := m["OPENAI_API_KEY"].(string); k != "" {
			return "", "API key", true
		}
		return "", "", false
	}
	c := provider.JWTClaims(tok)
	email = provider.Str(c, "email")
	if email == "" {
		email = provider.Str(c, "https://api.openai.com/profile", "email")
	}
	plan = provider.Str(c, "https://api.openai.com/auth", "chatgpt_plan_type")
	return email, PlanName(plan), true
}

// Detect reads ~/.codex/auth.json and ~/.codex-*/auth.json. When Codex keeps
// its sign-in in the Keychain instead, the folder's config.toml still marks
// it as a Codex home; the identity is then unknown until the first read.
func (P) Detect(env *provider.Env) []provider.Detection {
	var out []provider.Detection
	def := filepath.Join(env.Home, ".codex")
	dirs, _ := filepath.Glob(filepath.Join(env.Home, ".codex-*"))
	sort.Strings(dirs)
	for _, d := range append([]string{def}, dirs...) {
		email, plan, ok := identity(d)
		if !ok {
			if _, err := os.Stat(filepath.Join(d, "config.toml")); err != nil || d != def {
				continue
			}
			if !keyringMode(d) {
				continue
			}
		}
		det := provider.Detection{Provider: "codex", Identity: email, Plan: plan, Source: d + "/auth.json"}
		if d != def {
			det.ProfileDir = d
		}
		if !ok {
			det.Source, det.Note = d+"/config.toml", "sign-in kept in Keychain; identity shows after the first read"
		}
		out = append(out, det)
	}
	return out
}

func keyringMode(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	return err == nil && strings.Contains(string(b), "cli_auth_credentials_store")
}

// PlanName makes Codex plan ids readable.
func PlanName(p string) string {
	names := map[string]string{"free": "Free", "go": "Go", "plus": "Plus", "pro": "Pro", "pro_lite": "Pro Lite", "pro_max": "Pro Max",
		"team": "Team", "business": "Business", "enterprise": "Enterprise", "edu": "Edu"}
	if n, ok := names[p]; ok {
		return "ChatGPT " + n
	}
	if p == "" {
		return ""
	}
	return "ChatGPT " + strings.ReplaceAll(p, "_", " ")
}

// Server is a started app-server. Start is replaceable in tests.
type Server struct {
	In   io.WriteCloser
	Out  io.Reader
	Stop func()
}

// Start launches `codex -s read-only -a never app-server`.
var Start = func(ctx context.Context, bin string, env []string) (*Server, error) {
	cmd := exec.CommandContext(ctx, bin, "-s", "read-only", "-a", "never", "app-server")
	cmd.Env = append(os.Environ(), env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Server{In: in, Out: out, Stop: func() {
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}}, nil
}

type rpcMsg struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params any             `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// call sends requests and collects responses by id.
func call(ctx context.Context, s *Server, reqs []rpcMsg) (map[int]rpcMsg, error) {
	enc := json.NewEncoder(s.In)
	want := map[int]bool{}
	for _, r := range reqs {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
		if r.ID != nil {
			want[*r.ID] = true
		}
	}
	got := map[int]rpcMsg{}
	lines := make(chan []byte)
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(s.Out)
		sc.Buffer(make([]byte, 64*1024), 8<<20)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-ctx.Done():
				return
			}
		}
		errc <- sc.Err()
	}()
	for len(got) < len(want) {
		select {
		case <-ctx.Done():
			return got, errors.New("codex app-server did not answer in time")
		case err := <-errc:
			if err == nil {
				err = errors.New("codex app-server exited early")
			}
			return got, err
		case b := <-lines:
			var m rpcMsg
			if json.Unmarshal(b, &m) != nil || m.ID == nil || m.Method != "" {
				continue // notifications and server requests
			}
			if want[*m.ID] {
				got[*m.ID] = m
			}
		}
	}
	return got, nil
}

func id(n int) *int { return &n }

type window struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

type snapshot struct {
	LimitID   string  `json:"limitId"`
	LimitName *string `json:"limitName"`
	Primary   *window `json:"primary"`
	Secondary *window `json:"secondary"`
	Credits   *struct {
		HasCredits bool `json:"hasCredits"`
		Unlimited  bool `json:"unlimited"`
		Balance    any  `json:"balance"`
	} `json:"credits"`
	IndividualLimit *struct {
		Limit            any      `json:"limit"`
		Used             any      `json:"used"`
		RemainingPercent *float64 `json:"remainingPercent"`
		ResetsAt         *float64 `json:"resetsAt"`
	} `json:"individualLimit"`
	SpendControlReached bool   `json:"spendControlReached"`
	PlanType            string `json:"planType"`
}

// RateLimits is the account/rateLimits/read result.
type RateLimits struct {
	OrdinaryUsageAllowed *bool               `json:"ordinaryUsageAllowed"`
	RateLimits           *snapshot           `json:"rateLimits"`
	ByLimitID            map[string]snapshot `json:"rateLimitsByLimitId"`
	ResetCredits         *struct {
		AvailableCount *float64         `json:"availableCount"`
		Credits        []map[string]any `json:"credits"`
	} `json:"rateLimitResetCredits"`
}

type accountRead struct {
	Account *struct {
		Type     string `json:"type"`
		Email    string `json:"email"`
		PlanType string `json:"planType"`
	} `json:"account"`
}

func windowFrom(w *window, prefix string) *model.Window {
	if w == nil || w.UsedPercent == nil {
		return nil
	}
	out := model.Window{UsedPct: w.UsedPercent, Unit: "%"}
	if w.ResetsAt != nil {
		out.ResetsAt = provider.ParseTime(*w.ResetsAt)
	}
	mins := 0.0
	if w.WindowDurationMins != nil {
		mins = *w.WindowDurationMins
	}
	switch {
	case mins == 300:
		out.Kind, out.Name = model.FiveHour, "5-hour"
	case mins == 10080:
		out.Kind, out.Name = model.Weekly, "Weekly"
	case mins >= 40000:
		out.Kind, out.Name = model.Monthly, "Monthly"
	case mins > 0 && int(mins)%1440 == 0:
		out.Kind, out.Name = model.Weekly, fmt.Sprintf("%d-day", int(mins)/1440)
	case mins > 0:
		out.Kind, out.Name = model.FiveHour, fmt.Sprintf("%g-hour", mins/60)
	default:
		out.Kind, out.Name = model.Weekly, "Window"
	}
	if prefix != "" {
		out.Kind = model.ModelWeekly
		out.Model = prefix
		out.Name = out.Name + " (" + prefix + ")"
	}
	return &out
}

// Normalize converts app-server results to a reading.
func Normalize(rl *RateLimits, acct *accountRead) *model.Reading {
	r := &model.Reading{Source: "codex app-server"}
	if acct != nil && acct.Account != nil {
		r.Identity = acct.Account.Email
		r.Plan = PlanName(acct.Account.PlanType)
	}
	if rl.RateLimits != nil {
		s := rl.RateLimits
		if r.Plan == "" {
			r.Plan = PlanName(s.PlanType)
		}
		for _, w := range []*window{s.Primary, s.Secondary} {
			if nw := windowFrom(w, ""); nw != nil {
				r.Windows = append(r.Windows, *nw)
			}
		}
		if s.Credits != nil && !s.Credits.Unlimited && (s.Credits.HasCredits || s.Credits.Balance != nil) {
			r.Balances = append(r.Balances, model.Money{Amount: provider.Num(s.Credits.Balance), Currency: "credits", Label: "Credits"})
		}
		if il := s.IndividualLimit; il != nil {
			w := model.Window{Kind: model.Monthly, Name: "Workspace credit limit", Used: provider.Num(il.Used), Limit: provider.Num(il.Limit), Unit: "credits"}
			if il.RemainingPercent != nil {
				u := 100 - *il.RemainingPercent
				w.UsedPct = &u
			}
			if il.ResetsAt != nil {
				w.ResetsAt = provider.ParseTime(*il.ResetsAt)
			}
			if w.Pct() != nil {
				r.Windows = append(r.Windows, w)
			}
		}
		if s.SpendControlReached {
			r.Notes = append(r.Notes, "Workspace spend control reached.")
		}
	}
	// Extra limits (e.g. per-model) beyond the main "codex" one.
	ids := make([]string, 0, len(rl.ByLimitID))
	for k := range rl.ByLimitID {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		s := rl.ByLimitID[k]
		if k == "codex" || (rl.RateLimits != nil && k == rl.RateLimits.LimitID) {
			continue
		}
		name := k
		if s.LimitName != nil && *s.LimitName != "" {
			name = *s.LimitName
		}
		for _, w := range []*window{s.Primary, s.Secondary} {
			if nw := windowFrom(w, name); nw != nil {
				r.Windows = append(r.Windows, *nw)
			}
		}
	}
	if rl.OrdinaryUsageAllowed != nil && !*rl.OrdinaryUsageAllowed {
		r.UsagePaused = true
		r.Notes = append(r.Notes, "Included usage is paused for this account.")
	}
	if rc := rl.ResetCredits; rc != nil && rc.AvailableCount != nil && *rc.AvailableCount > 0 {
		note := fmt.Sprintf("%.0f saved reset(s) available", *rc.AvailableCount)
		var soonest *time.Time
		for _, c := range rc.Credits {
			for _, k := range []string{"expiresAt", "expires_at", "expiry"} {
				if t := provider.ParseTime(c[k]); t != nil && (soonest == nil || t.Before(*soonest)) {
					soonest = t
				}
			}
		}
		if soonest != nil {
			note += ", first expires " + soonest.Local().Format("2 Jan")
		}
		r.Notes = append(r.Notes, note+".")
	}
	return r
}

func (p P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	caps := p.Capabilities()
	bin, err := env.LookPath("codex")
	if err != nil {
		return nil, provider.Errf(model.StatusToolMissing, "Codex (`codex`) is not installed or not on PATH")
	}
	gate, err := env.Gate(ctx, caps, bin)
	if err != nil {
		return nil, err
	}
	var extra []string
	if acct.ProfileDir != "" {
		extra = append(extra, "CODEX_HOME="+acct.ProfileDir)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	srv, err := Start(ctx, bin, extra)
	if err != nil {
		return nil, provider.Errf(model.StatusToolMissing, "could not start codex app-server: %v", err)
	}
	defer srv.Stop()
	res, err := call(ctx, srv, []rpcMsg{
		{ID: id(1), Method: "initialize", Params: map[string]any{"clientInfo": map[string]string{"name": "aitank", "title": "aitank", "version": strings.TrimPrefix(provider.UserAgent, "aitank/")}}},
		{Method: "initialized"},
		{ID: id(2), Method: "account/read", Params: map[string]any{}},
		{ID: id(3), Method: "account/rateLimits/read"},
	})
	if err != nil {
		return nil, provider.Errf(model.StatusError, "codex app-server: %v", err)
	}
	if e := res[1].Error; e != nil {
		return nil, provider.Errf(model.StatusUnsupported, "codex app-server refused initialize: %s", e.Message)
	}
	var ar accountRead
	if m := res[2]; m.Error == nil {
		_ = json.Unmarshal(m.Result, &ar)
	}
	if ar.Account == nil && res[2].Error == nil {
		return nil, provider.Errf(model.StatusAuthNeeded, "Codex is not signed in in this profile; run `aitank codex %s` and sign in", acct.ID)
	}
	if ar.Account != nil && ar.Account.Type == "apiKey" {
		return nil, provider.Errf(model.StatusUnsupported, "Codex is signed in with an API key, which has no plan windows; add an OpenAI admin key as openai-api instead")
	}
	m := res[3]
	if m.Error != nil {
		st := model.StatusError
		if m.Error.Code == -32601 || strings.Contains(strings.ToLower(m.Error.Message), "method") {
			st = model.StatusUnsupported
		}
		if strings.Contains(strings.ToLower(m.Error.Message), "auth") || strings.Contains(strings.ToLower(m.Error.Message), "login") {
			st = model.StatusAuthNeeded
		}
		return nil, provider.Errf(st, "codex %s: account/rateLimits/read: %s", gate.Version, m.Error.Message)
	}
	var rl RateLimits
	if err := json.Unmarshal(m.Result, &rl); err != nil {
		return nil, provider.Errf(model.StatusUnsupported, "codex %s returned an unexpected rate-limit shape: %v", gate.Version, err)
	}
	r := Normalize(&rl, &ar)
	r.FetchedAt = env.Now()
	if len(r.Windows) == 0 && len(r.Balances) == 0 {
		return nil, provider.Errf(model.StatusUnsupported, "codex %s reported no usage windows", gate.Version)
	}
	if gate.Untested {
		r.Notes = append(r.Notes, fmt.Sprintf("Codex %s is newer than the version this reader was checked against (%s).", gate.Version, caps.TestedMax))
	}
	return r, nil
}
