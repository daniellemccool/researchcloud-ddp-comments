package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// rosterString renders a roster for comparison; an empty list and a nil one
// read the same.
func rosterString(r roster) string {
	return fmt.Sprintf("known=%v never=%v lost=%v", r.Known, strings.Join(r.NeverClaimed, ","), r.Lost)
}

// TestRosterOf: the two member lists, one case each.
func TestRosterOf(t *testing.T) {
	roles := access.Config{CollectorRole: "src_co_students", AdminRole: "src_co_admin", TechAdminRole: "src_co_developer"}
	// 23:30 UTC on 11 November is 00:30 on 12 November in Amsterdam.
	revoked := time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC).Unix()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		members   map[string][]string
		known     bool
		claimed   map[string]bool
		seats     []string
		revokedAt map[string]int64
		want      roster
	}{
		{
			name:    "a collector who never claimed is listed",
			members: map[string][]string{"chris": {"src_co_students"}, "anna": {"src_co_students"}},
			known:   true, claimed: map[string]bool{"anna": true},
			want: roster{Known: true, NeverClaimed: []string{"chris"}},
		},
		{
			name:    "an admin without the collector role itself is not",
			members: map[string][]string{"ann": {"src_co_admin"}, "tess": {"src_co_developer"}},
			known:   true,
			want:    roster{Known: true},
		},
		{
			name:    "a member with unknown roles is in neither list",
			members: map[string][]string{"hal": nil, "ida": nil},
			known:   true, claimed: map[string]bool{"hal": true}, seats: []string{"ida"},
			want: roster{Known: true},
		},
		{
			name:    "a claimant absent from the list with a revoke event",
			members: map[string][]string{},
			known:   true, claimed: map[string]bool{"emma": true}, revokedAt: map[string]int64{"emma": revoked},
			want: roster{Known: true, Lost: []lostRow{{"emma", "removed 12 Nov"}}},
		},
		{
			name:    "a claimant absent from the list without one",
			members: map[string][]string{},
			known:   true, claimed: map[string]bool{"emma": true},
			want: roster{Known: true, Lost: []lostRow{{"emma", "removed"}}},
		},
		{
			name:    "a seat holder absent from the list, never claimed",
			members: map[string][]string{},
			known:   true, seats: []string{"jo"},
			want: roster{Known: true, Lost: []lostRow{{"jo", "removed"}}},
		},
		{
			name:    "a claimant present with other roles only",
			members: map[string][]string{"finn": {"other"}, "gwen": {}},
			known:   true, claimed: map[string]bool{"finn": true}, seats: []string{"gwen"},
			want: roster{Known: true, Lost: []lostRow{{"finn", "role removed"}, {"gwen", "role removed"}}},
		},
		{
			name:    "a claimant who holds only the admin role is not lost",
			members: map[string][]string{"gus": {"src_co_admin"}, "tess": {"src_co_developer"}},
			known:   true, claimed: map[string]bool{"gus": true, "tess": true},
			want: roster{Known: true},
		},
		{
			name: "both lists sorted by name",
			members: map[string][]string{
				"dewi": {"src_co_students"}, "chris": {"other", "src_co_students"}, "zoe": {"x"}, "bea": {"x"},
			},
			known: true, claimed: map[string]bool{"zoe": true, "yan": true}, seats: []string{"bea"},
			want: roster{Known: true, NeverClaimed: []string{"chris", "dewi"}, Lost: []lostRow{{"bea", "role removed"}, {"yan", "removed"}, {"zoe", "role removed"}}},
		},
		{
			name:    "no good poll yet",
			members: map[string][]string{"chris": {"src_co_students"}},
			known:   false, claimed: map[string]bool{"emma": true},
			want: roster{},
		},
	} {
		got := rosterOf(c.members, c.known, roles, c.claimed, c.seats, c.revokedAt, now)
		if rosterString(got) != rosterString(c.want) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, rosterString(got), rosterString(c.want))
		}
	}
}

