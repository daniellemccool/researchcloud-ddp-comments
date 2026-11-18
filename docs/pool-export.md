# Pool uploads from the participant export

`cmd/poolexport` turns the participant export into CSV files for the desk's
pool upload. It takes the export zip as downloaded, or the export unzipped into
a folder. It writes only TikTok video IDs that no earlier run wrote, shuffled
with a cryptographic random source, so no upload keeps a participant's viewing
order (ADR-0026).

## Running it

From the repository root, on the machine where the export is kept, pass the
downloaded zip and an output folder next to the exports:

```
go run ./cmd/poolexport -export <path to>/<date>_check_je_feed.zip -out <path to>/checkjefeed-pool
```

The full form is:

```
go run ./cmd/poolexport -export ZIP-OR-DIR -out DIR [-sent FILE (default <out>/sent-ids.txt)] [-all] [-max-rows 200000]
```

`-out` is required and must be outside the repository, and so must `-sent`:
the upload files and the sent list are the study's whole video list, and in the
checkout git and `scripts/box-sync.sh` would pick them up. The tool refuses a
folder that has, or is below one that has, a `.git` entry or this module's
`go.mod`, and writes nothing.

The tool reads the zip directly, in memory. It never extracts anything to disk
and writes no temporary file. Do not unzip the export first. Info-ZIP `unzip`
may refuse Eyra's export zips with "invalid zip file with overlapped components
(possible zip bomb)". That is a known false alarm: every entry keeps its sizes
in a data descriptor. The tool reads these zips without trouble.

The report on stderr gives only counts: files read, non-regular entries
ignored, test-participant files skipped, rows per table, links skipped,
distinct IDs, IDs already sent and IDs to write. After that it prints the
output paths and the size of the sent list.
Each output file is named `pool-upload-<UTC time>-<k>.csv`, for example
`pool-upload-20261118T150405Z-1.csv`. Each has the header `id` and at most
200000 IDs, which stays under the desk's 5 MB upload limit. When nothing is new,
it writes no file and says so.

## What it reads

- From a zip: the regular-file entries whose name ends in `-tiktok.json`, in
  any folder of the zip (the export has one top folder).
- From a folder: the regular files named `*-tiktok.json` directly in it, not in
  subfolders.

Onboarding files and other platforms' files are ignored. `*-tiktok.json`
entries that are not regular files, such as symlinks and folders, are ignored
too, and the report counts them.

Files of the study's test participant are skipped. A test file is a
`*-tiktok.json` file whose base name has the participant field `test` exactly:
the text after `participant=` up to the next `_`, as in
`..._participant=test_source=tiktok_...`. `participant=tester_` and
`participant=test2_` are real participants and are read. A test file holds
synthetic IDs, so it is never opened. It counts neither as a file read nor in
any row or ID count. The report gives only the line
`test-participant files skipped: N`. A video that is also in a real
participant's file is still written, because it comes from that file.

From each file it reads these tables:

| table | column |
|---|---|
| `tiktok_watch_history` | `Link` |
| `tiktok_like_list` | `Link` |
| `tiktok_favorite_videos` | `Link` |
| `tiktok_comments` | `Url` |

A video ID is the digit run after `/video/`, in both
`https://www.tiktokv.com/share/video/<id>/` and
`https://www.tiktok.com/@<user>/video/<id>`. A link that is empty, `null`, not a
string or has no `/video/<digits>` is skipped and counted. It never stops the
run. Other tables and keys are ignored.

## A bad file

Some files stop the run:

- a file that is not a JSON array of tables;
- a file with a table that is not an array of rows;
- a zip entry that is encrypted;
- a zip entry that uses a compression method other than store or deflate;
- a file larger than 1 GiB uncompressed. This is checked against both the
  declared size and the bytes actually read, as a zip-bomb guard.

The tool still tries every file first. Then it lists all the bad ones in one
error, writes nothing, and leaves the sent list unchanged. The error never
prints a file name, because the name carries the participant, and never prints
any content. It gives each bad file's position, its (uncompressed) size and the
fault, for example:

```
poolexport: 2 of 4 tiktok files are not valid (positions in byte order of name, see docs/pool-export.md); nothing written: file 1 (48 bytes): not valid JSON at byte 48; file 3 (37 bytes): wrong JSON type at byte 35
```

The position is K among the `*-tiktok.json` files that are read, sorted by name
in byte order. Skipped test files and non-regular entries are not counted. For a zip, the name is the full entry name, folders included. Byte
order is not the order of a plain `ls` (which follows the locale) or of a fish
glob (which sorts numbers naturally). To find entry K, run the matching command
below with the position as `K`. Both work in fish as well as bash.

For a zip:

```
unzip -Z ZIP | awk '$1 !~ /^l/ { sub(/^([^ ]+ +){8}/, ""); print }' | grep -- '-tiktok\.json$' | grep -Ev -- '(^|/|_)participant=test_[^/]*$' | env LC_ALL=C sort | sed -n 'Kp'
```

`unzip -Z` is zipinfo's listing. It only lists the zip and never extracts, so it
works even on Eyra's zips that `unzip` refuses to extract. The `awk` step drops
symlink entries, the lines whose mode starts with `l`, because the tool does not
count them in K. It then strips the first eight columns (mode, version, system,
size, type, method, date, time), which leaves the full entry name, even one with
spaces. The size column of `unzip -Z ZIP` is the uncompressed size from the
error.

In both commands, the `grep -Ev` step drops the test participant's files, which
the tool skips. The pattern checks the participant field of the base name only.

For a folder:

```
find DIR -maxdepth 1 -type f -name '*-tiktok.json' -printf '%f\n' | grep -Ev -- '(^|/|_)participant=test_[^/]*$' | env LC_ALL=C sort | sed -n 'Kp'
```

Then confirm that `stat -c %s DIR/<that name>` prints the size from the error.

## The sent list

The sent list (`sent-ids.txt` in the output folder unless `-sent` names another
file) holds every ID already written to an upload file. It has bare IDs only,
sorted ascending, one per line. **Keep it between runs.** It is how the next run
on the grown export knows which IDs are new. If you lose it, the next run writes
every ID again. That is harmless, because the desk skips IDs it already has, but
the upload is larger.

The list is updated only after every upload file is fully written and closed,
and it is replaced atomically. A run that fails part-way removes the upload
files it made and leaves the list as it was, so the next run writes those IDs
again.

`-all` ignores the list and writes every ID in the export. Use it to rebuild an
upload file that was lost before it reached the desk. The desk discards the IDs
it already holds.

## Uploading

On the workspace, open `/desk/admin/`. In the Collection section, use the upload
form ("Add to the pool") once for each `pool-upload-*.csv` file, in any order.
The desk adds the rows as the default kind, `video`, with no bucket and no note.

## Privacy

The export is participant data. Run the tool only on the machine where the
export is already kept, and never copy the export anywhere else. The upload
files and the sent list hold bare video IDs only. They contain no participant
identifier, no export file name and no other row content, so they are the only
things that leave that machine. The report prints counts and output paths, never
an export file name or any row content.
