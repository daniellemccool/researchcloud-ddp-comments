package desklog

import (
	"slices"
	"time"
)

// Time worked is read from `active` events only. The desk appends one per
// user per slot in which that user's page reported input, stamped with the
// slot's start, so equal marks are equal lines; a duplicate (the desk
// restarted in the middle of a slot) counts once. Time worked is the number
// of distinct slots times the slot length, so a stretch of work can read up
// to one slot high.

// SlotSeconds is the length of one slot of time worked: five minutes.
const SlotSeconds = 300

// slotMinutes is one slot in minutes, the unit Worked counts in.
const slotMinutes = SlotSeconds / 60

// SlotStart floors the unix time t to the start of its slot.
func SlotStart(t int64) int64 { return t - t%SlotSeconds }

// SlotsByUser returns each user's distinct `active` slot starts, ascending.
// Every other event is ignored.
func SlotsByUser(evs []Event) map[string][]int64 {
	seen := map[string]map[int64]bool{}
	for _, e := range evs {
		if e.Action != "active" {
			continue
		}
		if seen[e.User] == nil {
			seen[e.User] = map[int64]bool{}
		}
		seen[e.User][SlotStart(e.T)] = true
	}
	out := make(map[string][]int64, len(seen))
	for user, slots := range seen {
		for s := range slots {
			out[user] = append(out[user], s)
		}
		slices.Sort(out[user])
	}
	return out
}

// DayMinutes is the time worked on one local day.
type DayMinutes struct {
	Day     string // 2006-01-02 in the zone given to WorkedOf
	Minutes int
}

// Worked is one person's time worked, in minutes. Days holds every day with
// time, newest first, today included when it has time.
type Worked struct {
	Today, Week, Total int
	Days               []DayMinutes
}

// WorkedOf groups one user's distinct slot starts (as SlotsByUser returns
// them) by day and week in loc. A slot belongs to the local day of its
// start; Week is the Monday-to-Sunday week that holds now.
func WorkedOf(slots []int64, now time.Time, loc *time.Location) Worked {
	const dayLayout = "2006-01-02"
	n := now.In(loc)
	today := n.Format(dayLayout)
	sinceMonday := (int(n.Weekday()) + 6) % 7 // Monday 0 … Sunday 6
	weekStart := time.Date(n.Year(), n.Month(), n.Day()-sinceMonday, 0, 0, 0, 0, loc)
	weekEnd := weekStart.AddDate(0, 0, 7)

	var w Worked
	perDay := map[string]int{}
	for _, s := range slots {
		at := time.Unix(s, 0).In(loc)
		day := at.Format(dayLayout)
		perDay[day] += slotMinutes
		w.Total += slotMinutes
		if day == today {
			w.Today += slotMinutes
		}
		if !at.Before(weekStart) && at.Before(weekEnd) {
			w.Week += slotMinutes
		}
	}
	for day, minutes := range perDay {
		w.Days = append(w.Days, DayMinutes{Day: day, Minutes: minutes})
	}
	slices.SortFunc(w.Days, func(a, b DayMinutes) int {
		switch {
		case a.Day > b.Day:
			return -1
		case a.Day < b.Day:
			return 1
		}
		return 0
	})
	return w
}