// writeCaptures writes a one-line captures file for user under root, last
// written at mtime.
func writeCaptures(t *testing.T, root, user string, mtime time.Time) {
	t.Helper()
	p := capturesPath(root, user)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// inOrder reports an error unless every part is in b, each after the one
// before.
func inOrder(t *testing.T, b string, parts ...string) {
	t.Helper()
	last := -1
	for _, p := range parts {
		i := strings.Index(b, p)
		if i <= last {
			t.Errorf("%q is missing or out of order (at %d, after %d):\n%s", p, i, last, b)
			return
		}
		last = i
	}
}

// TestAdminStudentsTable: one row per live seat, in registry order, with
// the week and the total from the log, the upload state and the session.
// The clock stands at Tuesday 17 November 10:00 in Amsterdam.
func TestAdminStudentsTable(t *testing.T) {
	ready := provision.State{Steps: allDoneSteps()}
	broken := provision.State{Steps: map[string]string{"registry": "done", "dirs": "done", "template": "error: template missing"}}
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "erin", Index: 1, Live: true, Running: true, State: ready},
		{User: "dave", Index: 2, Live: false, Running: false, State: ready},
		{User: "alice", Index: 3, Live: true, Running: true, State: ready},
		{User: "carol", Index: 4, Live: true, Running: false, State: broken},
		{User: "bob", Index: 5, Live: true, Running: false, State: ready},
	}}
	// alice's last input is 09:52:30 in Amsterdam: 7.5 minutes before the
	// page's clock, shown as 7.
	clock := &fakeClock{t: time.Date(2026, 11, 17, 8, 52, 30, 0, time.UTC)}
	act := NewActivity(clock.now)
	act.Input("alice")
	reader := &fakeUploads{ups: fourcat.Uploads{"alice": {"a-search": time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC).Unix()}}}
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Now = func() time.Time { return tueTenAmsterdam }
		d.Cfg.FourcatEnabled = true
		d.Platform.ZeeschuimerModules = []string{"a"}
		d.Uploads = reader
		d.Activity = act
		d.AdminSessions = sess
	})
	lastWeek := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC).Unix() // Tuesday 10 November
	appendLog(t, root,
		desklog.Event{T: tueNine, User: "alice", Action: "active"},
		desklog.Event{T: monAfternoon, User: "alice", Action: "active"},
		desklog.Event{T: lastWeek, User: "alice", Action: "active"},
		desklog.Event{T: lastWeek, User: "bob", Action: "active"},
		desklog.Event{T: tueNine, User: "dave", Action: "active"},
	)
	writeCaptures(t, root, "alice", time.Unix(tueNine, 0))     // before her 09:30 upload
	writeCaptures(t, root, "bob", time.Unix(monAfternoon, 0))  // never uploaded
	writeCaptures(t, root, "dave", time.Unix(monAfternoon, 0)) // retired: no row

	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	inOrder(t, b,
		`<h2>Collection: TestTube</h2>`,
		`<h2>Students</h2><table><tr><th>Name</th><th>This week</th><th>Total</th><th>Uploaded</th><th>Session</th></tr>`,
		`<tr><td><a href="/results/?user=erin">erin</a></td><td>0:00</td><td>0:00</td><td>nothing captured</td><td>running <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="erin"><button>Stop</button></form></td></tr>`,
		`<tr><td><a href="/results/?user=alice">alice</a></td><td>0:10</td><td>0:15</td><td>yes, 09:30</td><td>running, input 7 min ago <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="alice"><button>Stop</button></form></td></tr>`,
		`<tr><td><a href="/results/?user=carol">carol</a></td><td>0:00</td><td>0:00</td><td>nothing captured</td><td>at template (template missing)</td></tr>`,
		`<tr><td><a href="/results/?user=bob">bob</a></td><td>0:00</td><td>0:05</td><td>no, captures since Mon 14:00</td><td>stopped</td></tr>`,
		`</table>`,
		`<h2>Problems</h2>`,
	)
	if strings.Contains(b, "dave") {
		t.Errorf("a retired seat has a row:\n%s", b)
	}
	if sess.statusCalls != 1 || sess.seatsCalls != 0 {
		t.Errorf("one page load called Status %d and Seats %d times, want 1 and 0", sess.statusCalls, sess.seatsCalls)
	}
	if reader.reads != 1 {
		t.Errorf("one page load read 4CAT %d times, want 1", reader.reads)
	}
}

