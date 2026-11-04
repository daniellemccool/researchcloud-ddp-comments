package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
)

// Steps are the first-visit steps in their fixed order.
var Steps = []string{"registry", "dirs", "template", "fourcat_account", "fourcat_token", "render"}

// RegistryFull is the recorded value of a registry step that found every seat
// taken: exactly "error: registry full".
const RegistryFull = "error: registry full"

// StepError is an error this package raises itself ("template missing",
// "no 4CAT password file", …); its message is recorded verbatim.
type StepError struct{ Msg string }

func (e *StepError) Error() string { return e.Msg }

// State is the on-disk shape of state/users/<user>/provision.json: each step
// maps to "done" or "error: <text>", plus the applied template generation and
// the last update time.
type State struct {
	Steps             map[string]string `json:"steps"`
	AppliedGeneration string            `json:"applied_generation"`
	Updated           int64             `json:"updated"`
}

// Current returns the first step whose value is not "done", or "" when all
// are done.
func (s State) Current() string {
	for _, step := range Steps {
		if s.Steps[step] != "done" {
			return step
		}
	}
	return ""
}

// Ready reports whether every step is done.
func (s State) Ready() bool { return s.Current() == "" }

// Error returns the current step's value when it starts with "error: ", else "".
func (s State) Error() string {
	v := s.Steps[s.Current()]
	if strings.HasPrefix(v, "error: ") {
		return v
	}
	return ""
}

// userRe is the username grammar (the same shape internal/registry enforces):
// a name is validated before it is joined into any path.
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// StatePath returns <DeskRoot>/state/users/<user>/provision.json.
func StatePath(cfg *config.Config, user string) string {
	return filepath.Join(cfg.DeskRoot, "state", "users", user, "provision.json")
}

// Load reads the user's step file. A missing, unparseable or wrongly shaped
// file returns the zero State (every step is idempotent, so starting over is
// safe); unknown step names are dropped.
func Load(cfg *config.Config, user string) State {
	zero := State{Steps: map[string]string{}}
	if !userRe.MatchString(user) {
		return zero
	}
	data, err := os.ReadFile(StatePath(cfg, user))
	if err != nil {
		return zero
	}
	var raw struct {
		Steps             map[string]string `json:"steps"`
		AppliedGeneration string            `json:"applied_generation"`
		Updated           int64             `json:"updated"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Steps == nil {
		return zero
	}
	st := State{Steps: make(map[string]string, len(raw.Steps)), AppliedGeneration: raw.AppliedGeneration, Updated: raw.Updated}
	for _, name := range Steps {
		if v, ok := raw.Steps[name]; ok {
			st.Steps[name] = v
		}
	}
	return st
}

// save rewrites the step file atomically (temp file in the same directory,
// 0600, rename) after every step. The file is root's: it carries free-form
// error text.
func (p *Provisioner) save(user string, st *State) error {
	st.Updated = p.now().Unix()
	path := StatePath(p.Cfg, user)
	dir := filepath.Dir(path)
	if err := ensureDir(dir, 0o700, 0, 0); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(dir, "provision.json", data, 0o600, 0, 0)
}

// describe turns a step's error into the text recorded in the step file: the
// message of a known error verbatim, "registry full" for registry.ErrFull,
// and the Go type name only for anything else (its message might carry a
// secret).
func describe(err error) string {
	var loginErr *fourcat.LoginError
	if errors.As(err, &loginErr) {
		return loginErr.Error()
	}
	if errors.Is(err, fourcat.ErrNeedsAdopt) || errors.Is(err, fourcat.ErrSetupPending) {
		return err.Error()
	}
	var stepErr *StepError
	if errors.As(err, &stepErr) {
		return stepErr.Error()
	}
	if errors.Is(err, registry.ErrFull) {
		return "registry full"
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Error()
	}
	return fmt.Sprintf("%T", err)
}
