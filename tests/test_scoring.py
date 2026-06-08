from __future__ import annotations

from media_dedupe.models import ImageMetadata, RecommendationAction, VideoMetadata
from media_dedupe.scoring import (
    choose_recommended_item,
    score_image_quality,
    score_video_quality,
)


def test_score_image_quality_prefers_higher_resolution_readable_image() -> None:
    low = ImageMetadata(800, 600, "JPEG", None, True)
    high = ImageMetadata(4000, 3000, "JPEG", None, True)

    assert score_image_quality(high, size_bytes=2_000_000) > score_image_quality(
        low, size_bytes=2_000_000
    )


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
