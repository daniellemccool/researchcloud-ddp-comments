// Package config loads the desk's desk.json strictly: an unknown key or a
// missing key is an error naming it, and the gate secret is read from
// <desk root>/secrets/gate.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Config is the converted, validated desk configuration. The 20 parameters
// of desk.json arrive as strings and are converted here; layout_version is
// the one integer in the file.
type Config struct {
	DeskRoot           string // container-local: the directory desk.json was loaded from
	HostDeskRoot       string // the desk_root value in the file (the host path)
	FQDN               string
	CollectorRole      string
	AdminRole          string
	Contact            string
	Platform           string
	HideMedia          bool
	FourcatEnabled     bool
	BatchSizes         []int
	IdleSeconds        int
	TimerMinutes       int
	MaxSessions        int
	MaxRunning         int    // "auto" → runtime.NumCPU()-1, minimum 1
	StoragePath        string // as written ("auto" or a path)
	FourcatImage       string
	DeskImage          string
	DeskproxyImage     string
	ZeeschuimerVersion string
	TemplateSHA256     string
	COID               string
	COName             string
	LayoutVersion      int
	PackageVersion     string
	AppliedAt          string
	GateSecret         string // from <DeskRoot>/secrets/gate, trimmed
}

// rawConfig mirrors the 24 keys of desk.json exactly; DisallowUnknownFields
// rejects anything else.
type rawConfig struct {
	DeskCollectorRole  string `json:"desk_collector_role"`
	DeskAdminRole      string `json:"desk_admin_role"`
	DeskPlatform       string `json:"desk_platform"`
	DeskContact        string `json:"desk_contact"`
	DeskHideMedia      string `json:"desk_hide_media"`
	DeskBatchSizes     string `json:"desk_batch_sizes"`
	DeskIdleSeconds    string `json:"desk_idle_seconds"`
	DeskTimerMinutes   string `json:"desk_timer_minutes"`
	DeskMaxSessions    string `json:"desk_max_sessions"`
	DeskMaxRunning     string `json:"desk_max_running"`
	DeskStoragePath    string `json:"desk_storage_path"`
	FourcatEnabled     string `json:"fourcat_enabled"`
	FourcatImage       string `json:"fourcat_image"`
	DeskImage          string `json:"desk_image"`
	DeskproxyImage     string `json:"deskproxy_image"`
	ZeeschuimerVersion string `json:"zeeschuimer_version"`
	TemplateSHA256     string `json:"template_sha256"`
	COID               string `json:"co_id"`
	COName             string `json:"co_name"`
	WorkspaceFQDN      string `json:"workspace_fqdn"`
	DeskRoot           string `json:"desk_root"`
	LayoutVersion      int    `json:"layout_version"`
	PackageVersion     string `json:"package_version"`
	AppliedAt          string `json:"applied_at"`
}

// knownKeys are the 24 keys desk.json must hold, no others.
var knownKeys = []string{
	"desk_collector_role", "desk_admin_role", "desk_platform", "desk_contact",
	"desk_hide_media", "desk_batch_sizes", "desk_idle_seconds", "desk_timer_minutes",
	"desk_max_sessions", "desk_max_running", "desk_storage_path", "fourcat_enabled",
	"fourcat_image", "desk_image", "deskproxy_image", "zeeschuimer_version",
	"template_sha256", "co_id", "co_name", "workspace_fqdn",
	"desk_root", "layout_version", "package_version", "applied_at",
}

