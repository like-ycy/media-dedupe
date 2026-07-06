from __future__ import annotations

from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from statistics import median

from src.cache import Cache
from src.discovery import discover_media_files
from src.hashing import file_sha256, hash_similarity
from src.image_fingerprint import compute_image_phash, read_image_metadata
from src.matching import connected_components, is_video_metadata_candidate
from src.models import (
    DiscoveredFile,
    GroupType,
    ImageMetadata,
    MediaType,
    RecommendationAction,
    ReportGroup,
    ReportItem,
    SimilarityEdge,
    VideoMetadata,
)
from src.scoring import (
    choose_recommended_item,
    score_image_quality,
    score_video_quality,
)
from src.video_fingerprint import compute_video_frame_hashes, ffprobe_metadata


def scan_paths(
    paths: list[Path],
    *,
    cache_path: Path,
    similarity_threshold: float,
    recursive: bool,
    include_images: bool = True,
    include_videos: bool = True,
    workers: int = 1,
) -> list[ReportGroup]:
    if workers < 1:
        raise ValueError("workers must be at least 1")
    cache = Cache(cache_path)
    cache.initialize()
    scan_run_id = cache.start_scan_run(
        paths,
        {
            "similarity_threshold": similarity_threshold,
            "recursive": recursive,
            "include_images": include_images,
            "include_videos": include_videos,
            "workers": workers,
        },
    )

    def record_discovery_error(path: Path, stage: str, message: str) -> None:
        cache.record_error(scan_run_id, path, stage, message)

    discovered_files = [
        discovered
        for discovered in discover_media_files(
            paths, recursive=recursive, on_error=record_discovery_error
        )
        if (include_images or discovered.media_type != MediaType.IMAGE)
        and (include_videos or discovered.media_type != MediaType.VIDEO)
    ]
    file_ids: dict[Path, int] = {}
    unchanged_paths: set[Path] = set()
    for discovered in discovered_files:
        if cache.is_file_unchanged(discovered):
            unchanged_paths.add(discovered.path)
        file_ids[discovered.path] = cache.upsert_discovered_file(discovered)
    cache.mark_missing_except(set(file_ids.values()))

    groups: list[ReportGroup] = []
    next_group_id = 1
    exact_groups, used_exact_paths = _exact_duplicate_groups(
        discovered_files,
        file_ids,
        unchanged_paths,
        cache,
        scan_run_id,
        next_group_id,
        workers,
    )
    groups.extend(exact_groups)
    next_group_id += len(exact_groups)

    image_groups = _similar_image_groups(
        [
            item
            for item in discovered_files
            if item.media_type == MediaType.IMAGE and item.path not in used_exact_paths
        ],
        file_ids,
        unchanged_paths,
        cache,
        scan_run_id,
        next_group_id,
        similarity_threshold,
        workers,
    )
    groups.extend(image_groups)
    next_group_id += len(image_groups)

    video_groups = _similar_video_groups(
        [
            item
            for item in discovered_files
            if item.media_type == MediaType.VIDEO and item.path not in used_exact_paths
        ],
        file_ids,
        unchanged_paths,
        cache,
        scan_run_id,
        next_group_id,
        similarity_threshold,
        workers,
    )
    groups.extend(video_groups)

    cache.replace_report_groups(groups, scan_run_id)
    cache.finish_scan_run(
        scan_run_id,
        files_seen=len(discovered_files),
        files_failed=len(cache.load_errors(scan_run_id)),
    )
    return groups


def _exact_duplicate_groups(
    discovered_files: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    start_group_id: int,
    workers: int,
) -> tuple[list[ReportGroup], set[Path]]:
    size_buckets: dict[int, list[DiscoveredFile]] = defaultdict(list)
    discovered_by_path = {
        discovered.path: discovered for discovered in discovered_files
    }
    for discovered in discovered_files:
        size_buckets[discovered.size_bytes].append(discovered)

    groups: list[ReportGroup] = []
    used_paths: set[Path] = set()
    next_group_id = start_group_id
    for same_size_files in size_buckets.values():
        if len(same_size_files) < 2:
            continue
        hash_buckets: dict[str, list[Path]] = defaultdict(list)
        file_hashes = _get_file_hashes(
            same_size_files, file_ids, unchanged_paths, cache, scan_run_id, workers
        )
        for path, full_hash in file_hashes.items():
            if full_hash is not None:
                hash_buckets[full_hash].append(path)
        for duplicate_paths in hash_buckets.values():
            if len(duplicate_paths) < 2:
                continue
            used_paths.update(duplicate_paths)
            quality_scores = {
                file_ids[path]: _score_discovered_quality(
                    discovered_by_path[path],
                    file_ids,
                    unchanged_paths,
                    cache,
                    scan_run_id,
                )
                for path in duplicate_paths
            }
            actions = choose_recommended_item(quality_scores, review_delta=-1.0)
            recommended_file_id = min(
                file_ids[path]
                for path in duplicate_paths
                if actions[file_ids[path]] == RecommendationAction.KEEP_RECOMMENDED
            )
            items = tuple(
                ReportItem(
                    file_ids[path],
                    str(path),
                    actions[file_ids[path]],
                    1.0,
                    quality_scores[file_ids[path]],
                    _quality_reasons(discovered_by_path[path]),
                )
                for path in sorted(duplicate_paths)
            )
            groups.append(
                ReportGroup(
                    next_group_id, GroupType.EXACT, 1.0, recommended_file_id, items
                )
            )
            next_group_id += 1
    return groups, used_paths


