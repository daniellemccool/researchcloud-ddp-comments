package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// Constants ported from the legacy Python tool (desk/desklib/template.py).
const (
	xpiID            = "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"
	xpiRel           = "extensions/" + xpiID + ".xpi"
	xpiContainerPath = "/config/profile/" + xpiRel
	syncGUID         = "{00000000-0000-4000-8000-000000000000}"
	itemsDB          = "zeeschuimer-items"
	storageDB        = "webExtensions-storage-local"
	storageLabel     = "storage.local"
	userContext      = "^userContextId=4294967295"
)

// allowPrefs is the prefs.js allowlist, ported from the legacy Python tool:
// only the extension bookkeeping and the few settings it lists survive.
var allowPrefs = map[string]bool{
	"extensions.webextensions.uuids": true,
	"extensions.webextensions.ExtensionStorageIDB.migrated.{677bb78c-7168-4aa5-8e9b-32e9f26c6157}": true,
	"extensions.databaseSchema":                           true,
	"extensions.lastAppVersion":                           true,
	"extensions.lastPlatformVersion":                      true,
	"extensions.pendingOperations":                        true,
	"toolkit.legacyUserProfileCustomizations.stylesheets": true,
}

// prefLineRe matches a user_pref line and captures the preference name.
var prefLineRe = regexp.MustCompile(`^user_pref\("([^"]+)"\s*,`)

// uuidPrefRe matches the extensions.webextensions.uuids pref line and
// captures the raw (still escaped) JSON string value.
var uuidPrefRe = regexp.MustCompile(`(?m)^user_pref\("extensions\.webextensions\.uuids",\s*"((?:[^"\\]|\\.)*)"\)\s*;`)

// uuidCharsRe validates an extension UUID.
var uuidCharsRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// allowFiles are the profile paths the allowlist keeps.
var allowFiles = map[string]bool{
	"prefs.js":               true,
	"extensions.json":        true,
	"cookies.sqlite":         true,
	"chrome/userContent.css": true,
}

// allowAncestors are the directories the prune recurses into.
var allowAncestors = map[string]bool{
	"chrome":          true,
	"extensions":      true,
	"storage":         true,
	"storage/default": true,
}

// Sanitise deletes everything in dir that is not on the allowlist, filters
// prefs.js, extensions.json and cookies.sqlite in place, and rebuilds the
// kept IndexedDB files (emptying the nav store of the main database). dir is
// the working copy, never the original.
func Sanitise(dir string, p *platform.Platform, zsVersion, builtOn string) error {
	if err := removeSymlinks(dir); err != nil {
		return err
	}
	cookiesPath := filepath.Join(dir, "cookies.sqlite")
	if _, err := os.Stat(cookiesPath); err != nil {
		if os.IsNotExist(err) {
			return ErrNoCookiesDB
		}
		return err
	}
	if err := filterCookies(cookiesPath, p.KeepCookies); err != nil {
		return err
	}
	prefsPath := filepath.Join(dir, "prefs.js")
	if _, err := os.Stat(prefsPath); err != nil {
		if os.IsNotExist(err) {
			return errors.New("prefs.js missing from the profile: Firefox would assign a new extension UUID and lose the storage")
		}
		return err
	}
	uuid, err := zeeschuimerUUID(dir)
	if err != nil {
		return err
	}
	if err := prune(dir, uuid); err != nil {
		return err
	}
	if err := filterPrefs(prefsPath); err != nil {
		return err
	}
	return filterExtensionsJSON(dir, zsVersion, builtOn)
}

// zeeschuimerUUID returns Zeeschuimer's moz-extension uuid from the
// extensions.webextensions.uuids pref, or "" when it cannot be determined.
func zeeschuimerUUID(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "prefs.js"))
	if err != nil {
		return "", err
	}
	m := uuidPrefRe.FindStringSubmatch(string(data))
	if m == nil {
		return "", nil
	}
	inner, err := strconv.Unquote(`"` + m[1] + `"`)
	if err != nil {
		return "", nil
	}
	var mapping map[string]any
	if err := json.Unmarshal([]byte(inner), &mapping); err != nil {
		return "", nil
	}
	u, ok := mapping[xpiID].(string)
	if !ok || !uuidCharsRe.MatchString(u) {
		return "", nil
	}
	return u, nil
}

// extDirs returns the two extension directories of uuid: the plain one and
// the user-context one that holds webExtensions-storage-local.
func extDirs(uuid string) []string {
	if uuid == "" {
		return nil
	}
	return []string{"moz-extension+++" + uuid, "moz-extension+++" + uuid + userContext}
}

// filterPrefs rewrites prefs.js keeping only the allowlisted user_pref lines.
func filterPrefs(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var keep []string
	for _, line := range strings.Split(string(data), "\n") {
		m := prefLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil && allowPrefs[m[1]] {
			keep = append(keep, line)
		}
	}
	out := strings.Join(keep, "\n")
	if len(keep) > 0 {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// removeSymlinks deletes every symlink under root so nothing below can point
// outside the working copy.
func removeSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return os.Remove(path)
		}
		return nil
	})
}

