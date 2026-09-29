// Package update checks for, downloads and installs new aitank releases
// (FR-19). It is the only request aitank makes to its own project, and it
// can be turned off (update.policy = "off").
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

// PublicKey is the base64 ed25519 key that signs checksums.txt, set at
// release time with -ldflags. Without it, `aitank update` only reports new
// versions and never installs one.
var PublicKey = ""

// TagPrefix marks aitank releases in the repository's release list.
const TagPrefix = "aitank-v"

// Release is one published release.
type Release struct {
	Tag     string  `json:"tag_name"`
	Name    string  `json:"name"`
	Body    string  `json:"body"`
	Draft   bool    `json:"draft"`
	Pre     bool    `json:"prerelease"`
	Assets  []Asset `json:"assets"`
	Version string  `json:"-"`
}

// Asset is a release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

var client = &http.Client{Timeout: 30 * time.Second}

// Latest returns the newest non-draft, non-prerelease aitank release.
func Latest(ctx context.Context, url string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", provider.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("release check: HTTP %d", resp.StatusCode)
	}
	var rs []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rs); err != nil {
		return nil, err
	}
	var best *Release
	for i := range rs {
		r := &rs[i]
		if r.Draft || r.Pre || !strings.HasPrefix(r.Tag, TagPrefix) {
			continue
		}
		r.Version = strings.TrimPrefix(r.Tag, TagPrefix)
		if best == nil || Newer(r.Version, best.Version) {
			best = r
		}
	}
	if best == nil {
		return nil, errors.New("no aitank releases published yet")
	}
	return best, nil
}

// Newer reports whether version a is newer than b.
func Newer(a, b string) bool {
	va, ok1 := provider.ParseVersion(a)
	vb, ok2 := provider.ParseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	return provider.CompareVersions(va, vb) > 0
}

// BrewInstalled reports whether the running binary came from Homebrew.
func BrewInstalled() bool {
	p, err := os.Executable()
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return strings.Contains(p, "/Cellar/") || strings.Contains(p, "/homebrew/")
}

// Install downloads rel, verifies checksums.txt against its ed25519
// signature and the archive against checksums.txt, checks the macOS code
// signature, and replaces the running binary.
func Install(ctx context.Context, rel *Release) (string, error) {
	if BrewInstalled() {
		return "", errors.New("aitank was installed with Homebrew; run `brew upgrade aitank`")
	}
	if PublicKey == "" {
		return "", errors.New("this build has no release-signing key, so it cannot verify downloads; install the new version from the releases page or Homebrew")
	}
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("embedded release key is invalid")
	}
	archive := fmt.Sprintf("aitank_%s_darwin_all.tar.gz", rel.Version)
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	for _, n := range []string{archive, "checksums.txt", "checksums.txt.sig"} {
		if urls[n] == "" {
			return "", fmt.Errorf("release %s is missing %s", rel.Tag, n)
		}
	}
	sums, err := fetch(ctx, urls["checksums.txt"])
	if err != nil {
		return "", err
	}
	sigB64, err := fetch(ctx, urls["checksums.txt.sig"])
	if err != nil {
		return "", err
	}
	tgz, err := fetch(ctx, urls[archive])
	if err != nil {
		return "", err
	}
	bin, err := Verify(pub, sums, sigB64, archive, tgz)
	if err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	tmp := self + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", tmp).CombinedOutput(); err != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("code signature check failed: %s", strings.TrimSpace(string(out)))
		}
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return self, nil
}

// Verify checks checksums.txt against its base64 ed25519 signature, the
// archive against checksums.txt, and returns the aitank binary inside.
func Verify(pub ed25519.PublicKey, sums, sigB64 []byte, archive string, tgz []byte) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil || !ed25519.Verify(pub, sums, sig) {
		return nil, errors.New("signature check failed: checksums.txt is not signed by the aitank release key; nothing installed")
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == archive {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("checksums.txt has no entry for %s", archive)
	}
	sum := sha256.Sum256(tgz)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("checksum mismatch; nothing installed")
	}
	return extract(tgz, "aitank")
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", provider.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", filepath.Base(url), resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 100<<20))
}

func extract(tgz []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("archive has no %s binary", name)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 100<<20))
		}
	}
}

type checkState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func statePath() string { return filepath.Join(paths.Dir(), "update.json") }

// Cached returns the last known latest version, without network.
func Cached() string {
	var s checkState
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.Latest
}

// MaybeCheck runs at most one release check a day per the update policy.
// With "auto" it installs a newer version (unless Homebrew manages aitank).
func MaybeCheck(ctx context.Context, cfg *config.Config, now time.Time, current string) string {
	if cfg.Update.Policy == "off" || current == "dev" {
		return ""
	}
	var s checkState
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if now.Sub(s.CheckedAt) < 24*time.Hour {
		return ""
	}
	rel, err := Latest(ctx, cfg.Update.URL)
	s.CheckedAt = now
	if err == nil {
		s.Latest = rel.Version
	}
	if b, err := json.Marshal(s); err == nil {
		_ = paths.WriteFile(statePath(), b)
	}
	if err != nil || !Newer(rel.Version, current) {
		return ""
	}
	if cfg.Update.Policy == "auto" && !BrewInstalled() {
		if _, err := Install(ctx, rel); err != nil {
			return "update " + rel.Version + " available; auto-install failed: " + err.Error()
		}
		return "updated to " + rel.Version
	}
	return "update " + rel.Version + " available (aitank update)"
}
