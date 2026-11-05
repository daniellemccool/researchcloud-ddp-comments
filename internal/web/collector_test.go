package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// The real implementations satisfy the handler's small interfaces.
var (
	_ provisioner = (*provision.Provisioner)(nil)
	_ sessions    = (*session.Manager)(nil)
)

// testPlatform is the platform literal the collector tests run against:
// one kind "video" with an anchored id pattern, two problem codes.
func testPlatform() *platform.Platform {
	return &platform.Platform{
		Name:        "test",
		Label:       "TestTube",
		DefaultKind: "video",
		ItemKinds: map[string]platform.ItemKind{
			"video": {URL: "https://example.org/watch?v={id}", ID: regexp.MustCompile(`^[0-9]{1,20}$`)},
		},
		Problems: []platform.Problem{
			{Code: "NOT_FOUND", Label: "Not found", Note: false},
			{Code: "OTHER", Label: "Other", Note: true},
		},
		UploadHint: "upload them to 4CAT",
	}
}

type advanceCall struct {
	user  string
	admin bool
}

// fakeProvisioner records Advance calls; started/block/finished make the
// in-flight behaviour observable from the test.
type fakeProvisioner struct {
	mu        sync.Mutex
	calls     []advanceCall
	started   chan string
	block     chan struct{}
	finished  chan struct{}
	onAdvance func(user string)
	result    provision.State // returned by Advance when Steps is non-nil
}

func (f *fakeProvisioner) Advance(_ context.Context, user string, admin bool) provision.State {
	f.mu.Lock()
	f.calls = append(f.calls, advanceCall{user: user, admin: admin})
	f.mu.Unlock()
	if f.started != nil {
		f.started <- user
	}
	if f.onAdvance != nil {
		f.onAdvance(user)
	}
	if f.block != nil {
		<-f.block
	}
	if f.finished != nil {
		f.finished <- struct{}{}
	}
	if f.result.Steps != nil {
		return f.result
	}
	return provision.State{Steps: map[string]string{}}
}

func (f *fakeProvisioner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeProvisioner) firstCall() (advanceCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return advanceCall{}, false
	}
	return f.calls[0], true
}

type openCall struct {
	user, url string
}

// fakeSessions is the sessions fake: fixed Start result, fixed Ready flag,
// recorded OpenURL calls with a configurable exit code.
type fakeSessions struct {
	mu          sync.Mutex
	startResult session.StartResult
	ready       bool
	running     bool
	openRC      int
	openStderr  string
	openErr     error
	opens       []openCall
	starts      int
	onStart     func() // runs inside Start, before it returns
}

func (f *fakeSessions) Start(_ context.Context, _ string) (session.StartResult, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	if f.onStart != nil {
		f.onStart()
	}
	return f.startResult, nil
}

func (f *fakeSessions) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeSessions) Ready(_ context.Context, _ string) bool { return f.ready }

func (f *fakeSessions) IsRunning(_ context.Context, _ string) bool { return f.running }

func (f *fakeSessions) OpenURL(_ context.Context, user, url string) (int, string, error) {
	f.mu.Lock()
	f.opens = append(f.opens, openCall{user: user, url: url})
	f.mu.Unlock()
	return f.openRC, f.openStderr, f.openErr
}

func (f *fakeSessions) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opens)
}

type collectorEnv struct {
	srv  *http.Server
	root string
	cfg  *config.Config
	prov *fakeProvisioner
	sess *fakeSessions
	log  *desklog.Log
}

func newCollectorEnv(t *testing.T, fourcatEnabled bool) *collectorEnv {
	t.Helper()
	root := t.TempDir()
	plat := testPlatform()
	cfg := &config.Config{
		DeskRoot:           root,
		FQDN:               "desk.example.org",
		CollectorRole:      collectorRole,
		AdminRole:          adminRole,
		Contact:            testContact,
		Platform:           "test",
		BatchSizes:         []int{5, 10},
		TimerMinutes:       2,
		FourcatEnabled:     fourcatEnabled,
		ZeeschuimerVersion: "1.0",
	}
	prov := &fakeProvisioner{}
	sess := &fakeSessions{}
	dlog := &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind}
	ac := access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate}
	srv := New(Deps{
		Cfg:      cfg,
		Access:   ac,
		Platform: plat,
		LogPath:  filepath.Join(root, "state", "access.ndjson"),
		Prov:     prov,
		Sessions: sess,
		Log:      dlog,
	})
	return &collectorEnv{srv: srv, root: root, cfg: cfg, prov: prov, sess: sess, log: dlog}
}

