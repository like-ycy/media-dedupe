from __future__ import annotations

from html import escape
import json

from media_dedupe.models import (
    RecommendationAction,
    ReportError,
    ReportGroup,
    ReportItem,
)


def render_text_report(
    groups: list[ReportGroup], *, errors: list[ReportError] | None = None
) -> str:
    lines: list[str] = []
    for group in groups:
        lines.append(
            f"Group #{group.group_id} {group.group_type.value} confidence={group.confidence:.2f}"
        )
        recommended = _recommended_item(group)
        if recommended is not None:
            lines.append("Recommended keep:")
            lines.append(f"  {recommended.path}")
            lines.append(f"  reason: {', '.join(recommended.reasons)}")
        cleanup_items = [
            item
            for item in group.items
            if item.action == RecommendationAction.CLEANUP_CANDIDATE
        ]
        if cleanup_items:
            lines.append("")
            lines.append("Cleanup candidates:")
            for item in cleanup_items:
                lines.append(f"  {item.path}")
                lines.append(f"  similarity: {item.similarity:.2f}")
                lines.append(f"  reason: {', '.join(item.reasons)}")
        lines.append("")
    if errors:
        lines.append("Errors:")
        for error in errors:
            lines.append(f"  {error.path}")
            lines.append(f"  stage: {error.stage}")
            lines.append(f"  message: {error.message}")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def render_json_report(
    groups: list[ReportGroup], *, errors: list[ReportError] | None = None
) -> str:
    payload = {
        "groups": [
            {
                "id": group.group_id,
                "type": group.group_type.value,
                "confidence": group.confidence,
                "recommended_keep": _recommended_path(group),
                "items": [
                    {
                        "file_id": item.file_id,
                        "path": item.path,
                        "action": item.action.value,
                        "similarity": item.similarity,
                        "quality_score": item.quality_score,
                        "reasons": list(item.reasons),
                    }
                    for item in group.items
                ],
            }
            for group in groups
        ],
        "errors": [
            {"path": error.path, "stage": error.stage, "message": error.message}
            for error in errors or []
        ],
    }
    return json.dumps(payload, ensure_ascii=False, indent=2) + "\n"


def render_html_report(
    groups: list[ReportGroup], *, errors: list[ReportError] | None = None
) -> str:
    body = "\n".join(_render_html_group(group) for group in groups)
    errors_body = _render_html_errors(errors or [])
    return (
        "<!doctype html>\n"
        '<html lang="en">\n'
        "<head>\n"
        '  <meta charset="utf-8">\n'
        "  <title>Media Dedupe Report</title>\n"
        "</head>\n"
        "<body>\n"
        "  <h1>Media Dedupe Report</h1>\n"
        f"{body}\n"
        f"{errors_body}\n"
        "</body>\n"
        "</html>\n"
    )


def _recommended_item(group: ReportGroup) -> ReportItem | None:
    for item in group.items:
        if item.file_id == group.recommended_file_id:
            return item
    return None


def _recommended_path(group: ReportGroup) -> str | None:
    item = _recommended_item(group)
    return item.path if item is not None else None


def _render_html_group(group: ReportGroup) -> str:
    item_rows = "\n".join(
        "      <tr>"
        f"<td>{escape(item.action.value)}</td>"
        f"<td>{escape(item.path)}</td>"
        f"<td>{item.similarity:.2f}</td>"
        f"<td>{item.quality_score:.2f}</td>"
        f"<td>{escape(', '.join(item.reasons))}</td>"
        "</tr>"
        for item in group.items
    )
    return (
        f'  <section class="group {escape(group.group_type.value)}">\n'
        f"    <h2>Group #{group.group_id} {escape(group.group_type.value)} confidence={group.confidence:.2f}</h2>\n"
        "    <table>\n"
        "      <thead><tr><th>Action</th><th>Path</th><th>Similarity</th><th>Quality</th><th>Reasons</th></tr></thead>\n"
        f"    <tbody>\n{item_rows}\n    </tbody>\n"
        "    </table>\n"
        "  </section>"
    )


def _render_html_errors(errors: list[ReportError]) -> str:
    if not errors:
        return ""
    rows = "\n".join(
        "      <tr>"
        f"<td>{escape(error.path)}</td>"
        f"<td>{escape(error.stage)}</td>"
        f"<td>{escape(error.message)}</td>"
        "</tr>"
        for error in errors
    )
    return (
        '  <section class="errors">\n'
        "    <h2>Errors</h2>\n"
        "    <table>\n"
        "      <thead><tr><th>Path</th><th>Stage</th><th>Message</th></tr></thead>\n"
        f"      <tbody>\n{rows}\n      </tbody>\n"
        "    </table>\n"
        "  </section>"
    )
