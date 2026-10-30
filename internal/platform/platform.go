// Package platform loads and validates platform definitions
// (platforms/<name>/platform.json) strictly: an unknown key is an error.
package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// defaultMaxBodyBytes caps a captured response body when a rule sets no limit.
const defaultMaxBodyBytes = 16 << 20

// PostIDSpec says where a capture finds the id of the post it belongs to.
type PostIDSpec struct {
	From  string // "query" or "path"
	Param string // query parameter name, when From is "query"
	Group string // named regexp group, when From is "path"
}

// CaptureRule describes one kind of response to keep.
type CaptureRule struct {
	Name         string
	Path         *regexp.Regexp
	Items        string
	ID           string
	PostID       PostIDSpec
	QueryKeep    []string
	ResponseKeep []string
	Fields       map[string]string
	NoStream     bool
	MaxBodyBytes int64
}

// ItemKind is one kind of item a researcher can list: a URL template and the
// pattern its id must match.
type ItemKind struct {
	URL string
	ID  *regexp.Regexp
}

// CookieSpec names a cookie that survives a profile reset.
type CookieSpec struct {
	Host, Name string
}

// Problem is a code a collector can record against an item.
type Problem struct {
	Code, Label string
	Note        bool
}

// Platform is a loaded platform definition.
type Platform struct {
	Name, Label        string
	ZeeschuimerModules []string
	UploadHint         string
	ItemKinds          map[string]ItemKind
	DefaultKind        string
	Hosts              []string
	OpenPattern        *regexp.Regexp
	Captures           []CaptureRule
	Login              string
	KeepCookies        []CookieSpec
	Problems           []Problem
	Dir                string
}

type rawItemKind struct {
	URL string `json:"url"`
	ID  string `json:"id"`
}

type rawCookie struct {
	Host string `json:"host"`
	Name string `json:"name"`
}

type rawProblem struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Note  bool   `json:"note"`
}

type rawPostID struct {
	From  string `json:"from"`
	Param string `json:"param"`
	Group string `json:"group"`
}

type rawCapture struct {
	Name         string            `json:"name"`
	Path         string            `json:"path"`
	Items        string            `json:"items"`
	ID           string            `json:"id"`
	PostID       rawPostID         `json:"post_id"`
	QueryKeep    []string          `json:"query_keep"`
	ResponseKeep []string          `json:"response_keep"`
	Fields       map[string]string `json:"fields"`
	NoStream     bool              `json:"no_stream"`
	MaxBodyBytes int64             `json:"max_body_bytes"`
}

type rawPlatform struct {
	Name               string                 `json:"name"`
	Label              string                 `json:"label"`
	ZeeschuimerModules []string               `json:"zeeschuimer_modules"`
	UploadHint         string                 `json:"upload_hint"`
	ItemKinds          map[string]rawItemKind `json:"item_kinds"`
	DefaultKind        string                 `json:"default_kind"`
	Hosts              []string               `json:"hosts"`
	OpenPattern        string                 `json:"open_pattern"`
	Captures           []rawCapture           `json:"captures"`
	Login              string                 `json:"login"`
	KeepCookies        []rawCookie            `json:"keep_cookies"`
	Problems           []rawProblem           `json:"problems"`
}

// Load reads and validates the platform definition at path. An unknown key,
// an invalid regular expression or a default kind that is not declared is an
// error.
func Load(path string) (*Platform, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var raw rawPlatform
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("platform %s: %w", path, err)
	}

	p, err := build(raw)
	if err != nil {
		return nil, fmt.Errorf("platform %s: %w", path, err)
	}
	p.Dir = filepath.Dir(path)
	return p, nil
}

func build(raw rawPlatform) (*Platform, error) {
	p := &Platform{
		Name:               raw.Name,
		Label:              raw.Label,
		ZeeschuimerModules: raw.ZeeschuimerModules,
		UploadHint:         raw.UploadHint,
		ItemKinds:          make(map[string]ItemKind, len(raw.ItemKinds)),
		DefaultKind:        raw.DefaultKind,
		Hosts:              raw.Hosts,
		Login:              raw.Login,
	}

	for kind, k := range raw.ItemKinds {
		re, err := regexp.Compile(k.ID)
		if err != nil {
			return nil, fmt.Errorf("item_kinds.%s.id: %w", kind, err)
		}
		p.ItemKinds[kind] = ItemKind{URL: k.URL, ID: re}
	}
	if _, ok := p.ItemKinds[p.DefaultKind]; !ok {
		return nil, fmt.Errorf("default_kind %q is not in item_kinds", p.DefaultKind)
	}

	open, err := regexp.Compile(raw.OpenPattern)
	if err != nil {
		return nil, fmt.Errorf("open_pattern: %w", err)
	}
	p.OpenPattern = open

	for _, c := range raw.Captures {
		path, err := regexp.Compile(c.Path)
		if err != nil {
			return nil, fmt.Errorf("captures.%s.path: %w", c.Name, err)
		}
		rule := CaptureRule{
			Name:         c.Name,
			Path:         path,
			Items:        c.Items,
			ID:           c.ID,
			PostID:       PostIDSpec(c.PostID),
			QueryKeep:    c.QueryKeep,
			ResponseKeep: c.ResponseKeep,
			Fields:       c.Fields,
			NoStream:     c.NoStream,
			MaxBodyBytes: c.MaxBodyBytes,
		}
		if rule.MaxBodyBytes == 0 {
			rule.MaxBodyBytes = defaultMaxBodyBytes
		}
		p.Captures = append(p.Captures, rule)
	}

	for _, c := range raw.KeepCookies {
		p.KeepCookies = append(p.KeepCookies, CookieSpec(c))
	}
	for _, pr := range raw.Problems {
		p.Problems = append(p.Problems, Problem(pr))
	}
	return p, nil
}

// HostMatches reports whether host is one of the platform's hosts or a
// subdomain of one.
func (p *Platform) HostMatches(host string) bool {
	host = strings.ToLower(host)
	for _, h := range p.Hosts {
		h = strings.ToLower(h)
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// ItemURL builds the URL of an item of the given kind. The id must match the
// kind's id pattern.
func (p *Platform) ItemURL(kind, id string) (string, error) {
	k, ok := p.ItemKinds[kind]
	if !ok {
		return "", fmt.Errorf("unknown item kind %q", kind)
	}
	if !k.ID.MatchString(id) {
		return "", fmt.Errorf("id %q does not match %s for kind %q", id, k.ID, kind)
	}
	return strings.ReplaceAll(k.URL, "{id}", id), nil
}