// doMethod sends a gated request; a POST carries Sec-Fetch-Site: same-origin,
// as the collector page's own fetch() does.
func doMethod(t *testing.T, srv *http.Server, method, user, roles, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	access.SetTestHeaders(req, user, roles, testGate)
	if method == http.MethodPost {
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func (e *collectorEnv) get(t *testing.T, user, roles, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doMethod(t, e.srv, http.MethodGet, user, roles, path)
}

func (e *collectorEnv) post(t *testing.T, user, roles, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doMethod(t, e.srv, http.MethodPost, user, roles, path)
}

func writeProvisionState(t *testing.T, root, user string, steps map[string]string) {
	t.Helper()
	if err := writeStateFile(root, user, steps); err != nil {
		t.Fatalf("write provision state: %v", err)
	}
}

func writeStateFile(root, user string, steps map[string]string) error {
	data, err := json.Marshal(provision.State{Steps: steps})
	if err != nil {
		return err
	}
	path := filepath.Join(root, "state", "users", user, "provision.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func allDoneSteps() map[string]string {
	m := make(map[string]string, len(provision.Steps))
	for _, s := range provision.Steps {
		m[s] = "done"
	}
	return m
}

// makeTemplateDir creates <root>/templates/release/<generation>/ so the
// home handler's first check passes.
func makeTemplateDir(t *testing.T, e *collectorEnv) {
	t.Helper()
	dir := filepath.Join(e.root, "templates", "release", render.Generation(e.cfg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
}

func writePool(t *testing.T, e *collectorEnv, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, "pool.csv"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pool.csv: %v", err)
	}
}

func intPtr(i int) *int { return &i }

func notReadyText() string { return "The desk is not ready yet; ask " + testContact + "." }

func TestHomeNotReadyNoTemplate(t *testing.T) {
	e := newCollectorEnv(t, true)
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, notReadyText()) {
		t.Errorf("body %q lacks %q", b, notReadyText())
	}
}

func TestHomeNilDepsNotReady(t *testing.T) {
	root := t.TempDir()
	plat := testPlatform()
	cfg := &config.Config{
		DeskRoot:           root,
		CollectorRole:      collectorRole,
		AdminRole:          adminRole,
		Contact:            testContact,
		Platform:           "test",
		BatchSizes:         []int{5, 10},
		ZeeschuimerVersion: "1.0",
	}
	ac := access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate}
	// Prov and Sessions left nil, as cmd/desk wires them only in a later task.
	srv := New(Deps{
		Cfg:      cfg,
		Access:   ac,
		Platform: plat,
		LogPath:  filepath.Join(root, "state", "access.ndjson"),
		Log:      &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind},
	})
	rec := doMethod(t, srv, http.MethodGet, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, notReadyText()) {
		t.Errorf("body %q lacks %q", b, notReadyText())
	}
}

func TestHomeFullMessage(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{"registry": provision.RegistryFull})
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "The desk is full; ask " + testContact + "."
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("body %q lacks %q", b, want)
	}
	// The registry-full branch does not kick provisioning and carries no refresh.
	if b := rec.Body.String(); strings.Contains(b, "refresh") {
		t.Errorf("full branch body %q carries a refresh meta", b)
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.prov.callCount(); n != 0 {
		t.Errorf("Advance called %d times on the full branch, want 0", n)
	}
}

func TestHomeSetupFailedMessage(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{
		"registry": "done",
		"dirs":     "done",
		"template": "error: template missing",
	})
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "Setup failed at template; ask " + testContact + "."
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("body %q lacks %q", b, want)
	}
}

func TestHomeSetupPendingShowsNotReady(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{
		"registry":        "done",
		"dirs":            "done",
		"template":        "done",
		"fourcat_account": "error: " + fourcat.ErrSetupPending.Error(),
	})
	e.prov.started = make(chan string, 1)
	e.prov.block = make(chan struct{})
	e.prov.finished = make(chan struct{}, 1)

	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, notReadyText()) {
		t.Errorf("body %q lacks %q", b, notReadyText())
	}
	// F73: the setup-pending branch kicks provisioning (one Advance) while
	// still answering the not-ready message.
	<-e.prov.started
	close(e.prov.block)
	<-e.prov.finished
	if n := e.prov.callCount(); n != 1 {
		t.Errorf("Advance called %d times on the setup-pending branch, want 1", n)
	}
}

