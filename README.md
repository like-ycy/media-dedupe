# Media Deduplication

Local Python/uv command-line tool for finding exact and near-duplicate image/video files.

The first version is safety-first: it scans files, groups exact duplicates, records reusable metadata in SQLite, and reports quality-first keep recommendations. It does not automatically delete files.

## Install dependencies

```bash
uv sync
```

Video support requires `ffmpeg` and `ffprobe` on PATH. On macOS:

```bash
brew install ffmpeg
```

## Commands

Check environment:

```bash
uv run media-dedupe doctor
```

Scan one or more directories:

```bash
uv run media-dedupe scan ~/Pictures ~/Movies --format text
```

Write machine-readable output:

```bash
uv run media-dedupe scan ~/Pictures --format json
```

Write a report to a directory or an explicit file path:

```bash
uv run media-dedupe scan ~/Pictures --format json --output reports
uv run media-dedupe scan ~/Pictures --format json --output reports/latest.json
```

Render the last persisted report:

```bash
uv run media-dedupe report --format json
```

Inspect or clear cache:

```bash
uv run media-dedupe cache info
uv run media-dedupe cache --cache .media-dedupe/cache.sqlite info
uv run media-dedupe cache clear
```

## Safety

The tool does not delete or move files. Report items are marked as `keep_recommended`, `cleanup_candidate`, or `review_required` so users can review decisions before taking action.
