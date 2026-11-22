package web

import (
	"context"
	"html"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

// Whether what a person captured has reached 4CAT. The desk keeps no record
// of an upload; it compares the time the person's captures file was last
// written with the latest finished uploads 4CAT has for them, read through
// one statement and kept a minute. It is a state as of now, not a history.

// uploadKind is one of the four upload states.
type uploadKind int

const (
	uploadNone    uploadKind = iota // nothing captured, or the platform has no Zeeschuimer module
	uploadDone                      // every module has an upload at or after the latest capture
	uploadDue                       // something was captured after the last complete upload
	uploadUnknown                   // 4CAT could not be read, or no reader is wired
)

// UploadState is a person's upload state. At is, for uploadDone, the oldest
// of the modules' latest uploads, and for uploadDue the latest capture; it
// is zero otherwise.
type UploadState struct {
	Kind uploadKind
	At   time.Time
}

// uploadState is the rule, over the latest capture (captured false: none),
// the person's latest uploads per dataset type and the platform's
// Zeeschuimer modules. The dataset type of a module is <module>-search.
func uploadState(capture time.Time, captured bool, byType map[string]int64, modules []string) UploadState {
	if !captured || len(modules) == 0 {
		return UploadState{Kind: uploadNone}
	}
	var oldest int64
	for i, m := range modules {
		at, ok := byType[m+"-search"]
		if !ok || at < capture.Unix() {
			return UploadState{Kind: uploadDue, At: capture}
		}
		if i == 0 || at < oldest {
			oldest = at
		}
	}
	return UploadState{Kind: uploadDone, At: time.Unix(oldest, 0)}
}

// uploadsReader is what the upload state needs of *fourcat.Client.
type uploadsReader interface {
	LatestUploads(context.Context) (fourcat.Uploads, error)
}

const (
	uploadCacheFor  = 60 * time.Second // how long one read is kept, a failed one included
	uploadReadLimit = 5 * time.Second  // the time limit of one read
)

// uploadStates answers the upload state per person from a cached read of
// 4CAT. A failed read is kept as long as a good one, so a 4CAT that is down
// is asked once a minute and never once per check-in.
type uploadStates struct {
	reader  uploadsReader
	root    string // the desk root
	modules []string
	now     func() time.Time

	mu     sync.Mutex // held during a read: check-ins that arrive together wait for one read
	readAt time.Time  // zero: never read
	ups    fourcat.Uploads
	failed bool
}

// newUploadStates builds the upload states of d, or nil when no reader is
// wired. A nil *uploadStates answers uploadUnknown.
func newUploadStates(d Deps) *uploadStates {
	if d.Uploads == nil {
		return nil
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &uploadStates{reader: d.Uploads, root: d.Cfg.DeskRoot, modules: d.Platform.ZeeschuimerModules, now: now}
}

// capturesPath is the file a person's capture proxy appends to, one line
// per captured record; its modification time is their latest capture.
func capturesPath(deskRoot, user string) string {
	return filepath.Join(deskRoot, "state", "captures", user, "captures.ndjson")
}

// Of is user's upload state. A missing or empty captures file means nothing
// was captured, and 4CAT is not asked. The capture time is cut to whole
// seconds, because 4CAT's times are whole seconds.
func (u *uploadStates) Of(ctx context.Context, user string) UploadState {
	if u == nil {
		return UploadState{Kind: uploadUnknown}
	}
	if len(u.modules) == 0 {
		return UploadState{Kind: uploadNone} // nothing to upload on this platform; 4CAT is not asked
	}
	st, err := os.Stat(capturesPath(u.root, user))
	if os.IsNotExist(err) || (err == nil && st.Size() == 0) {
		return UploadState{Kind: uploadNone}
	}
	if err != nil {
		log.Printf("web: captures file of %s: %v", user, err)
		return UploadState{Kind: uploadUnknown}
	}
	ups, ok := u.latest(ctx)
	if !ok {
		return UploadState{Kind: uploadUnknown}
	}
	return uploadState(st.ModTime().Truncate(time.Second), true, ups[user], u.modules)
}

// latest returns the cached uploads, reading 4CAT when the cache is older
// than uploadCacheFor; false means the read failed. The read keeps ctx's
// values but not its cancellation: its result serves everyone for a minute,
// so it must not fail because the request that started it went away.
func (u *uploadStates) latest(ctx context.Context) (fourcat.Uploads, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if !u.readAt.IsZero() && now.Sub(u.readAt) < uploadCacheFor {
		return u.ups, !u.failed
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadReadLimit)
	defer cancel()
	ups, err := u.reader.LatestUploads(readCtx)
	u.readAt, u.ups, u.failed = now, ups, err != nil
	if err != nil {
		log.Printf("web: 4CAT uploads: %v", err)
	}
	return u.ups, !u.failed
}

// when formats t for a reader at now, in the desk's zone: the time alone on
// the same day ("14:02"), the weekday with it within the six days before
// ("Mon 14:02"), and the date otherwise ("28 Oct 14:02").
func when(t, now time.Time) string {
	t, now = t.In(deskZone), now.In(deskZone)
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, deskZone) }
	// Whole days between the two dates; a day with a clock change is 23 or
	// 25 hours long, so round.
	switch days := int(math.Round(day(now).Sub(day(t)).Hours() / 24)); {
	case days == 0:
		return t.Format("15:04")
	case days >= 1 && days <= 6:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// uploadFooter is the inner HTML of the student's footer for an upload
// state. hint is the platform's upload_hint, trusted HTML, inserted as it
// is; nothing a person typed goes in. Only uploadDue warns: its note opens
// with an alert.
func uploadFooter(hint string, st UploadState, now time.Time) string {
	plain := "Before you leave: click <b>Z</b> in the browser's toolbar, " + hint + "."
	switch st.Kind {
	case uploadDue:
		return `<span class="alert">What you captured since ` + html.EscapeString(when(st.At, now)) + ` is not in 4CAT yet.</span> ` + plain
	case uploadDone:
		return "Uploaded to 4CAT at " + html.EscapeString(when(st.At, now)) + "."
	}
	return plain
}
