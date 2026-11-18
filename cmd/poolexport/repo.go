package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// modulePath is this repository's Go module; a go.mod naming it marks a
// checkout even where .git is absent (the box copy has none).
const modulePath = "github.com/daniellemccool/researchcloud-ddp-comments"

// refuseInsideRepository returns an error when path, made absolute with its
// existing part's symlinks resolved, lies in a folder that has a .git entry or
// this module's go.mod, or below one. The flag names the option in the error.
func refuseInsideRepository(flag, path string) error {
	abs, err := resolve(path)
	if err != nil {
		return fmt.Errorf("%s %s: %w", flag, path, err)
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		marker, err := repositoryMarker(dir)
		if err != nil {
			return fmt.Errorf("%s %s: %w", flag, path, err)
		}
		if marker != "" {
			return fmt.Errorf("%s %s is inside the repository at %s (it has %s); choose a folder outside the repository, next to the exports", flag, path, dir, marker)
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// resolve makes path absolute and resolves the symlinks of its longest
// existing prefix; the rest, not created yet, is joined on unchanged.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// repositoryMarker names what makes dir a repository root: ".git" when it
// has a .git entry of any type, "go.mod of <module>" when its go.mod declares
// this module, or "" for neither. A dir that is not a folder (an existing
// sent list, or a part not created yet) has neither.
func repositoryMarker(dir string) (string, error) {
	if fi, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) || (err == nil && !fi.IsDir()) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return ".git", nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == "module" && strings.Trim(f[1], `"`) == modulePath {
			return "go.mod of " + modulePath, nil
		}
	}
	return "", nil
}
