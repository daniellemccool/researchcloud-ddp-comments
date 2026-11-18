// Command poolexport turns the participant export (the zip as downloaded, or
// the export unzipped into a folder) into pool uploads for the desk: the
// distinct TikTok video IDs not sent before, shuffled with crypto/rand, in CSV
// files with the header `id` and at most -max-rows rows each. It runs on the
// study lead's own machine, where the export is kept, and writes nothing
// derived from the export except bare video IDs (ADR-0026).
//
// Usage:
//
//	go run ./cmd/poolexport -export ZIP-OR-DIR -out DIR [-sent FILE] [-all] [-max-rows N]
//
// -out is required and, like -sent, must be outside any repository: the
// uploads and the sent list are the study's video list, and inside the
// checkout git and box-sync would pick them up.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const usageLine = "usage: go run ./cmd/poolexport -export ZIP-OR-DIR -out DIR [-sent FILE (default <out>/sent-ids.txt)] [-all] [-max-rows 200000]"

func main() {
	err := run(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "poolexport: %v\n", err)
		os.Exit(1)
	}
}

// run is the whole command: extract, read the sent list, keep the new IDs (or
// all with -all), shuffle, write the uploads, and only then add the written
// IDs to the sent list, so a run that fails part-way leaves the list as it was
// and the next run writes those IDs again. It reports counts and output paths
// to stderr, never a file name of the export or any row content.
func run(args []string, stderr io.Writer) error {
	fl := flag.NewFlagSet("poolexport", flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.Usage = func() {
		_, _ = fmt.Fprintln(stderr, usageLine)
		fl.PrintDefaults()
	}
	export := fl.String("export", "", "the export zip (read in memory, never extracted) or unzipped folder; reads its *-tiktok.json files")
	out := fl.String("out", "", "folder for the pool-upload-*.csv files, outside the repository (required)")
	sentPath := fl.String("sent", "", "the sent list (default <out>/sent-ids.txt); keep it between runs")
	all := fl.Bool("all", false, "write every ID, sent before or not (to rebuild a lost upload)")
	maxRows := fl.Int("max-rows", 200000, "most IDs per upload file (the desk takes 5 MB per upload)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *export == "" || fl.NArg() > 0 {
		fl.Usage()
		return errors.New("-export is required and no other arguments are taken")
	}
	if *out == "" {
		fl.Usage()
		return errors.New("-out is required: name a folder outside the repository, next to the exports")
	}
	if *maxRows < 1 {
		return fmt.Errorf("-max-rows must be at least 1, not %d", *maxRows)
	}
	if *sentPath == "" {
		*sentPath = filepath.Join(*out, "sent-ids.txt")
	}
	if err := refuseInsideRepository("-out", *out); err != nil {
		return err
	}
	if err := refuseInsideRepository("-sent", *sentPath); err != nil {
		return err
	}

	ids, st, err := extractIDs(*export)
	if err != nil {
		return err
	}
	sent, err := readSent(*sentPath)
	if err != nil {
		return err
	}
	fresh := map[string]struct{}{}
	for id := range ids {
		if _, ok := sent[id]; ok {
			st.AlreadySent++
			if !*all {
				continue
			}
		}
		fresh[id] = struct{}{}
	}
	st.Written = len(fresh)
	report(stderr, st)
	if len(fresh) == 0 {
		_, _ = fmt.Fprintln(stderr, "no new IDs: no upload file written, sent list unchanged")
		return nil
	}

	order, err := shuffled(fresh)
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	paths, err := writeUploads(*out, stamp, order, *maxRows)
	if err != nil {
		return err
	}
	for id := range fresh {
		sent[id] = struct{}{}
	}
	if err := writeSent(*sentPath, sent); err != nil {
		for _, p := range paths {
			_ = os.Remove(p)
		}
		return fmt.Errorf("%w (upload files removed; nothing was recorded as sent)", err)
	}
	for _, p := range paths {
		_, _ = fmt.Fprintf(stderr, "wrote %s\n", p)
	}
	_, _ = fmt.Fprintf(stderr, "sent list %s now holds %d IDs\n", *sentPath, len(sent))
	return nil
}

// report prints the counts of a run, nothing else.
func report(w io.Writer, st stats) {
	_, _ = fmt.Fprintf(w, "tiktok files read: %d\n", st.Files)
	_, _ = fmt.Fprintf(w, "tiktok entries not regular files (ignored): %d\n", st.NonRegular)
	_, _ = fmt.Fprintf(w, "test-participant files skipped: %d\n", st.TestSkipped)
	for _, name := range tableOrder {
		_, _ = fmt.Fprintf(w, "rows in %s: %d\n", name, st.Rows[name])
	}
	_, _ = fmt.Fprintf(w, "links without a video ID (skipped): %d\n", st.Skipped)
	_, _ = fmt.Fprintf(w, "distinct video IDs: %d\n", st.Distinct)
	_, _ = fmt.Fprintf(w, "already sent: %d\n", st.AlreadySent)
	_, _ = fmt.Fprintf(w, "to write: %d\n", st.Written)
}
