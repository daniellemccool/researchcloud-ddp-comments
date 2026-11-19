package web

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// newAdminServer builds the gated server with the Admin handler mounted and
// returns it with the temp desk root (for pool.csv and the desk log).
func newAdminServer(t *testing.T) (*http.Server, string) {
	t.Helper()
	return newAdminServerWith(t, nil)
}

// newAdminServerWith is newAdminServer with fill free to set further Deps
// (and fields of Cfg) before the admin handler and the server are built.
func newAdminServerWith(t *testing.T, fill func(*Deps)) (*http.Server, string) {
	t.Helper()
	root := t.TempDir()
	plat := testPlatform()
	cfg := &config.Config{
		DeskRoot:      root,
		FQDN:          "desk.example.org",
		CollectorRole: collectorRole,
		AdminRole:     adminRole,
		Contact:       testContact,
		Platform:      "test",
	}
	d := Deps{
		Cfg:      cfg,
		Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
		Platform: plat,
		LogPath:  filepath.Join(root, "state", "access.ndjson"),
		Log:      &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind},
	}
	if fill != nil {
		fill(&d)
	}
	d.Admin = NewAdmin(d)
	return New(d), root
}

// fakeAdminSessions stands in for *session.Manager on the admin page and
// counts what was asked of it.
type fakeAdminSessions struct {
	rows      []session.Row
	statusErr error
	seats     []registry.Row
	seatsErr  error
	stopErr   error

	mu          sync.Mutex
	statusCalls int
	seatsCalls  int
	stops       []string
	// For each Stop call, at the time of the call: whether the context
	// had already ended, and whether it had a deadline.
	stopCtxEnded    []bool
	stopCtxDeadline []bool
}

func (f *fakeAdminSessions) Status(context.Context) ([]session.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.rows, f.statusErr
}

func (f *fakeAdminSessions) Seats() ([]registry.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seatsCalls++
	return f.seats, f.seatsErr
}

func (f *fakeAdminSessions) Stop(ctx context.Context, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, user)
	_, hasDeadline := ctx.Deadline()
	f.stopCtxEnded = append(f.stopCtxEnded, ctx.Err() != nil)
	f.stopCtxDeadline = append(f.stopCtxDeadline, hasDeadline)
	return f.stopErr
}

// appendLog appends events to the desk log under root.
func appendLog(t *testing.T, root string, evs ...desklog.Event) {
	t.Helper()
	lg := &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: "video"}
	if err := lg.Append(evs...); err != nil {
		t.Fatal(err)
	}
}

// uploadBody builds a multipart form with one file part (field may be ""
// for no part at all) and returns (content-type, body).
func uploadBody(t *testing.T, field, content string) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if field != "" {
		fw, err := mw.CreateFormFile(field, "pool.csv")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return mw.FormDataContentType(), &buf
}

// postPool sends POST /desk/admin/pool as the admin with the extra headers;
// the Content-Length header is set explicitly from the body (the handler
// reads only the header).
func postPool(t *testing.T, srv *http.Server, hdrs map[string]string, ctype string, body *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	return postPoolCL(t, srv, hdrs, ctype, body, strconv.Itoa(body.Len()))
}

// postPoolCL is postPool with an explicit Content-Length header value; cl ==
// "" sends no Content-Length header at all.
func postPoolCL(t *testing.T, srv *http.Server, hdrs map[string]string, ctype string, body *bytes.Buffer, cl string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/desk/admin/pool", body)
	access.SetTestHeaders(req, "root", adminRole, testGate)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	if cl != "" {
		req.Header.Set("Content-Length", cl)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func getAdmin(t *testing.T, srv *http.Server, user, roles, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, srv, http.MethodGet, user, roles, path, nil, nil)
}

func TestAdminHeaders(t *testing.T) {
	srv, _ := newAdminServer(t)
	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /desk/admin/: status = %d, want 200", rec.Code)
	}
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("GET header %s = %q, want %q", k, got, want)
		}
	}
	// The same three headers on a 403 from the admin handler (bad CSRF).
	ctype, body := uploadBody(t, "file", "id\n1\n")
	rec = postPool(t, srv, map[string]string{"Sec-Fetch-Site": "cross-site"}, ctype, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status = %d, want 403", rec.Code)
	}
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("403 header %s = %q, want %q", k, got, want)
		}
	}
}

