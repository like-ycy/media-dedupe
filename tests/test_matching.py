from __future__ import annotations

from src.matching import connected_components, is_video_metadata_candidate
from src.models import GroupType, SimilarityEdge, VideoMetadata


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
