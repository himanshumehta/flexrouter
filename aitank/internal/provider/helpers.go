package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/secrets"
)

// UserAgent is sent on every request aitank makes.
var UserAgent = "aitank/dev"

// extraDirs are searched after PATH, because the launchd agent starts with
// a minimal PATH and vendor CLIs usually live in these places.
func extraDirs() []string {
	h := paths.Home()
	return []string{
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(h, ".local", "bin"),
		filepath.Join(h, ".claude", "local"),
		filepath.Join(h, ".npm-global", "bin"),
		filepath.Join(h, ".bun", "bin"),
		filepath.Join(h, ".volta", "bin"),
		filepath.Join(h, "bin"),
		"/Applications/Cursor.app/Contents/Resources/app/bin",
	}
}

// LookPath finds a binary on PATH or in the usual install folders.
func LookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range extraDirs() {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Key fetches an account's pasted key from the Keychain.
func (e *Env) Key(acct *config.Account) (string, error) {
	k, err := e.Secrets.Get(acct.ID)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", Errf(model.StatusAuthNeeded, "no key stored; run `aitank add %s` again", acct.Provider)
	}
	if err != nil {
		return "", &Error{Status: model.StatusAuthNeeded, Err: err}
	}
	return k, nil
}

// Request describes one HTTP call.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    io.Reader
}

// JSON performs a request and decodes a JSON response into out. HTTP errors
// map to statuses: 401/403 → auth needed, 429 → rate-limited (with
// Retry-After), others → error.
func (e *Env) JSON(ctx context.Context, r Request, out any) error {
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, r.Body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return &Error{Status: model.StatusError, Err: fmt.Errorf("%s: network error: %v", req.URL.Host, errors.Unwrap(err))}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &Error{Status: model.StatusAuthNeeded, Err: fmt.Errorf("%s: HTTP %d (sign-in or key rejected)", req.URL.Host, resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return &Error{Status: model.StatusRateLimited, Err: fmt.Errorf("%s: HTTP 429 rate limited", req.URL.Host), RetryAfter: RetryAfter(resp.Header.Get("Retry-After"), e.Now())}
	case resp.StatusCode >= 300:
		return &Error{Status: model.StatusError, Err: fmt.Errorf("%s: HTTP %d: %s", req.URL.Host, resp.StatusCode, snippet(body))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Status: model.StatusUnsupported, Err: fmt.Errorf("%s: unexpected response shape: %v", req.URL.Host, err)}
	}
	return nil
}

// RetryAfter parses a Retry-After header (seconds or HTTP date).
func RetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion pulls the first x.y.z out of a version string.
func ParseVersion(s string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// CompareVersions returns -1, 0 or 1.
func CompareVersions(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// GateResult is the outcome of a version gate.
type GateResult struct {
	Version  string
	Untested bool // newer than the newest version the reader was checked against
}

// Gate runs `<tool> --version` and checks it against the provider's range.
// Below MinVersion the reader refuses (unsupported). Above TestedMax it runs
// but adds a note, and refuses only when the reading fails to parse. With
// allow_untested_vendor_versions the minimum is not enforced either.
func (e *Env) Gate(ctx context.Context, caps Capabilities, bin string) (GateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := e.Run(ctx, nil, bin, "--version")
	if err != nil {
		return GateResult{}, &Error{Status: model.StatusToolMissing, Err: fmt.Errorf("%s --version failed: %v", caps.VendorTool, err)}
	}
	v, ok := ParseVersion(string(out))
	if !ok {
		return GateResult{}, Errf(model.StatusUnsupported, "could not parse %s version from %q", caps.VendorTool, snippet(out))
	}
	res := GateResult{Version: fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])}
	e.VersionSeen = res.Version
	allow := e.Config != nil && e.Config.AllowUntested
	if minV, ok := ParseVersion(caps.MinVersion); ok && CompareVersions(v, minV) < 0 && !allow {
		return res, Errf(model.StatusUnsupported, "%s %s is older than %s, the oldest version this reader supports; update it or set allow_untested_vendor_versions = true", caps.VendorTool, res.Version, caps.MinVersion)
	}
	if maxV, ok := ParseVersion(caps.TestedMax); ok && CompareVersions(v, maxV) > 0 {
		res.Untested = true
	}
	return res, nil
}

// ExpandHome turns a leading ~ into the home folder.
func (e *Env) ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(e.Home, p[2:])
	}
	return p
}

// ParseTime accepts RFC 3339 strings, unix seconds or unix milliseconds.
func ParseTime(v any) *time.Time {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if p, err := time.Parse(layout, t); err == nil {
				return &p
			}
		}
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return ParseTime(n)
		}
	case float64:
		if t <= 0 {
			return nil
		}
		if t > 1e12 {
			p := time.UnixMilli(int64(t)).UTC()
			return &p
		}
		p := time.Unix(int64(t), 0).UTC()
		return &p
	case int64:
		return ParseTime(float64(t))
	case json.Number:
		f, err := t.Float64()
		if err == nil {
			return ParseTime(f)
		}
	}
	return nil
}

// JWTClaims decodes a JWT's payload without verifying it. aitank uses this
// only to read identity claims (email, user id) from a sign-in the vendor
// tool already stores locally; the token itself is never shown or stored.
func JWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	data, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil
	}
	return claims
}

// Str digs a string out of nested maps: Str(m, "a", "b").
func Str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// Num reads a number that may be encoded as a JSON number or a string.
func Num(v any) *float64 {
	switch t := v.(type) {
	case float64:
		return &t
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return &f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return &f
		}
	case int:
		f := float64(t)
		return &f
	}
	return nil
}

// ReadJSONFile reads a local JSON file into a generic map.
func ReadJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