func TestAdminCSRF(t *testing.T) {
	srv, _ := newAdminServer(t)

	// Sec-Fetch-Site: same-origin → accepted (303 after the upload).
	ctype, body := uploadBody(t, "file", "id\n1\n")
	rec := postPool(t, srv, map[string]string{"Sec-Fetch-Site": "same-origin"}, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("same-origin: status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	// Sec-Fetch-Site: cross-site → refused.
	ctype, body = uploadBody(t, "file", "id\n1\n")
	rec = postPool(t, srv, map[string]string{"Sec-Fetch-Site": "cross-site"}, ctype, body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status = %d, want 403", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "Refused: this request did not come from the admin page.") {
		t.Errorf("cross-site body %q lacks the refusal text", b)
	}
	// No Sec-Fetch-Site, Origin matching Host (httptest's Host is example.com) → accepted.
	ctype, body = uploadBody(t, "file", "id\n1\n")
	rec = postPool(t, srv, map[string]string{"Origin": "https://example.com"}, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("matching Origin: status = %d, want 303", rec.Code)
	}
	// No Sec-Fetch-Site, mismatching Origin → refused.
	ctype, body = uploadBody(t, "file", "id\n1\n")
	rec = postPool(t, srv, map[string]string{"Origin": "https://evil.example"}, ctype, body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatching Origin: status = %d, want 403", rec.Code)
	}
	// Neither header → refused.
	ctype, body = uploadBody(t, "file", "id\n1\n")
	rec = postPool(t, srv, map[string]string{}, ctype, body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("neither header: status = %d, want 403", rec.Code)
	}
}

func TestAdminUploadLimits(t *testing.T) {
	srv, _ := newAdminServer(t)
	csrf := map[string]string{"Sec-Fetch-Site": "same-origin"}
	fresh := func() (string, *bytes.Buffer) { return uploadBody(t, "file", "id\n1\n") }

	// No Content-Length header at all → 411, as the Python did.
	ctype, body := fresh()
	rec := postPoolCL(t, srv, csrf, ctype, body, "")
	if rec.Code != http.StatusLengthRequired {
		t.Errorf("missing Content-Length: status = %d, want 411", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "Content-Length required") {
		t.Errorf("411 body %q lacks the text", b)
	}
	// Non-integer Content-Length → 411.
	ctype, body = fresh()
	rec = postPoolCL(t, srv, csrf, ctype, body, "abc")
	if rec.Code != http.StatusLengthRequired {
		t.Errorf("non-integer Content-Length: status = %d, want 411", rec.Code)
	}
	// Negative Content-Length → 400.
	ctype, body = fresh()
	rec = postPoolCL(t, srv, csrf, ctype, body, "-1")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("negative Content-Length: status = %d, want 400", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "bad Content-Length") {
		t.Errorf("400 body %q lacks the text", b)
	}
	// Over 5 MiB → 413.
	ctype, body = fresh()
	rec = postPoolCL(t, srv, csrf, ctype, body, "6291456")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("6 MiB Content-Length: status = %d, want 413", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "upload larger than 5 MB") {
		t.Errorf("413 body %q lacks the text", b)
	}
	// Not a multipart form → 400.
	rec = postPool(t, srv, csrf, "text/plain", bytes.NewBufferString("id\n1\n"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("text/plain: status = %d, want 400", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "expected a multipart form") {
		t.Errorf("400 body %q lacks the text", b)
	}
	// Multipart without a file part → 400.
	ctype2, body2 := uploadBody(t, "other", "id\n1\n")
	rec = postPool(t, srv, csrf, ctype2, body2)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no file field: status = %d, want 400", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "no file field in the form") {
		t.Errorf("400 body %q lacks the text", b)
	}
}

func TestAdminUploadNotices(t *testing.T) {
	srv, root := newAdminServer(t)
	csrf := map[string]string{"Sec-Fetch-Site": "same-origin"}

	// A header without an id column: nothing added, notice shown once.
	ctype, body := uploadBody(t, "file", "name\nx\n")
	rec := postPool(t, srv, csrf, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("bad header: status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/desk/admin/" {
		t.Errorf("Location = %q, want /desk/admin/", loc)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	want := `<p class="notice" role="status">nothing added: line 1: header has no id column</p>`
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("GET after bad header: body lacks %q:\n%s", want, b)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	if b := rec.Body.String(); strings.Contains(b, `class="notice"`) {
		t.Errorf("second GET still shows a notice: %q", b)
	}

	// Two good rows and one empty id: added 2 of 3 with the reject line.
	ctype, body = uploadBody(t, "file", "id,note\n1,a\n,b\n3,c\n")
	rec = postPool(t, srv, csrf, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rejects: status = %d, want 303", rec.Code)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	want = `added 2 of 3; rejected lines: 3: id is empty`
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("GET after rejects: body lacks %q:\n%s", want, b)
	}

	// A clean upload: added 2 of 2; no rejects.
	ctype, body = uploadBody(t, "file", "id\n7\n8\n")
	rec = postPool(t, srv, csrf, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clean: status = %d, want 303", rec.Code)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	want = `added 2 of 2; no rejects`
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("GET after clean upload: body lacks %q:\n%s", want, b)
	}
	rows, err := pool.Read(filepath.Join(root, "pool.csv"), "video")
	if err != nil {
		t.Fatalf("pool.Read: %v", err)
	}
	if len(rows) != 4 {
		t.Errorf("pool has %d rows, want 4 (1, 3, 7, 8)", len(rows))
	}

	// 21 rejects: the first 20 are listed, then "(and 1 more)".
	var sb strings.Builder
	sb.WriteString("id,note\n")
	for i := 0; i < 21; i++ {
		sb.WriteString(",x\n")
	}
	ctype, body = uploadBody(t, "file", sb.String())
	rec = postPool(t, srv, csrf, ctype, body)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("21 rejects: status = %d, want 303", rec.Code)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	b := rec.Body.String()
	if !strings.Contains(b, "added 0 of 21; rejected lines: 2: id is empty") {
		t.Errorf("21 rejects: body lacks the start of the reject list:\n%s", b)
	}
	if !strings.Contains(b, "(and 1 more)") {
		t.Errorf("21 rejects: body lacks %q:\n%s", "(and 1 more)", b)
	}
}

// TestAdminCollectionSection: the collection's two lines under the
// platform's name, the upload form as it was, and no placeholder sentence;
// the same with the pool read on every view and through the cache.
func TestAdminCollectionSection(t *testing.T) {
	poolWays(t, 3, adminCollectionSection)
}

func adminCollectionSection(t *testing.T, fill func(*Deps)) {
	srv, root := newAdminServerWith(t, fill)
	poolPath := filepath.Join(root, "pool.csv")
	if err := os.WriteFile(poolPath, []byte("id,kind,bucket,note\n1,video,,a\n2,video,,b\n3,video,,c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendLog(t, root,
		desklog.Event{T: 1, User: "root", Action: "claim", Kind: "video", ID: "1", Batch: 1, Code: ""},
		desklog.Event{T: 2, User: "alice", Action: "claim", Kind: "video", ID: "2", Batch: 1, Code: ""},
		desklog.Event{T: 3, User: "alice", Action: "problem", Kind: "video", ID: "2", Code: "NOT_FOUND"},
	)
	captures := filepath.Join(root, "state", "captures", "alice", "captures.ndjson")
	if err := os.MkdirAll(filepath.Dir(captures), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(captures, []byte("{}\n{}\n{}\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		`<h2>Collection: TestTube</h2>`,
		`<p>Queue 3 videos · unclaimed 1 · claimed 2 · done 1 (1 problem) · open 1</p>`,
		`<p>Records captured 4 · collectors who took part 2</p>`,
		`<form method="post" action="/desk/admin/pool" enctype="multipart/form-data"><label>Add to the pool (CSV with an id column; kind, bucket, note optional): <input type="file" name="file" accept=".csv,text/csv" required></label> <button>Upload</button></form>`,
		`<title>Desk admin</title>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("admin page lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "arrive in plan 2b") || strings.Contains(b, "<h2>Queue</h2>") || strings.Contains(b, "<h2>Members</h2>") {
		t.Errorf("admin page still has the old headings or the placeholder sentence:\n%s", b)
	}
	// The sections come in a fixed order.
	last := -1
	for _, h := range []string{"<h2>Collection: ", "<h2>Problems</h2>", "<h2>Health</h2>"} {
		i := strings.Index(b, h)
		if i <= last {
			t.Errorf("section %q is missing or out of order (at %d, after %d)", h, i, last)
		}
		last = i
	}
	// A second view of the unchanged pool reads the same.
	queue := `<p>Queue 3 videos · unclaimed 1 · claimed 2 · done 1 (1 problem) · open 1</p>`
	if b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String(); !strings.Contains(b, queue) {
		t.Errorf("second view lacks %q:\n%s", queue, b)
	}

	// An unreadable pool.csv shows the error line instead of the queue line;
	// the rest of the page is still there.
	if err := os.WriteFile(poolPath, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	b = rec.Body.String()
	if !strings.Contains(b, "pool.csv unreadable: pool.csv: unrecognised header [a b]") {
		t.Errorf("unreadable pool line missing from:\n%s", b)
	}
	if !strings.Contains(b, "<h2>Problems</h2>") || !strings.Contains(b, "Records captured 4") {
		t.Errorf("with an unreadable pool the rest of the page is gone:\n%s", b)
	}
}

// logFailServer builds the admin server with a pool of three, one running
// seat (alice), a CO list with a collector who never claimed (chris) and the
// desk log at the path setLog returns for the desk root.
func logFailServer(t *testing.T, setLog func(t *testing.T, root string) string) *http.Server {
	t.Helper()
	ready := provision.State{Steps: allDoneSteps()}
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		root := d.Cfg.DeskRoot
		if err := os.WriteFile(filepath.Join(root, "pool.csv"), []byte("id,kind,bucket,note\n1,video,,a\n2,video,,b\n3,video,,c\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		d.Log.Path = setLog(t, root)
		d.AdminSessions = &fakeAdminSessions{rows: []session.Row{{User: "alice", Index: 1, Live: true, Running: true, State: ready}}}
		d.Members = fakeMembers{known: true, members: map[string][]string{"chris": {collectorRole}}}
	})
	return srv
}

// An unreadable desk log is not an empty one: the page says at its top that
// the log cannot be read (without the error text), and every figure that
// comes from the log reads unknown. The pool total and the Stop button,
// which do not come from the log, still show. A readable empty log is no
// failure: no warning, and the zeros stay.
func TestAdminUnreadableDeskLog(t *testing.T) {
	const warning = `<p class="warn">The desk log cannot be read, so the figures that come from it are unknown.</p>`
	stop := `<form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="alice"><button>Stop</button></form>`
	for _, tc := range []struct {
		name   string
		setLog func(t *testing.T, root string) string
		secret string // a part of the error text that must not reach the page
	}{
		{"parent is a regular file", func(t *testing.T, root string) string {
			f := filepath.Join(root, "notadir")
			if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(f, "desk-log.ndjson")
		}, "not a directory"},
		{"malformed line in the middle", func(t *testing.T, root string) string {
			p := filepath.Join(root, "desk-log.ndjson")
			data := `{"t":1,"user":"alice","action":"claim","kind":"video","id":"1"}` + "\n" +
				`{"t":2,"user":"bob","act` + "\n" +
				`{"t":3,"user":"alice","action":"problem","kind":"video","id":"1","code":"NOT_FOUND"}` + "\n"
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}, "line 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := logFailServer(t, tc.setLog)
			rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			b := rec.Body.String()
			for _, want := range []string{
				`<h1>Desk admin</h1>` + warning + `<h2>Collection: `,
				`<p>Queue 3 videos · unclaimed unknown · claimed unknown · done unknown (problems unknown) · open unknown</p>`,
				` · collectors who took part unknown</p>`,
				`<h2>Problems</h2><p>unknown: the desk log cannot be read</p>`,
				`<tr><td>alice</td><td>unknown</td><td>unknown</td><td>running ` + stop + `</td></tr>`,
				`<p>Hold the collector role, never claimed: unknown: the desk log cannot be read</p>`,
				`<p>Worked here, no longer have access: unknown: the desk log cannot be read</p>`,
				`<h2>Health</h2>`,
			} {
				if !strings.Contains(b, want) {
					t.Errorf("page lacks %q:\n%s", want, b)
				}
			}
			for _, bad := range []string{"No problems reported.", "0:00", "chris", "claimed 0", tc.secret} {
				if strings.Contains(b, bad) {
					t.Errorf("page with an unreadable desk log has %q:\n%s", bad, b)
				}
			}
			if n := strings.Count(b, `class="warn"`); n != 1 {
				t.Errorf("page has %d warnings, want 1:\n%s", n, b)
			}
		})
	}

	t.Run("readable empty log", func(t *testing.T) {
		srv := logFailServer(t, func(t *testing.T, root string) string {
			p := filepath.Join(root, "desk-log.ndjson")
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		})
		b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
		for _, want := range []string{
			`<p>Queue 3 videos · unclaimed 3 · claimed 0 · done 0 (0 problems) · open 0</p>`,
			` · collectors who took part 0</p>`,
			`<h2>Problems</h2><p>No problems reported.</p>`,
			`<tr><td>alice</td><td>0:00</td><td>0:00</td><td>running ` + stop + `</td></tr>`,
			`<p>Hold the collector role, never claimed: chris</p>`,
			`<p>Worked here, no longer have access: alice (removed)</p>`,
		} {
			if !strings.Contains(b, want) {
				t.Errorf("page lacks %q:\n%s", want, b)
			}
		}
		if strings.Contains(b, "desk log cannot be read") || strings.Contains(b, `class="warn"`) {
			t.Errorf("page with a readable empty desk log has a warning:\n%s", b)
		}
	})
}

// TestAdminProblemsNewestFirst: every problem report, newest first, with
// the linked item, the platform's label for the code, the note, the student
// and the time in Amsterdam time. The clock stands at Tuesday 10:00.
func TestAdminProblemsNewestFirst(t *testing.T) {
	srv, root := newAdminServerWith(t, func(d *Deps) { d.Now = func() time.Time { return tueTenAmsterdam } })
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if !strings.Contains(b, "<h2>Problems</h2><p>No problems reported.</p>") {
		t.Errorf("page without reports lacks the empty problems line:\n%s", b)
	}

	appendLog(t, root,
		desklog.Event{T: monAfternoon, User: "alice", Action: "problem", Kind: "video", ID: "111", Code: "NOT_FOUND"},
		desklog.Event{T: tueNine, User: "bob", Action: "problem", Kind: "video", ID: "222", Code: "OTHER", Note: "kept spinning"},
		desklog.Event{T: tueNine, User: "bob", Action: "open", Kind: "video", ID: "333", ExecRC: rc(0)},
	)
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	newer := `<tr><td><a href="https://example.org/watch?v=222">video 222</a></td><td>Other</td><td>kept spinning</td><td>bob</td><td>09:00</td></tr>`
	older := `<tr><td><a href="https://example.org/watch?v=111">video 111</a></td><td>Not found</td><td></td><td>alice</td><td>Mon 14:00</td></tr>`
	i, j := strings.Index(b, newer), strings.Index(b, older)
	if i < 0 || j < 0 {
		t.Fatalf("problems table lacks a row (newer at %d, older at %d):\n%s", i, j, b)
	}
	if i > j {
		t.Error("the older report comes before the newer one")
	}
	if strings.Contains(b, "333") {
		t.Error("an open event shows in the problems table")
	}

	// The order is by the report's time, not its place in the log: a report
	// written last but timed Monday 13:00 comes last.
	appendLog(t, root, desklog.Event{T: monAfternoon - 3600, User: "carol", Action: "problem", Kind: "video", ID: "444", Code: "NOT_FOUND"})
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	oldest := `<tr><td><a href="https://example.org/watch?v=444">video 444</a></td><td>Not found</td><td></td><td>carol</td><td>Mon 13:00</td></tr>`
	if k := strings.Index(b, oldest); k < 0 || k < strings.Index(b, older) {
		t.Errorf("the report timed Monday 13:00 is missing or not last (at %d):\n%s", k, b)
	}
}

// TestAdminEscapesWhatPeopleTyped: a note, an item id and a user name that
// hold HTML are shown as text, and an id the platform cannot open gets no
// link at all.
func TestAdminEscapesWhatPeopleTyped(t *testing.T) {
	srv, root := newAdminServer(t)
	logPath := filepath.Join(root, "state", "desk-log.ndjson")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"t":1,"user":"<i>","action":"problem","kind":"video","id":"\"><b>","code":"<u>","note":"<script>x</script>"}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	for _, raw := range []string{"<script>x</script>", `"><b>`, "<i>", "<u>"} {
		if strings.Contains(b, raw) {
			t.Errorf("the page holds %q raw:\n%s", raw, b)
		}
	}
	for _, want := range []string{"&lt;script&gt;x&lt;/script&gt;", "video &#34;&gt;&lt;b&gt;", "&lt;i&gt;", "&lt;u&gt;"} {
		if !strings.Contains(b, want) {
			t.Errorf("the page lacks the escaped %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, `<a href="https://example.org/watch`) {
		t.Errorf("an item id the platform cannot open was linked:\n%s", b)
	}
}

// TestAdminHealthSeatsLine: pairs running of the cap, the configured value
// and what memory allows, and the live seats of the session limit.
func TestAdminHealthSeatsLine(t *testing.T) {
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "alice", Index: 1, Running: true, Live: true},
		{User: "bob", Index: 2, Running: false, Live: true},
		{User: "carol", Index: 3, Running: false, Live: false},
	}}
	seats := SeatFigures{Configured: 3}
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		d.Cfg.MaxRunning, d.Cfg.MaxSessions = 3, 99
		d.AdminSessions = sess
		d.Seats = seats
	})
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p>Seats: 1 of 3 running (configured 3; memory allows unknown) · registered 2 of 99</p>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}

	// The cap lowered by memory: configured 7, memory allows 3.
	srv, _ = newAdminServerWith(t, func(d *Deps) {
		d.Cfg.MaxRunning, d.Cfg.MaxSessions = 3, 99
		d.AdminSessions = sess
		d.Seats = SeatFigures{Configured: 7, MemoryAllows: 3}
	})
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p>Seats: 1 of 3 running (configured 7; memory allows 3) · registered 2 of 99</p>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}

	// Docker not answering: the line says so, the live seats come from the
	// registry, and the rest of the page still renders.
	failing := &fakeAdminSessions{statusErr: errors.New("docker: no answer"), seats: []registry.Row{
		{User: "alice", Index: 1, Status: "live"}, {User: "bob", Index: 2, Status: "live"}, {User: "carol", Index: 3, Status: "retired"},
	}}
	srv, _ = newAdminServerWith(t, func(d *Deps) {
		d.Cfg.MaxRunning, d.Cfg.MaxSessions = 3, 99
		d.AdminSessions = failing
		d.Seats = seats
	})
	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	b = rec.Body.String()
	if want := `<p class="warn">Seats: the running count is unavailable; at most 3 may run (configured 3; memory allows unknown) · registered 2 of 99</p>`; rec.Code != http.StatusOK || !strings.Contains(b, want) {
		t.Errorf("status %d; page lacks %q:\n%s", rec.Code, want, b)
	}
	if !strings.Contains(b, "<h2>Collection: TestTube</h2>") || !strings.Contains(b, "<h2>Problems</h2>") {
		t.Errorf("with Docker not answering the rest of the page is gone:\n%s", b)
	}

	// No session data wired: no seats line.
	srv, _ = newAdminServer(t)
	if b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String(); strings.Contains(b, "Seats:") {
		t.Errorf("page without session data shows a seats line:\n%s", b)
	}
}

// hangingSessions is a Docker that takes the status call and never answers:
// Status blocks until its context ends and returns the context's error.
type hangingSessions struct{ fakeAdminSessions }

func (h *hangingSessions) Status(ctx context.Context) ([]session.Row, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestAdminStatusHasATimeLimit: a status call that never answers is cut off
// by the page's own time limit and becomes the "running count is
// unavailable" line; the rest of the page renders. The limit is shortened
// for the test, and the request's own deadline is far longer, so a page
// that waits for the request to end is caught by the elapsed time.
func TestAdminStatusHasATimeLimit(t *testing.T) {
	sess := &hangingSessions{fakeAdminSessions{seats: []registry.Row{
		{User: "alice", Index: 1, Status: "live"}, {User: "bob", Index: 2, Status: "retired"},
	}}}
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		d.Cfg.MaxRunning, d.Cfg.MaxSessions = 3, 99
		d.AdminSessions = sess
		d.Seats = SeatFigures{Configured: 3, MemoryAllows: 3}
	})
	srv.Handler.(*handler).admin.(*adminHandler).statusLimit = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/desk/admin/", nil).WithContext(ctx)
	access.SetTestHeaders(req, "root", adminRole, testGate)
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.Handler.ServeHTTP(rec, req)
	if took := time.Since(start); took > time.Second {
		t.Errorf("the page took %v: it waited for the request to end instead of its own time limit", took.Round(time.Millisecond))
	}
	b := rec.Body.String()
	if want := `<p class="warn">Seats: the running count is unavailable; at most 3 may run (configured 3; memory allows 3) · registered 1 of 99</p>`; rec.Code != http.StatusOK || !strings.Contains(b, want) {
		t.Errorf("status %d; page lacks %q:\n%s", rec.Code, want, b)
	}
	for _, h := range []string{"<h2>Collection: TestTube</h2>", "<h2>Problems</h2>", "<h2>Health</h2>"} {
		if !strings.Contains(b, h) {
			t.Errorf("page lacks %q:\n%s", h, b)
		}
	}
}

func TestAdminFourcatLinksOnlyWhenEnabled(t *testing.T) {
	srv, _ := newAdminServerWith(t, func(d *Deps) { d.Cfg.FourcatEnabled = true })
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p>4CAT: <a href="/results/">datasets</a> · <a href="/admin/">status</a> · <a href="/logs/">logs</a></p>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	srv, _ = newAdminServer(t)
	if b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String(); strings.Contains(b, "4CAT:") {
		t.Errorf("page without 4CAT shows the 4CAT links:\n%s", b)
	}
}

func TestAdminRedirectNoSlash(t *testing.T) {
	srv, _ := newAdminServer(t)
	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /desk/admin: status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/desk/admin/" {
		t.Errorf("Location = %q, want /desk/admin/", loc)
	}
	// The 301 answer carries the three admin headers too.
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("301 header %s = %q, want %q", k, got, want)
		}
	}
}

