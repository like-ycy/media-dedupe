from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from src.models import (
    DiscoveredFile,
    FileStatus,
    GroupType,
    ImageMetadata,
    RecommendationAction,
    ReportError,
    ReportGroup,
    ReportItem,
    VideoMetadata,
)


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
            row = connection.execute(
                "SELECT id FROM files WHERE path = ?", (str(discovered.path),)
            ).fetchone()
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
                connection.execute(
                    "UPDATE files SET status = ?", (FileStatus.MISSING.value,)
                )
                return
            placeholders = ",".join("?" for _ in active_file_ids)
            connection.execute(
                f"UPDATE files SET status = ? WHERE id NOT IN ({placeholders})",
                (FileStatus.MISSING.value, *sorted(active_file_ids)),
            )

    def get_file_status(self, file_id: int) -> str:
        with self.connect() as connection:
            row = connection.execute(
                "SELECT status FROM files WHERE id = ?", (file_id,)
            ).fetchone()
        if row is None:
            raise KeyError(file_id)
        return str(row["status"])

    def save_file_hash(self, file_id: int, size_bytes: int, full_hash: str) -> None:
        with self.connect() as connection:
            connection.execute(
                """
                INSERT INTO file_hashes (file_id, size_bytes, full_hash, hash_algorithm)
                VALUES (?, ?, ?, ?)
                ON CONFLICT(file_id) DO UPDATE SET
                    size_bytes = excluded.size_bytes,
                    full_hash = excluded.full_hash,
                    hash_algorithm = excluded.hash_algorithm
                """,
                (file_id, size_bytes, full_hash, "sha256"),
            )

    def load_file_hash(self, file_id: int) -> str | None:
        with self.connect() as connection:
            row = connection.execute(
                "SELECT full_hash FROM file_hashes WHERE file_id = ?", (file_id,)
            ).fetchone()
        if row is None or row["full_hash"] is None:
            return None
        return str(row["full_hash"])

    def save_image_metadata(self, file_id: int, metadata: ImageMetadata) -> None:
        with self.connect() as connection:
            connection.execute(
                """
                INSERT INTO media_metadata (
                    file_id, width, height, format_name, orientation, is_readable, metadata_type
                )
                VALUES (?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(file_id) DO UPDATE SET
                    width = excluded.width,
                    height = excluded.height,
                    duration_ms = NULL,
                    frame_rate = NULL,
                    bit_rate = NULL,
                    codec = NULL,
                    format_name = excluded.format_name,
                    orientation = excluded.orientation,
                    has_audio = NULL,
                    is_readable = excluded.is_readable,
                    metadata_type = excluded.metadata_type
                """,
                (
                    file_id,
                    metadata.width,
                    metadata.height,
                    metadata.format_name,
                    metadata.orientation,
                    int(metadata.is_readable),
                    "image",
                ),
            )

    def load_image_metadata(self, file_id: int) -> ImageMetadata | None:
        with self.connect() as connection:
            row = connection.execute(
                """
                SELECT width, height, format_name, orientation, is_readable
                FROM media_metadata
                WHERE file_id = ? AND metadata_type = 'image'
                """,
                (file_id,),
            ).fetchone()
        if row is None:
            return None
        return ImageMetadata(
            width=int(row["width"] or 0),
            height=int(row["height"] or 0),
            format_name=str(row["format_name"] or ""),
            orientation=int(row["orientation"])
            if row["orientation"] is not None
            else None,
            is_readable=bool(row["is_readable"]),
        )

    def save_video_metadata(self, file_id: int, metadata: VideoMetadata) -> None:
        with self.connect() as connection:
            connection.execute(
                """
                INSERT INTO media_metadata (
                    file_id, width, height, duration_ms, frame_rate, bit_rate, codec,
                    has_audio, is_readable, metadata_type
                )
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(file_id) DO UPDATE SET
                    width = excluded.width,
                    height = excluded.height,
                    duration_ms = excluded.duration_ms,
                    frame_rate = excluded.frame_rate,
                    bit_rate = excluded.bit_rate,
                    codec = excluded.codec,
                    format_name = NULL,
                    orientation = NULL,
                    has_audio = excluded.has_audio,
                    is_readable = excluded.is_readable,
                    metadata_type = excluded.metadata_type
                """,
                (
                    file_id,
                    metadata.width,
                    metadata.height,
                    metadata.duration_ms,
                    metadata.frame_rate,
                    metadata.bit_rate,
                    metadata.codec,
                    int(metadata.has_audio),
                    int(metadata.is_readable),
                    "video",
                ),
            )

    def load_video_metadata(self, file_id: int) -> VideoMetadata | None:
        with self.connect() as connection:
            row = connection.execute(
                """
                SELECT duration_ms, width, height, frame_rate, bit_rate, codec, has_audio, is_readable
                FROM media_metadata
                WHERE file_id = ? AND metadata_type = 'video'
                """,
                (file_id,),
            ).fetchone()
        if row is None:
            return None
        return VideoMetadata(
            duration_ms=int(row["duration_ms"] or 0),
            width=int(row["width"] or 0),
            height=int(row["height"] or 0),
            frame_rate=float(row["frame_rate"])
            if row["frame_rate"] is not None
            else None,
            bit_rate=int(row["bit_rate"]) if row["bit_rate"] is not None else None,
            codec=str(row["codec"]) if row["codec"] is not None else None,
            has_audio=bool(row["has_audio"]),
            is_readable=bool(row["is_readable"]),
        )

    def save_perceptual_hashes(
        self, file_id: int, hash_type: str, hash_values: list[str]
    ) -> None:
        with self.connect() as connection:
            connection.execute(
                "DELETE FROM perceptual_hashes WHERE file_id = ? AND hash_type = ?",
                (file_id, hash_type),
            )
            for index, hash_value in enumerate(hash_values):
                connection.execute(
                    """
                    INSERT INTO perceptual_hashes (
                        file_id, frame_index, timestamp_ms, hash_type, hash_value, hash_size
                    )
                    VALUES (?, ?, ?, ?, ?, ?)
                    """,
                    (file_id, index, None, hash_type, hash_value, len(hash_value)),
                )

    def load_perceptual_hashes(self, file_id: int, hash_type: str) -> list[str]:
        with self.connect() as connection:
            rows = connection.execute(
                """
                SELECT hash_value FROM perceptual_hashes
                WHERE file_id = ? AND hash_type = ?
                ORDER BY frame_index, id
                """,
                (file_id, hash_type),
            ).fetchall()
        return [str(row["hash_value"]) for row in rows]

    def start_scan_run(self, input_paths: list[Path], config: dict[str, object]) -> int:
        with self.connect() as connection:
            cursor = connection.execute(
                """
                INSERT INTO scan_runs (input_paths_json, config_json)
                VALUES (?, ?)
                """,
                (
                    json.dumps([str(path) for path in input_paths], ensure_ascii=False),
                    json.dumps(config, ensure_ascii=False, sort_keys=True),
                ),
            )
            if cursor.lastrowid is None:
                raise RuntimeError("failed to create scan run")
            return cursor.lastrowid

    def finish_scan_run(
        self, scan_run_id: int, *, files_seen: int, files_failed: int
    ) -> None:
        with self.connect() as connection:
            connection.execute(
                """
                UPDATE scan_runs
                SET finished_at = CURRENT_TIMESTAMP, files_seen = ?, files_failed = ?
                WHERE id = ?
                """,
                (files_seen, files_failed, scan_run_id),
            )

    def record_error(
        self, scan_run_id: int, path: Path, stage: str, message: str
    ) -> None:
        with self.connect() as connection:
            connection.execute(
                """
                INSERT INTO errors (scan_run_id, path, stage, message)
                VALUES (?, ?, ?, ?)
                """,
                (scan_run_id, str(path), stage, message),
            )

    def load_errors(self, scan_run_id: int | None = None) -> list[ReportError]:
        self.initialize()
        query = "SELECT path, stage, message FROM errors"
        parameters: tuple[int, ...] = ()
        if scan_run_id is not None:
            query += " WHERE scan_run_id = ?"
            parameters = (scan_run_id,)
        query += " ORDER BY id"
        with self.connect() as connection:
            rows = connection.execute(query, parameters).fetchall()
        return [
            ReportError(
                path=str(row["path"]),
                stage=str(row["stage"]),
                message=str(row["message"]),
            )
            for row in rows
        ]

    def load_latest_errors(self) -> list[ReportError]:
        self.initialize()
        with self.connect() as connection:
            row = connection.execute(
                "SELECT id FROM scan_runs ORDER BY id DESC LIMIT 1"
            ).fetchone()
        if row is None:
            return []
        return self.load_errors(int(row["id"]))

    def count_rows(self, table_name: str) -> int:
        allowed_tables = {
            "files",
            "file_hashes",
            "media_metadata",
            "perceptual_hashes",
            "duplicate_groups",
            "duplicate_items",
            "scan_runs",
            "errors",
        }
        if table_name not in allowed_tables:
            raise ValueError(f"unsupported table: {table_name}")
        with self.connect() as connection:
            row = connection.execute(
                f"SELECT COUNT(*) AS count FROM {table_name}"
            ).fetchone()
        return int(row["count"])

    def replace_report_groups(
        self, groups: list[ReportGroup], scan_run_id: int | None = None
    ) -> None:
        with self.connect() as connection:
            connection.execute("DELETE FROM duplicate_items")
            connection.execute("DELETE FROM duplicate_groups")
            for group in groups:
                cursor = connection.execute(
                    """
                    INSERT INTO duplicate_groups (id, scan_run_id, group_type, confidence, recommended_file_id)
                    VALUES (?, ?, ?, ?, ?)
                    """,
                    (
                        group.group_id,
                        scan_run_id,
                        group.group_type.value,
                        group.confidence,
                        group.recommended_file_id,
                    ),
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
