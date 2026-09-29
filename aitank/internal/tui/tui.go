// Package tui is the full-screen `aitank watch` dashboard (FR-10). It uses
// plain ANSI sequences and the alternate screen, which Terminal.app, iTerm2,
// Ghostty, WezTerm and tmux all support.
package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
)

// Actions are the callbacks the dashboard runs for per-row keys.
type Actions struct {
	Load      func() *engine.View    // rebuild the view from the cache
	Refresh   func() string          // read all accounts; returns a status message
	Pause     func(id string) string // toggle pause
	Copy      func(id string) string // copy the launch command
	CanLaunch func(id string) bool   // whether Enter can launch this row
}

// Result tells the caller what to do after the dashboard closes.
type Result struct {
	LaunchID string // Enter was pressed on this account
}

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyEnter
	keyQuit
	keyRune
)

type event struct {
	k key
	r byte
}

// Run shows the dashboard until q, Ctrl-C or Enter.
func Run(act Actions, color bool) (Result, error) {
	in, out := os.Stdin, os.Stdout
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return Result{}, fmt.Errorf("watch needs an interactive terminal; use `aitank list` in scripts")
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return Result{}, err
	}
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		term.Restore(int(in.Fd()), old)
	}()

	events := make(chan event, 16)
	go readKeys(in, events)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	st := render.Style{On: color}
	var mu sync.Mutex
	v := act.Load()
	sel, msg := 0, ""
	msgAt := time.Time{}
	busy := false
	redraw := func() {
		mu.Lock()
		defer mu.Unlock()
		w, h, err := term.GetSize(int(out.Fd()))
		if err != nil || w <= 0 {
			w, h = 80, 24
		}
		if time.Since(msgAt) > 5*time.Second && !busy {
			msg = ""
		}
		fmt.Fprint(out, Frame(v, sel, w, h, msg, st))
	}
	setMsg := func(s string) { msg, msgAt = s, time.Now() }
	if i := nextIndex(v); i >= 0 {
		sel = i
	}
	redraw()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	reload := time.NewTicker(5 * time.Second)
	defer reload.Stop()
	done := make(chan string, 1)
	for {
		select {
		case <-tick.C:
			redraw()
		case <-reload.C:
			mu.Lock()
			v = act.Load()
			mu.Unlock()
			redraw()
		case <-winch:
			fmt.Fprint(out, "\x1b[2J")
			redraw()
		case m := <-done:
			mu.Lock()
			busy = false
			v = act.Load()
			setMsg(m)
			mu.Unlock()
			redraw()
		case e := <-events:
			n := len(v.Rows)
			id := ""
			if n > 0 && sel < n {
				id = v.Rows[sel].Account.ID
			}
			switch {
			case e.k == keyQuit || (e.k == keyRune && e.r == 'q'):
				return Result{}, nil
			case e.k == keyUp || (e.k == keyRune && e.r == 'k'):
				if sel > 0 {
					sel--
				}
			case e.k == keyDown || (e.k == keyRune && e.r == 'j'):
				if sel < n-1 {
					sel++
				}
			case e.k == keyEnter:
				if id != "" && act.CanLaunch(id) {
					return Result{LaunchID: id}, nil
				}
				if id != "" {
					setMsg(v.Rows[sel].Account.Provider + " accounts cannot be launched from aitank")
				}
			case e.k == keyRune && e.r == 'c' && id != "":
				setMsg(act.Copy(id))
			case e.k == keyRune && e.r == 'p' && id != "":
				setMsg(act.Pause(id))
				v = act.Load()
			case e.k == keyRune && e.r == 'r' && !busy:
				busy = true
				setMsg("Refreshing…")
				go func() { done <- act.Refresh() }()
			}
			redraw()
		}
	}
}

func nextIndex(v *engine.View) int {
	for i, r := range v.Rows {
		if r.Next {
			return i
		}
	}
	return -1
}

func readKeys(in *os.File, ch chan<- event) {
	buf := make([]byte, 16)
	for {
		n, err := in.Read(buf)
		if err != nil {
			ch <- event{k: keyQuit}
			return
		}
		b := buf[:n]
		for len(b) > 0 {
			switch {
			case len(b) >= 3 && b[0] == 0x1b && (b[1] == '[' || b[1] == 'O') && b[2] == 'A':
				ch <- event{k: keyUp}
				b = b[3:]
			case len(b) >= 3 && b[0] == 0x1b && (b[1] == '[' || b[1] == 'O') && b[2] == 'B':
				ch <- event{k: keyDown}
				b = b[3:]
			case b[0] == 0x1b && len(b) >= 2:
				b = b[len(b):] // other escape sequences are ignored
			case b[0] == 0x1b:
				ch <- event{k: keyQuit}
				b = b[1:]
			case b[0] == 3 || b[0] == 4: // Ctrl-C, Ctrl-D
				ch <- event{k: keyQuit}
				b = b[1:]
			case b[0] == '\r' || b[0] == '\n':
				ch <- event{k: keyEnter}
				b = b[1:]
			default:
				ch <- event{k: keyRune, r: b[0]}
				b = b[1:]
			}
		}
	}
}