func TestAdminForbiddenForCollector(t *testing.T) {
	srv, _ := newAdminServer(t)
	rec := getAdmin(t, srv, "dmccool2", collectorRole, "/desk/admin/")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("collector GET /desk/admin/: status = %d, want 403", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, testContact) {
		t.Errorf("403 body %q lacks the contact %q", b, testContact)
	}
	// The non-admin 403 carries the three admin headers too.
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("403 header %s = %q, want %q", k, got, want)
		}
	}
	// The same for the POST route.
	ctype, body := uploadBody(t, "file", "id\n1\n")
	req := httptest.NewRequest(http.MethodPost, "/desk/admin/pool", body)
	access.SetTestHeaders(req, "dmccool2", collectorRole, testGate)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Length", strconv.Itoa(body.Len()))
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("collector POST /desk/admin/pool: status = %d, want 403", rec.Code)
	}
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("POST 403 header %s = %q, want %q", k, got, want)
		}
	}
}

func TestAdminConcurrentUploadsLoseNothing(t *testing.T) {
	srv, root := newAdminServer(t)

	// 10 concurrent uploads of disjoint ids through the same admin handler;
	// pool.Add is a read-modify-write, so the handler must hold its lock (F75).
	const n = 10
	type upload struct {
		ctype string
		body  *bytes.Buffer
	}
	ups := make([]upload, n)
	for i := range ups {
		ups[i].ctype, ups[i].body = uploadBody(t, "file", "id\n"+strconv.Itoa(100+i)+"\n")
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/desk/admin/pool", ups[i].body)
			access.SetTestHeaders(req, "root", adminRole, testGate)
			req.Header.Set("Content-Type", ups[i].ctype)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Content-Length", strconv.Itoa(ups[i].body.Len()))
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusSeeOther {
			t.Errorf("upload %d: status = %d, want 303", i, c)
		}
	}
	rows, err := pool.Read(filepath.Join(root, "pool.csv"), "video")
	if err != nil {
		t.Fatalf("pool.Read: %v", err)
	}
	if len(rows) != n {
		t.Errorf("pool has %d rows, want all %d: %v", len(rows), n, rows)
	}
}