func TestHomeSetupFailedKicksAndRefreshes(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{
		"registry": "done",
		"dirs":     "done",
		"template": "error: template missing",
	})
	e.prov.started = make(chan string, 1)
	e.prov.block = make(chan struct{})
	e.prov.finished = make(chan struct{}, 1)

	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	want := "Setup failed at template; ask " + testContact + "."
	if !strings.Contains(b, want) {
		t.Errorf("body %q lacks %q", b, want)
	}
	// F79: the setup-failed answer carries the same refresh meta as the
	// provisioning answer.
	if !strings.Contains(b, `<meta http-equiv="refresh" content="3">`) {
		t.Errorf("body %q lacks the refresh meta", b)
	}
	// F79: the setup-failed branch kicks provisioning (one Advance).
	<-e.prov.started
	close(e.prov.block)
	<-e.prov.finished
	if n := e.prov.callCount(); n != 1 {
		t.Errorf("Advance called %d times on the setup-failed branch, want 1", n)
	}
}

func TestHomeSetupFailedHealsOnNextVisit(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{
		"registry": "done",
		"dirs":     "done",
		"template": "error: template missing: templates/release/test-1.0",
	})
	// The retried Advance succeeds: it returns a ready State and heals the
	// step file on disk.
	e.prov.result = provision.State{Steps: allDoneSteps()}
	e.prov.finished = make(chan struct{}, 1)
	e.prov.onAdvance = func(user string) {
		_ = writeStateFile(e.root, user, allDoneSteps())
	}
	e.sess.startResult = session.Started

	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("first visit: status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "Setup failed at template; ask "+testContact+".") {
		t.Errorf("first visit body %q lacks the failure text", b)
	}
	<-e.prov.finished // the kicked Advance has run and healed the step file

	rec2 := e.get(t, "alice", collectorRole, "/desk/")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second visit: status = %d, want 200", rec2.Code)
	}
	b2 := rec2.Body.String()
	if strings.Contains(b2, "Setup failed") {
		t.Errorf("second visit still shows the failure")
	}
	if !strings.Contains(b2, "<b>Your videos</b>") {
		t.Errorf("second visit did not render the collector page")
	}
	if n := e.sess.startCount(); n != 1 {
		t.Errorf("Start called %d times, want 1 (the second visit proceeded to Start)", n)
	}
}

func TestHomeProvisioningRefresh(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", map[string]string{}) // current = registry, no error
	e.prov.started = make(chan string, 1)
	e.prov.block = make(chan struct{})
	e.prov.finished = make(chan struct{}, 1)

	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("first visit: status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "Setting up your desk — registry") {
		t.Errorf("first visit body %q lacks the setting-up text", b)
	}
	if !strings.Contains(b, `<meta http-equiv="refresh" content="3">`) {
		t.Errorf("first visit body %q lacks the refresh meta", b)
	}
	<-e.prov.started // the Advance goroutine is in flight now

	rec2 := e.get(t, "alice", collectorRole, "/desk/")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second visit: status = %d, want 200", rec2.Code)
	}
	close(e.prov.block)
	<-e.prov.finished
	if n := e.prov.callCount(); n != 1 {
		t.Errorf("Advance called %d times for two quick visits, want 1", n)
	}
}

func TestHomeBusy(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	e.sess.startResult = session.Busy
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "The desk is busy, try again in a few minutes.") {
		t.Errorf("body %q lacks the busy text", b)
	}
}

func TestHomeStartFailed(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	e.sess.startResult = session.StartFailed
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "Your browser could not be started; try again in a minute or ask " + testContact + "."
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("body %q lacks %q", b, want)
	}
}

func TestHomeGoneRefresh(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	e.sess.startResult = session.Gone
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "Your desk is changing; this page reloads in a moment.") {
		t.Errorf("body %q lacks the gone text", b)
	}
	if !strings.Contains(b, `<meta http-equiv="refresh" content="3">`) {
		t.Errorf("body %q lacks the refresh meta", b)
	}
}

// readyPageEnv returns an env whose home renders the collector page for alice.
func readyPageEnv(t *testing.T, fourcatEnabled bool) *collectorEnv {
	t.Helper()
	e := newCollectorEnv(t, fourcatEnabled)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	e.sess.startResult = session.Started
	return e
}

