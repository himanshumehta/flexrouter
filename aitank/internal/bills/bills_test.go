package bills

import (
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
)

func d(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func TestNextRenewalClampsMonthEnd(t *testing.T) {
	b := config.Bill{Cycle: "monthly", Renewal: d(2026, 1, 31)}
	for now, want := range map[time.Time]time.Time{
		d(2026, 2, 10): d(2026, 2, 28),
		d(2026, 3, 1):  d(2026, 3, 31),
		d(2026, 1, 31): d(2026, 1, 31),
	} {
		if got := NextRenewal(b, now); !got.Equal(want) {
			t.Errorf("now %v: got %v want %v", now, got, want)
		}
	}
	y := config.Bill{Cycle: "yearly", Renewal: d(2024, 2, 29)}
	if got := NextRenewal(y, d(2025, 3, 1)); !got.Equal(d(2026, 2, 28)) {
		t.Errorf("yearly leap: %v", got)
	}
}

func TestTotalsAndDue(t *testing.T) {
	bs := []config.Bill{
		{ID: "1", Price: 200, Currency: "usd", Cycle: "monthly", Renewal: d(2026, 10, 2)},
		{ID: "2", Price: 120, Currency: "USD", Cycle: "yearly", Renewal: d(2027, 1, 1)},
		{ID: "3", Price: 9, Currency: "EUR", Cycle: "weekly", Renewal: d(2026, 9, 30)},
	}
	tot := Totals(bs)
	if tot["USD"] != 210 || tot["EUR"] != 39 {
		t.Fatalf("totals %v", tot)
	}
	due := Due(bs, d(2026, 9, 29), 3)
	if len(due) != 2 {
		t.Fatalf("due %v", due)
	}
}