// Without 4CAT the name is not a link and there is no upload column.
func TestAdminStudentsWithoutFourcat(t *testing.T) {
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "alice", Index: 1, Live: true, Running: false, State: provision.State{Steps: allDoneSteps()}},
	}}
	srv, _ := newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	want := `<h2>Students</h2><table><tr><th>Name</th><th>This week</th><th>Total</th><th>Session</th></tr>` +
		`<tr><td>alice</td><td>0:00</td><td>0:00</td><td>stopped</td></tr></table>`
	if !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	if strings.Contains(b, "/results/") || strings.Contains(b, "Uploaded") {
		t.Errorf("page without 4CAT links to 4CAT or has an upload column:\n%s", b)
	}

	// Only retired seats: no table, a line instead.
	sess = &fakeAdminSessions{rows: []session.Row{{User: "alice", Index: 1, Live: false}}}
	srv, _ = newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<h2>Students</h2><p>No one has a seat yet.</p>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}

	// No session data wired: the section has no table.
	srv, _ = newAdminServer(t)
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if !strings.Contains(b, "<h2>Students</h2>") || strings.Contains(b, "<th>Name</th>") {
		t.Errorf("page without session data: want the heading and no table:\n%s", b)
	}
}

// When Docker does not answer, the table still lists the live seats from
// the registry, read once for the whole page, with the session unknown;
// when the registry cannot be read either, one line says so.
func TestAdminStudentsWhenDockerDoesNotAnswer(t *testing.T) {
	sess := &fakeAdminSessions{statusErr: errors.New("docker: no answer"), seats: []registry.Row{
		{User: "alice", Index: 1, Status: "live"}, {User: "bob", Index: 2, Status: "retired"},
	}}
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Now = func() time.Time { return tueTenAmsterdam }
		d.Cfg.FourcatEnabled = true
		d.AdminSessions = sess
	})
	appendLog(t, root, desklog.Event{T: tueNine, User: "alice", Action: "active"})
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	want := `<tr><td><a href="/results/?user=alice">alice</a></td><td>0:05</td><td>0:05</td><td>unknown</td><td>unknown</td></tr></table>`
	if !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	if strings.Contains(b, "?user=bob") {
		t.Errorf("a retired seat has a row:\n%s", b)
	}
	if sess.statusCalls != 1 || sess.seatsCalls != 1 {
		t.Errorf("one page load called Status %d and Seats %d times, want 1 and 1", sess.statusCalls, sess.seatsCalls)
	}

	sess = &fakeAdminSessions{statusErr: errors.New("docker: no answer"), seatsErr: errors.New("registry: unreadable")}
	srv, _ = newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<h2>Students</h2><p class="warn">The students cannot be listed: the seat registry is unreadable.</p>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	if strings.Contains(b, "<th>Name</th>") {
		t.Errorf("page has a table without any seats to list:\n%s", b)
	}
}