// The CO poll status line sits under Health, as a warning when it alarms,
// and there is no line at all without a poller.
func TestAdminOverviewShowsPollLine(t *testing.T) {
	newServer := func(m interface {
		Absent(string) bool
		StatusLine() string
		Members() (map[string][]string, bool)
	}) *http.Server {
		root := t.TempDir()
		plat := testPlatform()
		d := Deps{
			Cfg:      &config.Config{DeskRoot: root, CollectorRole: collectorRole, AdminRole: adminRole, Contact: testContact, Platform: "test"},
			Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
			Platform: plat,
			LogPath:  filepath.Join(root, "state", "access.ndjson"),
			Log:      &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind},
			Members:  m,
		}
		d.Admin = NewAdmin(d)
		return New(d)
	}

	b := getAdmin(t, newServer(fakeMembers{line: "CO poll: last good 12:34, 0 failures"}), "root", adminRole, "/desk/admin/").Body.String()
	if want := `<h2>Health</h2><p>CO poll: last good 12:34, 0 failures</p>`; !strings.Contains(b, want) {
		t.Errorf("overview lacks %q:\n%s", want, b)
	}

	b = getAdmin(t, newServer(fakeMembers{line: "ALARM: CO poll: no good poll yet, 3 failures <x>"}), "root", adminRole, "/desk/admin/").Body.String()
	if want := `<h2>Health</h2><p class="warn">ALARM: CO poll: no good poll yet, 3 failures &lt;x&gt;</p>`; !strings.Contains(b, want) {
		t.Errorf("overview lacks the alarm line %q:\n%s", want, b)
	}

	b = getAdmin(t, newServer(nil), "root", adminRole, "/desk/admin/").Body.String()
	if strings.Contains(b, "CO poll") || !strings.Contains(b, "<h2>Health</h2>") {
		t.Errorf("overview without a poller shows a poll line:\n%s", b)
	}
}

