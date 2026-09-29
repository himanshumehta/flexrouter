// Package daemon manages the per-user launchd agent (FR-6.1, FR-6.2). The
// agent runs `aitank daemon run` every refresh interval; each run refreshes
// all accounts, evaluates alerts and exits.
package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
)

// Label is the launchd job label.
const Label = "com.aitank.agent"

// PlistPath is ~/Library/LaunchAgents/com.aitank.agent.plist.
func PlistPath() string {
	return filepath.Join(paths.Home(), "Library", "LaunchAgents", Label+".plist")
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist renders the agent definition.
func Plist(binary string, interval time.Duration) string {
	secs := int(interval.Seconds())
	if secs < 60 {
		secs = 60
	}
	path := strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin", filepath.Join(paths.Home(), ".local", "bin")}, ":")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>run</string>
	</array>
	<key>StartInterval</key>
	<integer>%d</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, Label, esc(binary), secs, esc(path), esc(paths.AgentLog()), esc(paths.AgentLog()))
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// Install writes the plist and (re)loads the agent.
func Install(binary string, interval time.Duration) error {
	if err := paths.EnsureDir(); err != nil {
		return err
	}
	p := PlistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", domain()+"/"+Label).Run()
	if err := os.WriteFile(p, []byte(Plist(binary, interval)), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain(), p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall unloads the agent and removes its plist.
func Uninstall() error {
	_ = exec.Command("launchctl", "bootout", domain()+"/"+Label).Run()
	err := os.Remove(PlistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Status describes the agent.
type Status struct {
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Binary    string `json:"binary,omitempty"`
	LastExit  string `json:"last_exit,omitempty"`
	Interval  string `json:"interval,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// GetStatus inspects the plist and launchctl.
func GetStatus() Status {
	var s Status
	data, err := os.ReadFile(PlistPath())
	if err != nil {
		return s
	}
	s.Installed = true
	txt := string(data)
	if i := strings.Index(txt, "<array>"); i >= 0 {
		rest := txt[i:]
		if a := strings.Index(rest, "<string>"); a >= 0 {
			if b := strings.Index(rest[a:], "</string>"); b >= 0 {
				s.Binary = rest[a+8 : a+b]
			}
		}
	}
	if i := strings.Index(txt, "<integer>"); i >= 0 {
		if j := strings.Index(txt[i:], "</integer>"); j >= 0 {
			if n, err := strconv.Atoi(txt[i+9 : i+j]); err == nil {
				s.Interval = (time.Duration(n) * time.Second).String()
			}
		}
	}
	out, err := exec.Command("launchctl", "print", domain()+"/"+Label).CombinedOutput()
	if err != nil {
		s.Detail = "not loaded in launchd"
		return s
	}
	s.Loaded = true
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "last exit code") {
			s.LastExit = strings.TrimSpace(strings.TrimPrefix(line, "last exit code ="))
		}
	}
	return s
}

// Logs returns the last n lines of the agent log.
func Logs(n int) (string, error) {
	data, err := os.ReadFile(paths.AgentLog())
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// TrimLog keeps the agent log under 1 MB.
func TrimLog() {
	fi, err := os.Stat(paths.AgentLog())
	if err != nil || fi.Size() < 1<<20 {
		return
	}
	if s, err := Logs(2000); err == nil {
		_ = paths.WriteFile(paths.AgentLog(), []byte(s+"\n"))
	}
}
