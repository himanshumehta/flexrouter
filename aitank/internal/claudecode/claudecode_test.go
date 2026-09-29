package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

// statusSample is the §1a statusLine stdin, trimmed to the fields we use
// plus a few we ignore.
const statusSample = `{
  "session_id": "abc",
  "model": {"id": "claude-fable", "display_name": "Fable"},
  "version": "2.1.284",
  "rate_limits": {
    "five_hour":  { "used_percentage": 23.5, "resets_at": 1738425600 },
    "seven_day":  { "used_percentage": 41.2, "resets_at": 1738857600 },
    "spend_limit":{ "used_percentage": 62.8, "resets_at": 1740787200 }
  }
}`

func parse(t *testing.T, s string) *StatusInput {
	t.Helper()
	var in StatusInput
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		t.Fatal(err)
	}
	return &in
}

func TestStatusInputWindows(t *testing.T) {
	ws := parse(t, statusSample).Windows()
	tests := []struct {
		kind  model.WindowKind
		name  string
		pct   float64
		reset int64
	}{
		{model.FiveHour, "5-hour", 23.5, 1738425600},
		{model.Weekly, "Weekly", 41.2, 1738857600},
		{model.Monthly, "Spend limit", 62.8, 1740787200},
	}
	if len(ws) != len(tests) {
		t.Fatalf("windows = %+v", ws)
	}
	for i, tc := range tests {
		w := ws[i]
		if w.Kind != tc.kind || w.Name != tc.name || *w.UsedPct != tc.pct {
			t.Errorf("[%d] = %+v", i, w)
		}
		if w.ResetsAt == nil || w.ResetsAt.Unix() != tc.reset || w.ResetsAt.Location() != time.UTC {
			t.Errorf("[%d] reset = %v, want epoch %d", i, w.ResetsAt, tc.reset)
		}
	}

	partial := []struct {
		name string
		body string
		n    int
	}{
		{"no rate_limits", `{"session_id":"x"}`, 0},
		{"only five_hour", `{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":1738425600}}}`, 1},
		{"null percentage", `{"rate_limits":{"five_hour":{"used_percentage":null}}}`, 0},
		{"no reset", `{"rate_limits":{"seven_day":{"used_percentage":5}}}`, 1},
	}
	for _, tc := range partial {
		t.Run(tc.name, func(t *testing.T) {
			if got := parse(t, tc.body).Windows(); len(got) != tc.n {
				t.Errorf("got %+v", got)
			}
		})
	}
	var nilIn *StatusInput
	if nilIn.Windows() != nil {
		t.Error("nil input should give nil")
	}
}

func TestMerge(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	acct := &config.Account{ID: "claude-1", Provider: "claude", Plan: "Max 20x"}
	oldReset := now.Add(time.Hour)
	prev := &model.Reading{
		AccountID: "claude-1", Provider: "claude", Plan: "Max 20x", Identity: "me@x", Source: "usage endpoint",
		FetchedAt: now.Add(-10 * time.Minute),
		Windows: []model.Window{
			{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(10), ResetsAt: &oldReset},
			{Kind: model.Weekly, Name: "Weekly", UsedPct: model.F(20)},
			{Kind: model.ModelWeekly, Name: "Weekly (Fable)", Model: "Fable", UsedPct: model.F(5)},
		},
	}
	ws := parse(t, statusSample).Windows()[:2]
	r := Merge(prev, acct, ws, now)

	if len(r.Windows) != 3 {
		t.Fatalf("windows = %+v", r.Windows)
	}
	if *r.Windows[0].UsedPct != 23.5 || r.Windows[0].ResetsAt.Unix() != 1738425600 {
		t.Errorf("five_hour not replaced: %+v", r.Windows[0])
	}
	if *r.Windows[1].UsedPct != 41.2 {
		t.Errorf("weekly not replaced: %+v", r.Windows[1])
	}
	if r.Windows[2].Model != "Fable" || *r.Windows[2].UsedPct != 5 {
		t.Errorf("per-model window lost: %+v", r.Windows[2])
	}
	if r.Identity != "me@x" || r.Source != "claude code statusline" || !r.FetchedAt.Equal(now) {
		t.Errorf("reading = %+v", r)
	}
	// prev is not mutated.
	if *prev.Windows[0].UsedPct != 10 || prev.Source != "usage endpoint" {
		t.Errorf("prev mutated: %+v", prev)
	}

	// No previous reading: windows are taken as is.
	r = Merge(nil, acct, ws, now)
	if r.AccountID != "claude-1" || r.Plan != "Max 20x" || len(r.Windows) != 2 {
		t.Errorf("fresh merge = %+v", r)
	}
	// A new kind (spend limit) is appended.
	r = Merge(prev, acct, parse(t, statusSample).Windows(), now)
	if len(r.Windows) != 4 || r.Windows[3].Kind != model.Monthly {
		t.Errorf("spend limit not appended: %+v", r.Windows)
	}
}

