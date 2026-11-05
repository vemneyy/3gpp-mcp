"""Regression against the competing hits observed on the user's full server."""

import json
import sqlite3
from collections import Counter
from pathlib import Path
from fastapi.testclient import TestClient

from backend import app as module
from backend.ranking_context import context_quality, subject_title, technology_in


def test_banners_are_not_the_subject_and_e_utran_is_not_utran():
    title = "Digital cellular telecommunications system (Phase 2+) (GSM); Universal Mobile Telecommunications System (UMTS); LTE; 5G; Mobile radio interface Layer 3 specification"
    assert subject_title(title) == "mobile radio interface layer 3 specification"
    assert technology_in("e utran initial attach", "4g")
    assert not technology_in("e utran initial attach", "3g")
    assert technology_in("utran initial attach", "3g")


def test_explicit_specialized_intent_remains_searchable():
    title = "LTE; Single Radio Voice Call Continuity (SRVCC)"
    generic, _ = context_quality(
        "4g", "e utran attach procedure for srvcc", [], title, ["4g", "attach"]
    )
    explicit, _ = context_quality(
        "4g", "e utran attach procedure for srvcc", [], title, ["4g", "srvcc", "attach"]
    )
    assert explicit > generic
    title = "LTE; Architecture enhancements for non-3GPP accesses"
    parents = [
        "functional description for pmip based s5 s8",
        "pmip procedures for geran utran over s4",
    ]
    generic, _ = context_quality("4g", "attach", parents, title, ["4g", "attach"])
    explicit, _ = context_quality(
        "4g", "attach", parents, title, ["4g", "pmip", "attach"]
    )
    assert explicit > generic


def test_initial_attach_beats_full_server_competitors(tmp_path, monkeypatch):
    db = sqlite3.connect(tmp_path / "context.db")
    db.executescript((Path(__file__).parent / "schema.sql").read_text())
    examples = json.loads(
        (Path(__file__).parent / "testdata/attach_live_competitors.json").read_text()
    )
    docs = {}
    sections = {}
    for example in examples:
        spec = example["spec"]
        if spec not in docs:
            did = len(docs) + 1
            docs[spec] = did
            db.execute(
                "INSERT INTO documents VALUES(?,?,?,?,?,?,?,?)",
                (
                    did,
                    spec,
                    "3gpp",
                    spec[:2],
                    example["title"],
                    "19.0.0",
                    "https://www.etsi.org/" + spec + ".pdf",
                    "hash",
                ),
            )
            db.execute(
                "INSERT INTO document_search VALUES(?,?,?,?)",
                (did, module.normalize(example["title"]), spec.replace(".", ""), ""),
            )
            db.executemany(
                "INSERT INTO document_postings VALUES(?,?,?)",
                [
                    (t, did, n)
                    for t, n in Counter(
                        module.normalize(example["title"]).split()
                    ).items()
                ],
            )
        did = docs[spec]
        for item in [
            *example["parents"],
            {"number": example["clause"], "title": example["heading"]},
        ]:
            key = (did, item["number"])
            body = (
                ("EPS attach procedure. " * max(1, min(example["mentions"], 1000)))
                if item["number"] == example["clause"]
                else ""
            )
            sections[key] = (item["title"], body)
    for sid, ((did, number), (title, body)) in enumerate(sections.items(), 1):
        db.execute(
            "INSERT INTO sections VALUES(?,?,?,?,?)", (sid, did, number, title, 1)
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
            (sid, "content", sum(b.values()), sum(a.values())),
        )
    db.commit()
    db.close()
    monkeypatch.setattr(module, "DB_PATH", str(tmp_path / "context.db"))
    client = TestClient(module.app)
    result = client.get("/api/search?q=4g+attach").json()
    assert result["page_size"] == 5
    first = result["results"][0]
    assert first["spec"] == "23.401" and first["sections"][0]["number"] == "5.3.2.1"
    assert all(
        d["spec"] not in {"23.402", "23.236", "33.128", "23.272", "23.139", "24.008"}
        for d in result["results"]
    )
    scores = [d["score"] for d in result["results"]]
    assert scores == sorted(scores, reverse=True)
    html = client.get("/?q=4g+attach").text
    assert 'aria-label="Collections"' not in html
    assert 'href="/glossary' not in html and ">Specifications</a>" not in html
    assert "Search within results" in html