// fakeRetention is the retention pass's view: a fixed line and its failure.
type fakeRetention struct {
	line   string
	failed bool
}

func (f fakeRetention) StatusLine() (string, bool) { return f.line, f.failed }

// The snapshot retention line closes Health, escaped and as a warning after
// a failed pass; there is no line at all without the pass.
func TestAdminOverviewShowsRetentionLine(t *testing.T) {
	newServer := func(r retentionView) *http.Server {
		root := t.TempDir()
		plat := testPlatform()
		d := Deps{
			Cfg:       &config.Config{DeskRoot: root, CollectorRole: collectorRole, AdminRole: adminRole, Contact: testContact, Platform: "test"},
			Access:    access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
			Platform:  plat,
			LogPath:   filepath.Join(root, "state", "access.ndjson"),
			Log:       &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind},
			Members:   fakeMembers{line: "CO poll: last good 12:34, 0 failures"},
			Retention: r,
		}
		d.Admin = NewAdmin(d)
		return New(d)
	}
	for _, c := range []struct {
		r    fakeRetention
		want string
	}{
		{fakeRetention{line: "Snapshot retention: not run yet"},
			`<p>CO poll: last good 12:34, 0 failures</p><p>Snapshot retention: not run yet</p>`},
		{fakeRetention{line: "Snapshot retention: last run 2026-11-18 12:00, 9 snapshots read, 3 marked to expire, 2 unmarked"},
			`<p>Snapshot retention: last run 2026-11-18 12:00, 9 snapshots read, 3 marked to expire, 2 unmarked</p>`},
		{fakeRetention{line: "Snapshot retention: failed at 2026-11-18 12:00 <x>", failed: true},
			`<p class="warn">Snapshot retention: failed at 2026-11-18 12:00 &lt;x&gt;</p>`},
		{fakeRetention{line: "Snapshot retention (dry run): last run 2026-11-18 12:00, 9 snapshots read, 3 would be marked to expire, 2 would be unmarked"},
			`<p>CO poll: last good 12:34, 0 failures</p><p>Snapshot retention (dry run): last run 2026-11-18 12:00, 9 snapshots read, 3 would be marked to expire, 2 would be unmarked</p>`},
		{fakeRetention{line: "Snapshot retention (dry run): failed at 2026-11-18 12:00", failed: true},
			`<p class="warn">Snapshot retention (dry run): failed at 2026-11-18 12:00</p>`},
	} {
		b := getAdmin(t, newServer(c.r), "root", adminRole, "/desk/admin/").Body.String()
		if !strings.Contains(b, c.want) {
			t.Errorf("overview lacks %q:\n%s", c.want, b)
		}
	}

	b := getAdmin(t, newServer(nil), "root", adminRole, "/desk/admin/").Body.String()
	if strings.Contains(b, "Snapshot retention") || !strings.Contains(b, "<h2>Health</h2>") {
		t.Errorf("overview without the pass shows a retention line:\n%s", b)
	}
}

