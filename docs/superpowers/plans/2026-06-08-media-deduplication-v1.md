# Media Deduplication V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Python/uv command-line media deduplication tool that scans one or more directories, detects exact and near-duplicate images/videos, stores reusable facts in SQLite, and outputs quality-first keep recommendations in text and JSON reports.

**Architecture:** Implement a layered pipeline: CLI → file discovery → SQLite cache → exact hashing → image perceptual hashing → video ffprobe/frame hashing → candidate matching/grouping → quality scoring → reports. Keep similarity detection separate from keep recommendation so thresholds and quality rules can evolve independently.

**Tech Stack:** Python 3.14 project managed by uv; standard library `argparse`, `sqlite3`, `hashlib`, `json`, `subprocess`; runtime dependencies `pillow`, `imagehash`, `rich`; dev dependencies `pytest`, existing `ruff`, existing `ty`. Video support uses system `ffmpeg` and `ffprobe` checked by `doctor`.

---

## File Structure

Create a package under `src/media_dedupe/` and keep `main.py` as a thin compatibility entrypoint.

- `pyproject.toml`: add runtime/dev dependencies and console script entrypoint.
- `main.py`: delegate to package CLI.
- `src/media_dedupe/__init__.py`: package metadata.
- `src/media_dedupe/__main__.py`: `python -m media_dedupe` entrypoint.
- `src/media_dedupe/cli.py`: argparse commands `scan`, `report`, `cache`, `doctor`.
- `src/media_dedupe/config.py`: constants, supported extensions, default thresholds.
- `src/media_dedupe/models.py`: dataclasses/enums shared across modules.
- `src/media_dedupe/discovery.py`: recursive media file discovery.
- `src/media_dedupe/cache.py`: SQLite schema, connection, upserts, query helpers.
- `src/media_dedupe/hashing.py`: exact file hashing and hash distance helpers.
- `src/media_dedupe/image_fingerprint.py`: image metadata and pHash extraction.
- `src/media_dedupe/video_fingerprint.py`: ffprobe metadata and ffmpeg frame extraction.
- `src/media_dedupe/matching.py`: candidate generation, similarity edges, connected components.
- `src/media_dedupe/scoring.py`: quality scoring and keep recommendation.
- `src/media_dedupe/reports.py`: text and JSON report generation.
- `src/media_dedupe/pipeline.py`: orchestration for `scan` and `report` commands.
- `tests/`: pytest tests mirroring module responsibilities.
- `tests/fixtures/`: small generated fixture files used by tests.

Each task below is intentionally small enough to implement and verify independently.

## Global Rules for Every Python-Code Task

After modifying Python files, run the project-required checks on each modified Python file:

```bash
uv run ruff format <file>
uv run ruff check <file>
uv run ty check <file>
```

For tests, run targeted pytest commands first, then the full suite once the task passes.

Commits are listed because the planning skill expects frequent commits, but do not commit unless the user explicitly asks for commits during execution.

---

### Task 1: Project Package and CLI Skeleton

**Files:**
- Modify: `pyproject.toml`
- Modify: `main.py`
- Create: `src/media_dedupe/__init__.py`
- Create: `src/media_dedupe/__main__.py`
- Create: `src/media_dedupe/cli.py`
- Create: `src/media_dedupe/config.py`
- Create: `tests/test_cli.py`

- [ ] **Step 1: Add dependencies and console script to `pyproject.toml`**

Use uv to add runtime and test dependencies:

```bash
uv add pillow imagehash rich
uv add --dev pytest
```

Then edit `pyproject.toml` so it includes this script entry:

```toml
[project.scripts]
media-dedupe = "media_dedupe.cli:main"
```

Expected: `dependencies` includes `pillow`, `imagehash`, and `rich`; `dependency-groups.dev` includes `pytest` while keeping `ruff` and `ty`.

- [ ] **Step 2: Write failing CLI smoke tests in `tests/test_cli.py`**

```python
from __future__ import annotations

import pytest

from media_dedupe.cli import build_parser, run


def test_parser_accepts_scan_command() -> None:
    parser = build_parser()
    args = parser.parse_args(["scan", "~/Pictures", "~/Movies", "--format", "json"])

    assert args.command == "scan"
    assert args.paths == ["~/Pictures", "~/Movies"]
    assert args.format == "json"


def test_run_doctor_returns_success(capsys: pytest.CaptureFixture[str]) -> None:
    exit_code = run(["doctor"])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert "media-dedupe doctor" in captured.out

```

- [ ] **Step 3: Run tests to verify they fail**

```bash
uv run pytest tests/test_cli.py -v
```

Expected: FAIL because `media_dedupe.cli` does not exist.

- [ ] **Step 4: Create package constants in `src/media_dedupe/config.py`**

```python
from __future__ import annotations

DEFAULT_SIMILARITY_THRESHOLD = 0.80
DEFAULT_WORKERS = 4
DEFAULT_CACHE_PATH = ".media-dedupe/cache.sqlite"

IMAGE_EXTENSIONS = frozenset(
    {
        ".jpg",
        ".jpeg",
        ".png",
        ".webp",
        ".heic",
        ".heif",
        ".tiff",
        ".bmp",
        ".gif",
    }
)

VIDEO_EXTENSIONS = frozenset(
    {
        ".mp4",
        ".mov",
        ".mkv",
        ".avi",
        ".webm",
        ".m4v",
        ".flv",
        ".wmv",
        ".mpeg",
        ".mpg",
    }
)

SKIPPED_DIRECTORY_NAMES = frozenset({".git", ".venv", "__pycache__", ".DS_Store"})
```

- [ ] **Step 5: Create `src/media_dedupe/cli.py` skeleton**

```python
from __future__ import annotations

import argparse
from collections.abc import Sequence

from rich.console import Console

from media_dedupe.config import DEFAULT_CACHE_PATH, DEFAULT_SIMILARITY_THRESHOLD, DEFAULT_WORKERS


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="media-dedupe")
    subparsers = parser.add_subparsers(dest="command", required=True)

    scan = subparsers.add_parser("scan", help="scan directories for duplicate media")
    scan.add_argument("paths", nargs="+")
    scan.add_argument("--similarity", type=float, default=DEFAULT_SIMILARITY_THRESHOLD)
    scan.add_argument("--workers", type=int, default=DEFAULT_WORKERS)
    scan.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    scan.add_argument("--output", default="reports")
    scan.add_argument("--format", choices=("text", "json", "html"), default="text")
    scan.add_argument("--no-video", action="store_true")
    scan.add_argument("--no-image", action="store_true")
    scan.add_argument("--recursive", action=argparse.BooleanOptionalAction, default=True)

    report = subparsers.add_parser("report", help="render the last scan report")
    report.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    report.add_argument("--format", choices=("text", "json", "html"), default="text")
    report.add_argument("--output", default="reports")

    cache = subparsers.add_parser("cache", help="inspect or clear cache")
    cache_subparsers = cache.add_subparsers(dest="cache_command", required=True)
    cache_subparsers.add_parser("info", help="show cache information")
    cache_subparsers.add_parser("clear", help="clear cache database")
    cache.add_argument("--cache", default=DEFAULT_CACHE_PATH)

    subparsers.add_parser("doctor", help="check runtime dependencies")
    return parser


def run(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    console = Console()

    if args.command == "doctor":
        console.print("media-dedupe doctor")
        console.print("Python package checks will run in a later task.")
        return 0

    if args.command == "scan":
        console.print("scan command is wired; pipeline will run in a later task.")
        return 0

    if args.command == "report":
        console.print("report command is wired; rendering will run in a later task.")
        return 0

    if args.command == "cache":
        console.print(f"cache {args.cache_command} command is wired.")
        return 0

    parser.error(f"unsupported command: {args.command}")
    return 2


def main() -> None:
    raise SystemExit(run())
```

- [ ] **Step 6: Create package entrypoints**

`src/media_dedupe/__init__.py`:

```python
from __future__ import annotations

__all__ = ["__version__"]

__version__ = "0.1.0"
```

`src/media_dedupe/__main__.py`:

```python
from __future__ import annotations

from media_dedupe.cli import main


if __name__ == "__main__":
    main()
```

`main.py`:

```python
from __future__ import annotations

from media_dedupe.cli import main


if __name__ == "__main__":
    main()
```

- [ ] **Step 7: Verify CLI tests pass**

```bash
uv run pytest tests/test_cli.py -v
```

Expected: PASS.

- [ ] **Step 8: Run required checks on modified Python files**

```bash
uv run ruff format main.py src/media_dedupe/__init__.py src/media_dedupe/__main__.py src/media_dedupe/cli.py src/media_dedupe/config.py tests/test_cli.py
uv run ruff check main.py src/media_dedupe/__init__.py src/media_dedupe/__main__.py src/media_dedupe/cli.py src/media_dedupe/config.py tests/test_cli.py
uv run ty check main.py src/media_dedupe/__init__.py src/media_dedupe/__main__.py src/media_dedupe/cli.py src/media_dedupe/config.py tests/test_cli.py
```

Expected: all commands exit 0.

- [ ] **Step 9: Commit if commits are authorized**

```bash
git add pyproject.toml uv.lock main.py src/media_dedupe tests/test_cli.py
git commit -m "feat: add media dedupe cli skeleton"
```

---

### Task 2: Core Models and File Discovery

**Files:**
- Create: `src/media_dedupe/models.py`
- Create: `src/media_dedupe/discovery.py`
- Create: `tests/test_discovery.py`