// The two member lists sit under the table, from the last good CO list, the
// log's claims and revokes, and every seat, live or retired.
func TestAdminMemberLists(t *testing.T) {
	ready := provision.State{Steps: allDoneSteps()}
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "anna", Index: 1, Live: true, State: ready},
		{User: "ivan", Index: 2, Live: true, State: ready},
		{User: "jo", Index: 3, Live: false, State: ready},
	}}
	members := fakeMembers{known: true, members: map[string][]string{
		"anna":  {collectorRole},
		"chris": {collectorRole},
		"dewi":  {collectorRole, adminRole},
		"finn":  {"other"},
		"gus":   {adminRole},
		"tess":  {"src_co_developer"},
		"hal":   nil,
	}}
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Access.TechAdminRole = "src_co_developer"
		d.AdminSessions = sess
		d.Members = members
	})
	appendLog(t, root,
		desklog.Event{T: monAfternoon, User: "anna", Action: "claim", Kind: "video", ID: "1"},
		desklog.Event{T: monAfternoon, User: "emma", Action: "claim", Kind: "video", ID: "2"},
		desklog.Event{T: monAfternoon, User: "finn", Action: "claim", Kind: "video", ID: "3"},
		desklog.Event{T: monAfternoon, User: "gus", Action: "claim", Kind: "video", ID: "4"},
		desklog.Event{T: monAfternoon, User: "tess", Action: "claim", Kind: "video", ID: "5"},
		desklog.Event{T: monAfternoon, User: "hal", Action: "claim", Kind: "video", ID: "6"},
		desklog.Event{T: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC).Unix(), User: "emma", Action: "revoke", Code: "co_absent"},
		desklog.Event{T: time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC).Unix(), User: "emma", Action: "revoke", Code: "co_absent"},
	)
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	inOrder(t, b,
		`<h2>Students</h2>`,
		`</table>`,
		`<p>Hold the collector role, never claimed: chris, dewi</p>`,
		`<p>Worked here, no longer have access: emma (removed 12 Nov), finn (role removed), ivan (removed), jo (removed)</p>`,
		`<h2>Problems</h2>`,
	)

	// Empty lists read "none".
	srv, root = newAdminServerWith(t, func(d *Deps) {
		d.Members = fakeMembers{known: true, members: map[string][]string{"anna": {collectorRole}}}
	})
	appendLog(t, root, desklog.Event{T: monAfternoon, User: "anna", Action: "claim", Kind: "video", ID: "1"})
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	inOrder(t, b,
		`<h2>Students</h2>`,
		`<p>Hold the collector role, never claimed: none</p>`,
		`<p>Worked here, no longer have access: none</p>`,
		`<h2>Problems</h2>`,
	)

	// No poller wired: neither line.
	srv, _ = newAdminServer(t)
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if strings.Contains(b, "never claimed") || strings.Contains(b, "no longer have access") {
		t.Errorf("page without a poller shows the member lists:\n%s", b)
	}
}

// While there is no good poll, both lists read unknown.
func TestAdminListsUnknownWithoutPoll(t *testing.T) {
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Members = fakeMembers{known: false}
	})
	appendLog(t, root, desklog.Event{T: monAfternoon, User: "emma", Action: "claim", Kind: "video", ID: "1"})
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	for _, want := range []string{
		`<p>Hold the collector role, never claimed: unknown: no member list yet</p>`,
		`<p>Worked here, no longer have access: unknown: no member list yet</p>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q:\n%s", want, b)
		}
	}
}

// Names from the registry and the CO list, and a step's error text, are
// escaped in the text and in the link.
func TestAdminStudentsEscapes(t *testing.T) {
	odd := `x"<y>&z`
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: odd, Index: 1, Live: true, State: provision.State{Steps: allDoneSteps()}},
		{User: "carol", Index: 2, Live: true, State: provision.State{Steps: map[string]string{"registry": "error: <b>bad</b>"}}},
	}}
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		d.Cfg.FourcatEnabled = true
		d.AdminSessions = sess
		d.Members = fakeMembers{known: true, members: map[string][]string{"<m>": {collectorRole}}}
	})
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	for _, want := range []string{
		`<tr><td><a href="/results/?user=x%22%3Cy%3E%26z">x&#34;&lt;y&gt;&amp;z</a></td>`,
		`<td>at registry (&lt;b&gt;bad&lt;/b&gt;)</td>`,
		`<p>Hold the collector role, never claimed: &lt;m&gt;</p>`,
		`<p>Worked here, no longer have access: carol (removed), x&#34;&lt;y&gt;&amp;z (removed)</p>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q:\n%s", want, b)
		}
	}
	for _, raw := range []string{odd, "<b>bad", "<m>"} {
		if strings.Contains(b, raw) {
			t.Errorf("the page holds %q raw:\n%s", raw, b)
		}
	}
}

// A running student whom the tracker holds only because the idle stop
// asked about them (no input ever reported) reads plain "running".
func TestAdminStudentsRunningWithoutInput(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 8, 52, 30, 0, time.UTC)}
	act := NewActivity(clock.now)
	act.Idle("erin", time.Hour) // what a reaper pass does for every running user
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "erin", Index: 1, Live: true, Running: true, State: provision.State{Steps: allDoneSteps()}},
	}}
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		d.Now = func() time.Time { return tueTenAmsterdam }
		d.Activity = act
		d.AdminSessions = sess
	})
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<tr><td>erin</td><td>0:00</td><td>0:00</td><td>running <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="erin"><button>Stop</button></form></td></tr>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	if strings.Contains(b, "running, input") {
		t.Errorf("a student who never gave input shows an input time:\n%s", b)
	}
}