// postStop sends POST /desk/admin/stop with the form body as user with
// roles and the extra headers.
func postStop(t *testing.T, srv *http.Server, user, roles string, hdrs map[string]string, form string) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range hdrs {
		h[k] = v
	}
	return doReq(t, srv, http.MethodPost, user, roles, "/desk/admin/stop", strings.NewReader(form), h)
}

// deskEvents reads the desk log under root; none when it does not exist.
func deskEvents(t *testing.T, root string) []desklog.Event {
	t.Helper()
	evs, err := (&desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: "video"}).Read()
	if err != nil {
		t.Fatalf("read desk log: %v", err)
	}
	return evs
}

// wantAdminHeaders checks the framing and no-store headers of an answer.
func wantAdminHeaders(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	for k, want := range map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: header %s = %q, want %q", what, k, got, want)
		}
	}
}

// stopSessions is a seat list with alice running and bob stopped, both live.
func stopSessions() *fakeAdminSessions {
	ready := provision.State{Steps: allDoneSteps()}
	return &fakeAdminSessions{
		rows: []session.Row{
			{User: "alice", Index: 1, Live: true, Running: true, State: ready},
			{User: "bob", Index: 2, Live: true, Running: false, State: ready},
		},
		seats: []registry.Row{{User: "alice", Index: 1, Status: "live"}, {User: "bob", Index: 2, Status: "live"}},
	}
}

