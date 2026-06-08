from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from media_dedupe.video_fingerprint import (
    build_frame_timestamps,
    compute_video_frame_hashes,
    extract_frame_to_image,
    ffprobe_metadata,
)
from media_dedupe.models import VideoMetadata


def test_build_frame_timestamps_skips_edges() -> None:
    assert build_frame_timestamps(duration_ms=10_000, frame_count=3) == [
        500,
        5_000,
        9_500,
    ]


def test_ffprobe_metadata_parses_json(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
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


def test_extract_frame_to_image_invokes_ffmpeg(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    calls: list[list[str]] = []

    def fake_run(args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        output_path = Path(args[-1])
        output_path.write_bytes(b"frame")
        return subprocess.CompletedProcess(
            args=args, returncode=0, stdout="", stderr=""
        )

    monkeypatch.setattr(subprocess, "run", fake_run)
    output = tmp_path / "frame.jpg"

    result = extract_frame_to_image(
        tmp_path / "clip.mp4", timestamp_ms=1_500, output_path=output
    )

    assert result == output
    assert output.exists()
    assert "ffmpeg" in calls[0][0]


def test_extract_frame_to_image_sets_timeout(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    timeouts: list[object] = []

    def fake_run(args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        timeouts.append(kwargs.get("timeout"))
        output_path = Path(args[-1])
        output_path.write_bytes(b"frame")
        return subprocess.CompletedProcess(
            args=args, returncode=0, stdout="", stderr=""
        )

    monkeypatch.setattr(subprocess, "run", fake_run)

    extract_frame_to_image(
        tmp_path / "clip.mp4", timestamp_ms=1_500, output_path=tmp_path / "frame.jpg"
    )

    assert timeouts == [30]


def test_compute_video_frame_hashes_stops_after_frame_failure_limit(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    calls: list[int] = []

    def fake_extract(
        path: Path, *, timestamp_ms: int, output_path: Path
    ) -> Path | None:
        calls.append(timestamp_ms)
        return None

    monkeypatch.setattr(
        "media_dedupe.video_fingerprint.extract_frame_to_image", fake_extract
    )

    hashes = compute_video_frame_hashes(
        tmp_path / "clip.mp4",
        frame_count=5,
        frame_failure_limit=2,
        metadata=VideoMetadata(10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True),
    )

    assert hashes == []
    assert len(calls) == 2
