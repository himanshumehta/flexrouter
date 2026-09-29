package state

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer|token|key|authorization|cookie|password|secret)(["'\s:=]+)[^\s"',;]+`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*`), // JWT
	regexp.MustCompile(`\b(sk|pk|rk|xai|gho|ghp|ghu|github_pat|sess)[-_][A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`\b[A-Za-z0-9_\-]{40,}\b`), // long opaque tokens
}

// Redact removes anything that looks like a credential from text that is
// about to be logged or shown (FR-15.2).
func Redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if len(sub) == 3 && re == secretPatterns[0] {
				return sub[1] + sub[2] + "[redacted]"
			}
			return "[redacted]"
		})
	}
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// Mask shows a key as "sk-…a1f3" (FR-15.2).
func Mask(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 12 {
		return "…"
	}
	prefix := ""
	if i := strings.IndexAny(key, "-_"); i > 0 && i <= 8 {
		prefix = key[:i+1]
	}
	return prefix + "…" + key[len(key)-4:]
}
