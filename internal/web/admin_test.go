package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

// newAdminServer builds the gated server with the Admin handler mounted and
// returns it with the temp desk root (for pool.csv and the desk log).
func newAdminServer(t *testing.T) (*http.Server, string) {
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
	d.Admin = NewAdmin(d)
	return New(d), root
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

func TestAdminQueueLine(t *testing.T) {
	srv, root := newAdminServer(t)
	poolPath := filepath.Join(root, "pool.csv")
	if err := os.WriteFile(poolPath, []byte("id,kind,bucket,note\n1,video,,a\n2,video,,b\n3,video,,c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lg := &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: "video"}
	if err := lg.Append(desklog.Event{T: 1, User: "root", Action: "claim", Kind: "video", ID: "1", Batch: 1, Code: ""}); err != nil {
		t.Fatal(err)
	}

	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		`<h2>Queue</h2>`,
		`<p>2 of 3 videos unclaimed in the pool.</p>`,
		`<form method="post" action="/desk/admin/pool" enctype="multipart/form-data"><label>Add to the pool (CSV with an id column; kind, bucket, note optional): <input type="file" name="file" accept=".csv,text/csv" required></label> <button>Upload</button></form>`,
		`<p>Sessions, 4CAT, export and problems arrive in plan 2b.</p>`,
		`<title>Desk admin</title>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("admin page lacks %q:\n%s", want, b)
		}
	}

	// An unreadable pool.csv shows the error line instead.
	if err := os.WriteFile(poolPath, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	b = rec.Body.String()
	if !strings.Contains(b, "pool.csv unreadable: pool.csv: unrecognised header [a b]") {
		t.Errorf("unreadable pool line missing from:\n%s", b)
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

// The overview shows the CO poll status line, as a warning when it alarms,
// and no line at all without a poller.
func TestAdminOverviewShowsPollLine(t *testing.T) {
	newServer := func(m interface {
		Absent(string) bool
		StatusLine() string
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
	if want := `<h2>Members</h2><p>CO poll: last good 12:34, 0 failures</p>`; !strings.Contains(b, want) {
		t.Errorf("overview lacks %q:\n%s", want, b)
	}

	b = getAdmin(t, newServer(fakeMembers{line: "ALARM: CO poll: no good poll yet, 3 failures <x>"}), "root", adminRole, "/desk/admin/").Body.String()
	if want := `<p class="warn">ALARM: CO poll: no good poll yet, 3 failures &lt;x&gt;</p>`; !strings.Contains(b, want) {
		t.Errorf("overview lacks the alarm line %q:\n%s", want, b)
	}

	b = getAdmin(t, newServer(nil), "root", adminRole, "/desk/admin/").Body.String()
	if strings.Contains(b, "CO poll") || strings.Contains(b, "<h2>Members</h2>") {
		t.Errorf("overview without a poller shows a poll line:\n%s", b)
	}
}
