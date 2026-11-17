package desklog

import (
	"reflect"
	"testing"
	"time"
)

func TestSlotStartFloorsToFiveMinutes(t *testing.T) {
	for _, c := range []struct{ in, want int64 }{
		{0, 0}, {299, 0}, {300, 300}, {301, 300}, {1_000_000, 999_900},
	} {
		if got := SlotStart(c.in); got != c.want {
			t.Errorf("SlotStart(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestSlotsByUserDistinct: a duplicate mark (the desk restarted in the
// middle of a slot) counts once, each user's slots come back ascending, and
// only `active` events count.
func TestSlotsByUserDistinct(t *testing.T) {
	evs := []Event{
		{T: 600, User: "alice", Action: "active", Code: ""},
		{T: 600, User: "alice", Action: "active", Code: ""},
		{T: 300, User: "alice", Action: "active", Code: ""},
		{T: 900, User: "bob", Action: "active", Code: ""},
		{T: 1200, User: "alice", Action: "claim", Kind: "video", ID: "1", Batch: 1, Code: ""},
		{T: 1500, User: "bob", Action: "stop", Code: "idle_no_input", IdleS: 900},
	}
	got := SlotsByUser(evs)
	want := map[string][]int64{"alice": {300, 600}, "bob": {900}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SlotsByUser = %v, want %v", got, want)
	}
}

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

// TestWorkedOfDaysAndWeek: a slot belongs to the local day of its start, and
// the week is the Monday-to-Sunday week that holds now. Sunday 15 November
// 23:55 and Monday 16 November 00:00 are five minutes apart and fall in
// different days and different weeks.
func TestWorkedOfDaysAndWeek(t *testing.T) {
	loc := amsterdam(t)
	sunday := time.Date(2026, 11, 15, 23, 55, 0, 0, loc).Unix()
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, loc).Unix()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, loc) // a Tuesday

	got := WorkedOf([]int64{sunday, monday}, now, loc)
	want := Worked{Today: 0, Week: 5, Total: 10, Days: []DayMinutes{{"2026-11-16", 5}, {"2026-11-15", 5}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkedOf = %+v, want %+v", got, want)
	}

	today := time.Date(2026, 11, 17, 9, 30, 0, 0, loc).Unix()
	got = WorkedOf([]int64{sunday, monday, today}, now, loc)
	want = Worked{Today: 5, Week: 10, Total: 15, Days: []DayMinutes{{"2026-11-17", 5}, {"2026-11-16", 5}, {"2026-11-15", 5}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkedOf with a slot today = %+v, want %+v", got, want)
	}
}

// TestWorkedOfWeekEndsOnSunday: on a Sunday the week still began the Monday
// before, and next Monday's slot is not in it.
func TestWorkedOfWeekEndsOnSunday(t *testing.T) {
	loc := amsterdam(t)
	mon := time.Date(2026, 11, 16, 9, 0, 0, 0, loc).Unix()
	sun := time.Date(2026, 11, 22, 23, 55, 0, 0, loc).Unix()
	next := time.Date(2026, 11, 23, 0, 0, 0, 0, loc).Unix()
	now := time.Date(2026, 11, 22, 23, 59, 0, 0, loc) // a Sunday
	got := WorkedOf([]int64{mon, sun, next}, now, loc)
	if got.Week != 10 || got.Today != 5 || got.Total != 15 {
		t.Errorf("WorkedOf on a Sunday = %+v, want Week 10, Today 5, Total 15", got)
	}
}

func TestWorkedOfNothing(t *testing.T) {
	got := WorkedOf(nil, time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), time.UTC)
	if got.Today != 0 || got.Week != 0 || got.Total != 0 || len(got.Days) != 0 {
		t.Errorf("WorkedOf(nil) = %+v, want the zero value", got)
	}
}