func TestHomePageRendersRows(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,First clip\n222,video,b1,Second clip\n333,video,b2,\n444,video,b2,\n")
	if err := e.log.Append(
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""},
		desklog.Event{T: 2, User: "alice", Action: "claim", Kind: "video", ID: "222", Batch: 1, Code: ""},
		desklog.Event{T: 3, User: "alice", Action: "open", Kind: "video", ID: "111", Code: "", ExecRC: intPtr(0)},
		desklog.Event{T: 4, User: "alice", Action: "open", Kind: "video", ID: "222", Code: "", ExecRC: intPtr(0)},
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		"First clip",
		"Second clip",
		"Open next video",
		`<option value="NOT_FOUND">Not found</option>`,
		`<option value="OTHER">Other</option>`,
		"Get 5 more videos",
		"Get 10 more videos",
		"upload them to 4CAT", // the footer hint, FourcatEnabled
		`<ul class="rows">`,
		`data-state="done"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestHomePageFooterHintOnlyWhenFourcatEnabled(t *testing.T) {
	e := readyPageEnv(t, false)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,First clip\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); strings.Contains(b, "upload them to 4CAT") || strings.Contains(b, "<footer>") {
		t.Errorf("page shows the footer hint although 4CAT is disabled")
	}
}

func TestHomePageNoMore(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,First clip\n222,video,b1,Second clip\n")
	if err := e.log.Append(
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""},
		desklog.Event{T: 2, User: "alice", Action: "claim", Kind: "video", ID: "222", Batch: 1, Code: ""},
		desklog.Event{T: 3, User: "alice", Action: "open", Kind: "video", ID: "111", Code: "", ExecRC: intPtr(0)},
		desklog.Event{T: 4, User: "alice", Action: "open", Kind: "video", ID: "222", Code: "", ExecRC: intPtr(0)},
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "No more videos right now; tell the operator.") {
		t.Errorf("page lacks the no-more hint")
	}
}

func TestHomePageEarlierBatchAndFinishHint(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n333,video,b2,\n")
	if err := e.log.Append(
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""},
		desklog.Event{T: 2, User: "alice", Action: "claim", Kind: "video", ID: "222", Batch: 1, Code: ""},
		desklog.Event{T: 3, User: "alice", Action: "open", Kind: "video", ID: "111", Code: "", ExecRC: intPtr(0)},
		desklog.Event{T: 4, User: "alice", Action: "open", Kind: "video", ID: "222", Code: "", ExecRC: intPtr(0)},
		desklog.Event{T: 5, User: "alice", Action: "claim", Kind: "video", ID: "333", Batch: 2, Code: ""},
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		"2 done earlier",
		"Finish these first: open each one, or mark a problem. Then you can get more.",
		"Video 1", // no pool note → "<Kind> <n>" title
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(b, "Get 5 more videos") {
		t.Errorf("page offers more although shown rows are unfinished")
	}
}

func TestHomePageProblemMessageAndDisabledNext(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n")
	if err := e.log.Append(
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""},
		desklog.Event{T: 2, User: "alice", Action: "claim", Kind: "video", ID: "222", Batch: 1, Code: ""},
		desklog.Event{T: 3, User: "alice", Action: "open", Kind: "video", ID: "111", Code: "", ExecRC: intPtr(0)},
		desklog.Event{T: 4, User: "alice", Action: "problem", Kind: "video", ID: "222", Code: "OTHER", Note: "some <note>"},
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		`<div class="msg warn">— Other: some &lt;note&gt;</div>`, // the problem message, escaped
		`data-state="problem"`,
		`<button id="next" class="next" onclick="openNext()" disabled>Open next video</button>`, // all shown done
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestHomePageHelpOpenOnlyWhenNoClaims(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n")

	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, `<details open><summary>How to collect</summary>`) {
		t.Errorf("page without claims lacks the open help details")
	}

	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec2 := e.get(t, "alice", collectorRole, "/desk/")
	if b := rec2.Body.String(); !strings.Contains(b, `<details><summary>How to collect</summary>`) {
		t.Errorf("page with claims lacks the closed help details")
	}
}

func TestAdminGetsCollectorPageWithAdminLink(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "boss", map[string]string{})
	e.prov.finished = make(chan struct{}, 1)
	e.prov.onAdvance = func(user string) {
		_ = writeStateFile(e.root, user, allDoneSteps()) // the advance "completes" provisioning
	}

	rec := e.get(t, "boss", adminRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("first visit: status = %d, want 200", rec.Code)
	}
	<-e.prov.finished
	call, ok := e.prov.firstCall()
	if !ok || !call.admin || call.user != "boss" {
		t.Fatalf("Advance call = %+v, want boss with admin=true", call)
	}

	e.sess.startResult = session.Started
	rec2 := e.get(t, "boss", adminRole, "/desk/")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second visit: status = %d, want 200", rec2.Code)
	}
	if b := rec2.Body.String(); !strings.Contains(b, `<a href="/desk/admin/">admin</a>`) {
		t.Errorf("admin page lacks the admin link")
	}
}

func TestStatusJSON(t *testing.T) {
	e := newCollectorEnv(t, true)
	e.sess.ready = true
	rec := e.get(t, "alice", collectorRole, "/desk/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if b := rec.Body.String(); b != `{"ready":true}` {
		t.Errorf("body = %q, want {\"ready\":true}", b)
	}
}

func TestStatusNotReadyWhenFirefoxExited(t *testing.T) {
	e := newCollectorEnv(t, true)
	e.sess.ready = false
	rec := e.get(t, "alice", collectorRole, "/desk/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); b != `{"ready":false}` {
		t.Errorf("body = %q, want {\"ready\":false}", b)
	}
}

func TestClaimValidation(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	// Six rows: alice's busy claim below takes 111, bob claims the other five.
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n333,video,b1,\n444,video,b1,\n555,video,b1,\n666,video,b1,\n")
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writeProvisionState(t, e.root, "bob", allDoneSteps())
	writeProvisionState(t, e.root, "carol", allDoneSteps())

	// n not a batch size → 400; non-digits → 400.
	if rec := e.post(t, "alice", collectorRole, "/desk/claim?n=7"); rec.Code != http.StatusBadRequest {
		t.Errorf("n=7: status = %d, want 400", rec.Code)
	}
	if rec := e.post(t, "alice", collectorRole, "/desk/claim?n=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("n=abc: status = %d, want 400", rec.Code)
	}
	// Not provisioned → 409.
	writeProvisionState(t, e.root, "alice", map[string]string{})
	if rec := e.post(t, "alice", collectorRole, "/desk/claim?n=5"); rec.Code != http.StatusConflict {
		t.Errorf("not ready: status = %d, want 409", rec.Code)
	}
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	// Unfinished claims → busy → 409.
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if rec := e.post(t, "alice", collectorRole, "/desk/claim?n=5"); rec.Code != http.StatusConflict {
		t.Errorf("busy: status = %d, want 409", rec.Code)
	}
	// OK: default n (BatchSizes[0]) → 204 and the log grew by 5 claims.
	before := countClaims(t, e, "bob")
	if rec := e.post(t, "bob", collectorRole, "/desk/claim"); rec.Code != http.StatusNoContent {
		t.Errorf("ok: status = %d, want 204", rec.Code)
	}
	if after := countClaims(t, e, "bob"); after-before != 5 {
		t.Errorf("bob's claims grew by %d, want 5", after-before)
	}
	// Pool exhausted → 409.
	if rec := e.post(t, "carol", collectorRole, "/desk/claim?n=5"); rec.Code != http.StatusConflict {
		t.Errorf("empty: status = %d, want 409", rec.Code)
	}
}

func countClaims(t *testing.T, e *collectorEnv, user string) int {
	t.Helper()
	evs, err := e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.User == user && ev.Action == "claim" {
			n++
		}
	}
	return n
}

func TestOpenOnlyClaimed(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Unclaimed → 404 and no OpenURL call.
	if rec := e.post(t, "alice", collectorRole, "/desk/open?id=222"); rec.Code != http.StatusNotFound {
		t.Errorf("unclaimed: status = %d, want 404", rec.Code)
	}
	if n := e.sess.openCount(); n != 0 {
		t.Errorf("OpenURL called %d times for an unclaimed item, want 0", n)
	}
	// Invalid id / unknown kind → 404.
	if rec := e.post(t, "alice", collectorRole, "/desk/open?id=abc"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status = %d, want 404", rec.Code)
	}
	if rec := e.post(t, "alice", collectorRole, "/desk/open?kind=photo&id=111"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind: status = %d, want 404", rec.Code)
	}

	// Claimed, rc 0 → 204 and an open event with exec_rc 0.
	e.sess.openRC = 0
	if rec := e.post(t, "alice", collectorRole, "/desk/open?id=111"); rec.Code != http.StatusNoContent {
		t.Errorf("rc 0: status = %d, want 204", rec.Code)
	}
	if n := e.sess.openCount(); n != 1 {
		t.Fatalf("OpenURL called %d times, want 1", n)
	}
	evs, err := e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var open *desklog.Event
	for i := range evs {
		if evs[i].Action == "open" {
			open = &evs[i]
		}
	}
	if open == nil || open.ID != "111" || open.ExecRC == nil || *open.ExecRC != 0 {
		t.Errorf("open event = %+v, want id 111 with exec_rc 0", open)
	}

	// rc 1 → 500 and exec_rc 1.
	e.sess.openRC = 1
	if rec := e.post(t, "alice", collectorRole, "/desk/open?id=111"); rec.Code != http.StatusInternalServerError {
		t.Errorf("rc 1: status = %d, want 500", rec.Code)
	}
	evs, err = e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Action != "open" || last.ExecRC == nil || *last.ExecRC != 1 {
		t.Errorf("last event = %+v, want open with exec_rc 1", last)
	}
}

func TestOpenNilSessions503(t *testing.T) {
	e := newCollectorEnv(t, true)
	// Sessions left nil, as until the next task wires it (ruling F60).
	srv := New(Deps{
		Cfg:      e.cfg,
		Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
		Platform: testPlatform(),
		LogPath:  filepath.Join(e.root, "state", "access.ndjson"),
		Prov:     e.prov,
		Log:      e.log,
	})
	// An unclaimed id: the nil guard fires before the claimed check, so the
	// answer is 503, not 404.
	rec := doMethod(t, srv, http.MethodPost, "alice", collectorRole, "/desk/open?id=999")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if b := rec.Body.String(); b != "desk not ready" {
		t.Errorf("body = %q, want %q", b, "desk not ready")
	}
}

// makeLogUnwritable makes appends to the log fail while keeping the test
// runnable as root: root ignores permission bits, so a directory goes in
// place of the file (reads fail too); a non-root run just removes write
// permission, which keeps reads working and exercises the append failure.
func makeLogUnwritable(t *testing.T, logPath string) {
	t.Helper()
	if os.Geteuid() == 0 {
		if err := os.Remove(logPath); err != nil {
			t.Fatalf("remove log: %v", err)
		}
		if err := os.Mkdir(logPath, 0o755); err != nil {
			t.Fatalf("mkdir over log: %v", err)
		}
		return
	}
	if err := os.Chmod(logPath, 0o444); err != nil {
		t.Fatalf("chmod log: %v", err)
	}
}

func TestOpenAndProblemUnwritableLog500(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	makeLogUnwritable(t, filepath.Join(e.root, "state", "desk-log.ndjson"))

	if rec := e.post(t, "alice", collectorRole, "/desk/open?id=111"); rec.Code != http.StatusInternalServerError {
		t.Errorf("open: status = %d, want 500", rec.Code)
	} else if b := rec.Body.String(); !strings.Contains(b, "could not") {
		t.Errorf("open: body = %q, want a plain could-not message", b)
	}
	q := url.Values{"id": {"111"}, "code": {"OTHER"}, "note": {"x"}}
	if rec := e.post(t, "alice", collectorRole, "/desk/problem?"+q.Encode()); rec.Code != http.StatusInternalServerError {
		t.Errorf("problem: status = %d, want 500", rec.Code)
	} else if b := rec.Body.String(); !strings.Contains(b, "could not") {
		t.Errorf("problem: body = %q, want a plain could-not message", b)
	}
}

func TestOpenTransportErrorRecordsErr(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	e.sess.openErr = errors.New("docker exec failed: boom")
	rec := e.post(t, "alice", collectorRole, "/desk/open?id=111")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	evs, err := e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Action != "open" || last.ExecRC == nil || *last.ExecRC != -1 || last.ExecErr != "docker exec failed: boom" {
		t.Errorf("last event = %+v, want open with exec_rc -1 and exec_err the transport error", last)
	}
}

func TestProblemCodes(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	problem := func(q url.Values) *httptest.ResponseRecorder {
		return e.post(t, "alice", collectorRole, "/desk/problem?"+q.Encode())
	}

	// Lower-case code upper-cases to a known code → 204, event code NOT_FOUND.
	if rec := problem(url.Values{"id": {"111"}, "code": {"not_found"}}); rec.Code != http.StatusNoContent {
		t.Errorf("not_found: status = %d, want 204", rec.Code)
	}
	evs, err := e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Action != "problem" || last.Code != "NOT_FOUND" {
		t.Errorf("last event = %+v, want problem NOT_FOUND", last)
	}

	// A note-requiring code without a note → 400.
	if rec := problem(url.Values{"id": {"111"}, "code": {"OTHER"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("OTHER without note: status = %d, want 400", rec.Code)
	}
	// A 400-rune note is cut to 300 runes.
	long := strings.Repeat("a", 400)
	if rec := problem(url.Values{"id": {"111"}, "code": {"OTHER"}, "note": {long}}); rec.Code != http.StatusNoContent {
		t.Errorf("OTHER with note: status = %d, want 204", rec.Code)
	}
	evs, err = e.log.Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	last = evs[len(evs)-1]
	if last.Code != "OTHER" || len([]rune(last.Note)) != 300 {
		t.Errorf("note = %d runes, code %q; want 300 runes, OTHER", len([]rune(last.Note)), last.Code)
	}
	// Unknown code → 400.
	if rec := problem(url.Values{"id": {"111"}, "code": {"WHATEVER"}, "note": {"x"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown code: status = %d, want 400", rec.Code)
	}
	// Unclaimed item → 404.
	if rec := problem(url.Values{"id": {"222"}, "code": {"OTHER"}, "note": {"x"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unclaimed: status = %d, want 404", rec.Code)
	}
}

func TestPageEscapesUserContent(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,<script>alert(1)</script>\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("page lacks the escaped note")
	}
	if strings.Contains(b, "<script>alert(1)</script>") {
		t.Errorf("page contains the raw note")
	}
}

func TestNonAdminForbiddenOnAdminAndFourcat(t *testing.T) {
	e := newCollectorEnv(t, true)
	for _, path := range []string{"/desk/admin/", "/fourcat/"} {
		rec := e.get(t, "alice", collectorRole, path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as collector: status = %d, want 403", path, rec.Code)
		}
		if b := rec.Body.String(); !strings.Contains(b, testContact) {
			t.Errorf("GET %s: body %q lacks the contact", path, b)
		}
	}
	if rec := e.post(t, "alice", collectorRole, "/desk/admin/pool"); rec.Code != http.StatusForbidden {
		t.Errorf("POST /desk/admin/pool as collector: status = %d, want 403", rec.Code)
	}
	// Admins reach the routes; with the Deps handlers still nil they are 404.
	for _, path := range []string{"/desk/admin/", "/fourcat/"} {
		if rec := e.get(t, "boss", adminRole, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as admin: status = %d, want 404 (handler nil)", path, rec.Code)
		}
	}
	if rec := e.post(t, "boss", adminRole, "/desk/admin/pool"); rec.Code != http.StatusNotFound {
		t.Errorf("POST /desk/admin/pool as admin: status = %d, want 404 (handler nil)", rec.Code)
	}
	if rec := e.get(t, "alice", collectorRole, "/desk/vnc/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /desk/vnc/: status = %d, want 404 (relay nil)", rec.Code)
	}
}

func TestUnknownRoute404(t *testing.T) {
	e := newCollectorEnv(t, true)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/desk/xyz"},
		{http.MethodGet, "/other"},
		{http.MethodPost, "/desk/nope"},
	} {
		rec := doMethod(t, e.srv, c.method, "alice", collectorRole, c.path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

func TestKindsAdapterAnchors(t *testing.T) {
	p := &platform.Platform{
		DefaultKind: "video",
		ItemKinds: map[string]platform.ItemKind{
			"video": {URL: "https://example.org/{id}", ID: regexp.MustCompile(`[0-9]+`)},
			"any":   {URL: "https://example.org/any/{id}"}, // nil ID regexp
		},
	}
	k := newPlatformKinds(p)
	if k.Default() != "video" {
		t.Errorf("Default() = %q, want video", k.Default())
	}
	re, ok := k.Kind("video")
	if !ok {
		t.Fatal("Kind(video) not found")
	}
	if !re.MatchString("123") {
		t.Errorf("pattern %q does not match \"123\"", re)
	}
	if re.MatchString("a1b") {
		t.Errorf("pattern %q matches \"a1b\"; the adapter must anchor the platform pattern", re)
	}
	if _, ok := k.Kind("photo"); ok {
		t.Errorf("Kind(photo) found, want not found")
	}
	if _, ok := k.Kind("any"); ok {
		t.Errorf("Kind(any) with a nil ID regexp found, want unknown (false), never match-everything")
	}
}

// postWith POSTs path as alice with extra headers set on the request.
func postWith(t *testing.T, srv *http.Server, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	access.SetTestHeaders(req, "alice", collectorRole, testGate)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

// The collector page may be framed by the desk's own origin only (review M2).
func TestHomeFrameAncestorsSelf(t *testing.T) {
	e := readyPageEnv(t, true)
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n")
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("page: status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("page: Content-Security-Policy = %q, want %q", got, "frame-ancestors 'self'")
	}
	// A message answer of the same route carries it too.
	e2 := newCollectorEnv(t, true)
	rec = e2.get(t, "alice", collectorRole, "/desk/")
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("message: Content-Security-Policy = %q, want %q", got, "frame-ancestors 'self'")
	}
}

// A cross-site POST to a collector route is refused before it has any effect;
// the page's own same-origin fetch passes (review M2, the admin's csrfOK).
func TestCollectorPostsRefuseCrossSite(t *testing.T) {
	e := newCollectorEnv(t, true)
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	writePool(t, e, "id,kind,bucket,note\n111,video,b1,\n222,video,b1,\n")
	if err := e.log.Append(desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "111", Batch: 1, Code: ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	refused := []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
		{"Origin": "https://evil.example"},
		{}, // neither header
	}
	paths := []string{"/desk/claim?n=5", "/desk/open?id=111", "/desk/problem?id=111&code=NOT_FOUND"}
	before, err := e.log.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		for _, h := range refused {
			if rec := postWith(t, e.srv, p, h); rec.Code != http.StatusForbidden {
				t.Errorf("%s with %v: status = %d, want 403", p, h, rec.Code)
			}
		}
	}
	after, err := e.log.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("refused posts changed the desk log: %d events, want %d", len(after), len(before))
	}
	if n := e.sess.openCount(); n != 0 {
		t.Errorf("refused posts ran OpenURL %d times, want 0", n)
	}
	// The page's own fetch: Sec-Fetch-Site same-origin, or (an older
	// browser) an Origin equal to the Host.
	if rec := postWith(t, e.srv, "/desk/open?id=111", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusNoContent {
		t.Errorf("same-origin open: status = %d, want 204", rec.Code)
	}
	if rec := postWith(t, e.srv, "/desk/open?id=111", map[string]string{"Origin": "http://example.com"}); rec.Code != http.StatusNoContent {
		t.Errorf("same-host Origin open: status = %d, want 204", rec.Code)
	}
}

// activeEnv is a collectorEnv whose server carries a fake-clock Activity.
func newActiveEnv(t *testing.T) (*collectorEnv, *Activity, *time.Time) {
	t.Helper()
	e := newCollectorEnv(t, false)
	clock := time.Unix(1_000_000, 0)
	act := NewActivity(func() time.Time { return clock })
	e.srv.Handler.(*handler).activity = act
	return e, act, &clock
}

func TestActiveRecordsInputAndAnswersRunning(t *testing.T) {
	e, act, clock := newActiveEnv(t)
	e.sess.running = true
	act.Idle("alice", time.Hour) // creates the entry at t0
	*clock = clock.Add(10 * time.Minute)
	if idle, _ := act.Idle("alice", 5*time.Minute); !idle {
		t.Fatal("setup: alice should be idle before the heartbeat")
	}
	rec := e.post(t, "alice", collectorRole, "/desk/active")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"running":true}` {
		t.Fatalf("active = %d %q, want 200 {\"running\":true}", rec.Code, rec.Body.String())
	}
	if idle, why := act.Idle("alice", 5*time.Minute); idle && why == "no input" {
		t.Errorf("alice idle (%s) right after /desk/active; want fresh input", why)
	}
}