// An admin's same-origin stop stops the pair once, writes a stop event with
// code admin and the admin's name, restarts the user's idle clocks and
// returns to the page with a one-shot notice.
func TestAdminStopStopsAndLogs(t *testing.T) {
	sess := stopSessions()
	clock := &fakeClock{t: time.Date(2026, 11, 17, 8, 52, 30, 0, time.UTC)}
	act := NewActivity(clock.now)
	act.Input("alice")
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Now = func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) }
		d.AdminSessions = sess
		d.Activity = act
	})
	appendLog(t, root, desklog.Event{T: 1794900000, User: "alice", Action: "claim", Kind: "video", ID: "1"})

	rec := postStop(t, srv, "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("stop: status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/desk/admin/" {
		t.Errorf("stop: Location = %q, want /desk/admin/", got)
	}
	wantAdminHeaders(t, "stop", rec)
	if len(sess.stops) != 1 || sess.stops[0] != "alice" {
		t.Errorf("stops = %q, want [alice]", sess.stops)
	}
	if sess.statusCalls != 0 {
		t.Errorf("the stop asked for the session status %d times, want 0", sess.statusCalls)
	}
	evs := deskEvents(t, root)
	if len(evs) != 2 {
		t.Fatalf("desk log has %d events, want 2: %+v", len(evs), evs)
	}
	want := desklog.Event{T: 1794906000, User: "alice", Action: "stop", Code: "admin", Note: "by root"}
	if got := evs[1]; got != want {
		t.Errorf("last event = %+v, want %+v", got, want)
	}
	if _, ok := act.LastInput("alice"); ok {
		t.Errorf("alice's idle clocks were not restarted after the stop")
	}

	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p class="notice" role="status">stopped alice</p>`; !strings.Contains(b, want) {
		t.Errorf("page after the stop lacks %q:\n%s", want, b)
	}
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if strings.Contains(b, "stopped alice") {
		t.Errorf("the notice was shown twice:\n%s", b)
	}
}

// A stop that passed every refusal runs to its end when the admin's request
// has gone away: Stop gets a context that has not ended and has its own
// time limit, and the event, the restart of the idle clocks and the notice
// follow as usual.
func TestAdminStopOutlivesTheRequest(t *testing.T) {
	sess := stopSessions()
	clock := &fakeClock{t: time.Date(2026, 11, 17, 8, 52, 30, 0, time.UTC)}
	act := NewActivity(clock.now)
	act.Input("alice")
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Now = func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) }
		d.AdminSessions = sess
		d.Activity = act
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the admin's request is already gone
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/desk/admin/stop", strings.NewReader("user=alice"))
	access.SetTestHeaders(req, "root", adminRole, testGate)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("stop: status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if len(sess.stops) != 1 || sess.stops[0] != "alice" {
		t.Fatalf("stops = %q, want [alice]", sess.stops)
	}
	if sess.stopCtxEnded[0] {
		t.Errorf("Stop was given a context that had already ended")
	}
	if !sess.stopCtxDeadline[0] {
		t.Errorf("Stop was given a context without a time limit")
	}
	evs := deskEvents(t, root)
	want := desklog.Event{T: 1794906000, User: "alice", Action: "stop", Code: "admin", Note: "by root"}
	if len(evs) != 1 || evs[0] != want {
		t.Errorf("desk log = %+v, want [%+v]", evs, want)
	}
	if _, ok := act.LastInput("alice"); ok {
		t.Errorf("alice's idle clocks were not restarted after the stop")
	}
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p class="notice" role="status">stopped alice</p>`; !strings.Contains(b, want) {
		t.Errorf("page after the stop lacks %q:\n%s", want, b)
	}
}

