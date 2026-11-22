package web

import (
	"context"
	"html"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// The Students section of the admin page: one row per live seat, then the
// two member lists. Time worked is only shown, never sorted, ranked or
// flagged, and the member lists only show who lost access: nothing is
// revoked or stopped because of them.

// lostRow is one person who worked here and no longer has access, with why:
// "removed", "removed <date>" or "role removed".
type lostRow struct {
	User, Why string
}

// roster is the admin page's two member lists. Known is false while there
// is no good CO poll; the lists are then empty.
type roster struct {
	Known        bool
	NeverClaimed []string
	Lost         []lostRow
}

// rosterOf builds the two member lists from the last good CO list (members,
// known false: none yet), the role names, the users with a claim in the
// log, every seat (live and retired) and each user's last revoke time.
//
// NeverClaimed holds the members whose roles are known and contain the
// collector role itself (an admin role alone does not count) and who never
// claimed. Lost holds everyone with a claim or a seat who is absent from
// the list ("removed", with the day of their last revoke when the log has
// one) or present with known roles holding none of the three role names
// ("role removed"). A member whose roles are unknown (nil) is in neither
// list. Role names are compared whole, by equality. Both lists are sorted
// by name. now is the page's clock; the removal day does not depend on it.
func rosterOf(members map[string][]string, known bool, roles access.Config, claimed map[string]bool, seats []string, revokedAt map[string]int64, now time.Time) roster {
	if !known {
		return roster{}
	}
	r := roster{Known: true}
	for user, held := range members {
		if held != nil && !claimed[user] && holds(held, roles.CollectorRole) {
			r.NeverClaimed = append(r.NeverClaimed, user)
		}
	}
	worked := map[string]bool{}
	for user, ok := range claimed {
		if ok {
			worked[user] = true
		}
	}
	for _, user := range seats {
		worked[user] = true
	}
	for user := range worked {
		held, present := members[user]
		switch {
		case !present:
			why := "removed"
			if t, ok := revokedAt[user]; ok {
				why += " " + time.Unix(t, 0).In(deskZone).Format("2 Jan")
			}
			r.Lost = append(r.Lost, lostRow{User: user, Why: why})
		case held != nil && !holds(held, roles.CollectorRole) && !holds(held, roles.AdminRole) && !holds(held, roles.TechAdminRole):
			r.Lost = append(r.Lost, lostRow{User: user, Why: "role removed"})
		}
	}
	sort.Strings(r.NeverClaimed)
	sort.Slice(r.Lost, func(i, j int) bool { return r.Lost[i].User < r.Lost[j].User })
	return r
}

// holds reports whether the role list holds the role name, compared whole;
// an empty name (a role not configured) is never held.
func holds(held []string, name string) bool {
	return name != "" && slices.Contains(held, name)
}

// claimsAndRevokes is one pass over the log's events: the users with a
// claim, and each user's last revoke time.
func claimsAndRevokes(evs []desklog.Event) (claimed map[string]bool, revokedAt map[string]int64) {
	claimed, revokedAt = map[string]bool{}, map[string]int64{}
	for _, e := range evs {
		switch e.Action {
		case "claim":
			claimed[e.User] = true
		case "revoke":
			if t, ok := revokedAt[e.User]; !ok || e.T > t {
				revokedAt[e.User] = e.T
			}
		}
	}
	return claimed, revokedAt
}

// studentsSection is the Students section: the table of live seats and the
// two member lists; logKnown is false when the desk log cannot be read.
func (a *adminHandler) studentsSection(ctx context.Context, evs []desklog.Event, logKnown bool, sd seatData) string {
	return `<h2>Students</h2>` + a.studentsTable(ctx, evs, logKnown, sd) + a.memberLists(evs, logKnown, sd)
}

// student is one row of the Students table: a live seat, its session cell
// and whether its pair runs (it then has a Stop button).
type student struct {
	user, session string
	running       bool
}

// studentsTable is one row per live seat, in registry order: the name
// (linked to the person's 4CAT datasets when 4CAT is enabled), this week,
// total, the upload state (only with 4CAT) and the session. When the
// session status failed, the live seats come from the registry and the
// session reads "unknown"; when the registry failed too, one line says the
// students cannot be listed. When the desk log cannot be read (logKnown
// false), this week and total read "unknown". "" without session data.
func (a *adminHandler) studentsTable(ctx context.Context, evs []desklog.Event, logKnown bool, sd seatData) string {
	if a.sessions == nil {
		return ""
	}
	now := a.now()
	var list []student
	switch {
	case sd.statusErr == nil:
		for _, r := range sd.rows {
			if r.Live {
				list = append(list, student{user: r.User, session: a.sessionCell(r, now), running: r.Running})
			}
		}
	case sd.seatsErr == nil:
		for _, r := range sd.seats {
			if r.Status == "live" {
				list = append(list, student{user: r.User, session: "unknown"})
			}
		}
	default:
		return `<p class="warn">The students cannot be listed: the seat registry is unreadable.</p>`
	}
	if len(list) == 0 {
		return `<p>No one has a seat yet.</p>`
	}
	slots := desklog.SlotsByUser(evs)
	var b strings.Builder
	b.WriteString(`<table><tr><th>Name</th><th>This week</th><th>Total</th>`)
	if a.cfg.FourcatEnabled {
		b.WriteString(`<th>Uploaded</th>`)
	}
	b.WriteString(`<th>Session</th></tr>`)
	for _, s := range list {
		week, total := "unknown", "unknown"
		if logKnown {
			w := desklog.WorkedOf(slots[s.user], now, deskZone)
			week, total = hm(w.Week), hm(w.Total)
		}
		b.WriteString(`<tr><td>` + a.nameCell(s.user) + `</td><td>` + week + `</td><td>` + total + `</td>`)
		if a.cfg.FourcatEnabled {
			st := a.uploads.Of(ctx, s.user)
			cell := html.EscapeString(uploadCell(st, now))
			if st.Kind == uploadDue {
				cell = `<span class="late">` + cell + `</span>` // captures not in 4CAT yet: the coral node
			}
			b.WriteString(`<td>` + cell + `</td>`)
		}
		b.WriteString(`<td>` + html.EscapeString(s.session) + stopButton(s) + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// stopButton is the Stop form of a row whose pair runs (Row.Running, also
// while its provisioning is not ready), after the session text; "" for any
// other row, and for every row while the session status is unknown.
func stopButton(s student) string {
	if !s.running {
		return ""
	}
	return ` <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="` +
		html.EscapeString(s.user) + `"><button>Stop` + goMark + `</button></form>`
}

// nameCell is the person's name, linked to /results/?user=<name> when 4CAT
// is enabled; the name is escaped in the text, and query-escaped and then
// escaped in the link.
func (a *adminHandler) nameCell(user string) string {
	name := html.EscapeString(user)
	if !a.cfg.FourcatEnabled {
		return name
	}
	return `<a href="` + html.EscapeString("/results/?user="+url.QueryEscape(user)) + `">` + name + `</a>`
}

// sessionCell is the session column: the provisioning step while it is not
// ready, as `desk status` prints it ("at <step>", plus " (<error text>)"
// when the step holds an error); else "stopped", or "running", with
// "input <n> min ago" (whole minutes, rounded down) when the activity
// tracker has a real input of the user (LastInput; never the idle clock).
func (a *adminHandler) sessionCell(r session.Row, now time.Time) string {
	if !r.State.Ready() {
		s := "at " + r.State.Current()
		if errText := r.State.Error(); errText != "" {
			s += " (" + strings.TrimPrefix(errText, "error: ") + ")"
		}
		return s
	}
	if !r.Running {
		return "stopped"
	}
	if a.activity != nil {
		if t, ok := a.activity.LastInput(r.User); ok {
			ago := max(now.Sub(t), 0)
			return "running, input " + strconv.Itoa(int(ago/time.Minute)) + " min ago"
		}
	}
	return "running"
}

// uploadCell is the upload column's text for an upload state.
func uploadCell(st UploadState, now time.Time) string {
	switch st.Kind {
	case uploadDone:
		return "yes, " + when(st.At, now)
	case uploadDue:
		return "no, captures since " + when(st.At, now)
	case uploadNone:
		return "nothing captured"
	}
	return "unknown"
}

// memberLists is the two member lists under the table, from the CO poll's
// last good list; while there is none, both read "unknown: no member list
// yet", and while the desk log cannot be read (logKnown false), both read
// logUnknown: the first needs the claims, the second the claims and the
// revoke days. "" without a poller.
func (a *adminHandler) memberLists(evs []desklog.Event, logKnown bool, sd seatData) string {
	if a.members == nil {
		return ""
	}
	members, known := a.members.Members()
	claimed, revokedAt := claimsAndRevokes(evs)
	r := rosterOf(members, known, a.roles, claimed, sd.names(), revokedAt, a.now())
	never, lost := "unknown: no member list yet", "unknown: no member list yet"
	switch {
	case !r.Known:
		// both stay "unknown: no member list yet"
	case !logKnown:
		never, lost = logUnknown, logUnknown
	default:
		never, lost = "none", "none"
		if len(r.NeverClaimed) > 0 {
			names := make([]string, len(r.NeverClaimed))
			for i, u := range r.NeverClaimed {
				names[i] = html.EscapeString(u)
			}
			never = strings.Join(names, ", ")
		}
		if len(r.Lost) > 0 {
			names := make([]string, len(r.Lost))
			for i, l := range r.Lost {
				names[i] = html.EscapeString(l.User + " (" + l.Why + ")")
			}
			lost = strings.Join(names, ", ")
		}
	}
	return `<dl class="facts"><div><dt>Hold the collector role, never claimed</dt><dd>` + never + `</dd></div>` +
		`<div><dt>Worked here, no longer have access</dt><dd>` + lost + `</dd></div></dl>`
}
