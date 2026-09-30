// Package render turns views into terminal text: colours (FR-9.3/9.4),
// countdowns (FR-9.6), rows (FR-9.2) and compact output (FR-9.7).
package render

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

// Dash is how unknown values are shown (FR-5.4).
const Dash = "—"

// Style is a set of ANSI styles; all empty when colour is off.
type Style struct {
	On bool
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	amber  = "\x1b[33m"
	cyan   = "\x1b[36m"
	invert = "\x1b[7m"
)

// ColorEnabled decides whether to colour output written to w: off with
// NO_COLOR, --no-color, TERM=dumb or when w is not a terminal (FR-9.4).
func ColorEnabled(w io.Writer, noColorFlag bool) bool {
	if noColorFlag || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("AITANK_FORCE_COLOR") != "" {
		return true
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (s Style) wrap(code, text string) string {
	if !s.On || text == "" {
		return text
	}
	return code + text + reset
}

func (s Style) Bold(t string) string   { return s.wrap(bold, t) }
func (s Style) Dim(t string) string    { return s.wrap(dim, t) }
func (s Style) Red(t string) string    { return s.wrap(red, t) }
func (s Style) Green(t string) string  { return s.wrap(green, t) }
func (s Style) Amber(t string) string  { return s.wrap(amber, t) }
func (s Style) Cyan(t string) string   { return s.wrap(cyan, t) }
func (s Style) Invert(t string) string { return s.wrap(invert, t) }

// LeftColor colours an overall "left" percentage: green, amber at or below
// the amber threshold (40%), red at or below the red threshold.
func (s Style) LeftColor(c config.Colors, left *float64, text string) string {
	if left == nil {
		return text
	}
	switch {
	case *left <= c.LeftRed:
		return s.Red(text)
	case *left <= c.LeftAmber:
		return s.Amber(text)
	default:
		return s.Green(text)
	}
}

// UsedColor colours a window's used percentage: amber at 60%, red at 90%.
func (s Style) UsedColor(c config.Colors, used *float64, text string) string {
	if used == nil {
		return text
	}
	switch {
	case *used >= c.BarRed:
		return s.Red(text)
	case *used >= c.BarAmber:
		return s.Amber(text)
	default:
		return s.Green(text)
	}
}

// Pct formats a percentage or a dash.
func Pct(v *float64) string {
	if v == nil {
		return Dash
	}
	return fmt.Sprintf("%.0f%%", math.Floor(*v+1e-9))
}

// Countdown formats a duration as "2h 54m" or "6d 9h" (FR-9.6).
func Countdown(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	mins := int(d.Round(time.Minute).Minutes())
	if d < time.Minute {
		return "<1m"
	}
	days, hours, m := mins/(24*60), (mins/60)%24, mins%60
	switch {
	case days > 0:
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		if m == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// Until formats the countdown to t, or a dash.
func Until(t *time.Time, now time.Time) string {
	if t == nil {
		return Dash
	}
	return Countdown(t.Sub(now))
}

// Age formats "(22m old)" for stale data (FR-6.6).
func Age(d time.Duration) string {
	return "(" + Countdown(d) + " old)"
}

// Bar draws a used-percentage bar of width cells.
func (s Style) Bar(c config.Colors, used *float64, width int) string {
	if used == nil {
		return s.Dim(strings.Repeat("·", width))
	}
	n := int(math.Round(*used / 100 * float64(width)))
	if n > width {
		n = width
	}
	return s.UsedColor(c, used, strings.Repeat("█", n)) + s.Dim(strings.Repeat("░", width-n))
}

// Money formats an amount or a dash.
func Money(m *model.Money) string {
	if m == nil || m.Amount == nil {
		return Dash
	}
	sym := map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "CNY": "¥", "JPY": "¥", "INR": "₹"}[strings.ToUpper(m.Currency)]
	if sym != "" {
		return fmt.Sprintf("%s%.2f", sym, *m.Amount)
	}
	if m.Currency == "" {
		return fmt.Sprintf("%.2f", *m.Amount)
	}
	return fmt.Sprintf("%.2f %s", *m.Amount, m.Currency)
}

// WindowShort is a compact window label: 5h, wk, wk·opus, mo.
func WindowShort(w model.Window) string {
	switch w.Kind {
	case model.FiveHour:
		return "5h"
	case model.Weekly:
		return "wk"
	case model.ModelWeekly:
		return "wk·" + strings.ToLower(w.Model)
	case model.Monthly, model.BillingCycle:
		if w.Name != "" && w.Name != "Monthly" && w.Name != "Billing cycle" {
			return strings.ToLower(w.Name)
		}
		return "mo"
	case model.Prepaid:
		return "credit"
	}
	return strings.ToLower(w.Name)
}

// Width is the display width of s, ignoring ANSI codes.
func Width(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
		default:
			n++
		}
	}
	return n
}

// Pad right-pads s to width w.
func Pad(s string, w int) string {
	if d := w - Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// Truncate cuts plain text to w runes with an ellipsis.
func Truncate(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	r := []rune(s)
	return string(r[:w-1]) + "…"
}

// Summary is the one-line output of bare `aitank` (FR-9.1).
func Summary(v *engine.View, st Style) string {
	p := v.Rec.Overall
	if p == nil {
		if len(v.Rows) == 0 {
			return "aitank: no accounts tracked yet. Run `aitank init` to find signed-in tools."
		}
		return st.Red("No usable account right now") + " " + st.Dim("(see `aitank list`)")
	}
	row := v.Row(p.AccountID)
	left := p.Left
	s := fmt.Sprintf("Next: %s  %s left", st.Bold(p.Label), st.LeftColor(v.Config.Colors, &left, Pct(&left)))
	if p.ResetsAt != nil {
		s += fmt.Sprintf(" · %s resets in %s", p.Binding, Countdown(p.ResetsAt.Sub(v.Now)))
	}
	if row != nil {
		for _, f := range row.Forecasts {
			if f.Warn {
				s += " · " + st.Amber("⚠ "+f.Message())
				break
			}
		}
	}
	return s
}

// StatusText is a row's status for display, with age when stale.
func StatusText(r *engine.Row, st Style) string {
	switch r.Status {
	case model.StatusOK:
		if r.Reading != nil && r.Reading.UsagePaused {
			return st.Red("usage paused")
		}
		return st.Green("OK")
	case model.StatusStale:
		return st.Amber("stale " + Age(r.Age))
	case model.StatusPaused:
		return st.Dim("paused")
	case model.StatusNeverRead:
		return st.Dim("not read yet")
	default:
		txt := r.Status.Label()
		if r.Reading != nil && r.Age > 0 {
			txt += " " + Age(r.Age)
		}
		return st.Red(txt)
	}
}

// List prints one row per account (FR-9.2). compact keeps each line within
// 80 columns (FR-9.7).
func List(w io.Writer, v *engine.View, st Style, compact bool) {
	if len(v.Rows) == 0 {
		fmt.Fprintln(w, "No accounts tracked yet. Run `aitank init` or `aitank add <provider>`.")
		return
	}
	c := v.Config.Colors
	for _, r := range v.Rows {
		mark := "  "
		if r.Active && r.Next {
			mark = st.Green("●") + st.Cyan("▶")
		} else if r.Active {
			mark = st.Green("● ")
		} else if r.Next {
			mark = st.Cyan("▶ ")
		}
		plan := Dash
		if r.Reading != nil && r.Reading.Plan != "" {
			plan = r.Reading.Plan
		} else if r.Account.Plan != "" {
			plan = r.Account.Plan
		}
		label := r.Account.Label()
		leftTxt := st.LeftColor(c, r.Left, Pct(r.Left)+" left")
		if compact {
			line := mark + Pad(st.Bold(Truncate(label, 22)), 22) + " " + Pad(leftTxt, 9) + " "
			var parts []string
			if r.Reading != nil {
				for _, win := range r.Reading.Windows {
					if len(parts) == 3 {
						break
					}
					u := win.Pct()
					parts = append(parts, WindowShort(win)+" "+st.UsedColor(c, u, Pct(u))+" "+st.Dim(Until(win.ResetsAt, v.Now)))
				}
				if len(parts) == 0 {
					parts = append(parts, balanceText(r.Reading))
				}
			}
			line += strings.Join(parts, "  ")
			if r.Status != model.StatusOK {
				line += " " + StatusText(r, st)
			}
			fmt.Fprintln(w, clip(line, 80))
			continue
		}
		fmt.Fprintf(w, "%s%s  %s  %s  %s\n", mark, st.Bold(label), st.Dim(Truncate(plan, 24)), leftTxt, StatusText(r, st))
		if r.Reading != nil {
			for _, win := range r.Reading.Windows {
				u := win.Pct()
				name := win.Name
				if name == "" {
					name = WindowShort(win)
				}
				detail := ""
				if win.Used != nil && win.Limit != nil {
					detail = fmt.Sprintf(" %s/%s%s", num(*win.Used), num(*win.Limit), unit(win.Unit))
				}
				fmt.Fprintf(w, "    %s %s %s used%s  resets in %s\n", Pad(Truncate(name, 18), 18), st.Bar(c, u, 20), Pad(st.UsedColor(c, u, Pct(u)), 4), detail, Until(win.ResetsAt, v.Now))
			}
			for i := range r.Reading.Balances {
				b := r.Reading.Balances[i]
				lbl := b.Label
				if lbl == "" {
					lbl = "Balance"
				}
				fmt.Fprintf(w, "    %s %s\n", Pad(Truncate(lbl, 18), 18), Money(&b))
			}
			if r.Reading.Spend != nil {
				lbl := r.Reading.Spend.Label
				if lbl == "" {
					lbl = "Spend"
				}
				fmt.Fprintf(w, "    %s %s\n", Pad(Truncate(lbl, 18), 18), Money(r.Reading.Spend))
			}
			for _, n := range r.Reading.Notes {
				fmt.Fprintf(w, "    %s\n", st.Dim(n))
			}
		}
		for _, f := range forecastWarnings(r) {
			fmt.Fprintf(w, "    %s\n", st.Amber("⚠ "+f))
		}
		if r.Status != model.StatusOK && r.Status != model.StatusPaused && r.Error != "" {
			fmt.Fprintf(w, "    %s\n", st.Dim("last error: "+Truncate(r.Error, 100)))
		}
	}
}

func forecastWarnings(r *engine.Row) []string {
	var out []string
	for _, f := range r.Forecasts {
		if f.Warn {
			out = append(out, f.Message())
		}
	}
	return out
}

func balanceText(r *model.Reading) string {
	if len(r.Balances) > 0 {
		b := r.Balances[0]
		return "bal " + Money(&b)
	}
	if r.Spend != nil {
		return "spend " + Money(r.Spend)
	}
	return Dash
}

func num(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%.2f", f)
}

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// clip cuts a styled line to n visible columns.
func clip(s string, n int) string {
	if Width(s) <= n {
		return s
	}
	var b strings.Builder
	w, esc := 0, false
	for _, r := range s {
		if r == '\x1b' {
			esc = true
		}
		if esc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		if w >= n-1 {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		w++
	}
	if strings.Contains(s, "\x1b") {
		b.WriteString(reset)
	}
	return b.String()
}

// Clip cuts a styled line to n visible columns.
func Clip(s string, n int) string { return clip(s, n) }
