from __future__ import annotations

import json

from src.models import (
    GroupType,
    RecommendationAction,
    ReportError,
    ReportGroup,
    ReportItem,
)
from src.reports import (
    render_html_report,
    render_json_report,
    render_text_report,
)


def make_group() -> ReportGroup:
    return ReportGroup(
        group_id=12,
        group_type=GroupType.SIMILAR_VIDEO,
        confidence=0.91,
        recommended_file_id=1,
        items=(
            ReportItem(
                1,
                "/Movies/movie_1080p.mp4",
                RecommendationAction.KEEP_RECOMMENDED,
                1.0,
                0.92,
                ("higher resolution",),
            ),
            ReportItem(
                2,
                "/Downloads/movie_720p.mp4",
                RecommendationAction.CLEANUP_CANDIDATE,
                0.91,
                0.63,
                ("lower resolution",),
            ),
        ),
    )


def test_render_text_report_includes_recommendation() -> None:
    text = render_text_report([make_group()])

    assert "Group #12 similar_video confidence=0.91" in text
    assert "Recommended keep" in text
    assert "/Movies/movie_1080p.mp4" in text


def test_render_json_report_is_machine_readable() -> None:
    payload = json.loads(render_json_report([make_group()]))

    assert payload["groups"][0]["type"] == "similar_video"
    assert payload["groups"][0]["recommended_keep"] == "/Movies/movie_1080p.mp4"


def test_render_json_report_includes_errors() -> None:
    payload = json.loads(
        render_json_report(
            [],
            errors=[
                ReportError(
                    path="/Pictures/bad.png",
                    stage="image_metadata",
                    message="unreadable image",
                )
            ],
        )
    )

    assert payload["groups"] == []
    assert payload["errors"] == [
        {
            "path": "/Pictures/bad.png",
            "stage": "image_metadata",
            "message": "unreadable image",
        }
    ]


def test_render_html_report_escapes_user_controlled_text() -> None:
    group = ReportGroup(
        group_id=1,
        group_type=GroupType.SIMILAR_IMAGE,
        confidence=0.9,
        recommended_file_id=1,
        items=(
            ReportItem(
                1,
                '/Pictures/<script>alert("x")</script>.png',
                RecommendationAction.KEEP_RECOMMENDED,
                1.0,
                0.9,
                ('reason with <tag> & "quotes"',),
            ),
        ),
    )

    html = render_html_report(
        [group],
        errors=[ReportError("/bad/<img>.png", "image_metadata", "bad <image>")],
    )

    assert "<script>" not in html
    assert "&lt;script&gt;" in html
    assert "reason with &lt;tag&gt; &amp; &quot;quotes&quot;" in html
    assert "/bad/&lt;img&gt;.png" in html
