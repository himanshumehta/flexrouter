// Package config is aitank's single TOML file (FR-17.1). It holds settings,
// tracked accounts and bills. It never holds secrets: pasted keys live in the
// macOS Keychain (FR-15.1).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
)

// Duration is a time.Duration that reads and writes as "5m".
type Duration struct{ time.Duration }

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}
func (d Duration) String() string {
	s := d.Duration.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

type Colors struct {
	LeftAmber float64 `toml:"left_amber"` // overall left: amber at or below (FR-9.3)
	LeftRed   float64 `toml:"left_red"`   // overall left: red at or below
	BarAmber  float64 `toml:"bar_amber"`  // window bar: amber at % used
	BarRed    float64 `toml:"bar_red"`    // window bar: red at % used
}

type Alerts struct {
	DefaultAt      float64 `toml:"default_at"`       // % left threshold used by `alerts on` without --at
	Sound          bool    `toml:"sound"`            // play a sound with notifications
	Bell           bool    `toml:"bell"`             // ring the terminal bell in `watch`
	Forecast       bool    `toml:"forecast"`         // alert on forecast exhaustion under 60 min
	Refilled       bool    `toml:"refilled"`         // alert when a window refills
	BillDaysBefore int     `toml:"bill_days_before"` // renewal reminder lead time (FR-14.3)
}

type Update struct {
	Policy string `toml:"policy"` // auto | notify | off (FR-19.2)
	URL    string `toml:"url"`    // releases API URL
}

type Recommend struct {
	IgnoreModelLimits bool    `toml:"ignore_model_limits"` // FR-7.3
	CloseMargin       float64 `toml:"close_margin"`        // points within which accounts count as close (FR-7.4)
}

// AccountAlerts are per-account alert settings (FR-13.1). Alerts are off
// unless Enabled.
type AccountAlerts struct {
	Enabled bool    `toml:"enabled"`
	At      float64 `toml:"at"`
}

// Account is one tracked account.
type Account struct {
	ID       string `toml:"id"`
	Provider string `toml:"provider"`
	Nickname string `toml:"nickname"`
	Plan     string `toml:"plan,omitempty"`
	Identity string `toml:"identity,omitempty"` // email/username seen locally
	// Auth is how the account is read: "local" (existing vendor sign-in),
	// "key" (pasted key in Keychain) or "device" (device-code sign-in whose
	// token aitank stores in Keychain).
	Auth    string `toml:"auth"`
	KeyHint string `toml:"key_hint,omitempty"` // masked, e.g. sk-…a1f3 (FR-15.2)
	// ProfileDir is the Claude/Codex config folder the account is bound to
	// (FR-11.1). Empty means the vendor default (~/.claude, ~/.codex).
	ProfileDir     string            `toml:"profile_dir,omitempty"`
	ProfileCreated bool              `toml:"profile_created,omitempty"` // aitank created ProfileDir (FR-3.5)
	Paused         bool              `toml:"paused,omitempty"`
	QuietUntil     time.Time         `toml:"quiet_until,omitempty"`
	Alerts         AccountAlerts     `toml:"alerts"`
	Options        map[string]string `toml:"options,omitempty"`
	AddedAt        time.Time         `toml:"added_at"`
}

// Label is "provider nickname" (matches launch command format: aitank claude work).
func (a *Account) Label() string {
	if a.Nickname != "" {
		return a.Provider + " " + a.Nickname
	}
	return a.Provider + " " + a.ID
}

// Bill is a manually entered subscription (FR-14.1).
type Bill struct {
	ID       string    `toml:"id"`
	Name     string    `toml:"name"`
	Price    float64   `toml:"price"`
	Currency string    `toml:"currency"`
	Cycle    string    `toml:"cycle"` // weekly | monthly | quarterly | yearly
	Renewal  time.Time `toml:"renewal"`
}

type Config struct {
	RefreshInterval Duration  `toml:"refresh_interval"` // FR-6.1
	StaleAfter      Duration  `toml:"stale_after"`      // FR-6.6
	ForecastWindow  Duration  `toml:"forecast_window"`  // FR-8.2 pace window
	ForecastWarn    Duration  `toml:"forecast_warn"`    // FR-8.3 warn threshold
	AllowUntested   bool      `toml:"allow_untested_vendor_versions"`
	Colors          Colors    `toml:"colors"`
	Recommend       Recommend `toml:"recommend"`
	Alerts          Alerts    `toml:"alerts"`
	Update          Update    `toml:"update"`
	Accounts        []Account `toml:"accounts"`
	Bills           []Bill    `toml:"bills"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		RefreshInterval: Duration{5 * time.Minute},
		StaleAfter:      Duration{15 * time.Minute},
		ForecastWindow:  Duration{60 * time.Minute},
		ForecastWarn:    Duration{60 * time.Minute},
		Colors:          Colors{LeftAmber: 40, LeftRed: 10, BarAmber: 60, BarRed: 90},
		Recommend:       Recommend{CloseMargin: 5},
		Alerts:          Alerts{DefaultAt: 20, Sound: true, Forecast: true, Refilled: true, BillDaysBefore: 3},
		Update:          Update{Policy: "notify", URL: "https://api.github.com/repos/himanshumehta/flexrouter/releases"},
	}
}

// Load reads the config file, or returns defaults when it does not exist.
func Load() (*Config, error) {
	c := Default()
	data, err := os.ReadFile(paths.Config())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := Decode(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", paths.Config(), err)
	}
	return c, nil
}

// Decode parses TOML into c, keeping c's defaults for missing keys.
func Decode(data []byte, c *Config) error {
	_, err := toml.Decode(string(data), c)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Validate rejects values that would break the tool.
func (c *Config) Validate() error {
	if c.RefreshInterval.Duration < time.Minute {
		return fmt.Errorf("refresh_interval must be at least 1m")
	}
	if c.StaleAfter.Duration <= 0 {
		return fmt.Errorf("stale_after must be positive")
	}
	switch c.Update.Policy {
	case "auto", "notify", "off":
	default:
		return fmt.Errorf("update.policy must be auto, notify or off")
	}
	seen := map[string]bool{}
	for _, a := range c.Accounts {
		if a.ID == "" || seen[a.ID] {
			return fmt.Errorf("duplicate or empty account id %q", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// Encode renders c as TOML.
func (c *Config) Encode() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# aitank configuration. Edit directly or use `aitank config set`.\n# Secrets are never stored here; pasted keys live in the macOS Keychain.\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Save writes the config with 0600 permissions.
func (c *Config) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := c.Encode()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(); err != nil {
		return err
	}
	return paths.WriteFile(paths.Config(), data)
}

// Account finds an account by id, nickname or provider/nickname.
func (c *Config) Account(ref string) (*Account, error) {
	var matches []*Account
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.ID == ref || a.Label() == ref {
			return a, nil
		}
		if a.Nickname != "" && a.Nickname == ref {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no account %q (see `aitank accounts`)", ref)
	default:
		return nil, fmt.Errorf("%q matches several accounts; use the id or provider/nickname", ref)
	}
}

// NewAccountID returns a short id like "claude-2".
func (c *Config) NewAccountID(provider string) string {
	for n := 1; ; n++ {
		id := provider + "-" + strconv.Itoa(n)
		if _, err := c.Account(id); err != nil {
			return id
		}
	}
}

// RemoveAccount drops an account from the config.
func (c *Config) RemoveAccount(id string) {
	out := c.Accounts[:0]
	for _, a := range c.Accounts {
		if a.ID != id {
			out = append(out, a)
		}
	}
	c.Accounts = out
}

// Keys lists the settable scalar keys for `aitank config get|set`.
func Keys() []string {
	keys := make([]string, 0, len(settable))
	for k := range settable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type field struct {
	get func(c *Config) string
	set func(c *Config, v string) error
}

func durField(p func(c *Config) *Duration) field {
	return field{
		get: func(c *Config) string { return p(c).String() },
		set: func(c *Config, v string) error { return p(c).UnmarshalText([]byte(v)) },
	}
}

func numField(p func(c *Config) *float64) field {
	return field{
		get: func(c *Config) string { return strconv.FormatFloat(*p(c), 'f', -1, 64) },
		set: func(c *Config, v string) error {
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
			if err != nil {
				return err
			}
			*p(c) = f
			return nil
		},
	}
}

func boolField(p func(c *Config) *bool) field {
	return field{
		get: func(c *Config) string { return strconv.FormatBool(*p(c)) },
		set: func(c *Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			*p(c) = b
			return nil
		},
	}
}

var settable = map[string]field{
	"refresh_interval":               durField(func(c *Config) *Duration { return &c.RefreshInterval }),
	"stale_after":                    durField(func(c *Config) *Duration { return &c.StaleAfter }),
	"forecast_window":                durField(func(c *Config) *Duration { return &c.ForecastWindow }),
	"forecast_warn":                  durField(func(c *Config) *Duration { return &c.ForecastWarn }),
	"allow_untested_vendor_versions": boolField(func(c *Config) *bool { return &c.AllowUntested }),
	"colors.left_amber":              numField(func(c *Config) *float64 { return &c.Colors.LeftAmber }),
	"colors.left_red":                numField(func(c *Config) *float64 { return &c.Colors.LeftRed }),
	"colors.bar_amber":               numField(func(c *Config) *float64 { return &c.Colors.BarAmber }),
	"colors.bar_red":                 numField(func(c *Config) *float64 { return &c.Colors.BarRed }),
	"recommend.ignore_model_limits":  boolField(func(c *Config) *bool { return &c.Recommend.IgnoreModelLimits }),
	"recommend.close_margin":         numField(func(c *Config) *float64 { return &c.Recommend.CloseMargin }),
	"alerts.default_at":              numField(func(c *Config) *float64 { return &c.Alerts.DefaultAt }),
	"alerts.sound":                   boolField(func(c *Config) *bool { return &c.Alerts.Sound }),
	"alerts.bell":                    boolField(func(c *Config) *bool { return &c.Alerts.Bell }),
	"alerts.forecast":                boolField(func(c *Config) *bool { return &c.Alerts.Forecast }),
	"alerts.refilled":                boolField(func(c *Config) *bool { return &c.Alerts.Refilled }),
	"alerts.bill_days_before": {
		get: func(c *Config) string { return strconv.Itoa(c.Alerts.BillDaysBefore) },
		set: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("want a whole number of days")
			}
			c.Alerts.BillDaysBefore = n
			return nil
		},
	},
	"update.policy": {
		get: func(c *Config) string { return c.Update.Policy },
		set: func(c *Config, v string) error { c.Update.Policy = v; return nil },
	},
	"update.url": {
		get: func(c *Config) string { return c.Update.URL },
		set: func(c *Config, v string) error { c.Update.URL = v; return nil },
	},
}

// Get returns a setting's value as text.
func (c *Config) Get(key string) (string, error) {
	f, ok := settable[key]
	if !ok {
		return "", fmt.Errorf("unknown key %q; known keys: %s", key, strings.Join(Keys(), ", "))
	}
	return f.get(c), nil
}

// Set changes a setting and validates the result.
func (c *Config) Set(key, value string) error {
	f, ok := settable[key]
	if !ok {
		return fmt.Errorf("unknown key %q; known keys: %s", key, strings.Join(Keys(), ", "))
	}
	next := *c
	if err := f.set(&next, value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}