// A stop is refused, with no Docker call and no log event, without the
// same-origin mark, for a name outside the grammar, for a name with no
// registry row, for an oversized body and for a non-admin.
func TestAdminStopRefusals(t *testing.T) {
	cases := []struct {
		name, user, roles string
		hdrs              map[string]string
		form              string
		code              int
		text              string
	}{
		{"cross-site", "root", adminRole, map[string]string{"Sec-Fetch-Site": "cross-site"}, "user=alice", 403, "Refused: this request did not come from the admin page."},
		{"no same-origin mark", "root", adminRole, nil, "user=alice", 403, "Refused: this request did not come from the admin page."},
		{"bad name", "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=Bad+User", 400, "Refused: not a username."},
		{"no name", "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "", 400, "Refused: not a username."},
		{"no registry row", "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=zoe", 400, "Refused: zoe has no seat."},
		{"oversized body", "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice&pad=" + strings.Repeat("x", 5000), 413, "Refused: the request is too large."},
		{"collector", "anna", collectorRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice", 403, testContact},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sess := stopSessions()
			srv, root := newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
			rec := postStop(t, srv, c.user, c.roles, c.hdrs, c.form)
			if rec.Code != c.code {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			if b := rec.Body.String(); !strings.Contains(b, c.text) {
				t.Errorf("body %q lacks %q", b, c.text)
			}
			wantAdminHeaders(t, c.name, rec)
			if len(sess.stops) != 0 || sess.statusCalls != 0 {
				t.Errorf("a refused stop called Stop %q and Status %d times", sess.stops, sess.statusCalls)
			}
			if evs := deskEvents(t, root); len(evs) != 0 {
				t.Errorf("a refused stop wrote %+v", evs)
			}
		})
	}

	// No session data wired: the route answers 404, like an unknown admin path.
	srv, root := newAdminServer(t)
	rec := postStop(t, srv, "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice")
	if rec.Code != http.StatusNotFound {
		t.Errorf("without session data: status = %d, want 404", rec.Code)
	}
	wantAdminHeaders(t, "without session data", rec)
	if evs := deskEvents(t, root); len(evs) != 0 {
		t.Errorf("a stop without session data wrote %+v", evs)
	}
}

// A failed stop returns to the page with the error as the notice and
// writes no event.
func TestAdminStopFailureWritesNoEvent(t *testing.T) {
	sess := stopSessions()
	sess.stopErr = errors.New("docker: container desk-proxy-alice is not ours")
	srv, root := newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	rec := postStop(t, srv, "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("failed stop: status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if len(sess.stops) != 1 {
		t.Errorf("stops = %q, want one", sess.stops)
	}
	if evs := deskEvents(t, root); len(evs) != 0 {
		t.Errorf("a failed stop wrote %+v", evs)
	}
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p class="notice" role="status">could not stop alice: docker: container desk-proxy-alice is not ours</p>`; !strings.Contains(b, want) {
		t.Errorf("page after the failed stop lacks %q:\n%s", want, b)
	}
}

// A stop that worked stands when the desk log cannot be written, and the
// notice says the log lacks it.
func TestAdminStopStandsWhenLogFails(t *testing.T) {
	sess := stopSessions()
	srv, _ := newAdminServerWith(t, func(d *Deps) {
		d.AdminSessions = sess
		// A directory where the log file should be: every append fails.
		p := filepath.Join(d.Cfg.DeskRoot, "state", "desk-log.ndjson")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	})
	rec := postStop(t, srv, "root", adminRole, map[string]string{"Sec-Fetch-Site": "same-origin"}, "user=alice")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("stop: status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if len(sess.stops) != 1 || sess.stops[0] != "alice" {
		t.Errorf("stops = %q, want [alice]", sess.stops)
	}
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p class="notice" role="status">stopped alice, but the desk log could not record the stop</p>`; !strings.Contains(b, want) {
		t.Errorf("page after the stop lacks %q:\n%s", want, b)
	}
}

// The Stop button follows Running: a running pair has it, also while its
// provisioning is not ready; a stopped pair has none, and neither has any
// row while the session status is unknown. The name in the form is escaped.
func TestStopButtonOnlyOnRunning(t *testing.T) {
	ready := provision.State{Steps: allDoneSteps()}
	notReady := provision.State{Steps: map[string]string{"registry": "done", "dirs": "done"}}
	sess := &fakeAdminSessions{rows: []session.Row{
		{User: "alice", Index: 1, Live: true, Running: true, State: ready},
		{User: "bob", Index: 2, Live: true, Running: false, State: ready},
		{User: "carol", Index: 3, Live: true, Running: true, State: notReady},
		{User: `x"<y>&z`, Index: 4, Live: true, Running: true, State: ready},
	}}
	srv, _ := newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	b := getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	for _, want := range []string{
		`<tr><td>alice</td><td>0:00</td><td>0:00</td><td>running <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="alice"><button>Stop</button></form></td></tr>`,
		`<tr><td>bob</td><td>0:00</td><td>0:00</td><td>stopped</td></tr>`,
		`<tr><td>carol</td><td>0:00</td><td>0:00</td><td>at template <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="carol"><button>Stop</button></form></td></tr>`,
		`<td>running <form method="post" action="/desk/admin/stop"><input type="hidden" name="user" value="x&#34;&lt;y&gt;&amp;z"><button>Stop</button></form></td></tr>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(b, `action="/desk/admin/stop"`); n != 3 {
		t.Errorf("page has %d Stop forms, want 3:\n%s", n, b)
	}

	sess = &fakeAdminSessions{statusErr: errors.New("docker: no answer"), seats: []registry.Row{{User: "alice", Index: 1, Status: "live"}}}
	srv, _ = newAdminServerWith(t, func(d *Deps) { d.AdminSessions = sess })
	b = getAdmin(t, srv, "root", adminRole, "/desk/admin/").Body.String()
	if want := `<tr><td>alice</td><td>0:00</td><td>0:00</td><td>unknown</td></tr>`; !strings.Contains(b, want) {
		t.Errorf("page lacks %q:\n%s", want, b)
	}
	if strings.Contains(b, "/desk/admin/stop") {
		t.Errorf("a row with an unknown session has a Stop button:\n%s", b)
	}
}
