package config

import (
	"os"
	"testing"
	"time"
)

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	t.Setenv("AITANK_HOME", t.TempDir())
	c := Default()
	c.Accounts = append(c.Accounts, Account{ID: "claude-1", Provider: "claude", Nickname: "me", Auth: "local", AddedAt: time.Unix(1, 0).UTC()})
	c.Bills = append(c.Bills, Bill{ID: "bill-1", Name: "Max", Price: 200, Currency: "USD", Cycle: "monthly", Renewal: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshInterval.Duration != 5*time.Minute || len(got.Accounts) != 1 || got.Accounts[0].Nickname != "me" || got.Bills[0].Price != 200 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	fi, _ := os.Stat(pathsConfig())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode().Perm())
	}
}

func TestGetSetValidates(t *testing.T) {
	c := Default()
	if err := c.Set("refresh_interval", "10m"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get("refresh_interval"); v != "10m" {
		t.Fatalf("got %q", v)
	}
	if err := c.Set("refresh_interval", "10s"); err == nil {
		t.Fatal("intervals under a minute must be rejected")
	}
	if err := c.Set("colors.left_amber", "30%"); err != nil || c.Colors.LeftAmber != 30 {
		t.Fatal("percent suffix should parse")
	}
	if err := c.Set("update.policy", "sometimes"); err == nil {
		t.Fatal("bad policy accepted")
	}
	if _, err := c.Get("nope"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestDurationString(t *testing.T) {
	for in, want := range map[time.Duration]string{5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Minute: "1h30m", 45 * time.Second: "45s"} {
		if got := (Duration{in}).String(); got != want {
			t.Errorf("%v: got %q want %q", in, got, want)
		}
	}
}

func TestAccountLookup(t *testing.T) {
	c := Default()
	c.Accounts = []Account{{ID: "claude-1", Provider: "claude", Nickname: "work"}, {ID: "codex-1", Provider: "codex", Nickname: "work"}}
	if _, err := c.Account("work"); err == nil {
		t.Fatal("ambiguous nickname should fail")
	}
	if a, err := c.Account("codex work"); err != nil || a.ID != "codex-1" {
		t.Fatal("provider nickname lookup failed")
	}
	if c.NewAccountID("claude") != "claude-2" {
		t.Fatal("new id")
	}
}
