"""Regression fixture derived from the user's 4g attach search failure."""

import sqlite3
from collections import Counter
from pathlib import Path
import pytest
from fastapi.testclient import TestClient
from backend import app as module


@pytest.fixture
def corpus(tmp_path, monkeypatch):
    db = sqlite3.connect(tmp_path / "relevance.db")
    db.executescript((Path(__file__).parent / "schema.sql").read_text())
    docs = [
        (1, "23.401", "GPRS enhancements for E-UTRAN access"),
        (2, "24.301", "LTE; Non-Access-Stratum (NAS) protocol for EPS; Stage 3"),
        (3, "23.502", "5G; Procedures for the 5G System"),
        (4, "99.999", "5G; Unrelated specification"),
    ]
    for did, spec, title in docs:
        db.execute(
            "INSERT INTO documents VALUES(?,?,?,?,?,?,?,?)",
            (
                did,
                spec,
                "3gpp",
                spec[:2],
                title,
                "19.0.0",
                f"https://www.etsi.org/{spec}.pdf",
                "hash",
            ),
        )
        db.execute(
            "INSERT INTO document_search VALUES(?,?,?,?)",
            (did, module.normalize(title), spec.replace(".", ""), ""),
        )
        db.executemany(
            "INSERT INTO document_postings VALUES(?,?,?)",
            [(w, did, n) for w, n in Counter(module.normalize(title).split()).items()],
        )
    clauses = [
        (
            1,
            1,
            "5.3.2",
            "E-UTRAN Initial Attach",
            (
                "The UE sends an Attach Request. The MME performs the EPS attach procedure. "
                * 400
            ),
            "content",
        ),
        (
            2,
            2,
            "5.5.1.2.5",
            "Attach not accepted by the network",
            "The attach request cannot be accepted. 4G-GUTI. attach attach attach",
            "content",
        ),
        (
            3,
            3,
            "4.11.2.4.1",
            "E-UTRAN Attach",
            "Attach with a native 4G-GUTI while moving from 5GC.",
            "content",
        ),
        (4, 1, "Annex G", "Change history", ("4g attach " * 2000), "history"),
        (5, 4, "2", "References", "4g attach EPS LTE", "references"),
        (
            6,
            4,
            "5",
            "Detach procedure",
            "Detach is not attach, a 4G procedure.",
            "content",
        ),
        (7, 1, "3.2", "Abbreviations", "MME Mobility Management Entity", "glossary"),
        (
            8,
            2,
            "8.2.2",
            "Attach complete",
            "This message is sent in response to ATTACH ACCEPT.",
            "content",
        ),
    ]
    for sid, did, num, title, body, kind in clauses:
        db.execute(
            "INSERT INTO sections VALUES(?,?,?,?,?)", (sid, did, num, title, sid + 10)
        )
        db.execute("INSERT INTO section_text VALUES(?,?)", (sid, body))
        a, b = (
            Counter(module.normalize(title).split()),
            Counter(module.normalize(body).split()),
        )
        db.executemany(
            "INSERT INTO postings VALUES(?,?,?,?,?)",
            [(w, sid, did, a[w], b[w]) for w in a.keys() | b.keys()],
        )
        db.execute(
            "INSERT INTO section_features VALUES(?,?,?,?)",
            (sid, kind, sum(b.values()), sum(a.values())),
        )
    db.execute(
        "INSERT INTO glossary VALUES(1,1,7,'MME','mme','Mobility Management Entity','mobility management entity','abbreviation')"
    )
    db.executemany(
        "INSERT INTO glossary_postings VALUES(?,1)",
        [(w,) for w in ["mme", "mobility", "management", "entity"]],
    )
    db.commit()
    db.close()
    monkeypatch.setattr(module, "DB_PATH", str(tmp_path / "relevance.db"))
    return TestClient(module.app)


