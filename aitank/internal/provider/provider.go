// Package provider defines the pluggable provider interface (FR-4.11) and
// the registry the core engine uses. Providers register themselves from
// their own packages, so adding one needs no core change (FR-4.12).
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/secrets"
)

// Family groups providers for "use next" (FR-7.1).
type Family string

const (
	FamilyClaude Family = "claude"
	FamilyCodex  Family = "codex"
	FamilyOther  Family = "other"
)

// AuthMethod is a way to add an account (FR-3.2).
type AuthMethod string

const (
	AuthLocal  AuthMethod = "local"  // existing vendor sign-in, read in place
	AuthKey    AuthMethod = "key"    // pasted key, stored in Keychain
	AuthDevice AuthMethod = "device" // device-code sign-in
)

// Capabilities describe a provider for the engine, `add`, and `privacy`.
type Capabilities struct {
	Family       Family
	Auth         []AuthMethod
	Windows      bool     // reports usage windows
	Balance      bool     // reports a balance or spend
	Launch       bool     // `aitank <provider> [id]` can start the vendor CLI
	Profiles     bool     // each account binds to its own profile folder
	Experimental bool     // shown with an "experimental" tag
	FilesRead    []string // every local file or store read (FR-16.3), ~ for home
	Endpoints    []string // every endpoint contacted (FR-15.4, FR-16.3)
	Commands     []string // vendor commands run
	KeyHelp      string   // what key to paste, for `add`
	// VendorTool is the CLI whose version gates the reader, if any.
	VendorTool string
	// MinVersion and TestedMax bound the vendor versions this reader was
	// written against (version gate for undocumented interfaces).
	MinVersion string
	TestedMax  string
}

// Detection is one signed-in tool found on disk (FR-2.3).
type Detection struct {
	Provider   string
	Identity   string // email or username, when stored locally
	Plan       string
	ProfileDir string // vendor config folder the sign-in lives in
	Source     string // what was read, for display
	Note       string
}

// Provider is one vendor module.
type Provider interface {
	ID() string
	Name() string
	Capabilities() Capabilities
	// Detect looks for local sign-ins. It must be read-only: no network, no
	// vendor processes, no prompts (FR-2.2).
	Detect(env *Env) []Detection
	// Read fetches the account's current usage.
	Read(ctx context.Context, env *Env, acct *config.Account) (*model.Reading, error)
}

// Switcher is optionally implemented by providers that can launch the
// vendor CLI in an account's profile folder (FR-4.11 switch()).
type Switcher interface {
	// LaunchSpec returns the binary to run and the environment variable that
	// points it at the account's profile folder.
	LaunchSpec(acct *config.Account) (binary string, envVar string, dir string)
}

// DeviceLogin is optionally implemented by providers with a device-code flow.
type DeviceLogin interface {
	DeviceLogin(ctx context.Context, env *Env, prompt func(url, code string)) (token string, identity string, err error)
}

// Env is everything a provider may touch. Tests replace its parts.
type Env struct {
	Home    string
	HTTP    *http.Client
	Secrets secrets.Store
	Now     func() time.Time
	// Run runs a vendor command and returns its stdout.
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// LookPath finds a vendor binary.
	LookPath func(name string) (string, error)
	// Getenv reads the process environment.
	Getenv func(string) string
	// Config is the loaded configuration (for version-gate overrides).
	Config *config.Config
	// VersionSeen records the vendor tool version a read ran against.
	VersionSeen string
}

// DefaultEnv is the real environment.
func DefaultEnv(cfg *config.Config) *Env {
	return &Env{
		Home:    paths.Home(),
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Secrets: secrets.Default(),
		Now:     time.Now,
		Run: func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = append(os.Environ(), env...)
			return cmd.Output()
		},
		LookPath: LookPath,
		Getenv:   os.Getenv,
		Config:   cfg,
	}
}

// Error is a read failure with the status to show (FR-18.1).
type Error struct {
	Status     model.Status
	Err        error
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Errf builds an Error.
func Errf(status model.Status, format string, a ...any) *Error {
	return &Error{Status: status, Err: fmt.Errorf(format, a...)}
}

// StatusOf maps any error to a status.
func StatusOf(err error) (model.Status, time.Duration) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Status, pe.RetryAfter
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return model.StatusToolMissing, 0
	}
	return model.StatusError, 0
}

var (
	regMu    sync.RWMutex
	registry = map[string]Provider{}
)

// Register adds a provider. Called from provider packages' init.
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[p.ID()]; dup {
		panic("provider registered twice: " + p.ID())
	}
	registry[p.ID()] = p
}

// Get returns a provider by id.
func Get(id string) (Provider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := registry[id]
	return p, ok
}

// All returns providers sorted by id.
func All() []Provider {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Provider, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// FamilyOf returns an account's provider family.
func FamilyOf(providerID string) Family {
	if p, ok := Get(providerID); ok {
		return p.Capabilities().Family
	}
	return FamilyOther
}
