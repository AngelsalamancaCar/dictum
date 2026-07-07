import zipfile
from io import BytesIO

import pytest

from packager.bundle import build_bundle, pack_bundle, render_prompt


def test_render_prompt_substitutes_string_and_structured_placeholders():
    rendered = render_prompt(
        "classify",
        {
            "typology_catalog": [{"name": "despido injustificado"}],
            "case_summary": "el actor demanda...",
        },
    )
    assert "el actor demanda..." in rendered
    assert '"name": "despido injustificado"' in rendered
    assert "{{" not in rendered


def test_render_prompt_strips_version_comment():
    rendered = render_prompt("classify", {"typology_catalog": [], "case_summary": "x"})
    assert "prompt_version" not in rendered


def test_render_prompt_missing_placeholder_raises():
    with pytest.raises(ValueError, match="case_summary"):
        render_prompt("classify", {"typology_catalog": []})


def test_render_prompt_unknown_use_case_raises():
    with pytest.raises(ValueError, match="unknown use_case"):
        render_prompt("nope", {})


def test_build_bundle_manifest_fields():
    bundle = build_bundle(
        "classify",
        {"typology_catalog": [], "case_summary": "resumen"},
        package_id="fixed-id",
    )
    assert bundle["package_id"] == "fixed-id"
    assert bundle["use_case"] == "classify"
    assert bundle["prompt_version"] == 1
    assert "resumen" in bundle["prompt"]
    assert bundle["output_schema"]["title"] == "classify_output"
    assert bundle["context"] == {"typology_catalog": [], "case_summary": "resumen"}


def test_build_bundle_generates_package_id_when_omitted():
    bundle = build_bundle("classify", {"typology_catalog": [], "case_summary": "x"})
    assert bundle["package_id"]


def test_build_bundle_unknown_use_case_raises():
    with pytest.raises(ValueError, match="unknown use_case"):
        build_bundle("nope", {})


def test_pack_bundle_zip_contents():
    bundle = build_bundle(
        "similar_explain",
        {"case_summary": "caso", "retrieved_ruling": "sentencia"},
        package_id="pkg-1",
    )
    archive_bytes = pack_bundle(bundle)

    with zipfile.ZipFile(BytesIO(archive_bytes)) as zf:
        names = set(zf.namelist())
        assert names == {
            "manifest.json",
            "prompts/similar_explain.md",
            "output_schema.json",
            "context/case_summary.json",
            "context/retrieved_ruling.json",
        }
        assert "caso" in zf.read("prompts/similar_explain.md").decode("utf-8")
        assert zf.read("context/case_summary.json").decode("utf-8") == '"caso"'
