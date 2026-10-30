package template

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
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

// allowRootFiles are the profile-root files the allowlist keeps.
var allowRootFiles = map[string]bool{
	"prefs.js":                   true,
	"extensions.json":            true,
	"extension-preferences.json": true,
	"cookies.sqlite":             true,
}

// Sanitise deletes everything in dir that is not on the allowlist, filters
// prefs.js and cookies.sqlite in place, and empties the nav object store of
// the Zeeschuimer IndexedDB. dir is the working copy, never the original.
func Sanitise(dir string, p *platform.Platform) error {
	extRel, idbFile, err := findStore(dir)
	if err != nil {
		return err
	}
	if err := emptyNavStore(idbFile); err != nil {
		return err
	}
	if err := filterCookies(filepath.Join(dir, "cookies.sqlite"), p.KeepCookies); err != nil {
		return err
	}
	if err := filterPrefs(filepath.Join(dir, "prefs.js")); err != nil {
		return err
	}
	if err := removeSymlinks(dir); err != nil {
		return err
	}
	return prune(dir, extRel)
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

// prune deletes everything under dir that the allowlist does not name. extRel
// is the extension directory (relative to dir) whose whole subtree is kept.
func prune(dir, extRel string) error {
	return pruneLevel(dir, "", extRel)
}

func pruneLevel(abs, rel, extRel string) error {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		childAbs := filepath.Join(abs, name)
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		if e.Type()&os.ModeSymlink != 0 {
			if err := os.Remove(childAbs); err != nil {
				return err
			}
			continue
		}
		switch rel {
		case "":
			switch {
			case allowRootFiles[name]:
				// kept
			case name == "extensions" && e.IsDir():
				if err := pruneExtensions(childAbs); err != nil {
					return err
				}
			case name == "chrome" && e.IsDir():
				if err := pruneChrome(childAbs); err != nil {
					return err
				}
			case name == "storage" && e.IsDir():
				if err := pruneLevel(childAbs, "storage", extRel); err != nil {
					return err
				}
			default:
				if err := os.RemoveAll(childAbs); err != nil {
					return err
				}
			}
		case "storage":
			if name == "default" && e.IsDir() {
				if err := pruneLevel(childAbs, "storage/default", extRel); err != nil {
					return err
				}
			} else if err := os.RemoveAll(childAbs); err != nil {
				return err
			}
		case "storage/default":
			if childRel != extRel || !e.IsDir() {
				if err := os.RemoveAll(childAbs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// pruneExtensions keeps only the *.xpi files of the extensions directory.
func pruneExtensions(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".xpi") || !e.Type().IsRegular() {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// pruneChrome keeps only chrome/userContent.css.
func pruneChrome(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "userContent.css" {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
