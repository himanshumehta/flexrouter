// Package paths locates aitank's own files and writes them with private
// permissions (FR-15.5).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Home returns the user's home directory. AITANK_TEST_HOME overrides it so tests
// never touch real vendor files.
func Home() string {
	if h := os.Getenv("AITANK_TEST_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Dir is aitank's data directory: ~/Library/Application Support/aitank on
// macOS. AITANK_HOME overrides it.
func Dir() string {
	if d := os.Getenv("AITANK_HOME"); d != "" {
		return d
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(Home(), "Library", "Application Support", "aitank")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "aitank")
	}
	return filepath.Join(Home(), ".local", "share", "aitank")
}

func Config() string   { return filepath.Join(Dir(), "config.toml") }
func Cache() string    { return filepath.Join(Dir(), "cache.json") }
func History() string  { return filepath.Join(Dir(), "history.jsonl") }
func ReadLog() string  { return filepath.Join(Dir(), "reads.jsonl") }
func State() string    { return filepath.Join(Dir(), "state.json") }
func Lock() string     { return filepath.Join(Dir(), ".lock") }
func Profiles() string { return filepath.Join(Dir(), "profiles") }
func Backups() string  { return filepath.Join(Dir(), "backups") }
func AgentLog() string { return filepath.Join(Dir(), "agent.log") }

// Profile is the folder aitank creates for one Claude or Codex account.
func Profile(accountID string) string { return filepath.Join(Profiles(), accountID) }

// EnsureDir creates aitank's directory with 0700.
func EnsureDir() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	return os.Chmod(Dir(), 0o700)
}

// WriteFile writes data atomically with 0600 permissions.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// AppendFile appends a line to a 0600 file.
func AppendFile(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// WithLock runs fn while holding an exclusive lock on aitank's lock file, so
// the agent, `aitank status` and interactive commands never interleave
// read-modify-write cycles on the cache.
func WithLock(fn func() error) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	f, err := os.OpenFile(Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// Backup copies path into aitank's backups folder and returns the copy's path.
func Backup(path, label string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(Backups(), fmt.Sprintf("%s-%s", label, time.Now().UTC().Format("20060102T150405.000Z")))
	if err := os.MkdirAll(Backups(), 0o700); err != nil {
		return "", err
	}
	return dst, os.WriteFile(dst, data, 0o600)
}
