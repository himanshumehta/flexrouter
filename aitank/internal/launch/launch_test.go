package launch

import (
	"strings"
	"testing"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/claude"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/codex"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/deepseek"
)

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"/Users/x/.claude-work", "/Users/x/.claude-work"},
		{"", "''"},
		{"/Users/x/Application Support/p", "'/Users/x/Application Support/p'"},
		{"it's", `'it'\''s'`},
		{"$HOME", "'$HOME'"},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{"--flag=v", "--flag=v"},
	}
	for _, tc := range tests {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestSpecCommand(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		extra []string
		want  string
	}{
		{"profile", Spec{"claude", "CLAUDE_CONFIG_DIR", "/Users/x/.claude-work"}, nil, "CLAUDE_CONFIG_DIR=/Users/x/.claude-work claude"},
		{"space in path", Spec{"claude", "CLAUDE_CONFIG_DIR", "/Users/x/Library/Application Support/aitank/profiles/claude-2"}, nil,
			"CLAUDE_CONFIG_DIR='/Users/x/Library/Application Support/aitank/profiles/claude-2' claude"},
		{"default profile", Spec{"codex", "CODEX_HOME", ""}, nil, "env -u CODEX_HOME codex"},
		{"extra args", Spec{"claude", "CLAUDE_CONFIG_DIR", "/p"}, []string{"--resume", "fix the bug"}, "CLAUDE_CONFIG_DIR=/p claude --resume 'fix the bug'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Command(tc.extra); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSpecEnviron(t *testing.T) {
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/inherited", "HOME=/Users/x", "CLAUDE_CONFIG_DIR_EXTRA=keep"}
	tests := []struct {
		name string
		spec Spec
		want []string
	}{
		{"default drops inherited", Spec{"claude", "CLAUDE_CONFIG_DIR", ""},
			[]string{"CLAUDE_CONFIG_DIR_EXTRA=keep", "HOME=/Users/x", "PATH=/bin"}},
		{"profile replaces inherited", Spec{"claude", "CLAUDE_CONFIG_DIR", "/p w"},
			[]string{"CLAUDE_CONFIG_DIR=/p w", "CLAUDE_CONFIG_DIR_EXTRA=keep", "HOME=/Users/x", "PATH=/bin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec.Environ(base)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
	if len(base) != 4 || base[1] != "CLAUDE_CONFIG_DIR=/inherited" {
		t.Error("base slice mutated")
	}
}

func TestFor(t *testing.T) {
	s, err := For(&config.Account{ID: "c", Provider: "claude", ProfileDir: "/p"})
	if err != nil || s != (Spec{"claude", "CLAUDE_CONFIG_DIR", "/p"}) {
		t.Errorf("claude: %+v %v", s, err)
	}
	s, err = For(&config.Account{ID: "x", Provider: "codex"})
	if err != nil || s != (Spec{"codex", "CODEX_HOME", ""}) {
		t.Errorf("codex: %+v %v", s, err)
	}
	if _, err := For(&config.Account{Provider: "deepseek"}); err == nil {
		t.Error("deepseek should not be launchable")
	}
	if _, err := For(&config.Account{Provider: "nope"}); err == nil {
		t.Error("unknown provider should fail")
	}
}
