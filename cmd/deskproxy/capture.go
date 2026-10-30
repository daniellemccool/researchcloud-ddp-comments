package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// Meta is the per-collector context stamped on every record.
type Meta struct {
	User, Session, Platform, Kind string
}

// ReqInfo describes the request a record came from. Query holds only the
// rule's query_keep parameters that were present, first value each.
type ReqInfo struct {
	Method string            `json:"method"`
	Host   string            `json:"host"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
}

// RespInfo is the response status plus the rule's response_keep keys, copied
// raw from the top-level body object. It marshals flat: {"status":200,"k":v}.
type RespInfo struct {
	Status int
	Keep   map[string]json.RawMessage
}

// MarshalJSON writes the kept keys beside status.
func (r RespInfo) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(r.Keep)+1)
	for k, v := range r.Keep {
		m[k] = v
	}
	status, err := json.Marshal(r.Status)
	if err != nil {
		return nil, err
	}
	m["status"] = status
	return marshalRaw(m)
}

// marshalRaw is json.Marshal without HTML escaping, so raw upstream bytes
// (<, > and & inside item or body) are written exactly as decoded.
func marshalRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Record is one line of captures.ndjson. Item or Body holds the upstream
// object exactly as decoded (ADR-0010); Fields adds named columns beside an
// item and never replaces it.
type Record struct {
	T        int64                      `json:"t"`
	User     string                     `json:"user"`
	Session  string                     `json:"session"`
	Platform string                     `json:"platform"`
	Capture  string                     `json:"capture"`
	Kind     string                     `json:"kind"`
	PostID   string                     `json:"post_id"`
	Request  ReqInfo                    `json:"request"`
	Response RespInfo                   `json:"response"`
	Item     json.RawMessage            `json:"item,omitempty"`
	Fields   map[string]json.RawMessage `json:"fields,omitempty"`
	Body     json.RawMessage            `json:"body,omitempty"`
	Error    string                     `json:"error,omitempty"`
}

// requestHost is the bare host (no port) a request is addressed to.
func requestHost(req *http.Request) string {
	h := req.URL.Hostname()
	if h == "" {
		h = req.Host
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
	}
	return h
}

// matchRule returns the first capture rule whose host and path match req.
func matchRule(p *platform.Platform, req *http.Request) *platform.CaptureRule {
	if !p.HostMatches(requestHost(req)) {
		return nil
	}
	for i := range p.Captures {
		if p.Captures[i].Path.MatchString(req.URL.Path) {
			return &p.Captures[i]
		}
	}
	return nil
}

// recordsFor turns one decoded response body into records. It does no I/O.
// A problem with the body yields one error record, never a panic or nothing.
func recordsFor(rule *platform.CaptureRule, req *http.Request, resp *http.Response, body []byte, meta Meta) []Record {
	base := newRecord(rule, req, resp, meta)

	if int64(len(body)) > rule.MaxBodyBytes {
		return []Record{errorRecord(base, "body too large")}
	}

	var top map[string]json.RawMessage
	isObject := json.Unmarshal(body, &top) == nil && top != nil
	if isObject && len(rule.ResponseKeep) > 0 {
		base.Response.Keep = make(map[string]json.RawMessage, len(rule.ResponseKeep))
		for _, k := range rule.ResponseKeep {
			if v, ok := top[k]; ok {
				base.Response.Keep[k] = v
			}
		}
	}

	if rule.Items == "" {
		if !json.Valid(body) {
			return []Record{errorRecord(base, "body is not valid JSON")}
		}
		base.Body = json.RawMessage(body)
		return []Record{base}
	}

	if !isObject {
		if !json.Valid(body) {
			return []Record{errorRecord(base, "body is not valid JSON")}
		}
		return []Record{errorRecord(base, "body is not a JSON object")}
	}
	raw, ok := top[rule.Items]
	if !ok {
		return []Record{errorRecord(base, fmt.Sprintf("items key %q is missing", rule.Items))}
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil || elems == nil {
		what := "is not an array"
		if string(raw) == "null" {
			what = "is null"
		}
		return []Record{errorRecord(base, fmt.Sprintf("items key %q %s", rule.Items, what))}
	}

	recs := make([]Record, 0, len(elems))
	for _, el := range elems {
		r := base
		r.Item = el
		r.Fields = fieldsOf(rule.Fields, el)
		recs = append(recs, r)
	}
	return recs
}

func newRecord(rule *platform.CaptureRule, req *http.Request, resp *http.Response, meta Meta) Record {
	q := req.URL.Query()
	keep := make(map[string]string, len(rule.QueryKeep))
	for _, k := range rule.QueryKeep {
		if vs, ok := q[k]; ok && len(vs) > 0 {
			keep[k] = vs[0]
		}
	}
	return Record{
		T:        time.Now().UnixMilli(),
		User:     meta.User,
		Session:  meta.Session,
		Platform: meta.Platform,
		Capture:  rule.Name,
		Kind:     meta.Kind,
		PostID:   postID(rule, req, q.Get),
		Request: ReqInfo{
			Method: req.Method,
			Host:   requestHost(req),
			Path:   req.URL.Path,
			Query:  keep,
		},
		Response: RespInfo{Status: resp.StatusCode},
	}
}

// errorRecord keeps the envelope and drops anything of the body.
func errorRecord(base Record, msg string) Record {
	base.Item, base.Body, base.Fields = nil, nil, nil
	base.Error = msg
	return base
}

func postID(rule *platform.CaptureRule, req *http.Request, query func(string) string) string {
	switch rule.PostID.From {
	case "query":
		return query(rule.PostID.Param)
	case "path":
		m := rule.Path.FindStringSubmatch(req.URL.Path)
		if m == nil {
			return ""
		}
		for i, name := range rule.Path.SubexpNames() {
			if name == rule.PostID.Group && name != "" {
				return m[i]
			}
		}
	}
	return ""
}

// fieldsOf extracts each column of the rule's fields map from the element by
// dotted path. A path that is not present leaves its key out.
func fieldsOf(spec map[string]string, el json.RawMessage) map[string]json.RawMessage {
	if len(spec) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(spec))
	for col, path := range spec {
		if v, ok := lookup(el, path); ok {
			out[col] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func lookup(raw json.RawMessage, path string) (json.RawMessage, bool) {
	cur := raw
	for _, seg := range strings.Split(path, ".") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[seg]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// recordWriter appends records to captures.ndjson, one fsynced line each.
type recordWriter struct {
	mu sync.Mutex
	f  *os.File
}

func openRecordWriter(path string) (*recordWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &recordWriter{f: f}, nil
}

func (w *recordWriter) write(recs []Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range recs {
		b, err := marshalRaw(r)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if _, err := w.f.Write(b); err != nil {
			return err
		}
		if err := w.f.Sync(); err != nil {
			return err
		}
	}
	return nil
}
