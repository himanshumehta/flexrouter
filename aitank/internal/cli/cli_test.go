package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/all"
)

// isolate points aitank's data dir and the home folder at temp dirs.
func isolate(t *testing.T) (home, data string) {
	t.Helper()
	home, data = t.TempDir(), t.TempDir()
	t.Setenv("AITANK_HOME", data)
	t.Setenv("AITANK_TEST_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("NO_COLOR", "1")
	return home, data
}

func TestStripJSONC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want any
	}{
		{"line comments", "{\n  // a comment\n  \"a\": 1 // trailing\n}", map[string]any{"a": 1.0}},
		{"block comments", `{ /* x */ "a": /* y */ 2 }`, map[string]any{"a": 2.0}},
		{"multiline block", "{\n/*\n * doc\n */\n\"a\": true}", map[string]any{"a": true}},
		{"trailing commas", "{\"a\": [1, 2, ], \"b\": {\"c\": 3,\n},\n}", map[string]any{"a": []any{1.0, 2.0}, "b": map[string]any{"c": 3.0}}},
		{"slashes in strings kept", `{"url": "https://example.com//x", "c": "/* not a comment */"}`, map[string]any{"url": "https://example.com//x", "c": "/* not a comment */"}},
		{"escaped quote in string", `{"a": "say \"// hi\"", /* c */ "b": ",}"}`, map[string]any{"a": `say "// hi"`, "b": ",}"}},
		{"comma inside string before brace", `{"a": "x, }"}`, map[string]any{"a": "x, }"}},
		{"comment at EOF without newline", `{"a": 1} // end`, map[string]any{"a": 1.0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := StripJSONC([]byte(tc.in))
			var got any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("not JSON after strip: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSwitchCursor(t *testing.T) {
	tests := []struct {
		name         string
		existing     string // "" = no file
		profile      string
		wantVars     []map[string]any
		wantLost     bool
		wantBackup   bool
		wantKeepKeys map[string]any
	}{
		{
			name:    "no settings file",
			profile: "/Users/x/.claude-work",
			wantVars: []map[string]any{
				{"name": "CLAUDE_CONFIG_DIR", "value": "/Users/x/.claude-work"},
			},
		},
		{
			name: "adds entry, keeps others, reports comments",
			existing: `{
  // editor
  "editor.fontSize": 14,
  "claudeCode.environmentVariables": [
    {"name": "FOO", "value": "bar"},
    {"name": "CLAUDE_CONFIG_DIR", "value": "/old"},
  ],
}`,
			profile: "/Users/x/.claude-work",
			wantVars: []map[string]any{
				{"name": "FOO", "value": "bar"},
				{"name": "CLAUDE_CONFIG_DIR", "value": "/Users/x/.claude-work"},
			},
			wantLost: true, wantBackup: true,
			wantKeepKeys: map[string]any{"editor.fontSize": 14.0},
		},
		{
			name:       "default profile removes entry",
			existing:   `{"claudeCode.environmentVariables": [{"name": "CLAUDE_CONFIG_DIR", "value": "/old"}, {"name": "FOO", "value": "bar"}]}`,
			profile:    "",
			wantVars:   []map[string]any{{"name": "FOO", "value": "bar"}},
			wantBackup: true,
		},
		{
			name:       "default profile with nothing left",
			existing:   `{"claudeCode.environmentVariables": [{"name": "CLAUDE_CONFIG_DIR", "value": "/old"}]}`,
			profile:    "",
			wantVars:   []map[string]any{},
			wantBackup: true,
		},
		{
			name:       "empty file",
			existing:   "  \n",
			profile:    "/p",
			wantVars:   []map[string]any{{"name": "CLAUDE_CONFIG_DIR", "value": "/p"}},
			wantBackup: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, data := isolate(t)
			path := filepath.Join(home, "Cursor", "User", "settings.json")
			os.MkdirAll(filepath.Dir(path), 0o700)
			if tc.existing != "" {
				os.WriteFile(path, []byte(tc.existing), 0o644)
			}
			backup, lost, err := SwitchCursor(path, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			if lost != tc.wantLost {
				t.Errorf("lostComments = %v", lost)
			}
			if (backup != "") != tc.wantBackup {
				t.Errorf("backup = %q", backup)
			}
			if backup != "" {
				if !strings.HasPrefix(backup, filepath.Join(data, "backups")) {
					t.Errorf("backup %s not in AITANK_HOME", backup)
				}
				if b, _ := os.ReadFile(backup); string(b) != tc.existing {
					t.Errorf("backup content = %q", b)
				}
			}
			b, _ := os.ReadFile(path)
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("result not JSON: %v\n%s", err, b)
			}
			vars, _ := got["claudeCode.environmentVariables"].([]any)
			wantJSON, _ := json.Marshal(tc.wantVars)
			gotJSON, _ := json.Marshal(vars)
			if vars == nil || string(wantJSON) != string(gotJSON) {
				t.Errorf("vars = %s, want %s", gotJSON, wantJSON)
			}
			for k, v := range tc.wantKeepKeys {
				if got[k] != v {
					t.Errorf("%s = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestSwitchCursorBadJSON(t *testing.T) {
	home, _ := isolate(t)
	path := filepath.Join(home, "settings.json")
	os.WriteFile(path, []byte(`{"a": `), 0o644)
	if _, _, err := SwitchCursor(path, "/p"); err == nil {
		t.Error("want parse error")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"a": ` {
		t.Error("file changed on parse error")
	}
}

func TestParsePicks(t *testing.T) {
	tests := []struct {
		in      string
		n       int
		want    []int
		wantErr bool
	}{
		{"", 3, nil, false},
		{"all", 3, []int{0, 1, 2}, false},
		{"ALL", 2, []int{0, 1}, false},
		{"1", 3, []int{0}, false},
		{"3,1", 3, []int{0, 2}, false},
		{"1, 2 2", 3, []int{0, 1}, false},
		{"0", 3, nil, true},
		{"4", 3, nil, true},
		{"x", 3, nil, true},
		{"1,-2", 3, nil, true},
	}
	for _, tc := range tests {
		got, err := parsePicks(tc.in, tc.n)
		if (err != nil) != tc.wantErr {
			t.Errorf("parsePicks(%q) err = %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parsePicks(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// run calls Execute in-process with stdout and stderr captured.
func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { b, _ := io.ReadAll(outR); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errC <- string(b) }()
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	code = Execute(args)
	outW.Close()
	errW.Close()
	return code, <-outC, <-errC
}

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	e := base64.RawURLEncoding
	return e.EncodeToString([]byte(`{"alg":"none"}`)) + "." + e.EncodeToString(b) + ".sig"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type envelope struct {
	Schema string          `json:"schema"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
}

func TestInitDryRunJSON(t *testing.T) {
	home, data := isolate(t)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"me@example.com"}}`)
	writeFile(t, filepath.Join(home, ".claude-work", ".claude.json"), `{"oauthAccount":{"emailAddress":"work@example.com"}}`)
	writeFile(t, filepath.Join(home, ".claude-empty", ".claude.json"), `{}`) // signed out
	idTok := fakeJWT(map[string]any{"email": "cx@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{"id_token":"`+idTok+`"}}`)
	writeFile(t, filepath.Join(home, ".local", "share", "kilo", "auth.json"), `{"kilo":{"type":"oauth","access":"tok"}}`)
	writeFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n    user: octocat\n    git_protocol: https\n")

	// One of them is already tracked.
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "claude-1", Provider: "claude", Auth: "local"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(data, "config.toml"))

	code, out, errOut := run(t, "init", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var env envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if env.Schema != "aitank/v1" || env.Kind != "detect" {
		t.Errorf("envelope = %s/%s", env.Schema, env.Kind)
	}
	var dets []struct {
		Provider   string `json:"provider"`
		Identity   string `json:"identity"`
		Plan       string `json:"plan"`
		ProfileDir string `json:"profile_dir"`
		Tracked    bool   `json:"tracked"`
	}
	if err := json.Unmarshal(env.Data, &dets); err != nil {
		t.Fatal(err)
	}
	type key struct{ prov, id string }
	got := map[key]bool{}
	for _, d := range dets {
		got[key{d.Provider, d.Identity}] = d.Tracked
	}
	want := map[key]bool{
		{"claude", "me@example.com"}:   true,
		{"claude", "work@example.com"}: false,
		{"codex", "cx@example.com"}:    false,
		{"kilo", ""}:                   false,
		{"copilot", "octocat"}:         false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detections = %v\nwant %v\nraw %s", got, want, env.Data)
	}
	for _, d := range dets {
		if d.Provider == "codex" && d.Plan != "ChatGPT Plus" {
			t.Errorf("codex plan = %q", d.Plan)
		}
		if d.Identity == "work@example.com" && d.ProfileDir != filepath.Join(home, ".claude-work") {
			t.Errorf("profile dir = %q", d.ProfileDir)
		}
	}
	after, _ := os.ReadFile(filepath.Join(data, "config.toml"))
	if !bytes.Equal(before, after) {
		t.Error("dry run changed the config")
	}
}

func TestListJSON(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{ID: "claude-1", Provider: "claude", Nickname: "personal", Auth: "local"},
		{ID: "deepseek-1", Provider: "deepseek", Nickname: "ds", Auth: "key"},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "list", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, errOut, out)
	}
	var env envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if env.Schema != "aitank/v1" || env.Kind != "list" {
		t.Errorf("envelope schema=%q kind=%q", env.Schema, env.Kind)
	}
	if !strings.Contains(out, `"schema": "aitank/v1"`) {
		t.Errorf("raw output lacks schema field: %s", out)
	}
	var data struct {
		Accounts []struct {
			ID      string   `json:"id"`
			Status  string   `json:"status"`
			LeftPct *float64 `json:"left_pct"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Accounts) != 2 {
		t.Fatalf("accounts = %+v", data.Accounts)
	}
	for _, a := range data.Accounts {
		if a.LeftPct != nil {
			t.Errorf("%s: unread account must have null left_pct, got %v", a.ID, *a.LeftPct)
		}
	}
}

func TestListJSONNoConfig(t *testing.T) {
	isolate(t)
	code, out, _ := run(t, "list", "--json")
	if code != 0 || !strings.Contains(out, `"schema": "aitank/v1"`) {
		t.Errorf("exit %d, out %s", code, out)
	}
}

func TestUsageErrorExitCode(t *testing.T) {
	isolate(t)
	if code, _, _ := run(t, "list", "extra-arg"); code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := run(t, "list", "--no-such-flag"); code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// buildBinary compiles cmd/aitank once per test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "aitank")
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/aitank")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func childEnv(home, data string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "AITANK_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "AITANK_HOME="+data, "AITANK_TEST_HOME="+home, "NO_COLOR=1")
}

const statusJSON = `{"session_id":"s1","model":{"id":"claude-fable","display_name":"Fable"},
 "rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1790000000},
                "seven_day":{"used_percentage":41.2,"resets_at":1790400000}}}`

func TestStatusBinary(t *testing.T) {
	bin := buildBinary(t)

	t.Run("empty cache is fast", func(t *testing.T) {
		home, data := t.TempDir(), t.TempDir()
		// Warm the page cache once so the measurement is of aitank, not the disk.
		warm := exec.Command(bin, "status")
		warm.Env = childEnv(home, data)
		warm.Stdin = strings.NewReader("{}")
		warm.Run()

		var best time.Duration
		var out []byte
		for i := 0; i < 3; i++ {
			cmd := exec.Command(bin, "status")
			cmd.Env = childEnv(home, data)
			cmd.Stdin = strings.NewReader(statusJSON)
			start := time.Now()
			o, err := cmd.Output()
			d := time.Since(start)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if i == 0 || d < best {
				best = d
			}
			out = o
		}
		t.Logf("aitank status wall time (best of 3): %v (target 50ms)", best)
		if best > 150*time.Millisecond {
			t.Errorf("status took %v, want under 150ms", best)
		}
		if !strings.Contains(string(out), "no Claude account tracked") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("statusline JSON is ingested", func(t *testing.T) {
		home, data := t.TempDir(), t.TempDir()
		t.Setenv("AITANK_HOME", data)
		t.Setenv("AITANK_TEST_HOME", home)
		cfg := config.Default()
		cfg.Accounts = []config.Account{{ID: "claude-1", Provider: "claude", Nickname: "personal", Auth: "local"}}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "status")
		cmd.Env = childEnv(home, data)
		cmd.Stdin = strings.NewReader(statusJSON)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(string(out), "personal") || !strings.Contains(string(out), "58") {
			t.Errorf("output = %q, want personal with ~58%% left", out)
		}
		b, err := os.ReadFile(filepath.Join(data, "cache.json"))
		if err != nil {
			t.Fatalf("cache not written: %v", err)
		}
		var cache struct {
			Accounts map[string]struct {
				LastGood *struct {
					Source  string `json:"source"`
					Windows []struct {
						Kind     string    `json:"kind"`
						UsedPct  *float64  `json:"used_pct"`
						ResetsAt time.Time `json:"resets_at"`
					} `json:"windows"`
				} `json:"last_good"`
				LastStatus string `json:"last_status"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(b, &cache); err != nil {
			t.Fatal(err)
		}
		e, ok := cache.Accounts["claude-1"]
		if !ok || e.LastGood == nil {
			t.Fatalf("no entry for claude-1: %s", b)
		}
		if e.LastGood.Source != "claude code statusline" || e.LastStatus != "ok" || len(e.LastGood.Windows) != 2 {
			t.Errorf("entry = %+v", e)
		}
		w := e.LastGood.Windows[0]
		if w.Kind != "five_hour" || w.UsedPct == nil || *w.UsedPct != 23.5 || w.ResetsAt.Unix() != 1790000000 {
			t.Errorf("five_hour = %+v", w)
		}
		if _, err := os.Stat(filepath.Join(data, "history.jsonl")); err != nil {
			t.Errorf("history not appended: %v", err)
		}

		// A session in another profile does not touch this account.
		other := exec.Command(bin, "status")
		other.Env = append(childEnv(home, data), "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude-x"))
		other.Stdin = strings.NewReader(strings.ReplaceAll(statusJSON, "23.5", "77.7"))
		if _, err := other.Output(); err != nil {
			t.Fatal(err)
		}
		b2, _ := os.ReadFile(filepath.Join(data, "cache.json"))
		if strings.Contains(string(b2), "77.7") {
			t.Error("reading from an untracked profile was stored")
		}
	})
}