def _similar_image_groups(
    discovered_images: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    start_group_id: int,
    threshold: float,
    workers: int,
) -> list[ReportGroup]:
    image_data = [
        item
        for item in _collect_image_data(
            discovered_images, file_ids, unchanged_paths, cache, scan_run_id, workers
        )
        if item is not None
    ]

    edges: list[SimilarityEdge] = []
    edge_similarities: dict[tuple[int, int], float] = {}
    for index, (left_path, left_hash, _) in enumerate(image_data):
        for right_path, right_hash, _ in image_data[index + 1 :]:
            similarity = hash_similarity(left_hash, right_hash)
            if similarity >= threshold:
                left_file_id = file_ids[left_path]
                right_file_id = file_ids[right_path]
                edges.append(
                    SimilarityEdge(
                        left_file_id,
                        right_file_id,
                        GroupType.SIMILAR_IMAGE,
                        similarity,
                        ("visual image hash match",),
                    )
                )
                edge_similarities[_edge_key(left_file_id, right_file_id)] = similarity
    return _groups_from_components(
        image_data,
        file_ids,
        connected_components(edges),
        edge_similarities,
        GroupType.SIMILAR_IMAGE,
        start_group_id,
        "visual image hash match",
    )


def _similar_video_groups(
    discovered_videos: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    start_group_id: int,
    threshold: float,
    workers: int,
) -> list[ReportGroup]:
    video_data = [
        item
        for item in _collect_video_data(
            discovered_videos, file_ids, unchanged_paths, cache, scan_run_id, workers
        )
        if item is not None
    ]

    edges: list[SimilarityEdge] = []
    edge_similarities: dict[tuple[int, int], float] = {}
    for index, (left_path, left_metadata, left_hashes, _) in enumerate(video_data):
        for right_path, right_metadata, right_hashes, _ in video_data[index + 1 :]:
            if not is_video_metadata_candidate(left_metadata, right_metadata):
                continue
            similarity = _video_hash_similarity(left_hashes, right_hashes)
            if similarity >= threshold:
                left_file_id = file_ids[left_path]
                right_file_id = file_ids[right_path]
                edges.append(
                    SimilarityEdge(
                        left_file_id,
                        right_file_id,
                        GroupType.SIMILAR_VIDEO,
                        similarity,
                        ("video frame hash match",),
                    )
                )
                edge_similarities[_edge_key(left_file_id, right_file_id)] = similarity
    return _groups_from_components(
        [
            (path, frame_hashes[0], quality_score)
            for path, _, frame_hashes, quality_score in video_data
        ],
        file_ids,
        connected_components(edges),
        edge_similarities,
        GroupType.SIMILAR_VIDEO,
        start_group_id,
        "video frame hash match",
    )


def _groups_from_components(
    media_data: list[tuple[Path, str, float]],
    file_ids: dict[Path, int],
    components: list[set[int]],
    edge_similarities: dict[tuple[int, int], float],
    group_type: GroupType,
    start_group_id: int,
    match_reason: str,
) -> list[ReportGroup]:
    path_by_file_id = {file_ids[path]: path for path, _, _ in media_data}
    quality_by_file_id = {
        file_ids[path]: quality_score for path, _, quality_score in media_data
    }
    groups: list[ReportGroup] = []
    next_group_id = start_group_id
    for component in components:
        if len(component) < 2:
            continue
        ordered_file_ids = sorted(component)
        quality_scores = {
            file_id: quality_by_file_id[file_id] for file_id in ordered_file_ids
        }
        actions = choose_recommended_item(quality_scores)
        recommended_file_id = max(
            quality_scores, key=lambda file_id: quality_scores[file_id]
        )
        item_similarities = {
            file_id: _component_similarity(file_id, ordered_file_ids, edge_similarities)
            for file_id in ordered_file_ids
        }
        items = tuple(
            ReportItem(
                file_id,
                str(path_by_file_id[file_id]),
                actions[file_id],
                item_similarities[file_id],
                quality_scores[file_id],
                (match_reason, *_score_reason_parts(quality_scores[file_id])),
            )
            for file_id in ordered_file_ids
        )
        groups.append(
            ReportGroup(
                next_group_id,
                group_type,
                min(item_similarities.values()),
                recommended_file_id,
                items,
            )
        )
        next_group_id += 1
    return groups


