// Package state holds aitank's local cache (FR-6.3), reading history
// (FR-8.1), read log (FR-18.3) and alert bookkeeping. Display commands read
// only these files and never touch the network.
package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
)

// Entry is the cached state of one account.
type Entry struct {
	LastGood    *model.Reading `json:"last_good,omitempty"`
	LastAttempt time.Time      `json:"last_attempt,omitempty"`
	LastStatus  model.Status   `json:"last_status,omitempty"` // status of the last attempt
	LastError   string         `json:"last_error,omitempty"`
	Failures    int            `json:"failures,omitempty"`
	NextAttempt time.Time      `json:"next_attempt,omitempty"` // backoff / retry-after (FR-6.8)
	VendorVer   string         `json:"vendor_version,omitempty"`
}

// Cache is every account's entry plus when a full refresh last ran.
type Cache struct {
	Version     int               `json:"version"`
	RefreshedAt time.Time         `json:"refreshed_at,omitempty"`
	Accounts    map[string]*Entry `json:"accounts"`
}

// LoadCache reads the cache; a missing or unreadable file gives an empty one.
func LoadCache() *Cache {
	c := &Cache{Version: 1, Accounts: map[string]*Entry{}}
	data, err := os.ReadFile(paths.Cache())
	if err != nil {
		return c
	}
	if json.Unmarshal(data, c) != nil || c.Accounts == nil {
		return &Cache{Version: 1, Accounts: map[string]*Entry{}}
	}
	return c
}

// Save writes the cache atomically.
func (c *Cache) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFile(paths.Cache(), data)
}

// Get returns an account's entry, creating it if needed.
func (c *Cache) Get(id string) *Entry {
	e := c.Accounts[id]
	if e == nil {
		e = &Entry{}
		c.Accounts[id] = e
	}
	return e
}

// Status works out what to show for an account right now (FR-18.1). A good
// reading older than staleAfter is stale (FR-6.6).
func (e *Entry) Status(now time.Time, staleAfter time.Duration, paused bool) model.Status {
	if paused {
		return model.StatusPaused
	}
	if e == nil || (e.LastGood == nil && e.LastStatus == "") {
		return model.StatusNeverRead
	}
	if e.LastStatus != "" && e.LastStatus != model.StatusOK {
		// The latest attempt failed. Show why, unless the last good values
		// are still fresh enough to use.
		if e.LastGood == nil || now.Sub(e.LastGood.FetchedAt) > staleAfter {
			return e.LastStatus
		}
	}
	if e.LastGood != nil && now.Sub(e.LastGood.FetchedAt) > staleAfter {
		return model.StatusStale
	}
	return model.StatusOK
}

// Point is one history sample of one window.
type Point struct {
	T        time.Time  `json:"t"`
	Account  string     `json:"a"`
	Window   string     `json:"w"`
	UsedPct  float64    `json:"u"`
	ResetsAt *time.Time `json:"r,omitempty"`
}

// HistoryKeep is how long samples are kept (FR-8.1 asks for at least 24h).
const HistoryKeep = 48 * time.Hour

// AppendHistory records every window with a known percentage.
func AppendHistory(r *model.Reading) error {
	var buf bytes.Buffer
	for _, w := range r.Windows {
		p := w.Pct()
		if p == nil {
			continue
		}
		line, _ := json.Marshal(Point{T: r.FetchedAt, Account: r.AccountID, Window: w.Key(), UsedPct: *p, ResetsAt: w.ResetsAt})
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return nil
	}
	return paths.AppendFile(paths.History(), buf.Bytes())
}

// LoadHistory returns samples newer than since.
func LoadHistory(since time.Time) []Point {
	f, err := os.Open(paths.History())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Point
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p Point
		if json.Unmarshal(sc.Bytes(), &p) == nil && !p.T.Before(since) {
			out = append(out, p)
		}
	}
	return out
}

// PruneHistory drops samples older than HistoryKeep.
func PruneHistory(now time.Time) error {
	pts := LoadHistory(now.Add(-HistoryKeep))
	var buf bytes.Buffer
	for _, p := range pts {
		line, _ := json.Marshal(p)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return paths.WriteFile(paths.History(), buf.Bytes())
}

// LogEntry is one read attempt (FR-18.3). Error text is redacted before it
// is stored.
type LogEntry struct {
	T        time.Time    `json:"t"`
	Account  string       `json:"account"`
	Provider string       `json:"provider"`
	Status   model.Status `json:"status"`
	Error    string       `json:"error,omitempty"`
	Millis   int64        `json:"ms"`
	Source   string       `json:"source,omitempty"`
}

const logKeep = 2000

// AppendLog records a read attempt.
func AppendLog(e LogEntry) error {
	e.Error = Redact(e.Error)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return paths.AppendFile(paths.ReadLog(), append(line, '\n'))
}

// LoadLog returns the log, oldest first.
func LoadLog() []LogEntry {
	f, err := os.Open(paths.ReadLog())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// TrimLog keeps the newest entries.
func TrimLog() error {
	all := LoadLog()
	if len(all) <= logKeep {
		return nil
	}
	var buf bytes.Buffer
	for _, e := range all[len(all)-logKeep:] {
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return paths.WriteFile(paths.ReadLog(), buf.Bytes())
}

// Alerts remembers which alerts fired, so each fires once per window cycle
// (FR-13.4), and small one-time flags.
type Alerts struct {
	Fired map[string]time.Time `json:"fired"`
	Flags map[string]bool      `json:"flags,omitempty"`
	// LastLeft remembers each account/window's last % left to spot refills.
	LastLeft map[string]float64 `json:"last_left,omitempty"`
}

// LoadAlerts reads alert bookkeeping.
func LoadAlerts() *Alerts {
	a := &Alerts{}
	data, err := os.ReadFile(paths.State())
	if err == nil {
		_ = json.Unmarshal(data, a)
	} else if !errors.Is(err, os.ErrNotExist) {
		// unreadable: start fresh rather than fail every run
	}
	if a.Fired == nil {
		a.Fired = map[string]time.Time{}
	}
	if a.Flags == nil {
		a.Flags = map[string]bool{}
	}
	if a.LastLeft == nil {
		a.LastLeft = map[string]float64{}
	}
	return a
}

// Save writes alert bookkeeping, dropping entries older than 60 days.
func (a *Alerts) Save(now time.Time) error {
	for k, t := range a.Fired {
		if now.Sub(t) > 60*24*time.Hour {
			delete(a.Fired, k)
		}
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFile(paths.State(), data)
}