// Frame renders one screen. It is pure so it can be tested.
func Frame(v *engine.View, sel, width, height int, msg string, st render.Style) string {
	if width < 20 {
		width = 20
	}
	if height < 6 {
		height = 6
	}
	var head []string
	title := st.Bold("aitank")
	if p := v.Rec.Overall; p != nil {
		left := p.Left
		title += "  use next: " + st.Cyan(p.Label) + " " + st.LeftColor(v.Config.Colors, &left, render.Pct(&left))
	} else if len(v.Rows) > 0 {
		title += "  " + st.Red("no usable account")
	}
	ago := "never refreshed"
	if !v.RefreshedAt.IsZero() {
		ago = fmt.Sprintf("Refreshed %s ago", secsAgo(v.Now.Sub(v.RefreshedAt)))
	}
	head = append(head, title+"  "+st.Dim(ago))
	head = append(head, "")

	// Build each row's block of lines.
	var blocks [][]string
	barW := 20
	if width < 70 {
		barW = 10
	}
	for i, r := range v.Rows {
		var lines []string
		mark := "  "
		if r.Next {
			mark = st.Cyan("▶ ")
		}
		plan := r.Account.Plan
		if r.Reading != nil && r.Reading.Plan != "" {
			plan = r.Reading.Plan
		}
		line := mark + st.Bold(r.Account.Label()) + "  " + st.Dim(plan) + "  " + st.LeftColor(v.Config.Colors, r.Left, render.Pct(r.Left)+" left") + "  " + render.StatusText(r, st)
		if i == sel {
			line = st.Invert(" ") + line
		} else {
			line = " " + line
		}
		lines = append(lines, line)
		if r.Reading != nil {
			for _, w := range r.Reading.Windows {
				u := w.Pct()
				name := w.Name
				if name == "" {
					name = render.WindowShort(w)
				}
				lines = append(lines, fmt.Sprintf("     %s %s %s  %s", render.Pad(render.Truncate(name, 16), 16), st.Bar(v.Config.Colors, u, barW), render.Pad(st.UsedColor(v.Config.Colors, u, render.Pct(u)), 4), st.Dim("resets "+render.Until(w.ResetsAt, v.Now))))
			}
			if len(r.Reading.Windows) == 0 {
				for _, b := range r.Reading.Balances {
					b := b
					lbl := b.Label
					if lbl == "" {
						lbl = "Balance"
					}
					lines = append(lines, "     "+render.Pad(lbl, 16)+" "+render.Money(&b))
				}
				if r.Reading.Spend != nil {
					lines = append(lines, "     "+render.Pad(r.Reading.Spend.Label, 16)+" "+render.Money(r.Reading.Spend))
				}
			}
		}
		for _, f := range r.Forecasts {
			if f.Warn {
				lines = append(lines, "     "+st.Amber("⚠ "+f.Message()))
			}
		}
		if r.Status != model.StatusOK && r.Status != model.StatusPaused && r.Error != "" {
			lines = append(lines, "     "+st.Dim(render.Truncate(r.Error, width-6)))
		}
		blocks = append(blocks, lines)
	}
	if len(blocks) == 0 {
		blocks = append(blocks, []string{"  No accounts tracked. Quit and run `aitank init`."})
	}

	footer := st.Dim("↑↓/jk move · enter launch · c copy command · r refresh · p pause · q quit")
	if msg != "" {
		footer = msg + "   " + footer
	}
	avail := height - len(head) - 2
	// Scroll so the selected block is visible.
	start := 0
	for {
		used := 0
		for i := start; i <= sel && i < len(blocks); i++ {
			used += len(blocks[i]) + 1
		}
		if used <= avail || start >= sel {
			break
		}
		start++
	}
	var body []string
	for i := start; i < len(blocks); i++ {
		if len(body)+len(blocks[i]) > avail {
			break
		}
		body = append(body, blocks[i]...)
		body = append(body, "")
	}

	var b strings.Builder
	b.WriteString("\x1b[H")
	all := append(append(head, body...), "")
	for len(all) < height-1 {
		all = append(all, "")
	}
	all = all[:height-1]
	all = append(all, footer)
	for i, l := range all {
		b.WriteString(render.Clip(l, width))
		b.WriteString("\x1b[K")
		if i < len(all)-1 {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func secsAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return render.Countdown(d)
}
