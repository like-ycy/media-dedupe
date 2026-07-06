from __future__ import annotations

import math

from src.models import ImageMetadata, RecommendationAction, VideoMetadata


def score_image_quality(metadata: ImageMetadata, *, size_bytes: int) -> float:
    if not metadata.is_readable:
        return 0.0
    megapixels = (metadata.width * metadata.height) / 1_000_000
    resolution_score = min(0.70, math.log1p(megapixels) / math.log1p(12) * 0.70)
    format_score = _image_format_score(metadata.format_name)
    size_score = min(
        0.10, math.log1p(max(size_bytes, 0)) / math.log1p(10_000_000) * 0.10
    )
    return min(1.0, resolution_score + format_score + size_score + 0.10)


def score_video_quality(metadata: VideoMetadata) -> float:
    if not metadata.is_readable:
        return 0.0
    pixels = metadata.width * metadata.height
    resolution_score = min(0.55, math.log1p(pixels) / math.log1p(3840 * 2160) * 0.55)
    bit_rate_score = 0.0
    if metadata.bit_rate is not None and metadata.bit_rate > 0:
        bit_rate_score = min(
            0.20, math.log1p(metadata.bit_rate) / math.log1p(20_000_000) * 0.20
        )
    codec_score = 0.10 if metadata.codec in {"h264", "hevc", "h265"} else 0.05
    audio_score = 0.05 if metadata.has_audio else 0.0
    duration_score = 0.10 if metadata.duration_ms > 0 else 0.0
    return min(
        1.0,
        resolution_score + bit_rate_score + codec_score + audio_score + duration_score,
    )


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
        return {
            file_id: RecommendationAction.REVIEW_REQUIRED for file_id in quality_scores
        }
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
