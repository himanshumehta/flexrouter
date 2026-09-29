// Package launch starts the user's own vendor CLI in an account's profile
// folder (FR-11). It only ever sets the folder path; it never reads, copies,
// exports or renews a sign-in token (FR-11.6).
package launch

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

// Spec is what to run for an account.
type Spec struct {
	Binary string // command name, e.g. "claude"
	EnvVar string // e.g. CLAUDE_CONFIG_DIR
	Dir    string // profile folder; empty means the vendor default
}

// For returns the launch spec for an account, or an error when its provider
// cannot launch.
func For(acct *config.Account) (Spec, error) {
	p, ok := provider.Get(acct.Provider)
	if !ok {
		return Spec{}, fmt.Errorf("unknown provider %q", acct.Provider)
	}
	sw, ok := p.(provider.Switcher)
	if !ok {
		return Spec{}, fmt.Errorf("%s accounts cannot be launched from aitank", p.Name())
	}
	bin, env, dir := sw.LaunchSpec(acct)
	return Spec{Binary: bin, EnvVar: env, Dir: dir}, nil
}

// Command is the one-line shell command for a spec (FR-11.4). It contains
// only the folder path.
func (s Spec) Command(extra []string) string {
	var b strings.Builder
	if s.Dir != "" {
		b.WriteString(s.EnvVar + "=" + ShellQuote(s.Dir) + " ")
	} else {
		// The default profile: make sure an inherited override is cleared.
		b.WriteString("env -u " + s.EnvVar + " ")
	}
	b.WriteString(s.Binary)
	for _, a := range extra {
		b.WriteString(" " + ShellQuote(a))
	}
	return b.String()
}

// Environ returns the process environment for the vendor CLI.
func (s Spec) Environ(base []string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, s.EnvVar+"=") {
			continue
		}
		out = append(out, kv)
	}
	if s.Dir != "" {
		out = append(out, s.EnvVar+"="+s.Dir)
	}
	sort.Strings(out)
	return out
}

// Exec replaces the current process with the vendor CLI, passing extra
// arguments through (FR-11.7).
func (s Spec) Exec(extra []string) error {
	path, err := provider.LookPath(s.Binary)
	if err != nil {
		return fmt.Errorf("%s is not installed or not on PATH", s.Binary)
	}
	if s.Dir != "" {
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return err
		}
	}
	argv := append([]string{s.Binary}, extra...)
	env := s.Environ(os.Environ())
	return syscall.Exec(path, argv, env)
}

// ShellQuote quotes s for POSIX shells when needed.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Copy puts text on the macOS clipboard with pbcopy.
func Copy(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy failed (macOS only): %v", err)
	}
	return nil
}