def _get_file_hashes(
    discovered_files: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    workers: int,
) -> dict[Path, str | None]:
    hashes: dict[Path, str | None] = {}
    to_compute: list[DiscoveredFile] = []
    for discovered in discovered_files:
        file_id = file_ids[discovered.path]
        if discovered.path not in unchanged_paths:
            to_compute.append(discovered)
            continue
        cached_hash = cache.load_file_hash(file_id)
        if cached_hash is not None:
            hashes[discovered.path] = cached_hash
        else:
            to_compute.append(discovered)

    for discovered, full_hash, error_message in _compute_file_hashes(
        to_compute, workers
    ):
        if error_message is not None:
            cache.record_error(scan_run_id, discovered.path, "file_hash", error_message)
            hashes[discovered.path] = None
            continue
        if full_hash is None:
            hashes[discovered.path] = None
            continue
        cache.save_file_hash(
            file_ids[discovered.path], discovered.size_bytes, full_hash
        )
        hashes[discovered.path] = full_hash
    return hashes


def _compute_file_hashes(
    discovered_files: list[DiscoveredFile], workers: int
) -> list[tuple[DiscoveredFile, str | None, str | None]]:
    if not discovered_files:
        return []
    if workers == 1:
        return [_compute_file_hash(discovered) for discovered in discovered_files]
    with ThreadPoolExecutor(max_workers=workers) as executor:
        return list(executor.map(_compute_file_hash, discovered_files))


def _compute_file_hash(
    discovered: DiscoveredFile,
) -> tuple[DiscoveredFile, str | None, str | None]:
    try:
        full_hash = file_sha256(discovered.path)
    except OSError as error:
        return discovered, None, str(error)
    return discovered, full_hash, None


def _collect_image_data(
    discovered_images: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    workers: int,
) -> list[tuple[Path, str, float] | None]:
    if not discovered_images:
        return []

    def collect(discovered: DiscoveredFile) -> tuple[Path, str, float] | None:
        metadata = _get_image_metadata(
            discovered, file_ids, unchanged_paths, cache, scan_run_id
        )
        if not metadata.is_readable:
            return None
        phashes = _get_image_hashes(
            discovered, file_ids, unchanged_paths, cache, scan_run_id
        )
        if not phashes:
            return None
        return (
            discovered.path,
            phashes[0],
            score_image_quality(metadata, size_bytes=discovered.size_bytes),
        )

    if workers == 1:
        return [collect(discovered) for discovered in discovered_images]
    with ThreadPoolExecutor(max_workers=workers) as executor:
        return list(executor.map(collect, discovered_images))


def _collect_video_data(
    discovered_videos: list[DiscoveredFile],
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
    workers: int,
) -> list[tuple[Path, VideoMetadata, list[str], float] | None]:
    if not discovered_videos:
        return []

    def collect(
        discovered: DiscoveredFile,
    ) -> tuple[Path, VideoMetadata, list[str], float] | None:
        metadata = _get_video_metadata(
            discovered, file_ids, unchanged_paths, cache, scan_run_id
        )
        if not metadata.is_readable:
            return None
        frame_hashes = _get_video_hashes(
            discovered, metadata, file_ids, unchanged_paths, cache, scan_run_id
        )
        if not frame_hashes:
            return None
        return (discovered.path, metadata, frame_hashes, score_video_quality(metadata))

    if workers == 1:
        return [collect(discovered) for discovered in discovered_videos]
    with ThreadPoolExecutor(max_workers=workers) as executor:
        return list(executor.map(collect, discovered_videos))


def _get_image_metadata(
    discovered: DiscoveredFile,
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
) -> ImageMetadata:
    file_id = file_ids[discovered.path]
    if discovered.path in unchanged_paths:
        cached_metadata = cache.load_image_metadata(file_id)
        if cached_metadata is not None:
            return cached_metadata
    metadata = read_image_metadata(discovered.path)
    cache.save_image_metadata(file_id, metadata)
    if not metadata.is_readable:
        cache.record_error(
            scan_run_id, discovered.path, "image_metadata", "unreadable image"
        )
    return metadata