- [ ] **Step 1: Write failing discovery tests**

```python
from __future__ import annotations

from pathlib import Path

from media_dedupe.discovery import discover_media_files
from media_dedupe.models import MediaType


def test_discover_media_files_finds_supported_media(tmp_path: Path) -> None:
    image = tmp_path / "photo.JPG"
    video = tmp_path / "clip.mp4"
    text = tmp_path / "notes.txt"
    image.write_bytes(b"image")
    video.write_bytes(b"video")
    text.write_text("notes", encoding="utf-8")

    discovered = list(discover_media_files([tmp_path], recursive=True))

    assert [item.path for item in discovered] == [image, video]
    assert [item.media_type for item in discovered] == [MediaType.IMAGE, MediaType.VIDEO]


def test_discover_media_files_skips_hidden_and_venv_directories(tmp_path: Path) -> None:
    visible = tmp_path / "visible.png"
    hidden_dir = tmp_path / ".hidden"
    venv_dir = tmp_path / ".venv"
    hidden_dir.mkdir()
    venv_dir.mkdir()
    visible.write_bytes(b"visible")
    (hidden_dir / "secret.png").write_bytes(b"secret")
    (venv_dir / "library.jpg").write_bytes(b"library")

    discovered = list(discover_media_files([tmp_path], recursive=True))

    assert [item.path for item in discovered] == [visible]
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_discovery.py -v
```

Expected: FAIL because discovery module does not exist.

- [ ] **Step 3: Implement core models in `src/media_dedupe/models.py`**

```python
from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path


class MediaType(StrEnum):
    IMAGE = "image"
    VIDEO = "video"


class FileStatus(StrEnum):
    ACTIVE = "active"
    MISSING = "missing"
    ERROR = "error"


class GroupType(StrEnum):
    EXACT = "exact"
    SIMILAR_IMAGE = "similar_image"
    SIMILAR_VIDEO = "similar_video"


class RecommendationAction(StrEnum):
    KEEP_RECOMMENDED = "keep_recommended"
    CLEANUP_CANDIDATE = "cleanup_candidate"
    REVIEW_REQUIRED = "review_required"


@dataclass(frozen=True, slots=True)
class DiscoveredFile:
    path: Path
    media_type: MediaType
    extension: str
    size_bytes: int
    mtime_ns: int
    inode: int
    device: int


@dataclass(frozen=True, slots=True)
class ImageMetadata:
    width: int
    height: int
    format_name: str
    orientation: int | None
    is_readable: bool


@dataclass(frozen=True, slots=True)
class VideoMetadata:
    duration_ms: int
    width: int
    height: int
    frame_rate: float | None
    bit_rate: int | None
    codec: str | None
    has_audio: bool
    is_readable: bool


@dataclass(frozen=True, slots=True)
class SimilarityEdge:
    left_file_id: int
    right_file_id: int
    group_type: GroupType
    similarity: float
    reasons: tuple[str, ...] = field(default_factory=tuple)


@dataclass(frozen=True, slots=True)
class ReportItem:
    file_id: int
    path: str
    action: RecommendationAction
    similarity: float
    quality_score: float
    reasons: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class ReportGroup:
    group_id: int
    group_type: GroupType
    confidence: float
    recommended_file_id: int
    items: tuple[ReportItem, ...]
```

- [ ] **Step 4: Implement discovery in `src/media_dedupe/discovery.py`**

```python
from __future__ import annotations

from collections.abc import Iterable, Iterator
from pathlib import Path

from media_dedupe.config import IMAGE_EXTENSIONS, SKIPPED_DIRECTORY_NAMES, VIDEO_EXTENSIONS
from media_dedupe.models import DiscoveredFile, MediaType


def classify_media_type(path: Path) -> MediaType | None:
    suffix = path.suffix.lower()
    if suffix in IMAGE_EXTENSIONS:
        return MediaType.IMAGE
    if suffix in VIDEO_EXTENSIONS:
        return MediaType.VIDEO
    return None


def discover_media_files(paths: Iterable[Path], *, recursive: bool) -> Iterator[DiscoveredFile]:
    seen_real_paths: set[Path] = set()
    for root in sorted(Path(path).expanduser() for path in paths):
        if root.is_file():
            yield from _discover_file(root, seen_real_paths)
            continue
        if not root.exists():
            continue
        if recursive:
            yield from _walk_directory(root, seen_real_paths)
        else:
            for child in sorted(root.iterdir()):
                if child.is_file():
                    yield from _discover_file(child, seen_real_paths)


def _walk_directory(root: Path, seen_real_paths: set[Path]) -> Iterator[DiscoveredFile]:
    for child in sorted(root.iterdir()):
        if child.is_dir():
            if child.name.startswith(".") or child.name in SKIPPED_DIRECTORY_NAMES:
                continue
            yield from _walk_directory(child, seen_real_paths)
        elif child.is_file():
            yield from _discover_file(child, seen_real_paths)


def _discover_file(path: Path, seen_real_paths: set[Path]) -> Iterator[DiscoveredFile]:
    media_type = classify_media_type(path)
    if media_type is None:
        return

    try:
        real_path = path.resolve(strict=True)
        stat = path.stat()
    except OSError:
        return

    if real_path in seen_real_paths:
        return
    seen_real_paths.add(real_path)

    yield DiscoveredFile(
        path=path,
        media_type=media_type,
        extension=path.suffix.lower(),
        size_bytes=stat.st_size,
        mtime_ns=stat.st_mtime_ns,
        inode=stat.st_ino,
        device=stat.st_dev,
    )
```

- [ ] **Step 5: Run discovery tests**

```bash
uv run pytest tests/test_discovery.py -v
```

Expected: PASS.

- [ ] **Step 6: Run required checks**

```bash
uv run ruff format src/media_dedupe/models.py src/media_dedupe/discovery.py tests/test_discovery.py
uv run ruff check src/media_dedupe/models.py src/media_dedupe/discovery.py tests/test_discovery.py
uv run ty check src/media_dedupe/models.py src/media_dedupe/discovery.py tests/test_discovery.py
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit if commits are authorized**

```bash
git add src/media_dedupe/models.py src/media_dedupe/discovery.py tests/test_discovery.py
git commit -m "feat: discover media files"
```

---

### Task 3: SQLite Cache Schema and File Upserts

**Files:**
- Create: `src/media_dedupe/cache.py`
- Create: `tests/test_cache.py`

- [ ] **Step 1: Write failing cache tests**

```python
from __future__ import annotations

from pathlib import Path

from media_dedupe.cache import Cache

from media_dedupe.models import DiscoveredFile, MediaType


def make_file(path: Path, size: int = 12) -> DiscoveredFile:
    path.write_bytes(b"x" * size)
    stat = path.stat()
    return DiscoveredFile(
        path=path,
        media_type=MediaType.IMAGE,
        extension=path.suffix,
        size_bytes=stat.st_size,
        mtime_ns=stat.st_mtime_ns,
        inode=stat.st_ino,
        device=stat.st_dev,
    )


