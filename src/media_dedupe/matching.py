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
    return _aspect_ratio_close(
        left.width, left.height, right.width, right.height, aspect_ratio_tolerance
    )


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
