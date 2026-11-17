package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
)

func TestHM(t *testing.T) {
	for _, c := range []struct {
		minutes int
		want    string
	}{
		{0, "0:00"}, {5, "0:05"}, {35, "0:35"}, {130, "2:10"}, {600, "10:00"}, {875, "14:35"},
	} {
		if got := hm(c.minutes); got != c.want {
			t.Errorf("hm(%d) = %q, want %q", c.minutes, got, c.want)
		}
	}
}

// checkInBody decodes a check-in answer (/desk/status or /desk/active).
func checkInBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("check-in status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var ans map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatalf("check-in body %q: %v", rec.Body.String(), err)
	}
	return ans
}

// The times below are written in UTC; Amsterdam is one hour ahead of UTC in
// November. The clock stands at Tuesday 17 November 2026, 10:00 in Amsterdam.
var (
	tueTenAmsterdam = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	tueNine         = time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC).Unix() // 09:00 in Amsterdam
	tueNineOhFive   = time.Date(2026, 11, 17, 8, 5, 0, 0, time.UTC).Unix()
	monAfternoon    = time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC).Unix() // Monday 14:00 in Amsterdam
)

// workedEnv is a ready collector page for alice with the clock fixed and the
// given `active` slots of alice in the desk log, plus one of bob's that must
// never count for her.
func workedEnv(t *testing.T, slots ...int64) *collectorEnv {
	t.Helper()
	e := newCollectorEnvWith(t, false, func(d *Deps) { d.Now = func() time.Time { return tueTenAmsterdam } })
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	evs := []desklog.Event{{T: tueNine, User: "bob", Action: "active", Code: ""}}
	for _, s := range slots {
		evs = append(evs, desklog.Event{T: s, User: "alice", Action: "active", Code: ""})
	}
	if err := e.log.Append(evs...); err != nil {
		t.Fatalf("append: %v", err)
	}
	return e
}

// TestPageShowsTimeWorked: the student's page carries today's and this
// week's time, and behind a fold every earlier day that has time.
func TestPageShowsTimeWorked(t *testing.T) {
	e := workedEnv(t, tueNine, tueNineOhFive, monAfternoon)
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if want := `<span id="worked">Today 0:10 · this week 0:15</span>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q", want)
	}
	i := strings.Index(b, `<details class="days">`)
	if i < 0 {
		t.Fatal("page has no fold of earlier days")
	}
	fold := b[i : i+strings.Index(b[i:], "</details>")]
	if !strings.Contains(fold, "Mon 16 Nov 0:05") {
		t.Errorf("fold %q lacks the line for Monday", fold)
	}
	if strings.Contains(fold, "17 Nov") {
		t.Errorf("fold %q lists today; it holds earlier days only", fold)
	}

	// No earlier day: the line is there and the fold is not.
	e = workedEnv(t, tueNine)
	b = e.get(t, "alice", collectorRole, "/desk/").Body.String()
	if want := `<span id="worked">Today 0:05 · this week 0:05</span>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q", want)
	}
	if strings.Contains(b, `<details class="days">`) {
		t.Error("page shows the fold although no earlier day has time")
	}
}

// TestCheckInCarriesTimeLine: both check-in answers carry the current line,
// so a page left open stays right; the heartbeat's answer already counts the
// slot it just marked (10:00), and ready and running are still there.
func TestCheckInCarriesTimeLine(t *testing.T) {
	e := workedEnv(t, tueNine, tueNineOhFive, monAfternoon)
	e.sess.ready = true
	e.sess.running = true

	ans := checkInBody(t, e.get(t, "alice", collectorRole, "/desk/status"))
	if ans["worked"] != "Today 0:10 · this week 0:15" {
		t.Errorf("status worked = %v, want Today 0:10 · this week 0:15", ans["worked"])
	}
	if ans["ready"] != true {
		t.Errorf("status ready = %v, want true", ans["ready"])
	}

	ans = checkInBody(t, e.post(t, "alice", collectorRole, "/desk/active"))
	if ans["worked"] != "Today 0:15 · this week 0:20" {
		t.Errorf("active worked = %v, want Today 0:15 · this week 0:20", ans["worked"])
	}
	if ans["running"] != true {
		t.Errorf("active running = %v, want true", ans["running"])
	}
}

// The check-in puts the answer's line into the page, so the time is right
// without a reload.
func TestPageCheckInUpdatesTimeLine(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	i := strings.Index(out, "setInterval(")
	if i < 0 {
		t.Fatal("page has no check-in interval")
	}
	if !strings.Contains(out[i:], "const j=await r.json();showFigures(j);") {
		t.Error("the check-in does not hand its answer to showFigures")
	}
	if !strings.Contains(out, "getElementById('worked')") || !strings.Contains(out, "textContent=j.worked") {
		t.Error("showFigures does not put the answer's line into the worked element")
	}
}