// prune deletes everything under dir that the allowlist does not name.
func prune(root, uuid string) error {
	return pruneLevel(root, "", uuid)
}

func pruneLevel(root, rel, uuid string) error {
	here := root
	if rel != "" {
		here = filepath.Join(root, filepath.FromSlash(rel))
	}
	entries, err := os.ReadDir(here)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		r := name
		if rel != "" {
			r = rel + "/" + name
		}
		p := filepath.Join(here, name)
		if e.Type()&os.ModeSymlink != 0 {
			if err := os.Remove(p); err != nil {
				return err
			}
			continue
		}
		switch {
		case allowFiles[r]:
			// kept
		case r == "extensions" && e.IsDir():
			if err := pruneExtensions(p); err != nil {
				return err
			}
		case r == "storage/default" && e.IsDir():
			if err := pruneStorageDefault(p, uuid); err != nil {
				return err
			}
		case allowAncestors[r] && e.IsDir():
			if err := pruneLevel(root, r, uuid); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// pruneExtensions keeps only Zeeschuimer's xpi in the extensions directory.
func pruneExtensions(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != xpiID+".xpi" || !e.Type().IsRegular() {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// pruneStorageDefault keeps only the extension's two directories under
// storage/default, cleaning each of them.
func pruneStorageDefault(dir, uuid string) error {
	keep := extDirs(uuid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if slices.Contains(keep, e.Name()) && e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			expected := storageDB
			if e.Name() == keep[0] {
				expected = itemsDB
			}
			if err := cleanExtDir(p, expected); err != nil {
				return err
			}
		} else if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// cleanExtDir keeps only idb/ inside an extension directory; inside idb it
// keeps only the expected database's sqlite file (rebuilt) and its .files
// directory.
func cleanExtDir(d, expectedDB string) error {
	entries, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "idb" {
			if err := os.RemoveAll(filepath.Join(d, e.Name())); err != nil {
				return err
			}
		}
	}
	idbDir := filepath.Join(d, "idb")
	fis, err := os.ReadDir(idbDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	keep := map[string]bool{}
	for _, fi := range fis {
		p := filepath.Join(idbDir, fi.Name())
		switch {
		case strings.HasSuffix(fi.Name(), ".sqlite") && fi.Type().IsRegular():
			if name, _, ok := readIDB(p); ok && name == expectedDB {
				if err := rebuildIDB(p, expectedDB == itemsDB); err != nil {
					return err
				}
				keep[fi.Name()] = true
			}
		case strings.HasSuffix(fi.Name(), ".files") && fi.IsDir():
			keep[fi.Name()] = true
		}
	}
	for _, fi := range fis {
		if !keep[fi.Name()] {
			if err := os.RemoveAll(filepath.Join(idbDir, fi.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// extensionsJSON is the filtered extensions.json: only schemaVersion and the
// addons list survive.
type extensionsJSON struct {
	SchemaVersion any   `json:"schemaVersion,omitempty"`
	Addons        []any `json:"addons"`
}

// filterExtensionsJSON rewrites extensions.json keeping only Zeeschuimer's
// addon entry, normalised.
func filterExtensionsJSON(dir, zsVersion, builtOn string) error {
	path := filepath.Join(dir, "extensions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("extensions.json is not valid JSON: %w", err)
	}
	encoded, err := json.Marshal(filterExtensions(parsed, zsVersion, builtOn))
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// filterExtensions keeps only Zeeschuimer's addon entry, with
// installation-specific fields normalised, ported from the Python tool.
func filterExtensions(data any, zsVersion, builtOn string) extensionsJSON {
	out := extensionsJSON{Addons: []any{}}
	m, ok := data.(map[string]any)
	if !ok {
		return out
	}
	if v, ok := m["schemaVersion"]; ok {
		out.SchemaVersion = v
	}
	addons, ok := m["addons"].([]any)
	if !ok {
		return out
	}
	ms := mtimeMillis(builtOn)
	for _, a := range addons {
		am, ok := a.(map[string]any)
		if !ok || am["id"] != xpiID {
			continue
		}
		e := map[string]any{}
		for k, v := range am {
			if !strings.Contains(strings.ToLower(k), "elemetry") {
				e[k] = v
			}
		}
		e["path"] = xpiContainerPath
		e["syncGUID"] = syncGUID
		e["installDate"] = ms
		e["updateDate"] = ms
		if e["sourceURI"] != policyURL(zsVersion) {
			delete(e, "sourceURI")
		}
		if _, ok := e["rootURI"]; ok {
			e["rootURI"] = "jar:file://" + xpiContainerPath + "!/"
		}
		out.Addons = append(out.Addons, e)
	}
	return out
}

// policyURL is the release URL of a Zeeschuimer version.
func policyURL(version string) string {
	return fmt.Sprintf("https://github.com/digitalmethodsinitiative/zeeschuimer/releases/download/v%s/zeeschuimer-%s.xpi", version, version)
}

// mtimeMillis is the built-on date as milliseconds since the epoch.
func mtimeMillis(builtOn string) int64 {
	day := builtOn
	if len(day) > 10 {
		day = day[:10]
	}
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		t = time.Now().UTC()
	}
	return t.Unix() * 1000
}
