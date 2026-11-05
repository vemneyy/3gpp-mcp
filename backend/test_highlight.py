import pytest
from backend.highlight import Plan, evidence, tokens
from backend.text import normalize
from backend.search_engine import FORMS, TECH, TECH_NAMES, STOP


@pytest.mark.parametrize(
    "text",
    [
        "PDU_session—establishment",
        "Cafe\u0301 café ﬁle ＭＳＩＳＤＮ",
        "5G/EPS/4G-GUTI",
        "“MSISDN” and AMF",
        "x½y",
    ],
)
def test_token_offsets_follow_index_normalization(text):
    parsed = list(tokens(text))
    assert " ".join(t[0] for t in parsed) == normalize(text)
    assert all(0 <= a < b <= len(text) for _, a, b in parsed)


def test_snippet_never_falls_back_to_unmatched_beginning():
    plan = Plan("MSISDN")
    assert plan.fragment("Nothing related in this text") is None
    body = "Unrelated beginning. " * 100 + "The subscriber MSISDN is returned."
    fragment = plan.fragment(body)
    assert any(p["match"] and p["text"] == "MSISDN" for p in fragment["parts"])
    assert "MSISDN" in fragment["text"]
    assert "".join(p["text"] for p in fragment["parts"]) == fragment["text"]


def test_match_only_in_heading_uses_that_field():
    result = evidence(
        Plan("MSISDN"),
        "This command returns subscriber data.",
        "Display MSISDN by IMSI",
        "Subscriber management",
    )
    assert result["source"] == "section_title"
    assert result["text"] == "Display MSISDN by IMSI"
    assert any(p["match"] and p["text"] == "MSISDN" for p in result["parts"])


def test_alias_is_explicit_and_detach_is_not_attach():
    plan = Plan("register", FORMS, TECH, TECH_NAMES, STOP)
    fragment = plan.fragment("Registration starts here.")
    assert fragment["expanded"] == [{"query": "register", "matched": "registration"}]
    assert fragment["parts"][0] == {"text": "Registration", "match": True}
    assert Plan("attach", FORMS, TECH, TECH_NAMES, STOP).fragment("Detach now.") is None


def test_original_characters_and_punctuation_survive():
    plan = Plan("cafe msisdn")
    text = "Cafe\u0301 and ＭＳＩＳＤＮ"
    parts = plan.parts(text)
    assert "".join(p["text"] for p in parts) == text
    assert [p["text"] for p in parts if p["match"]] == ["Cafe\u0301", "ＭＳＩＳＤＮ"]


def test_all_query_parts_in_a_compact_window():
    fragment = Plan("PDU session").fragment(
        "PDU alone. " + "noise " * 100 + "A PDU-session starts now."
    )
    assert [p["text"] for p in fragment["parts"] if p["match"]] == ["PDU", "session"]


def test_established_generation_does_not_force_a_late_special_case_excerpt():
    full = Plan("4g attach", FORMS, TECH, TECH_NAMES, STOP)
    core = Plan("attach", FORMS, TECH, TECH_NAMES, STOP)
    body = (
        "The Attach procedure registers the UE with the network. "
        + ("Background. " * 80)
        + "The IoT case uses EPS Attach without PDN Connectivity."
    )
    result = evidence(full, body, "E-UTRAN Initial Attach", "LTE; EPS", focus_plan=core)
    assert result["text"].startswith("The Attach procedure")
    assert any(p["match"] and p["text"] == "Attach" for p in result["parts"])
