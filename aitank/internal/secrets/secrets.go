// Package secrets stores pasted API keys and device-code tokens in the macOS
// Keychain and nowhere else (FR-15.1).
package secrets

import (
	"errors"
	"sync"
)

// Service is the Keychain service name every aitank item uses.
const Service = "aitank"

// ErrNotFound means no secret is stored for the account.
var ErrNotFound = errors.New("no key stored in Keychain for this account")

// ErrUnsupported means this platform has no supported secret store.
var ErrUnsupported = errors.New("secret storage needs the macOS Keychain")

// Store is where secrets live.
type Store interface {
	Get(account string) (string, error)
	Set(account, label, secret string) error
	Delete(account string) error
	// Check reports whether the store is usable (for `aitank doctor`).
	Check() error
}

var (
	mu      sync.Mutex
	current Store = platformStore()
)

// Default returns the active store.
func Default() Store {
	mu.Lock()
	defer mu.Unlock()
	return current
}

// SetDefault swaps the store; tests use a Memory store.
func SetDefault(s Store) {
	mu.Lock()
	defer mu.Unlock()
	current = s
}

// Memory is an in-process store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (s *Memory) Get(a string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[a]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (s *Memory) Set(a, _, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[a] = v
	return nil
}
func (s *Memory) Delete(a string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, a)
	return nil
}
func (s *Memory) Check() error { return nil }