def test_cache_upserts_discovered_file_and_detects_unchanged(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()
    discovered = make_file(tmp_path / "a.jpg")

    file_id = cache.upsert_discovered_file(discovered)

    assert file_id > 0
    assert cache.is_file_unchanged(discovered) is True


def test_cache_marks_missing_files(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()
    first = make_file(tmp_path / "a.jpg")
    second = make_file(tmp_path / "b.jpg")
    first_id = cache.upsert_discovered_file(first)
    second_id = cache.upsert_discovered_file(second)

    cache.mark_missing_except({first_id})

    assert cache.get_file_status(first_id) == "active"
    assert cache.get_file_status(second_id) == "missing"
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_cache.py -v
```

Expected: FAIL because `Cache` does not exist.

- [ ] **Step 3: Implement cache schema and helpers**

```python
from __future__ import annotations

import sqlite3
from pathlib import Path

from media_dedupe.models import DiscoveredFile, FileStatus


class Cache:
    def __init__(self, path: Path | str) -> None:
        self.path = Path(path)

    def connect(self) -> sqlite3.Connection:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        connection = sqlite3.connect(self.path)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys = ON")
        return connection

    def initialize(self) -> None:
        with self.connect() as connection:
            connection.executescript(
                """
                CREATE TABLE IF NOT EXISTS files (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    path TEXT NOT NULL UNIQUE,
                    media_type TEXT NOT NULL,
                    extension TEXT NOT NULL,
                    size_bytes INTEGER NOT NULL,
                    mtime_ns INTEGER NOT NULL,
                    inode INTEGER NOT NULL,
                    device INTEGER NOT NULL,
                    status TEXT NOT NULL,
                    last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );

                CREATE TABLE IF NOT EXISTS file_hashes (
                    file_id INTEGER PRIMARY KEY,
                    size_bytes INTEGER NOT NULL,
                    quick_hash TEXT,
                    full_hash TEXT,
                    hash_algorithm TEXT NOT NULL,
                    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS media_metadata (
                    file_id INTEGER PRIMARY KEY,
                    width INTEGER,
                    height INTEGER,
                    duration_ms INTEGER,
                    frame_rate REAL,
                    bit_rate INTEGER,
                    codec TEXT,
                    format_name TEXT,
                    orientation INTEGER,
                    has_audio INTEGER,
                    is_readable INTEGER NOT NULL,
                    metadata_type TEXT NOT NULL,
                    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS perceptual_hashes (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    file_id INTEGER NOT NULL,
                    frame_index INTEGER,
                    timestamp_ms INTEGER,
                    hash_type TEXT NOT NULL,
                    hash_value TEXT NOT NULL,
                    hash_size INTEGER NOT NULL,
                    frame_quality REAL,
                    decoder TEXT,
                    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS duplicate_groups (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    scan_run_id INTEGER,
                    group_type TEXT NOT NULL,
                    confidence REAL NOT NULL,
                    recommended_file_id INTEGER NOT NULL
                );

                CREATE TABLE IF NOT EXISTS duplicate_items (
                    group_id INTEGER NOT NULL,
                    file_id INTEGER NOT NULL,
                    action TEXT NOT NULL,
                    similarity REAL NOT NULL,
                    quality_score REAL NOT NULL,
                    reasons_json TEXT NOT NULL,
                    FOREIGN KEY(group_id) REFERENCES duplicate_groups(id) ON DELETE CASCADE,
                    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS scan_runs (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    input_paths_json TEXT NOT NULL,
                    config_json TEXT NOT NULL,
                    started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    finished_at TEXT,
                    files_seen INTEGER NOT NULL DEFAULT 0,
                    files_failed INTEGER NOT NULL DEFAULT 0
                );

                CREATE TABLE IF NOT EXISTS errors (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    scan_run_id INTEGER,
                    path TEXT NOT NULL,
                    stage TEXT NOT NULL,
                    message TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
                );
                """
            )

    def upsert_discovered_file(self, discovered: DiscoveredFile) -> int:
        with self.connect() as connection:
            connection.execute(
                """
                INSERT INTO files (path, media_type, extension, size_bytes, mtime_ns, inode, device, status, last_seen_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(path) DO UPDATE SET
                    media_type = excluded.media_type,
                    extension = excluded.extension,
                    size_bytes = excluded.size_bytes,
                    mtime_ns = excluded.mtime_ns,
                    inode = excluded.inode,
                    device = excluded.device,
                    status = excluded.status,
                    last_seen_at = CURRENT_TIMESTAMP
                """,
                (
                    str(discovered.path),
                    discovered.media_type.value,
                    discovered.extension,
                    discovered.size_bytes,
                    discovered.mtime_ns,
                    discovered.inode,
                    discovered.device,
                    FileStatus.ACTIVE.value,
                ),
            )
            row = connection.execute("SELECT id FROM files WHERE path = ?", (str(discovered.path),)).fetchone()
            return int(row["id"])

    def is_file_unchanged(self, discovered: DiscoveredFile) -> bool:
        with self.connect() as connection:
            row = connection.execute(
                "SELECT size_bytes, mtime_ns, inode, device, status FROM files WHERE path = ?",
                (str(discovered.path),),
            ).fetchone()
        if row is None:
            return False
        return (
            int(row["size_bytes"]) == discovered.size_bytes
            and int(row["mtime_ns"]) == discovered.mtime_ns
            and int(row["inode"]) == discovered.inode
            and int(row["device"]) == discovered.device
            and str(row["status"]) == FileStatus.ACTIVE.value
        )

    def mark_missing_except(self, active_file_ids: set[int]) -> None:
        with self.connect() as connection:
            if not active_file_ids:
                connection.execute("UPDATE files SET status = ?", (FileStatus.MISSING.value,))
                return
            placeholders = ",".join("?" for _ in active_file_ids)
            connection.execute(
                f"UPDATE files SET status = ? WHERE id NOT IN ({placeholders})",
                (FileStatus.MISSING.value, *sorted(active_file_ids)),
            )

    def get_file_status(self, file_id: int) -> str:
        with self.connect() as connection:
            row = connection.execute("SELECT status FROM files WHERE id = ?", (file_id,)).fetchone()
        if row is None:
            raise KeyError(file_id)
        return str(row["status"])
```

- [ ] **Step 4: Run cache tests**

```bash
uv run pytest tests/test_cache.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/cache.py tests/test_cache.py
uv run ruff check src/media_dedupe/cache.py tests/test_cache.py
uv run ty check src/media_dedupe/cache.py tests/test_cache.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/cache.py tests/test_cache.py
git commit -m "feat: add sqlite cache schema"
```

---

### Task 4: Exact Hashing and Distance Helpers

**Files:**
- Create: `src/media_dedupe/hashing.py`
- Create: `tests/test_hashing.py`

- [ ] **Step 1: Write failing hashing tests**

```python
from __future__ import annotations

from pathlib import Path

from media_dedupe.hashing import file_sha256, hamming_distance, hash_similarity


def test_file_sha256_matches_identical_files(tmp_path: Path) -> None:
    first = tmp_path / "a.bin"
    second = tmp_path / "b.bin"
    first.write_bytes(b"same content")
    second.write_bytes(b"same content")

    assert file_sha256(first) == file_sha256(second)


def test_hamming_distance_counts_different_bits() -> None:
    assert hamming_distance("0f", "00") == 4


def test_hash_similarity_returns_one_for_identical_hashes() -> None:
    assert hash_similarity("ffff", "ffff") == 1.0
    assert hash_similarity("ffff", "0000") == 0.0
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_hashing.py -v
```

Expected: FAIL because hashing module does not exist.

- [ ] **Step 3: Implement hashing helpers**

```python
from __future__ import annotations

import hashlib
from pathlib import Path


def file_sha256(path: Path, *, chunk_size: int = 1024 * 1024) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        while chunk := file.read(chunk_size):
            digest.update(chunk)
    return digest.hexdigest()


def hamming_distance(left_hex: str, right_hex: str) -> int:
    if len(left_hex) != len(right_hex):
        raise ValueError("hashes must have the same length")
    left = int(left_hex, 16)
    right = int(right_hex, 16)
    return (left ^ right).bit_count()


def hash_similarity(left_hex: str, right_hex: str) -> float:
    bit_count = len(left_hex) * 4
    if bit_count == 0:
        raise ValueError("hashes must not be empty")
    distance = hamming_distance(left_hex, right_hex)
    return max(0.0, 1.0 - (distance / bit_count))
```

- [ ] **Step 4: Run hashing tests**

```bash
uv run pytest tests/test_hashing.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/hashing.py tests/test_hashing.py
uv run ruff check src/media_dedupe/hashing.py tests/test_hashing.py
uv run ty check src/media_dedupe/hashing.py tests/test_hashing.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/hashing.py tests/test_hashing.py
git commit -m "feat: add exact hashing helpers"
```

---

### Task 5: Image Metadata and Perceptual Hashing

**Files:**
- Create: `src/media_dedupe/image_fingerprint.py`
- Create: `tests/test_image_fingerprint.py`

- [ ] **Step 1: Write failing image fingerprint tests**

```python
from __future__ import annotations

from pathlib import Path

from PIL import Image

from media_dedupe.image_fingerprint import compute_image_phash, read_image_metadata


def test_read_image_metadata_returns_size_and_format(tmp_path: Path) -> None:
    image_path = tmp_path / "photo.png"
    Image.new("RGB", (32, 24), color="red").save(image_path)

    metadata = read_image_metadata(image_path)

    assert metadata.width == 32
    assert metadata.height == 24
    assert metadata.format_name == "PNG"
    assert metadata.is_readable is True


def test_compute_image_phash_is_stable_for_same_file(tmp_path: Path) -> None:
    image_path = tmp_path / "photo.png"
    Image.new("RGB", (32, 24), color="blue").save(image_path)

    first = compute_image_phash(image_path)
    second = compute_image_phash(image_path)

    assert first == second
    assert len(first) > 0
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_image_fingerprint.py -v
```

Expected: FAIL because image fingerprint module does not exist.

- [ ] **Step 3: Implement image metadata and pHash**

```python
from __future__ import annotations

from pathlib import Path

import imagehash
from PIL import Image, UnidentifiedImageError

from media_dedupe.models import ImageMetadata


def read_image_metadata(path: Path) -> ImageMetadata:
    try:
        with Image.open(path) as image:
            width, height = image.size
            orientation = image.getexif().get(274)
            return ImageMetadata(
                width=width,
                height=height,
                format_name=image.format or path.suffix.lstrip(".").upper(),
                orientation=int(orientation) if orientation is not None else None,
                is_readable=True,
            )
    except (OSError, UnidentifiedImageError):
        return ImageMetadata(width=0, height=0, format_name="", orientation=None, is_readable=False)


def compute_image_phash(path: Path, *, hash_size: int = 8) -> str:
    with Image.open(path) as image:
        return str(imagehash.phash(image, hash_size=hash_size))
```

- [ ] **Step 4: Run image fingerprint tests**

```bash
uv run pytest tests/test_image_fingerprint.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/image_fingerprint.py tests/test_image_fingerprint.py
uv run ruff check src/media_dedupe/image_fingerprint.py tests/test_image_fingerprint.py
uv run ty check src/media_dedupe/image_fingerprint.py tests/test_image_fingerprint.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/image_fingerprint.py tests/test_image_fingerprint.py
git commit -m "feat: add image perceptual hashing"
```

---

### Task 6: Video Metadata and Frame Fingerprinting

**Files:**
- Create: `src/media_dedupe/video_fingerprint.py`
- Create: `tests/test_video_fingerprint.py`

- [ ] **Step 1: Write failing video command tests using monkeypatch**

```python
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from media_dedupe.video_fingerprint import build_frame_timestamps, ffprobe_metadata


def test_build_frame_timestamps_skips_edges() -> None:
    assert build_frame_timestamps(duration_ms=10_000, frame_count=3) == [500, 5_000, 9_500]


def test_ffprobe_metadata_parses_json(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    video_path = tmp_path / "clip.mp4"
    video_path.write_bytes(b"fake")

    def fake_run(*args: object, **kwargs: object) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout=(
                '{"streams":[{"codec_type":"video","width":1920,"height":1080,'
                '"duration":"12.5","avg_frame_rate":"30000/1001","bit_rate":"4000000",'
                '"codec_name":"h264"},{"codec_type":"audio"}],"format":{"duration":"12.5"}}'
            ),
            stderr="",
        )

    monkeypatch.setattr(subprocess, "run", fake_run)

    metadata = ffprobe_metadata(video_path)

    assert metadata.duration_ms == 12_500
    assert metadata.width == 1920
    assert metadata.height == 1080
    assert metadata.codec == "h264"
    assert metadata.has_audio is True
    assert metadata.is_readable is True
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_video_fingerprint.py -v
```

Expected: FAIL because video fingerprint module does not exist.

- [ ] **Step 3: Implement ffprobe parsing and timestamp selection**

```python
from __future__ import annotations

import json
import subprocess
from pathlib import Path

from media_dedupe.models import VideoMetadata


def build_frame_timestamps(*, duration_ms: int, frame_count: int) -> list[int]:
    if duration_ms <= 0 or frame_count <= 0:
        return []
    if frame_count == 1:
        return [duration_ms // 2]
    start = int(duration_ms * 0.05)
    end = int(duration_ms * 0.95)
    if start >= end:
        return [duration_ms // 2]
    step = (end - start) / (frame_count - 1)
    return [round(start + (step * index)) for index in range(frame_count)]


def ffprobe_metadata(path: Path) -> VideoMetadata:
    try:
        completed = subprocess.run(
            [
                "ffprobe",
                "-v",
                "error",
                "-print_format",
                "json",
                "-show_format",
                "-show_streams",
                str(path),
            ],
            check=True,
            capture_output=True,
            text=True,
            timeout=30,
        )
        payload = json.loads(completed.stdout)
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired, json.JSONDecodeError):
        return VideoMetadata(
            duration_ms=0,
            width=0,
            height=0,
            frame_rate=None,
            bit_rate=None,
            codec=None,
            has_audio=False,
            is_readable=False,
        )

    streams = payload.get("streams", [])
    video_stream = next((stream for stream in streams if stream.get("codec_type") == "video"), {})
    has_audio = any(stream.get("codec_type") == "audio" for stream in streams)
    duration_seconds = _float_or_none(video_stream.get("duration")) or _float_or_none(
        payload.get("format", {}).get("duration")
    )

    return VideoMetadata(
        duration_ms=round((duration_seconds or 0.0) * 1000),
        width=int(video_stream.get("width") or 0),
        height=int(video_stream.get("height") or 0),
        frame_rate=_parse_frame_rate(video_stream.get("avg_frame_rate")),
        bit_rate=_int_or_none(video_stream.get("bit_rate")),
        codec=video_stream.get("codec_name"),
        has_audio=has_audio,
        is_readable=bool(video_stream),
    )


def _parse_frame_rate(value: object) -> float | None:
    if not isinstance(value, str) or value in {"", "0/0"}:
        return None
    if "/" in value:
        numerator_text, denominator_text = value.split("/", 1)
        denominator = float(denominator_text)
        if denominator == 0:
            return None
        return float(numerator_text) / denominator
    return float(value)


def _float_or_none(value: object) -> float | None:
    try:
        return float(value) if value is not None else None
    except (TypeError, ValueError):
        return None


def _int_or_none(value: object) -> int | None:
    try:
        return int(value) if value is not None else None
    except (TypeError, ValueError):
        return None
```

- [ ] **Step 4: Add frame extraction function with an isolated test**

Append this test to `tests/test_video_fingerprint.py`:

```python
from media_dedupe.video_fingerprint import extract_frame_to_image


def test_extract_frame_to_image_invokes_ffmpeg(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    calls: list[list[str]] = []

    def fake_run(args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        output_path = Path(args[-1])
        output_path.write_bytes(b"frame")
        return subprocess.CompletedProcess(args=args, returncode=0, stdout="", stderr="")

    monkeypatch.setattr(subprocess, "run", fake_run)
    output = tmp_path / "frame.jpg"

    result = extract_frame_to_image(tmp_path / "clip.mp4", timestamp_ms=1_500, output_path=output)

    assert result == output
    assert output.exists()
    assert "ffmpeg" in calls[0][0]
```

Append this implementation to `src/media_dedupe/video_fingerprint.py`:

```python
def extract_frame_to_image(path: Path, *, timestamp_ms: int, output_path: Path) -> Path | None:
    output_path.parent.mkdir(parents=True, exist_ok=True)
    timestamp_seconds = timestamp_ms / 1000
    try:
        subprocess.run(
            [
                "ffmpeg",
                "-y",
                "-ss",
                f"{timestamp_seconds:.3f}",
                "-i",
                str(path),
                "-frames:v",
                "1",
                "-q:v",
                "2",
                str(output_path),
            ],
            check=True,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return None
    return output_path if output_path.exists() else None
```

- [ ] **Step 5: Run video tests**

```bash
uv run pytest tests/test_video_fingerprint.py -v
```

Expected: PASS.

- [ ] **Step 6: Run required checks**

```bash
uv run ruff format src/media_dedupe/video_fingerprint.py tests/test_video_fingerprint.py
uv run ruff check src/media_dedupe/video_fingerprint.py tests/test_video_fingerprint.py
uv run ty check src/media_dedupe/video_fingerprint.py tests/test_video_fingerprint.py
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit if commits are authorized**

```bash
git add src/media_dedupe/video_fingerprint.py tests/test_video_fingerprint.py
git commit -m "feat: add video metadata helpers"
```

---

### Task 7: Matching and Connected-Component Grouping

**Files:**
- Create: `src/media_dedupe/matching.py`
- Create: `tests/test_matching.py`

- [ ] **Step 1: Write failing matching tests**

```python
from __future__ import annotations

from media_dedupe.matching import connected_components, is_video_metadata_candidate
from media_dedupe.models import GroupType, SimilarityEdge, VideoMetadata


def test_connected_components_groups_transitive_edges() -> None:
    edges = [
        SimilarityEdge(1, 2, GroupType.EXACT, 1.0),
        SimilarityEdge(2, 3, GroupType.EXACT, 1.0),
        SimilarityEdge(9, 10, GroupType.SIMILAR_IMAGE, 0.9),
    ]

    assert connected_components(edges) == [{1, 2, 3}, {9, 10}]


def test_video_metadata_candidate_allows_small_duration_difference() -> None:
    left = VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)
    right = VideoMetadata(10_300, 1280, 720, 30.0, 2_000_000, "h264", True, True)

    assert is_video_metadata_candidate(left, right) is True


def test_video_metadata_candidate_rejects_large_duration_difference() -> None:
    left = VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)
    right = VideoMetadata(20_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    assert is_video_metadata_candidate(left, right) is False
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_matching.py -v
```

Expected: FAIL because matching module does not exist.

- [ ] **Step 3: Implement matching helpers**

```python
from __future__ import annotations

from media_dedupe.models import SimilarityEdge, VideoMetadata


def connected_components(edges: list[SimilarityEdge]) -> list[set[int]]:
    parent: dict[int, int] = {}

    def find(value: int) -> int:
        parent.setdefault(value, value)
        if parent[value] != value:
            parent[value] = find(parent[value])
        return parent[value]

    def union(left: int, right: int) -> None:
        left_root = find(left)
        right_root = find(right)
        if left_root != right_root:
            parent[right_root] = left_root

    for edge in edges:
        union(edge.left_file_id, edge.right_file_id)

    groups: dict[int, set[int]] = {}
    for file_id in parent:
        root = find(file_id)
        groups.setdefault(root, set()).add(file_id)
    return sorted(groups.values(), key=lambda group: min(group))


def is_video_metadata_candidate(
    left: VideoMetadata,
    right: VideoMetadata,
    *,
    duration_ratio_tolerance: float = 0.05,
    duration_absolute_tolerance_ms: int = 3_000,
    aspect_ratio_tolerance: float = 0.05,
) -> bool:
    if not left.is_readable or not right.is_readable:
        return False
    if left.duration_ms <= 0 or right.duration_ms <= 0:
        return False
    duration_delta = abs(left.duration_ms - right.duration_ms)
    allowed_delta = max(
        duration_absolute_tolerance_ms,
        round(max(left.duration_ms, right.duration_ms) * duration_ratio_tolerance),
    )
    if duration_delta > allowed_delta:
        return False
    return _aspect_ratio_close(left.width, left.height, right.width, right.height, aspect_ratio_tolerance)


def _aspect_ratio_close(
    left_width: int,
    left_height: int,
    right_width: int,
    right_height: int,
    tolerance: float,
) -> bool:
    if min(left_width, left_height, right_width, right_height) <= 0:
        return False
    left_ratio = left_width / left_height
    right_ratio = right_width / right_height
    return abs(left_ratio - right_ratio) <= tolerance
```

- [ ] **Step 4: Run matching tests**

```bash
uv run pytest tests/test_matching.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/matching.py tests/test_matching.py
uv run ruff check src/media_dedupe/matching.py tests/test_matching.py
uv run ty check src/media_dedupe/matching.py tests/test_matching.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/matching.py tests/test_matching.py
git commit -m "feat: add duplicate grouping helpers"
```

---

### Task 8: Quality Scoring and Recommendation Actions

**Files:**
- Create: `src/media_dedupe/scoring.py`
- Create: `tests/test_scoring.py`

- [ ] **Step 1: Write failing scoring tests**

```python
from __future__ import annotations

from media_dedupe.models import ImageMetadata, RecommendationAction, VideoMetadata
from media_dedupe.scoring import choose_recommended_item, score_image_quality, score_video_quality


def test_score_image_quality_prefers_higher_resolution_readable_image() -> None:
    low = ImageMetadata(800, 600, "JPEG", None, True)
    high = ImageMetadata(4000, 3000, "JPEG", None, True)

    assert score_image_quality(high, size_bytes=2_000_000) > score_image_quality(low, size_bytes=2_000_000)


def test_score_video_quality_prefers_readable_higher_resolution_video() -> None:
    low = VideoMetadata(60_000, 1280, 720, 30.0, 2_000_000, "h264", True, True)
    high = VideoMetadata(60_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    assert score_video_quality(high) > score_video_quality(low)


def test_choose_recommended_item_marks_close_scores_for_review() -> None:
    actions = choose_recommended_item({1: 0.90, 2: 0.89}, review_delta=0.02)

    assert actions[1] == RecommendationAction.REVIEW_REQUIRED
    assert actions[2] == RecommendationAction.REVIEW_REQUIRED


def test_choose_recommended_item_selects_clear_winner() -> None:
    actions = choose_recommended_item({1: 0.95, 2: 0.50}, review_delta=0.02)

    assert actions[1] == RecommendationAction.KEEP_RECOMMENDED
    assert actions[2] == RecommendationAction.CLEANUP_CANDIDATE
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_scoring.py -v
```

Expected: FAIL because scoring module does not exist.

- [ ] **Step 3: Implement quality scoring**

```python
from __future__ import annotations

import math

from media_dedupe.models import ImageMetadata, RecommendationAction, VideoMetadata


def score_image_quality(metadata: ImageMetadata, *, size_bytes: int) -> float:
    if not metadata.is_readable:
        return 0.0
    megapixels = (metadata.width * metadata.height) / 1_000_000
    resolution_score = min(0.70, math.log1p(megapixels) / math.log1p(12) * 0.70)
    format_score = _image_format_score(metadata.format_name)
    size_score = min(0.10, math.log1p(max(size_bytes, 0)) / math.log1p(10_000_000) * 0.10)
    return min(1.0, resolution_score + format_score + size_score + 0.10)


def score_video_quality(metadata: VideoMetadata) -> float:
    if not metadata.is_readable:
        return 0.0
    pixels = metadata.width * metadata.height
    resolution_score = min(0.55, math.log1p(pixels) / math.log1p(3840 * 2160) * 0.55)
    bit_rate_score = 0.0
    if metadata.bit_rate is not None and metadata.bit_rate > 0:
        bit_rate_score = min(0.20, math.log1p(metadata.bit_rate) / math.log1p(20_000_000) * 0.20)
    codec_score = 0.10 if metadata.codec in {"h264", "hevc", "h265"} else 0.05
    audio_score = 0.05 if metadata.has_audio else 0.0
    duration_score = 0.10 if metadata.duration_ms > 0 else 0.0
    return min(1.0, resolution_score + bit_rate_score + codec_score + audio_score + duration_score)


def choose_recommended_item(
    quality_scores: dict[int, float],
    *,
    review_delta: float = 0.02,
) -> dict[int, RecommendationAction]:
    if not quality_scores:
        return {}
    sorted_items = sorted(quality_scores.items(), key=lambda item: (-item[1], item[0]))
    winner_id, winner_score = sorted_items[0]
    if len(sorted_items) > 1 and abs(winner_score - sorted_items[1][1]) <= review_delta:
        return {file_id: RecommendationAction.REVIEW_REQUIRED for file_id in quality_scores}
    return {
        file_id: RecommendationAction.KEEP_RECOMMENDED
        if file_id == winner_id
        else RecommendationAction.CLEANUP_CANDIDATE
        for file_id in quality_scores
    }


def _image_format_score(format_name: str) -> float:
    normalized = format_name.upper()
    if normalized in {"TIFF", "PNG", "HEIC", "HEIF"}:
        return 0.10
    if normalized in {"JPEG", "JPG", "WEBP"}:
        return 0.07
    return 0.03
```

- [ ] **Step 4: Run scoring tests**

```bash
uv run pytest tests/test_scoring.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/scoring.py tests/test_scoring.py
uv run ruff check src/media_dedupe/scoring.py tests/test_scoring.py
uv run ty check src/media_dedupe/scoring.py tests/test_scoring.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/scoring.py tests/test_scoring.py
git commit -m "feat: add quality scoring"
```

---

### Task 9: Text and JSON Reports

**Files:**
- Create: `src/media_dedupe/reports.py`
- Create: `tests/test_reports.py`

- [ ] **Step 1: Write failing report tests**

```python
from __future__ import annotations

import json

from media_dedupe.models import DiscoveredFile, GroupType, RecommendationAction, ReportGroup, ReportItem
from media_dedupe.reports import render_json_report, render_text_report


def make_group() -> ReportGroup:
    return ReportGroup(
        group_id=12,
        group_type=GroupType.SIMILAR_VIDEO,
        confidence=0.91,
        recommended_file_id=1,
        items=(
            ReportItem(1, "/Movies/movie_1080p.mp4", RecommendationAction.KEEP_RECOMMENDED, 1.0, 0.92, ("higher resolution",)),
            ReportItem(2, "/Downloads/movie_720p.mp4", RecommendationAction.CLEANUP_CANDIDATE, 0.91, 0.63, ("lower resolution",)),
        ),
    )


def test_render_text_report_includes_recommendation() -> None:
    text = render_text_report([make_group()])

    assert "Group #12 similar_video confidence=0.91" in text
    assert "Recommended keep" in text
    assert "/Movies/movie_1080p.mp4" in text


def test_render_json_report_is_machine_readable() -> None:
    payload = json.loads(render_json_report([make_group()]))

    assert payload["groups"][0]["type"] == "similar_video"
    assert payload["groups"][0]["recommended_keep"] == "/Movies/movie_1080p.mp4"
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_reports.py -v
```

Expected: FAIL because reports module does not exist.

- [ ] **Step 3: Implement report rendering**

```python
from __future__ import annotations

import json

from media_dedupe.models import RecommendationAction, ReportGroup, ReportItem


def render_text_report(groups: list[ReportGroup]) -> str:
    lines: list[str] = []
    for group in groups:
        lines.append(f"Group #{group.group_id} {group.group_type.value} confidence={group.confidence:.2f}")
        recommended = _recommended_item(group)
        if recommended is not None:
            lines.append("Recommended keep:")
            lines.append(f"  {recommended.path}")
            lines.append(f"  reason: {', '.join(recommended.reasons)}")
        cleanup_items = [item for item in group.items if item.action == RecommendationAction.CLEANUP_CANDIDATE]
        if cleanup_items:
            lines.append("")
            lines.append("Cleanup candidates:")
            for item in cleanup_items:
                lines.append(f"  {item.path}")
                lines.append(f"  similarity: {item.similarity:.2f}")
                lines.append(f"  reason: {', '.join(item.reasons)}")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def render_json_report(groups: list[ReportGroup]) -> str:
    payload = {
        "groups": [
            {
                "id": group.group_id,
                "type": group.group_type.value,
                "confidence": group.confidence,
                "recommended_keep": _recommended_path(group),
                "items": [
                    {
                        "file_id": item.file_id,
                        "path": item.path,
                        "action": item.action.value,
                        "similarity": item.similarity,
                        "quality_score": item.quality_score,
                        "reasons": list(item.reasons),
                    }
                    for item in group.items
                ],
            }
            for group in groups
        ]
    }
    return json.dumps(payload, ensure_ascii=False, indent=2) + "\n"


def _recommended_item(group: ReportGroup) -> ReportItem | None:
    for item in group.items:
        if item.file_id == group.recommended_file_id:
            return item
    return None


def _recommended_path(group: ReportGroup) -> str | None:
    item = _recommended_item(group)
    return item.path if item is not None else None
```

- [ ] **Step 4: Run report tests**

```bash
uv run pytest tests/test_reports.py -v
```

Expected: PASS.

- [ ] **Step 5: Run required checks**

```bash
uv run ruff format src/media_dedupe/reports.py tests/test_reports.py
uv run ruff check src/media_dedupe/reports.py tests/test_reports.py
uv run ty check src/media_dedupe/reports.py tests/test_reports.py
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit if commits are authorized**

```bash
git add src/media_dedupe/reports.py tests/test_reports.py
git commit -m "feat: add duplicate reports"
```

---

### Task 10: Pipeline Orchestration, Doctor, and End-to-End Scan

**Files:**
- Create: `src/media_dedupe/pipeline.py`
- Modify: `src/media_dedupe/cli.py`
- Create: `tests/test_pipeline.py`
- Modify: `tests/test_cli.py`

- [ ] **Step 1: Write failing pipeline smoke test**

```python
from __future__ import annotations

from pathlib import Path

from PIL import Image

from media_dedupe.pipeline import scan_paths


def test_scan_paths_finds_exact_duplicate_images(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths([tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True)

    assert len(groups) == 1
    assert groups[0].confidence == 1.0
    assert {item.path for item in groups[0].items} == {str(first), str(second)}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
uv run pytest tests/test_pipeline.py -v
```

Expected: FAIL because pipeline module does not exist.

- [ ] **Step 3: Implement minimal exact-duplicate pipeline**

```python
from __future__ import annotations

from collections import defaultdict
from pathlib import Path

from media_dedupe.cache import Cache
from media_dedupe.discovery import discover_media_files
from media_dedupe.hashing import file_sha256
from media_dedupe.models import GroupType, RecommendationAction, ReportGroup, ReportItem
from media_dedupe.scoring import choose_recommended_item


def scan_paths(
    paths: list[Path],
    *,
    cache_path: Path,
    similarity_threshold: float,
    recursive: bool,
) -> list[ReportGroup]:
    cache = Cache(cache_path)
    cache.initialize()
    discovered_files = list(discover_media_files(paths, recursive=recursive))
    file_ids: dict[Path, int] = {}
    for discovered in discovered_files:
        file_ids[discovered.path] = cache.upsert_discovered_file(discovered)
    cache.mark_missing_except(set(file_ids.values()))

    size_buckets: dict[int, list[Path]] = defaultdict(list)
    for discovered in discovered_files:
        size_buckets[discovered.size_bytes].append(discovered.path)

    groups: list[ReportGroup] = []
    next_group_id = 1
    for same_size_paths in size_buckets.values():
        if len(same_size_paths) < 2:
            continue
        hash_buckets: dict[str, list[Path]] = defaultdict(list)
        for path in same_size_paths:
            hash_buckets[file_sha256(path)].append(path)
        for duplicate_paths in hash_buckets.values():
            if len(duplicate_paths) < 2:
                continue
            quality_scores = {file_ids[path]: 0.5 for path in duplicate_paths}
            actions = choose_recommended_item(quality_scores, review_delta=-1.0)
            recommended_file_id = min(file_ids[path] for path in duplicate_paths if actions[file_ids[path]] == RecommendationAction.KEEP_RECOMMENDED)
            items = tuple(
                ReportItem(
                    file_id=file_ids[path],
                    path=str(path),
                    action=actions[file_ids[path]],
                    similarity=1.0,
                    quality_score=quality_scores[file_ids[path]],
                    reasons=("identical file hash",),
                )
                for path in sorted(duplicate_paths)
            )
            groups.append(
                ReportGroup(
                    group_id=next_group_id,
                    group_type=GroupType.EXACT,
                    confidence=1.0,
                    recommended_file_id=recommended_file_id,
                    items=items,
                )
            )
            next_group_id += 1
    return groups
```

- [ ] **Step 4: Run pipeline test**

```bash
uv run pytest tests/test_pipeline.py -v
```

Expected: PASS.

- [ ] **Step 5: Add CLI integration for scan/report/cache/doctor**

Modify `src/media_dedupe/cli.py` so `run()` calls the pipeline and report renderers:

```python
from __future__ import annotations

import argparse
import shutil
from collections.abc import Sequence
from pathlib import Path

from rich.console import Console

from media_dedupe.config import DEFAULT_CACHE_PATH, DEFAULT_SIMILARITY_THRESHOLD, DEFAULT_WORKERS
from media_dedupe.pipeline import scan_paths
from media_dedupe.reports import render_json_report, render_text_report


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="media-dedupe")
    subparsers = parser.add_subparsers(dest="command", required=True)

    scan = subparsers.add_parser("scan", help="scan directories for duplicate media")
    scan.add_argument("paths", nargs="+")
    scan.add_argument("--similarity", type=float, default=DEFAULT_SIMILARITY_THRESHOLD)
    scan.add_argument("--workers", type=int, default=DEFAULT_WORKERS)
    scan.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    scan.add_argument("--output", default="reports")
    scan.add_argument("--format", choices=("text", "json", "html"), default="text")
    scan.add_argument("--no-video", action="store_true")
    scan.add_argument("--no-image", action="store_true")
    scan.add_argument("--recursive", action=argparse.BooleanOptionalAction, default=True)

    report = subparsers.add_parser("report", help="render the last scan report")
    report.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    report.add_argument("--format", choices=("text", "json", "html"), default="text")
    report.add_argument("--output", default="reports")

    cache = subparsers.add_parser("cache", help="inspect or clear cache")
    cache_subparsers = cache.add_subparsers(dest="cache_command", required=True)
    cache_subparsers.add_parser("info", help="show cache information")
    cache_subparsers.add_parser("clear", help="clear cache database")
    cache.add_argument("--cache", default=DEFAULT_CACHE_PATH)

    subparsers.add_parser("doctor", help="check runtime dependencies")
    return parser


def run(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    console = Console()

    if args.command == "doctor":
        return _run_doctor(console)

    if args.command == "scan":
        groups = scan_paths(
            [Path(path) for path in args.paths],
            cache_path=Path(args.cache),
            similarity_threshold=args.similarity,
            recursive=args.recursive,
        )
        if args.format == "json":
            console.print(render_json_report(groups), end="")
        else:
            console.print(render_text_report(groups), end="")
        return 0

    if args.command == "report":
        console.print("report command will read stored groups after Task 11 extends persistence.")
        return 0

    if args.command == "cache":
        cache_path = Path(args.cache)
        if args.cache_command == "info":
            console.print(f"cache path: {cache_path}")
            console.print(f"exists: {cache_path.exists()}")
            return 0
        if args.cache_command == "clear":
            if cache_path.exists():
                cache_path.unlink()
            console.print(f"cleared cache: {cache_path}")
            return 0

    parser.error(f"unsupported command: {args.command}")
    return 2


def _run_doctor(console: Console) -> int:
    console.print("media-dedupe doctor")
    ffmpeg = shutil.which("ffmpeg")
    ffprobe = shutil.which("ffprobe")
    console.print(f"ffmpeg: {ffmpeg or 'missing'}")
    console.print(f"ffprobe: {ffprobe or 'missing'}")
    if ffmpeg is None or ffprobe is None:
        console.print("Install ffmpeg on macOS with: brew install ffmpeg")
    return 0


def main() -> None:
    raise SystemExit(run())
```

- [ ] **Step 6: Extend CLI tests for scan JSON**

Append to `tests/test_cli.py`:

```python
from pathlib import Path
from PIL import Image


def test_run_scan_outputs_json(capsys: pytest.CaptureFixture[str], tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(["scan", str(tmp_path), "--cache", str(tmp_path / "cache.sqlite"), "--format", "json"])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert '"type": "exact"' in captured.out
```

- [ ] **Step 7: Run pipeline and CLI tests**

```bash
uv run pytest tests/test_pipeline.py tests/test_cli.py -v
```

Expected: PASS.

- [ ] **Step 8: Run required checks**

```bash
uv run ruff format src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_pipeline.py tests/test_cli.py
uv run ruff check src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_pipeline.py tests/test_cli.py
uv run ty check src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_pipeline.py tests/test_cli.py
```

Expected: all commands exit 0.

- [ ] **Step 9: Run full test suite**

```bash
uv run pytest -v
```

Expected: all tests PASS.

- [ ] **Step 10: Commit if commits are authorized**

```bash
git add src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_pipeline.py tests/test_cli.py
git commit -m "feat: wire exact duplicate scan pipeline"
```

---

### Task 11: Integrate Image Similarity into Scan

**Files:**
- Modify: `src/media_dedupe/pipeline.py`
- Modify: `tests/test_pipeline.py`

- [ ] **Step 1: Add a failing near-duplicate image pipeline test**

Append this test to `tests/test_pipeline.py`:

```python
def test_scan_paths_finds_visually_similar_images(tmp_path: Path) -> None:
    first = tmp_path / "red_a.png"
    second = tmp_path / "red_b.png"
    Image.new("RGB", (64, 64), color=(255, 0, 0)).save(first)
    Image.new("RGB", (64, 64), color=(250, 0, 0)).save(second)
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths([tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True)

    similar_groups = [group for group in groups if group.group_type.value == "similar_image"]
    assert len(similar_groups) == 1
    assert similar_groups[0].confidence >= 0.80
    assert {item.path for item in similar_groups[0].items} == {str(first), str(second)}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
uv run pytest tests/test_pipeline.py::test_scan_paths_finds_visually_similar_images -v
```

Expected: FAIL because `scan_paths` only creates exact duplicate groups.

- [ ] **Step 3: Replace `src/media_dedupe/pipeline.py` with exact + image similarity orchestration**

```python
from __future__ import annotations

from collections import defaultdict
from pathlib import Path

from media_dedupe.cache import Cache
from media_dedupe.discovery import discover_media_files
from media_dedupe.hashing import file_sha256, hash_similarity
from media_dedupe.image_fingerprint import compute_image_phash, read_image_metadata
from media_dedupe.models import DiscoveredFile, GroupType, MediaType, RecommendationAction, ReportGroup, ReportItem
from media_dedupe.scoring import choose_recommended_item, score_image_quality


def scan_paths(
    paths: list[Path],
    *,
    cache_path: Path,
    similarity_threshold: float,
    recursive: bool,
) -> list[ReportGroup]:
    cache = Cache(cache_path)
    cache.initialize()
    discovered_files = list(discover_media_files(paths, recursive=recursive))
    file_ids: dict[Path, int] = {}
    for discovered in discovered_files:
        file_ids[discovered.path] = cache.upsert_discovered_file(discovered)
    cache.mark_missing_except(set(file_ids.values()))

    groups: list[ReportGroup] = []
    used_exact_paths: set[Path] = set()
    next_group_id = 1
    exact_groups, used_exact_paths = _exact_duplicate_groups(discovered_files, file_ids, next_group_id)
    groups.extend(exact_groups)
    next_group_id += len(exact_groups)

    image_groups = _similar_image_groups(
        [item for item in discovered_files if item.media_type == MediaType.IMAGE and item.path not in used_exact_paths],
        file_ids,
        next_group_id,
        similarity_threshold,
    )
    groups.extend(image_groups)
    return groups


def _exact_duplicate_groups(discovered_files: list[DiscoveredFile], file_ids: dict[Path, int], start_group_id: int) -> tuple[list[ReportGroup], set[Path]]:
    size_buckets: dict[int, list[Path]] = defaultdict(list)
    for discovered in discovered_files:
        size_buckets[discovered.size_bytes].append(discovered.path)

    groups: list[ReportGroup] = []
    used_paths: set[Path] = set()
    next_group_id = start_group_id
    for same_size_paths in size_buckets.values():
        if len(same_size_paths) < 2:
            continue
        hash_buckets: dict[str, list[Path]] = defaultdict(list)
        for path in same_size_paths:
            hash_buckets[file_sha256(path)].append(path)
        for duplicate_paths in hash_buckets.values():
            if len(duplicate_paths) < 2:
                continue
            used_paths.update(duplicate_paths)
            quality_scores = {file_ids[path]: 0.5 for path in duplicate_paths}
            actions = choose_recommended_item(quality_scores, review_delta=-1.0)
            recommended_file_id = min(file_ids[path] for path in duplicate_paths if actions[file_ids[path]] == RecommendationAction.KEEP_RECOMMENDED)
            items = tuple(
                ReportItem(file_ids[path], str(path), actions[file_ids[path]], 1.0, quality_scores[file_ids[path]], ("identical file hash",))
                for path in sorted(duplicate_paths)
            )
            groups.append(ReportGroup(next_group_id, GroupType.EXACT, 1.0, recommended_file_id, items))
            next_group_id += 1
    return groups, used_paths


def _similar_image_groups(discovered_images: list[DiscoveredFile], file_ids: dict[Path, int], start_group_id: int, threshold: float) -> list[ReportGroup]:
    image_data: list[tuple[Path, str, float]] = []
    for discovered in discovered_images:
        metadata = read_image_metadata(discovered.path)
        if not metadata.is_readable:
            continue
        image_data.append((discovered.path, compute_image_phash(discovered.path), score_image_quality(metadata, size_bytes=discovered.size_bytes)))

    groups: list[ReportGroup] = []
    grouped_paths: set[Path] = set()
    next_group_id = start_group_id
    for index, (left_path, left_hash, left_score) in enumerate(image_data):
        if left_path in grouped_paths:
            continue
        members: list[tuple[Path, float, float]] = [(left_path, 1.0, left_score)]
        for right_path, right_hash, right_score in image_data[index + 1 :]:
            if right_path in grouped_paths:
                continue
            similarity = hash_similarity(left_hash, right_hash)
            if similarity >= threshold:
                members.append((right_path, similarity, right_score))
        if len(members) < 2:
            continue
        quality_scores = {file_ids[path]: quality_score for path, _, quality_score in members}
        actions = choose_recommended_item(quality_scores)
        recommended_file_id = max(quality_scores, key=quality_scores.get)
        items = tuple(
            ReportItem(file_ids[path], str(path), actions[file_ids[path]], similarity, quality_score, ("visual image hash match",))
            for path, similarity, quality_score in sorted(members, key=lambda member: str(member[0]))
        )
        confidence = min(similarity for _, similarity, _ in members)
        groups.append(ReportGroup(next_group_id, GroupType.SIMILAR_IMAGE, confidence, recommended_file_id, items))
        grouped_paths.update(path for path, _, _ in members)
        next_group_id += 1
    return groups
```

- [ ] **Step 4: Run image pipeline test**

```bash
uv run pytest tests/test_pipeline.py::test_scan_paths_finds_visually_similar_images -v
```

Expected: PASS.

- [ ] **Step 5: Run full pipeline tests**

```bash
uv run pytest tests/test_pipeline.py -v
```

Expected: PASS.

- [ ] **Step 6: Run required checks**

```bash
uv run ruff format src/media_dedupe/pipeline.py tests/test_pipeline.py
uv run ruff check src/media_dedupe/pipeline.py tests/test_pipeline.py
uv run ty check src/media_dedupe/pipeline.py tests/test_pipeline.py
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit if commits are authorized**

```bash
git add src/media_dedupe/pipeline.py tests/test_pipeline.py
git commit -m "feat: detect similar images in scan"
```

---

### Task 12: Integrate Video Similarity into Scan

**Files:**
- Modify: `src/media_dedupe/pipeline.py`
- Modify: `tests/test_pipeline.py`

- [ ] **Step 1: Add a failing video similarity pipeline test with monkeypatched video helpers**

Append this test to `tests/test_pipeline.py`:

```python
import pytest

from media_dedupe.models import VideoMetadata


def test_scan_paths_finds_similar_videos(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    first = tmp_path / "clip_a.mp4"
    second = tmp_path / "clip_b.mp4"
    first.write_bytes(b"video-a")
    second.write_bytes(b"video-b")
    cache_path = tmp_path / "cache.sqlite"

    def fake_metadata(path: Path) -> VideoMetadata:
        return VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    def fake_video_hashes(path: Path) -> list[str]:
        return ["ffff", "ff0f", "f0ff"]

    monkeypatch.setattr("media_dedupe.pipeline.ffprobe_metadata", fake_metadata)
    monkeypatch.setattr("media_dedupe.pipeline.compute_video_frame_hashes", fake_video_hashes)

    groups = scan_paths([tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True)

    video_groups = [group for group in groups if group.group_type.value == "similar_video"]
    assert len(video_groups) == 1
    assert video_groups[0].confidence >= 0.80
    assert {item.path for item in video_groups[0].items} == {str(first), str(second)}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
uv run pytest tests/test_pipeline.py::test_scan_paths_finds_similar_videos -v
```

Expected: FAIL because `scan_paths` does not compute video similarity.

- [ ] **Step 3: Add video frame hash helper to `src/media_dedupe/video_fingerprint.py`**

Append this helper:

```python
from tempfile import TemporaryDirectory

from media_dedupe.image_fingerprint import compute_image_phash


def compute_video_frame_hashes(path: Path, *, frame_count: int = 8) -> list[str]:
    metadata = ffprobe_metadata(path)
    timestamps = build_frame_timestamps(duration_ms=metadata.duration_ms, frame_count=frame_count)
    hashes: list[str] = []
    with TemporaryDirectory() as temporary_directory:
        temporary_path = Path(temporary_directory)
        for index, timestamp_ms in enumerate(timestamps):
            frame_path = temporary_path / f"frame-{index}.jpg"
            extracted = extract_frame_to_image(path, timestamp_ms=timestamp_ms, output_path=frame_path)
            if extracted is None:
                continue
            hashes.append(compute_image_phash(extracted))
    return hashes
```

- [ ] **Step 4: Update `src/media_dedupe/pipeline.py` imports and scan flow**

Add imports:

```python
from statistics import median

from media_dedupe.matching import is_video_metadata_candidate
from media_dedupe.scoring import score_video_quality
from media_dedupe.video_fingerprint import compute_video_frame_hashes, ffprobe_metadata
```

Before `return groups` in `scan_paths`, add:

```python
    video_groups = _similar_video_groups(
        [item for item in discovered_files if item.media_type == MediaType.VIDEO and item.path not in used_exact_paths],
        file_ids,
        next_group_id + len(image_groups),
        similarity_threshold,
    )
    groups.extend(video_groups)
```

Append these helpers:

```python
def _similar_video_groups(discovered_videos: list[DiscoveredFile], file_ids: dict[Path, int], start_group_id: int, threshold: float) -> list[ReportGroup]:
    video_data = []
    for discovered in discovered_videos:
        metadata = ffprobe_metadata(discovered.path)
        if not metadata.is_readable:
            continue
        frame_hashes = compute_video_frame_hashes(discovered.path)
        if not frame_hashes:
            continue
        video_data.append((discovered.path, metadata, frame_hashes, score_video_quality(metadata)))

    groups: list[ReportGroup] = []
    grouped_paths: set[Path] = set()
    next_group_id = start_group_id
    for index, (left_path, left_metadata, left_hashes, left_score) in enumerate(video_data):
        if left_path in grouped_paths:
            continue
        members: list[tuple[Path, float, float]] = [(left_path, 1.0, left_score)]
        for right_path, right_metadata, right_hashes, right_score in video_data[index + 1 :]:
            if right_path in grouped_paths:
                continue
            if not is_video_metadata_candidate(left_metadata, right_metadata):
                continue
            similarity = _video_hash_similarity(left_hashes, right_hashes)
            if similarity >= threshold:
                members.append((right_path, similarity, right_score))
        if len(members) < 2:
            continue
        quality_scores = {file_ids[path]: quality_score for path, _, quality_score in members}
        actions = choose_recommended_item(quality_scores)
        recommended_file_id = max(quality_scores, key=quality_scores.get)
        items = tuple(
            ReportItem(file_ids[path], str(path), actions[file_ids[path]], similarity, quality_score, ("video frame hash match",))
            for path, similarity, quality_score in sorted(members, key=lambda member: str(member[0]))
        )
        confidence = min(similarity for _, similarity, _ in members)
        groups.append(ReportGroup(next_group_id, GroupType.SIMILAR_VIDEO, confidence, recommended_file_id, items))
        grouped_paths.update(path for path, _, _ in members)
        next_group_id += 1
    return groups


def _video_hash_similarity(left_hashes: list[str], right_hashes: list[str]) -> float:
    pair_count = min(len(left_hashes), len(right_hashes))
    if pair_count == 0:
        return 0.0
    similarities = [hash_similarity(left_hashes[index], right_hashes[index]) for index in range(pair_count)]
    return float(median(similarities))
```

- [ ] **Step 5: Run video pipeline test**

```bash
uv run pytest tests/test_pipeline.py::test_scan_paths_finds_similar_videos -v
```

Expected: PASS.

- [ ] **Step 6: Run full pipeline tests**

```bash
uv run pytest tests/test_pipeline.py -v
```

Expected: PASS.

- [ ] **Step 7: Run required checks**

```bash
uv run ruff format src/media_dedupe/pipeline.py src/media_dedupe/video_fingerprint.py tests/test_pipeline.py
uv run ruff check src/media_dedupe/pipeline.py src/media_dedupe/video_fingerprint.py tests/test_pipeline.py
uv run ty check src/media_dedupe/pipeline.py src/media_dedupe/video_fingerprint.py tests/test_pipeline.py
```

Expected: all commands exit 0.

- [ ] **Step 8: Commit if commits are authorized**

```bash
git add src/media_dedupe/pipeline.py src/media_dedupe/video_fingerprint.py tests/test_pipeline.py
git commit -m "feat: detect similar videos in scan"
```

---

### Task 13: Persist and Re-render Duplicate Groups

**Files:**
- Modify: `src/media_dedupe/cache.py`
- Modify: `src/media_dedupe/pipeline.py`
- Modify: `src/media_dedupe/cli.py`
- Create: `tests/test_report_persistence.py`

- [ ] **Step 1: Write failing persistence test**

```python
from __future__ import annotations

from pathlib import Path
from PIL import Image

from media_dedupe.cache import Cache
from media_dedupe.pipeline import load_last_report_groups, scan_paths


def test_scan_persists_groups_for_report_command(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    scan_paths([tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True)
    groups = load_last_report_groups(Cache(cache_path))

    assert len(groups) == 1
    assert groups[0].confidence == 1.0
```

- [ ] **Step 2: Run test to verify it fails**

```bash
uv run pytest tests/test_report_persistence.py -v
```

Expected: FAIL because `load_last_report_groups` does not exist and groups are not persisted.

- [ ] **Step 3: Add report persistence methods to `Cache`**

Add these imports to `src/media_dedupe/cache.py`:

```python
import json

from media_dedupe.models import GroupType, RecommendationAction, ReportGroup, ReportItem
```

Add these methods to `Cache`:

```python
    def replace_report_groups(self, groups: list[ReportGroup]) -> None:
        with self.connect() as connection:
            connection.execute("DELETE FROM duplicate_items")
            connection.execute("DELETE FROM duplicate_groups")
            for group in groups:
                cursor = connection.execute(
                    """
                    INSERT INTO duplicate_groups (id, group_type, confidence, recommended_file_id)
                    VALUES (?, ?, ?, ?)
                    """,
                    (group.group_id, group.group_type.value, group.confidence, group.recommended_file_id),
                )
                group_id = int(cursor.lastrowid or group.group_id)
                for item in group.items:
                    connection.execute(
                        """
                        INSERT INTO duplicate_items (group_id, file_id, action, similarity, quality_score, reasons_json)
                        VALUES (?, ?, ?, ?, ?, ?)
                        """,
                        (
                            group_id,
                            item.file_id,
                            item.action.value,
                            item.similarity,
                            item.quality_score,
                            json.dumps(list(item.reasons), ensure_ascii=False),
                        ),
                    )

    def load_report_groups(self) -> list[ReportGroup]:
        with self.connect() as connection:
            group_rows = connection.execute(
                "SELECT id, group_type, confidence, recommended_file_id FROM duplicate_groups ORDER BY id"
            ).fetchall()
            groups: list[ReportGroup] = []
            for group_row in group_rows:
                item_rows = connection.execute(
                    """
                    SELECT duplicate_items.file_id, files.path, duplicate_items.action,
                           duplicate_items.similarity, duplicate_items.quality_score, duplicate_items.reasons_json
                    FROM duplicate_items
                    JOIN files ON files.id = duplicate_items.file_id
                    WHERE duplicate_items.group_id = ?
                    ORDER BY duplicate_items.file_id
                    """,
                    (group_row["id"],),
                ).fetchall()
                items = tuple(
                    ReportItem(
                        file_id=int(item_row["file_id"]),
                        path=str(item_row["path"]),
                        action=RecommendationAction(str(item_row["action"])),
                        similarity=float(item_row["similarity"]),
                        quality_score=float(item_row["quality_score"]),
                        reasons=tuple(json.loads(str(item_row["reasons_json"]))),
                    )
                    for item_row in item_rows
                )
                groups.append(
                    ReportGroup(
                        group_id=int(group_row["id"]),
                        group_type=GroupType(str(group_row["group_type"])),
                        confidence=float(group_row["confidence"]),
                        recommended_file_id=int(group_row["recommended_file_id"]),
                        items=items,
                    )
                )
            return groups
```

- [ ] **Step 4: Update pipeline to persist and load reports**

Append to `src/media_dedupe/pipeline.py`:

```python
def load_last_report_groups(cache: Cache) -> list[ReportGroup]:
    cache.initialize()
    return cache.load_report_groups()
```

Before `return groups` in `scan_paths`, insert:

```python
    cache.replace_report_groups(groups)
```

- [ ] **Step 5: Wire `report` CLI command to persisted groups**

In `src/media_dedupe/cli.py`, import `Cache` and `load_last_report_groups`:

```python
from media_dedupe.cache import Cache
from media_dedupe.pipeline import load_last_report_groups, scan_paths
```

Replace the `report` branch with:

```python
    if args.command == "report":
        groups = load_last_report_groups(Cache(Path(args.cache)))
        if args.format == "json":
            console.print(render_json_report(groups), end="")
        else:
            console.print(render_text_report(groups), end="")
        return 0
```

- [ ] **Step 6: Run persistence tests**

```bash
uv run pytest tests/test_report_persistence.py -v
```

Expected: PASS.

- [ ] **Step 7: Run full test suite**

```bash
uv run pytest -v
```

Expected: all tests PASS.

- [ ] **Step 8: Run required checks**

```bash
uv run ruff format src/media_dedupe/cache.py src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_report_persistence.py
uv run ruff check src/media_dedupe/cache.py src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_report_persistence.py
uv run ty check src/media_dedupe/cache.py src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_report_persistence.py
```

Expected: all commands exit 0.

- [ ] **Step 9: Commit if commits are authorized**

```bash
git add src/media_dedupe/cache.py src/media_dedupe/pipeline.py src/media_dedupe/cli.py tests/test_report_persistence.py
git commit -m "feat: persist duplicate reports"
```

---

### Task 14: README Usage and Final Verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Write README content**

Replace `README.md` with:

```markdown
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

Render the last persisted report:

```bash
uv run media-dedupe report --format json
```

Inspect or clear cache:

```bash
uv run media-dedupe cache info
uv run media-dedupe cache clear
```

## Safety

The tool does not delete or move files. Report items are marked as `keep_recommended`, `cleanup_candidate`, or `review_required` so users can review decisions before taking action.
```

- [ ] **Step 2: Run all tests**

```bash
uv run pytest -v
```

Expected: all tests PASS.

- [ ] **Step 3: Run required Python checks for all source and tests**

```bash
uv run ruff format src tests main.py
uv run ruff check src tests main.py
uv run ty check src tests main.py
```

Expected: all commands exit 0.

- [ ] **Step 4: Run CLI smoke commands**

```bash
uv run media-dedupe doctor
uv run media-dedupe cache info
```

Expected: both commands exit 0. `doctor` may report missing ffmpeg/ffprobe with installation instructions; that is acceptable.

- [ ] **Step 5: Commit if commits are authorized**

```bash
git add README.md
git commit -m "docs: add usage guide"
```

---

## Self-Review Notes

- Spec coverage: tasks cover CLI, file discovery, SQLite cache, exact hashing, image pHash primitives and scan integration, video ffprobe/frame extraction primitives and scan integration, grouping, quality scoring, text/JSON reports, doctor, persistence, and tests.
- Safety boundary: no task implements automatic deletion, file moving, GUI, cloud scanning, semantic similarity, or embedding search.
- Python 3.14 risk: dependency selection is conservative; `argparse`, `sqlite3`, `hashlib`, `json`, and `subprocess` are standard library. Runtime third-party dependencies are limited to `pillow`, `imagehash`, and `rich`, and Task 1 requires `uv add` to validate installability before implementation proceeds.
- Verification: each Python task includes targeted pytest plus `uv run ruff format`, `uv run ruff check`, and `uv run ty check` for modified files, matching project instructions.