func TestActiveNotRunning(t *testing.T) {
	e, _, _ := newActiveEnv(t)
	rec := e.post(t, "alice", collectorRole, "/desk/active")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"running":false}` {
		t.Fatalf("active = %d %q, want 200 {\"running\":false}", rec.Code, rec.Body.String())
	}
}

func TestActiveRefusesCrossSite(t *testing.T) {
	e, act, clock := newActiveEnv(t)
	e.sess.running = true
	act.Idle("alice", time.Hour)
	*clock = clock.Add(10 * time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/desk/active", nil)
	access.SetTestHeaders(req, "alice", collectorRole, testGate)
	rec := httptest.NewRecorder()
	e.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site active = %d, want 403", rec.Code)
	}
	if idle, _ := act.Idle("alice", 5*time.Minute); !idle {
		t.Error("a refused request recorded input")
	}
}

func TestActiveWithoutActivityStillAnswers(t *testing.T) {
	e := newCollectorEnv(t, false)
	e.sess.running = true
	rec := e.post(t, "alice", collectorRole, "/desk/active")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"running":true}` {
		t.Fatalf("active = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHomeStartedCountsAsInput(t *testing.T) {
	e, act, clock := newActiveEnv(t)
	e.sess.startResult = session.Started
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	makeTemplateDir(t, e)
	const idle = 15 * time.Minute
	// Stale clocks: a screen opened then closed, then no input for longer than idle.
	closed := act.openScreen("alice", func() {})
	closed()
	*clock = clock.Add(idle + time.Hour)
	if gone, _ := act.Idle("alice", idle); !gone {
		t.Fatal("setup: alice should be idle")
	}
	if rec := e.get(t, "alice", collectorRole, "/desk/"); rec.Code != http.StatusOK {
		t.Fatalf("home = %d", rec.Code)
	}
	*clock = clock.Add(idle - time.Second)
	if gone, why := act.Idle("alice", idle); gone {
		t.Errorf("alice idle (%s) %v after a Started home visit; want both clocks restarted", why, idle-time.Second)
	}
}