func TestSessionAccount(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{
		{ID: "codex-1", Provider: "codex"},
		{ID: "claude-default", Provider: "claude"},
		{ID: "claude-work", Provider: "claude", ProfileDir: "/Users/x/.claude-work"},
		{ID: "codex-work", Provider: "codex", ProfileDir: "/Users/x/.claude-other"},
	}}
	tests := []struct {
		dir  string
		want string
	}{
		{"", "claude-default"},
		{"/Users/x/.claude-work", "claude-work"},
		{"/Users/x/.claude-work/", "claude-work"},
		{"/Users/x/./.claude-work", "claude-work"},
		{"/Users/x/.claude-other", ""}, // only a codex account there
		{"/Users/x/.claude-nope", ""},
	}
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			a := SessionAccount(cfg, tc.dir)
			got := ""
			if a != nil {
				got = a.ID
			}
			if got != tc.want {
				t.Errorf("SessionAccount(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
	if a := SessionAccount(cfg, "/Users/x/.claude-work"); a != &cfg.Accounts[2] {
		t.Error("should return a pointer into cfg.Accounts")
	}
	if SessionAccount(&config.Config{}, "") != nil {
		t.Error("empty config should give nil")
	}
}

func setupEnv(t *testing.T) (home, aitankHome string) {
	t.Helper()
	home, aitankHome = t.TempDir(), t.TempDir()
	t.Setenv("AITANK_HOME", aitankHome)
	t.Setenv("AITANK_TEST_HOME", home)
	return
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings not JSON: %v\n%s", err, b)
	}
	return m
}

func stopFailure(t *testing.T, m map[string]any) []any {
	t.Helper()
	hooks, _ := m["hooks"].(map[string]any)
	list, _ := hooks["StopFailure"].([]any)
	return list
}

func TestApplyCreates(t *testing.T) {
	home, _ := setupEnv(t)
	path := SettingsPath(home, "")
	if path != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("SettingsPath = %s", path)
	}
	res, err := Apply(path, "/usr/local/bin/aitank", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Backup != "" {
		t.Errorf("res = %+v", res)
	}
	m := readSettings(t, path)
	sl := m["statusLine"].(map[string]any)
	if sl["command"] != "/usr/local/bin/aitank status" || sl["type"] != "command" {
		t.Errorf("statusLine = %v", sl)
	}
	list := stopFailure(t, m)
	if len(list) != 1 || list[0].(map[string]any)["matcher"] != "rate_limit" {
		t.Errorf("StopFailure = %v", list)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}

	// Second run is a no-op.
	res, err = Apply(path, "/usr/local/bin/aitank", false)
	if err != nil || res.Changed {
		t.Errorf("second run: res=%+v err=%v", res, err)
	}
	if len(stopFailure(t, readSettings(t, path))) != 1 {
		t.Error("hook duplicated")
	}
}

func TestApplyMerges(t *testing.T) {
	home, aitankHome := setupEnv(t)
	path := SettingsPath(home, filepath.Join(home, ".claude-work"))
	os.MkdirAll(filepath.Dir(path), 0o700)
	orig := `{
  "model": "opus",
  "env": {"FOO": "bar"},
  "hooks": {
    "StopFailure": [{"matcher": "overloaded", "hooks": [{"type": "command", "command": "notify-send overloaded"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "check.sh"}]}]
  }
}`
	os.WriteFile(path, []byte(orig), 0o600)

	res, err := Apply(path, "aitank", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Backup == "" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.HasPrefix(res.Backup, filepath.Join(aitankHome, "backups")) {
		t.Errorf("backup %s not under AITANK_HOME", res.Backup)
	}
	if b, _ := os.ReadFile(res.Backup); string(b) != orig {
		t.Errorf("backup content differs")
	}
	m := readSettings(t, path)
	if m["model"] != "opus" || m["env"].(map[string]any)["FOO"] != "bar" {
		t.Errorf("other keys lost: %v", m)
	}
	hooks := m["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("PreToolUse hook lost")
	}
	list := stopFailure(t, m)
	if len(list) != 2 || list[0].(map[string]any)["matcher"] != "overloaded" || list[1].(map[string]any)["matcher"] != "rate_limit" {
		t.Errorf("StopFailure = %v", list)
	}

	res, err = Apply(path, "aitank", false)
	if err != nil || res.Changed {
		t.Errorf("second run: %+v %v", res, err)
	}
}

func TestApplyForeignStatusLine(t *testing.T) {
	home, _ := setupEnv(t)
	path := SettingsPath(home, "")
	os.MkdirAll(filepath.Dir(path), 0o700)
	orig := `{"statusLine":{"type":"command","command":"~/bin/mystatus.sh"}}`
	os.WriteFile(path, []byte(orig), 0o600)

	_, err := Apply(path, "aitank", false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want refusal mentioning --force, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Error("file changed despite refusal")
	}

	res, err := Apply(path, "aitank", true)
	if err != nil || !res.Changed {
		t.Fatalf("force: %+v %v", res, err)
	}
	if readSettings(t, path)["statusLine"].(map[string]any)["command"] != "aitank status" {
		t.Error("statusLine not replaced with force")
	}
}

func TestApplyInvalidJSON(t *testing.T) {
	home, _ := setupEnv(t)
	path := SettingsPath(home, "")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"a": `), 0o600)
	if _, err := Apply(path, "aitank", false); err == nil {
		t.Error("want error on invalid JSON")
	}
}

func TestHookInputIsLimitHit(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`{"hook_event_name":"StopFailure","error":"rate_limit"}`, true},
		{`{"hook_event_name":"StopFailure","error":"unknown","error_details":"Claude usage limit reached. Resets 5pm"}`, true},
		{`{"error":"server_error","error_details":"5-hour limit reached"}`, true},
		{`{"error":"overloaded"}`, false},
		{`{"error":"authentication_failed","error_details":"bad token"}`, false},
		{`{}`, false},
	}
	for _, tc := range tests {
		var h HookInput
		if err := json.Unmarshal([]byte(tc.in), &h); err != nil {
			t.Fatal(err)
		}
		if got := h.IsLimitHit(); got != tc.want {
			t.Errorf("%s → %v, want %v", tc.in, got, tc.want)
		}
	}
}
