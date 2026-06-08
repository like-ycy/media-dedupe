from __future__ import annotations

from pathlib import Path

import pytest
from PIL import Image

from media_dedupe.cache import Cache
from media_dedupe.models import RecommendationAction, ImageMetadata, VideoMetadata
from media_dedupe.pipeline import scan_paths


def test_scan_paths_finds_exact_duplicate_images(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    assert len(groups) == 1
    assert groups[0].confidence == 1.0
    assert {item.path for item in groups[0].items} == {str(first), str(second)}
    assert {item.quality_score for item in groups[0].items} != {0.5}
    assert any(
        "resolution" in reason for item in groups[0].items for reason in item.reasons
    )


def test_scan_paths_finds_visually_similar_images(tmp_path: Path) -> None:
    first = tmp_path / "red_a.png"
    second = tmp_path / "red_b.png"
    Image.new("RGB", (64, 64), color=(255, 0, 0)).save(first)
    Image.new("RGB", (64, 64), color=(250, 0, 0)).save(second)
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    similar_groups = [
        group for group in groups if group.group_type.value == "similar_image"
    ]
    assert len(similar_groups) == 1
    assert similar_groups[0].confidence >= 0.80
    assert {item.path for item in similar_groups[0].items} == {str(first), str(second)}


def test_scan_paths_finds_similar_videos(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    first = tmp_path / "clip_a.mp4"
    second = tmp_path / "clip_b.mp4"
    first.write_bytes(b"video-a")
    second.write_bytes(b"video-b")
    cache_path = tmp_path / "cache.sqlite"

    def fake_metadata(path: Path) -> VideoMetadata:
        return VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    def fake_video_hashes(
        path: Path, *, metadata: VideoMetadata | None = None
    ) -> list[str]:
        return ["ffff", "ff0f", "f0ff"]

    monkeypatch.setattr("media_dedupe.pipeline.ffprobe_metadata", fake_metadata)
    monkeypatch.setattr(
        "media_dedupe.pipeline.compute_video_frame_hashes", fake_video_hashes
    )

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    video_groups = [
        group for group in groups if group.group_type.value == "similar_video"
    ]
    assert len(video_groups) == 1
    assert video_groups[0].confidence >= 0.80
    assert {item.path for item in video_groups[0].items} == {str(first), str(second)}


def test_scan_paths_persists_fact_cache_and_scan_run(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )
    cache = Cache(cache_path)

    assert cache.count_rows("file_hashes") == 2
    assert cache.count_rows("media_metadata") == 2
    assert cache.count_rows("perceptual_hashes") == 2
    assert cache.count_rows("scan_runs") == 1


def test_scan_paths_reuses_cached_facts_for_unchanged_files(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    def fail_file_hash(path: Path) -> str:
        raise AssertionError(f"hash recomputed for {path}")

    def fail_metadata(path: Path) -> ImageMetadata:
        raise AssertionError(f"metadata recomputed for {path}")

    def fail_phash(path: Path) -> str:
        raise AssertionError(f"phash recomputed for {path}")

    monkeypatch.setattr("media_dedupe.pipeline.file_sha256", fail_file_hash)
    monkeypatch.setattr("media_dedupe.pipeline.read_image_metadata", fail_metadata)
    monkeypatch.setattr("media_dedupe.pipeline.compute_image_phash", fail_phash)

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    assert len(groups) == 1


def test_scan_paths_records_unreadable_image_errors(tmp_path: Path) -> None:
    image_path = tmp_path / "bad.png"
    image_path.write_bytes(b"not an image")
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )
    cache = Cache(cache_path)

    assert groups == []
    assert cache.count_rows("errors") == 1
    assert cache.count_rows("scan_runs") == 1


def test_scan_paths_records_missing_input_path_errors(tmp_path: Path) -> None:
    missing_path = tmp_path / "missing"
    cache_path = tmp_path / "cache.sqlite"

    groups = scan_paths(
        [missing_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )
    errors = Cache(cache_path).load_latest_errors()

    assert groups == []
    assert len(errors) == 1
    assert errors[0].path == str(missing_path)
    assert errors[0].stage == "discovery"
    assert "does not exist" in errors[0].message


def test_scan_paths_uses_workers_for_image_fingerprints(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    image_path = tmp_path / "a.png"
    Image.new("RGB", (16, 16), color="red").save(image_path)
    max_workers_seen: list[int | None] = []

    class FakeExecutor:
        def __init__(self, max_workers: int | None = None) -> None:
            max_workers_seen.append(max_workers)

        def __enter__(self) -> FakeExecutor:
            return self

        def __exit__(self, *args: object) -> None:
            return None

        def map(self, function, values):
            return [function(value) for value in values]

    monkeypatch.setattr("media_dedupe.pipeline.ThreadPoolExecutor", FakeExecutor)

    scan_paths(
        [tmp_path],
        cache_path=tmp_path / "cache.sqlite",
        similarity_threshold=0.80,
        recursive=True,
        workers=3,
    )

    assert 3 in max_workers_seen


def test_scan_paths_reuses_cached_video_facts(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    first = tmp_path / "clip_a.mp4"
    second = tmp_path / "clip_b.mp4"
    first.write_bytes(b"video-a")
    second.write_bytes(b"video-b")
    cache_path = tmp_path / "cache.sqlite"

    def fake_metadata(path: Path) -> VideoMetadata:
        return VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True)

    def fake_video_hashes(
        path: Path, *, metadata: VideoMetadata | None = None
    ) -> list[str]:
        return ["ffff", "ff0f", "f0ff"]

    monkeypatch.setattr("media_dedupe.pipeline.ffprobe_metadata", fake_metadata)
    monkeypatch.setattr(
        "media_dedupe.pipeline.compute_video_frame_hashes", fake_video_hashes
    )
    scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    def fail_metadata(path: Path) -> VideoMetadata:
        raise AssertionError(f"metadata recomputed for {path}")

    def fail_hashes(path: Path, *, metadata: VideoMetadata | None = None) -> list[str]:
        raise AssertionError(f"frame hashes recomputed for {path}")

    monkeypatch.setattr("media_dedupe.pipeline.ffprobe_metadata", fail_metadata)
    monkeypatch.setattr("media_dedupe.pipeline.compute_video_frame_hashes", fail_hashes)

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )

    assert [group.group_type.value for group in groups] == ["similar_video"]


def test_scan_paths_recommends_highest_quality_similar_image(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    first = tmp_path / "small.png"
    second = tmp_path / "large.png"
    first.write_bytes(b"small")
    second.write_bytes(b"large")
    metadata_by_path = {
        first: ImageMetadata(640, 480, "JPEG", None, True),
        second: ImageMetadata(4000, 3000, "PNG", None, True),
    }

    monkeypatch.setattr("media_dedupe.pipeline.file_sha256", lambda path: path.name)
    monkeypatch.setattr(
        "media_dedupe.pipeline.read_image_metadata", lambda path: metadata_by_path[path]
    )
    monkeypatch.setattr(
        "media_dedupe.pipeline.compute_image_phash", lambda path: "ffff"
    )

    groups = scan_paths(
        [tmp_path],
        cache_path=tmp_path / "cache.sqlite",
        similarity_threshold=0.80,
        recursive=True,
    )

    similar_group = next(
        group for group in groups if group.group_type.value == "similar_image"
    )
    keep_items = [
        item
        for item in similar_group.items
        if item.action == RecommendationAction.KEEP_RECOMMENDED
    ]
    assert [item.path for item in keep_items] == [str(second)]


def test_scan_paths_uses_connected_components_for_similar_images(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    third = tmp_path / "c.png"
    for path in (first, second, third):
        path.write_bytes(path.name.encode())
    cache_path = tmp_path / "cache.sqlite"

    metadata = ImageMetadata(64, 64, "PNG", None, True)
    hashes = {
        first: "0000000000000000",
        second: "000000000000000f",
        third: "00000000000000ff",
    }

    monkeypatch.setattr("media_dedupe.pipeline.file_sha256", lambda path: path.name)
    monkeypatch.setattr(
        "media_dedupe.pipeline.read_image_metadata", lambda path: metadata
    )
    monkeypatch.setattr(
        "media_dedupe.pipeline.compute_image_phash", lambda path: hashes[path]
    )

    groups = scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.90, recursive=True
    )

    similar_groups = [
        group for group in groups if group.group_type.value == "similar_image"
    ]
    assert len(similar_groups) == 1
    assert {item.path for item in similar_groups[0].items} == {
        str(first),
        str(second),
        str(third),
    }
