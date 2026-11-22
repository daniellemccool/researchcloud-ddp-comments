package web

import (
	"html"
	"log"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the desk image has no zone files; the zone below comes from the binary

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
)

// deskZone is the zone of every day, week and clock time the desk shows:
// the constant Europe/Amsterdam.
var deskZone = mustLoadZone("Europe/Amsterdam")

func mustLoadZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("web: zone " + name + ": " + err.Error())
	}
	return loc
}

// hm formats minutes as h:mm: 35 is 0:35, 130 is 2:10, 875 is 14:35.
func hm(minutes int) string {
	m := strconv.Itoa(minutes % 60)
	if len(m) < 2 {
		m = "0" + m
	}
	return strconv.Itoa(minutes/60) + ":" + m
}

// workedLine is the student's time line: "Today 0:35 · this week 2:10".
func workedLine(w desklog.Worked) string {
	return "Today " + hm(w.Today) + " · this week " + hm(w.Week)
}

// workedFrom is user's time worked from the log's events, as of now in the
// desk's zone.
func workedFrom(evs []desklog.Event, user string, now time.Time) desklog.Worked {
	return desklog.WorkedOf(desklog.SlotsByUser(evs)[user], now, deskZone)
}

// workedOf reads the desk log for user's time worked. A read error gives
// the zero value and a log line, so a check-in still answers.
func (h *handler) workedOf(user string) desklog.Worked {
	if h.deskLog == nil {
		return desklog.Worked{}
	}
	evs, err := h.deskLog.Read()
	if err != nil {
		log.Printf("web: desk log (time worked of %s): %v", user, err)
		return desklog.Worked{}
	}
	return workedFrom(evs, user, h.now())
}

// workedHTML is the header's time line, the one the check-ins keep current
// (id "worked").
func workedHTML(w desklog.Worked, _ time.Time) string {
	return `<div class="worked"><span id="worked">` + html.EscapeString(workedLine(w)) + `</span></div>`
}

// earlierDaysHTML is the header's Earlier days fold, when an earlier day has
// time: one line per such day, newest first, in a panel under the header.
// "" when no earlier day has time.
func earlierDaysHTML(w desklog.Worked, now time.Time) string {
	today := now.In(deskZone).Format("2006-01-02")
	var earlier []string
	for _, d := range w.Days {
		if d.Day == today {
			continue
		}
		label := d.Day
		if t, err := time.ParseInLocation("2006-01-02", d.Day, deskZone); err == nil {
			label = t.Format("Mon 2 Jan")
		}
		earlier = append(earlier, `<li>`+html.EscapeString(label+" "+hm(d.Minutes))+`</li>`)
	}
	if len(earlier) == 0 {
		return ""
	}
	return `<li class="site-nav__item"><details class="days"><summary>Earlier days</summary>` +
		`<div class="hdr-panel"><h2>Earlier days</h2><ul class="daylist">` + strings.Join(earlier, "") + `</ul></div></details></li>`
}
