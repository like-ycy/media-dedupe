from __future__ import annotations

from pathlib import Path

from media_dedupe.cache import Cache
from media_dedupe.models import DiscoveredFile, ImageMetadata, MediaType, VideoMetadata


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


def test_cache_persists_and_reuses_file_hash(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()
    discovered = make_file(tmp_path / "a.jpg")
    file_id = cache.upsert_discovered_file(discovered)

    cache.save_file_hash(file_id, discovered.size_bytes, "abc123")

    assert cache.load_file_hash(file_id) == "abc123"


def test_cache_persists_image_metadata(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()
    discovered = make_file(tmp_path / "a.jpg")
    file_id = cache.upsert_discovered_file(discovered)
    metadata = ImageMetadata(100, 80, "JPEG", 1, True)

    cache.save_image_metadata(file_id, metadata)

    assert cache.load_image_metadata(file_id) == metadata


def test_cache_persists_video_metadata_and_frame_hashes(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()
    discovered = make_file(tmp_path / "a.mp4")
    file_id = cache.upsert_discovered_file(discovered)
    metadata = VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    cache.save_video_metadata(file_id, metadata)
    cache.save_perceptual_hashes(file_id, "video_frame_phash", ["aaaa", "bbbb"])

    assert cache.load_video_metadata(file_id) == metadata
    assert cache.load_perceptual_hashes(file_id, "video_frame_phash") == [
        "aaaa",
        "bbbb",
    ]


def test_cache_records_scan_runs_and_errors(tmp_path: Path) -> None:
    cache = Cache(tmp_path / "cache.sqlite")
    cache.initialize()

    scan_run_id = cache.start_scan_run(
        [tmp_path], {"similarity_threshold": 0.8, "workers": 2}
    )
    cache.record_error(scan_run_id, tmp_path / "bad.png", "image_phash", "broken image")
    cache.finish_scan_run(scan_run_id, files_seen=3, files_failed=1)

    assert cache.count_rows("scan_runs") == 1
    assert cache.count_rows("errors") == 1
