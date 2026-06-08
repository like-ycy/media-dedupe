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


@dataclass(frozen=True, slots=True)
class ReportError:
    path: str
    stage: str
    message: str
