from __future__ import annotations

import json
import subprocess
from pathlib import Path
from tempfile import TemporaryDirectory

from src.image_fingerprint import compute_image_phash
from src.models import VideoMetadata

DEFAULT_VIDEO_FRAME_COUNT = 5
DEFAULT_VIDEO_FRAME_FAILURE_LIMIT = 2


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
    except (
        OSError,
        subprocess.CalledProcessError,
        subprocess.TimeoutExpired,
        json.JSONDecodeError,
    ):
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
    video_stream = next(
        (stream for stream in streams if stream.get("codec_type") == "video"), {}
    )
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


def extract_frame_to_image(
    path: Path, *, timestamp_ms: int, output_path: Path
) -> Path | None:
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
    except OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired:
        return None
    return output_path if output_path.exists() else None


def compute_video_frame_hashes(
    path: Path,
    *,
    frame_count: int = DEFAULT_VIDEO_FRAME_COUNT,
    metadata: VideoMetadata | None = None,
    frame_failure_limit: int = DEFAULT_VIDEO_FRAME_FAILURE_LIMIT,
) -> list[str]:
    metadata = metadata or ffprobe_metadata(path)
    timestamps = build_frame_timestamps(
        duration_ms=metadata.duration_ms, frame_count=frame_count
    )
    hashes: list[str] = []
    failed_frames = 0
    with TemporaryDirectory() as temporary_directory:
        temporary_path = Path(temporary_directory)
        for index, timestamp_ms in enumerate(timestamps):
            frame_path = temporary_path / f"frame-{index}.jpg"
            extracted = extract_frame_to_image(
                path, timestamp_ms=timestamp_ms, output_path=frame_path
            )
            if extracted is None:
                failed_frames += 1
                if failed_frames >= frame_failure_limit:
                    break
                continue
            hashes.append(compute_image_phash(extracted))
    return hashes


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
    if value is None:
        return None
    try:
        return float(str(value))
    except TypeError, ValueError:
        return None


def _int_or_none(value: object) -> int | None:
    if value is None:
        return None
    try:
        return int(str(value))
    except TypeError, ValueError:
        return None