def test_attach_regression(corpus):
    result = corpus.get("/api/search?q=4g+attach").json()
    assert result["documents"][0]["spec"] == "23.401"  # does not require literal "4g"
    assert len(result["documents"][0]["sections"]) == 1
    assert result["documents"][0]["sections"][0]["title"] == "E-UTRAN Initial Attach"
    sections = [s for d in result["documents"] for s in d["sections"]]
    assert all(s["title"] not in ["Change history", "References"] for s in sections)
    assert all("Change history" not in s["title"] for s in sections)


def test_number_is_a_selector_not_a_citation(corpus):
    result = corpus.get("/api/search?q=23.401+attach").json()
    assert [d["spec"] for d in result["documents"]] == ["23.401"]


def test_title_search_and_stopwords(corpus):
    result = corpus.get(
        "/api/search", params={"q": "Non-Access-Stratum protocol for EPS"}
    ).json()
    assert result["documents"][0]["spec"] == "24.301"
    assert result["documents"][0]["match_reason"] == "Specification title"


def test_glossary_with_source(corpus):
    result = corpus.get("/api/glossary?q=mobility+management").json()
    assert result["total"] == 1
    entry = result["entries"][0]
    assert (
        entry["term"] == "MME" and entry["definition"] == "Mobility Management Entity"
    )
    assert entry["spec"] == "23.401" and entry["url"].endswith("#page=18")
    assert corpus.get("/glossary?q=MME").status_code == 200
    assert "Glossary" in corpus.get("/?q=MME").text


def test_requested_procedure_detail_remains_searchable(corpus):
    result = corpus.get("/api/search?q=attach+complete").json()
    assert result["documents"][0]["sections"][0]["title"] == "Attach complete"
    history = corpus.get("/api/search?q=change+history").json()
    assert history["documents"][0]["sections"][0]["title"] == "Change history"


def test_field_priority_and_literal_evidence(corpus):
    from backend import app as module

    db = sqlite3.connect(module.DB_PATH)
    # An exact heading with no keyword in the body must beat incidental repeats.
    for sid, title, body in [
        (20, "MSISDN", "The subscriber number is returned."),
        (21, "Subscriber handling", "MSISDN " * 3000),
        (22, "The procedure of MSISDN-less SMS", "MSISDN appears here."),
    ]:
        db.execute(
            "INSERT INTO sections VALUES(?,?,?,?,?)", (sid, 1, str(sid), title, sid)
        )
        db.execute("INSERT INTO section_text VALUES(?,?)", (sid, body))
        a, b = (
            Counter(module.normalize(title).split()),
            Counter(module.normalize(body).split()),
        )
        db.executemany(
            "INSERT INTO postings VALUES(?,?,?,?,?)",
            [(w, sid, 1, a[w], b[w]) for w in a.keys() | b.keys()],
        )
        db.execute(
            "INSERT INTO section_features VALUES(?,?,?,?)",
            (sid, "content", sum(b.values()), sum(a.values())),
        )
    db.commit()
    db.close()
    data = corpus.get("/api/search?q=MSISDN").json()
    assert data["results"][0]["sections"][0]["id"] == 20
    first = data["results"][0]["sections"][0]
    assert first["preview_source"] == "section_title"
    assert first["preview"] == "MSISDN"
    assert first["preview_parts"] == [{"text": "MSISDN", "match": True}]
    assert data["results"][-1]["sections"][0]["id"] == 21
    html = corpus.get("/?q=MSISDN").text
    assert "<strong>MSISDN</strong>" in html and "Matched in section title." in html


def test_every_real_snippet_has_marked_evidence(corpus):
    for query in [
        "4g attach",
        "attach complete",
        "non access stratum protocol",
        "23.401 attach",
    ]:
        data = corpus.get("/api/search", params={"q": query, "page_size": 100}).json()
        for result in data["results"]:
            if result["sections"]:
                section = result["sections"][0]
                assert section["preview_parts"] and any(
                    p["match"] for p in section["preview_parts"]
                )
                assert (
                    "".join(p["text"] for p in section["preview_parts"])
                    == section["preview"]
                )