var (
	intRe      = regexp.MustCompile(`^[0-9]+$`)
	batchRe    = regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`)
	roleRe     = regexp.MustCompile(`^src_co_[A-Za-z0-9_-]+$`)
	platformRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// Load reads and validates the desk.json at path. DeskRoot is the directory
// the file was loaded from (the container-local desk root); the file's
// desk_root value is the host path and lands in HostDeskRoot. The gate
// secret is read from <DeskRoot>/secrets/gate and trimmed.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("desk.json: %w", err)
	}

	var raw rawConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("desk.json %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("desk.json %s: unexpected content after the JSON object", path)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("desk.json %s: %w", path, err)
	}
	for _, k := range knownKeys {
		v, ok := keys[k]
		if !ok {
			return nil, fmt.Errorf("desk.json %s: missing key %q", path, k)
		}
		if string(v) == "null" {
			return nil, fmt.Errorf("desk.json %s: key %q is null", path, k)
		}
	}
	if raw.LayoutVersion != 1 {
		return nil, fmt.Errorf("desk.json %s: layout_version: must be 1, got %d", path, raw.LayoutVersion)
	}

	cfg, err := convert(path, &raw)
	if err != nil {
		return nil, err
	}
	cfg.DeskRoot = filepath.Dir(path)
	cfg.HostDeskRoot = raw.DeskRoot

	gatePath := filepath.Join(cfg.DeskRoot, "secrets", "gate")
	gate, err := os.ReadFile(gatePath)
	if err != nil {
		return nil, fmt.Errorf("gate secret: %w", err)
	}
	cfg.GateSecret = strings.TrimSpace(string(gate))
	if cfg.GateSecret == "" {
		return nil, fmt.Errorf("gate secret %s is empty", gatePath)
	}
	return cfg, nil
}

func convert(path string, raw *rawConfig) (*Config, error) {
	fail := func(key, format string, args ...any) error {
		return fmt.Errorf("desk.json %s: %s: %s", path, key, fmt.Sprintf(format, args...))
	}

	collectorRole, err := checkRole(raw.DeskCollectorRole)
	if err != nil {
		return nil, fail("desk_collector_role", "%v", err)
	}
	adminRole, err := checkRole(raw.DeskAdminRole)
	if err != nil {
		return nil, fail("desk_admin_role", "%v", err)
	}
	if raw.DeskContact == "" {
		return nil, fail("desk_contact", "must not be empty")
	}
	if !platformRe.MatchString(raw.DeskPlatform) {
		return nil, fail("desk_platform", "must match ^[a-z][a-z0-9_-]*$ (it is joined into a path), got %q", raw.DeskPlatform)
	}
	hideMedia, err := parseBool(raw.DeskHideMedia)
	if err != nil {
		return nil, fail("desk_hide_media", "%v", err)
	}
	fourcatEnabled, err := parseBool(raw.FourcatEnabled)
	if err != nil {
		return nil, fail("fourcat_enabled", "%v", err)
	}
	batchSizes, err := parseBatchSizes(raw.DeskBatchSizes)
	if err != nil {
		return nil, fail("desk_batch_sizes", "%v", err)
	}
	idleSeconds, err := parseInt(raw.DeskIdleSeconds)
	if err != nil {
		return nil, fail("desk_idle_seconds", "%v", err)
	}
	timerMinutes, err := parseInt(raw.DeskTimerMinutes)
	if err != nil {
		return nil, fail("desk_timer_minutes", "%v", err)
	}
	maxSessions, err := parseInt(raw.DeskMaxSessions)
	if err != nil {
		return nil, fail("desk_max_sessions", "%v", err)
	}
	maxRunning, err := parseMaxRunning(raw.DeskMaxRunning)
	if err != nil {
		return nil, fail("desk_max_running", "%v", err)
	}

	return &Config{
		HostDeskRoot:       raw.DeskRoot,
		FQDN:               raw.WorkspaceFQDN,
		CollectorRole:      collectorRole,
		AdminRole:          adminRole,
		Contact:            raw.DeskContact,
		Platform:           raw.DeskPlatform,
		HideMedia:          hideMedia,
		FourcatEnabled:     fourcatEnabled,
		BatchSizes:         batchSizes,
		IdleSeconds:        idleSeconds,
		TimerMinutes:       timerMinutes,
		MaxSessions:        maxSessions,
		MaxRunning:         maxRunning,
		StoragePath:        raw.DeskStoragePath,
		FourcatImage:       raw.FourcatImage,
		DeskImage:          raw.DeskImage,
		DeskproxyImage:     raw.DeskproxyImage,
		ZeeschuimerVersion: raw.ZeeschuimerVersion,
		TemplateSHA256:     raw.TemplateSHA256,
		COID:               raw.COID,
		COName:             raw.COName,
		LayoutVersion:      raw.LayoutVersion,
		PackageVersion:     raw.PackageVersion,
		AppliedAt:          raw.AppliedAt,
	}, nil
}

func checkRole(v string) (string, error) {
	if !roleRe.MatchString(v) {
		return "", fmt.Errorf("must match ^src_co_[A-Za-z0-9_-]+$, got %q", v)
	}
	return v, nil
}

func parseBool(v string) (bool, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("must be \"true\" or \"false\", got %q", v)
}

func parseInt(v string) (int, error) {
	// Bound the value the same way the shell layer does: 1-9 digits, so a
	// huge string can never reach Atoi.
	if len(v) > 9 || !intRe.MatchString(v) {
		return 0, fmt.Errorf("must be 1-9 digits, got %q", v)
	}
	return strconv.Atoi(v)
}

func parseBatchSizes(v string) ([]int, error) {
	if !batchRe.MatchString(v) {
		return nil, fmt.Errorf("must match ^[0-9]+(,[0-9]+)*$, got %q", v)
	}
	parts := strings.Split(v, ",")
	sizes := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		sizes = append(sizes, n)
	}
	return sizes, nil
}

func parseMaxRunning(v string) (int, error) {
	if v == "auto" {
		return max(runtime.NumCPU()-1, 1), nil
	}
	return parseInt(v)
}
