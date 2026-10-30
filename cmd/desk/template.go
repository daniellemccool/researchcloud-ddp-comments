package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/template"
)

// templateCmd implements "desk template ...". It returns the process exit
// code: 0 on success, 1 on a release error, 2 on usage.
func templateCmd(args []string) int {
	if len(args) == 0 || args[0] != "release" {
		templateUsage()
		return 2
	}
	fs := flag.NewFlagSet("template release", flag.ContinueOnError)
	profile := fs.String("profile", "", "disposable Firefox profile directory (required)")
	platformName := fs.String("platform", "", "platform name under --platforms-dir (required)")
	platformsDir := fs.String("platforms-dir", "", "directory holding platforms/<name>/platform.json (required)")
	zsVersion := fs.String("zeeschuimer", "", "Zeeschuimer version (required)")
	out := fs.String("out", "", "output directory for the tar and its .sha256 (required)")
	if err := fs.Parse(args[1:]); err != nil {
		templateUsage()
		return 2
	}
	if *profile == "" || *platformName == "" || *platformsDir == "" || *zsVersion == "" || *out == "" || fs.NArg() != 0 {
		templateUsage()
		return 2
	}

	p, err := platform.Load(filepath.Join(*platformsDir, *platformName, "platform.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "desk: %v\n", err)
		return 1
	}
	man, err := template.Release(*profile, p, *zsVersion, *out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "desk: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "desk: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	fmt.Printf("sha256 %s  %s\n", man.SHA256, template.ArtifactName(man.Platform, man.Zeeschuimer))
	return 0
}

func templateUsage() {
	fmt.Fprintln(os.Stderr, "usage: desk template release --profile DIR --platform NAME --platforms-dir DIR --zeeschuimer VERSION --out DIR")
}