def _get_image_hashes(
    discovered: DiscoveredFile,
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
) -> list[str]:
    file_id = file_ids[discovered.path]
    if discovered.path in unchanged_paths:
        cached_hashes = cache.load_perceptual_hashes(file_id, "image_phash")
        if cached_hashes:
            return cached_hashes
    try:
        phash = compute_image_phash(discovered.path)
    except OSError as error:
        cache.record_error(scan_run_id, discovered.path, "image_phash", str(error))
        return []
    cache.save_perceptual_hashes(file_id, "image_phash", [phash])
    return [phash]


def _get_video_metadata(
    discovered: DiscoveredFile,
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
) -> VideoMetadata:
    file_id = file_ids[discovered.path]
    if discovered.path in unchanged_paths:
        cached_metadata = cache.load_video_metadata(file_id)
        if cached_metadata is not None:
            return cached_metadata
    metadata = ffprobe_metadata(discovered.path)
    cache.save_video_metadata(file_id, metadata)
    if not metadata.is_readable:
        cache.record_error(
            scan_run_id, discovered.path, "video_metadata", "unreadable video"
        )
    return metadata


def _get_video_hashes(
    discovered: DiscoveredFile,
    metadata: VideoMetadata,
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
) -> list[str]:
    file_id = file_ids[discovered.path]
    if discovered.path in unchanged_paths:
        cached_hashes = cache.load_perceptual_hashes(file_id, "video_frame_phash")
        if cached_hashes:
            return cached_hashes
    try:
        frame_hashes = compute_video_frame_hashes(discovered.path, metadata=metadata)
    except OSError as error:
        cache.record_error(
            scan_run_id, discovered.path, "video_frame_phash", str(error)
        )
        return []
    if not frame_hashes:
        cache.record_error(
            scan_run_id, discovered.path, "video_frame_phash", "no frames extracted"
        )
        return []
    cache.save_perceptual_hashes(file_id, "video_frame_phash", frame_hashes)
    return frame_hashes


def _score_discovered_quality(
    discovered: DiscoveredFile,
    file_ids: dict[Path, int],
    unchanged_paths: set[Path],
    cache: Cache,
    scan_run_id: int,
) -> float:
    if discovered.media_type == MediaType.IMAGE:
        metadata = _get_image_metadata(
            discovered, file_ids, unchanged_paths, cache, scan_run_id
        )
        if metadata.is_readable:
            _get_image_hashes(discovered, file_ids, unchanged_paths, cache, scan_run_id)
        return score_image_quality(metadata, size_bytes=discovered.size_bytes)
    metadata = _get_video_metadata(
        discovered, file_ids, unchanged_paths, cache, scan_run_id
    )
    return score_video_quality(metadata)


def _quality_reasons(discovered: DiscoveredFile) -> tuple[str, ...]:
    if discovered.media_type == MediaType.IMAGE:
        return ("identical file hash", "quality score uses image resolution and format")
    return (
        "identical file hash",
        "quality score uses video resolution bitrate and duration",
    )


def _score_reason_parts(quality_score: float) -> tuple[str, ...]:
    if quality_score <= 0:
        return ("unreadable media",)
    return ("quality score uses media metadata",)


def _edge_key(left_file_id: int, right_file_id: int) -> tuple[int, int]:
    return (min(left_file_id, right_file_id), max(left_file_id, right_file_id))


def _component_similarity(
    file_id: int,
    component_file_ids: list[int],
    edge_similarities: dict[tuple[int, int], float],
) -> float:
    if file_id == component_file_ids[0]:
        return 1.0
    similarities = [
        similarity
        for other_file_id in component_file_ids
        if other_file_id != file_id
        for similarity in [edge_similarities.get(_edge_key(file_id, other_file_id))]
        if similarity is not None
    ]
    if not similarities:
        return 1.0
    return max(similarities)


def _video_hash_similarity(left_hashes: list[str], right_hashes: list[str]) -> float:
    pair_count = min(len(left_hashes), len(right_hashes))
    if pair_count == 0:
        return 0.0
    similarities = [
        hash_similarity(left_hashes[index], right_hashes[index])
        for index in range(pair_count)
    ]
    return float(median(similarities))


def load_last_report_groups(cache: Cache) -> list[ReportGroup]:
    cache.initialize()
    return cache.load_report_groups()
