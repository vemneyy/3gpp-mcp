import sqlite3
from collections import Counter
from pathlib import Path
import pytest
from fastapi.testclient import TestClient
from backend import app as module


@pytest.fixture
def client(tmp_path, monkeypatch):
    path = tmp_path / "test.db"
    db = sqlite3.connect(path)
    db.executescript((Path(__file__).parent / "schema.sql").read_text())
    db.execute(
        "INSERT INTO documents VALUES(1,'23.501','3gpp','23','Architecture','19.0.0','https://www.etsi.org/deliver/test.pdf','hash')"
    )
    db.execute(
        "INSERT INTO documents VALUES(2,'04.08','gsm','04','GSM','8.0.0','https://www.etsi.org/deliver/gsm.pdf','hash')"
    )
    for i, title, body, page in [
        (1, "PDU session", "one mention", 3),
        (2, "Body only", "PDU session PDU session PDU session", 4),
        (3, "Another", "pdu session", 5),
        (4, "<script>alert(1)</script>", "pdu session", 6),
    ]:
        db.execute(
            "INSERT INTO sections VALUES(?,?,?,?,?)", (i, 1, str(i), title, page)
        )
        db.execute("INSERT INTO section_text VALUES(?,?)", (i, body))
    for row in db.execute(
        "SELECT s.id,s.title,t.body FROM sections s JOIN section_text t ON t.section_id=s.id"
    ).fetchall():
        a, b = (
            Counter(module.normalize(row[1]).split()),
            Counter(module.normalize(row[2]).split()),
        )
        db.executemany(
            "INSERT INTO postings VALUES(?,?,?,?,?)",
            [(t, row[0], 1, a[t], b[t]) for t in a.keys() | b.keys()],
        )
    for row in db.execute("SELECT id,spec,title,url FROM documents").fetchall():
        db.execute(
            "INSERT INTO document_search VALUES(?,?,?,?)",
            (
                row[0],
                module.normalize(row[2]),
                row[1].replace(".", ""),
                "123501" if row[0] == 1 else "100940",
            ),
        )
        db.executemany(
            "INSERT INTO document_postings VALUES(?,?,?)",
            [
                (t, row[0], n)
                for t, n in Counter(module.normalize(row[2]).split()).items()
            ],
        )
    for row in db.execute(
        "SELECT s.id,s.title,t.body FROM sections s JOIN section_text t ON t.section_id=s.id"
    ).fetchall():
        db.execute(
            "INSERT INTO section_features VALUES(?,?,?,?)",
            (
                row[0],
                "content",
                len(module.normalize(row[2]).split()),
                len(module.normalize(row[1]).split()),
            ),
        )
    db.commit()
    db.close()
    monkeypatch.setattr(module, "DB_PATH", str(path))
    return TestClient(module.app)


def test_normalization_and_ranking(client):
    a = client.get("/api/search", params={"q": "PDU-session", "page_size": 20}).json()
    b = client.get(
        "/api/search", params={"q": "ＰＤＵ / session", "page_size": 20}
    ).json()
    assert a["documents"][0]["sections"] == b["documents"][0]["sections"]
    doc = a["documents"][0]
    assert a["total"] == 4 and a["matched_documents"] == 1
    assert [s["number"] for s in doc["sections"]] == ["1"]
    assert doc["sections"][0]["url"].endswith("#page=4")


def test_filters_html_and_validation(client):
    assert (
        client.get("/api/search", params={"q": "pdu", "family": "gsm"}).json()["total"]
        == 0
    )
    text = client.get("/", params={"q": "pdu"}).text
    assert "<script>" not in text and "&lt;script&gt;" in text
    assert client.get("/api/search?page_size=0").status_code == 422
    assert client.get("/api/search?q=%22%20OR%20*").status_code == 200
    assert client.get("/health").status_code == 200
    assert client.get("/index.html").status_code == 200


def test_read_only_and_missing_index(client, tmp_path, monkeypatch):
    monkeypatch.setattr(module, "DB_PATH", str(tmp_path / "missing.db"))
    assert client.get("/api/search").status_code == 503
    assert client.get("/").status_code == 503
    assert not (tmp_path / "missing.db").exists()


def test_schema_is_shared():
    root = Path(__file__).parent.parent
    assert (root / "backend/schema.sql").read_bytes() == (
        root / "indexer/schema.sql"
    ).read_bytes()


def test_global_document_pagination(client):
    first = client.get("/api/search?page_size=1").json()
    second = client.get("/api/search?page_size=1&page=2").json()
    assert first["pages"] == 2 and second["pages"] == 2
    assert len(first["documents"]) == 1 and len(second["documents"]) == 1
    assert first["documents"][0]["spec"] != second["documents"][0]["spec"]
    assert first["page_size"] == 1 and "previews" not in first


def test_initial_page_is_light_and_toc_is_separate(client):
    result = client.get("/api/search").json()
    assert all(not d["sections"] for d in result["documents"])
    page = client.get("/").text
    assert 'name="family"' not in page and 'name="series"' not in page
    assert "PDF links use" not in page and "All words must" not in page
    assert len(page.encode()) < 5000
    toc = client.get("/document/23.501")
    assert toc.status_code == 200 and "PDU session" in toc.text
    assert client.get("/document/99.999").status_code == 404


def test_search_reads_candidates_once_and_only_selected_text(client, monkeypatch):
    from contextlib import contextmanager

    statements = []
    original = module.database

    @contextmanager
    def traced():
        with original() as db:
            db.set_trace_callback(statements.append)
            yield db

    monkeypatch.setattr(module, "database", traced)
    r = client.get("/api/search?q=pdu-session&page_size=1")
    assert r.status_code == 200
    candidate_sql = [s for s in statements if "WITH driver AS" in s]
    assert len(candidate_sql) == 1
    text_sql = [s for s in statements if "JOIN section_text" in s]
    assert len(text_sql) == 1 and "IN (1)" in text_sql[0]


def test_rarest_word_drives_intersection(client, monkeypatch):
    from contextlib import contextmanager

    original = module.database
    statements = []

    @contextmanager
    def traced():
        with original() as db:
            db.set_trace_callback(statements.append)
            yield db

    monkeypatch.setattr(module, "database", traced)
    assert client.get("/api/search?q=pdu+one").json()["total"] == 1
    sql = next(s for s in statements if "WITH driver AS" in s)
    assert "WHERE term='one'" in sql


def test_table_of_contents_pagination(client):
    db = sqlite3.connect(module.DB_PATH)
    db.executemany(
        "INSERT INTO sections(id,document_id,number,title,page) VALUES(?,2,?,?,1)",
        [(100 + i, str(i), "GSM heading " + str(i)) for i in range(103)],
    )
    db.commit()
    db.close()
    first = client.get("/document/04.08").text
    second = client.get("/document/04.08?page=2").text
    assert "GSM heading 99" in first and "GSM heading 100" not in first
    assert "GSM heading 100" in second and "GSM heading 99" not in second
    assert "/document/04.08?page=2" in first


def test_specification_identifiers_and_title(client):
    for query in [
        "23.501",
        "TS 23.501",
        "3GPP TS 23.501",
        "23501",
        "ETSI TS 123 501",
        "123501",
    ]:
        result = client.get("/api/search", params={"q": query}).json()
        assert result["total"] == 1, query
        assert result["documents"][0]["spec"] == "23.501"
        assert result["documents"][0]["sections"] == []
    assert (
        client.get("/api/search?q=Architecture").json()["documents"][0]["match_reason"]
        == "Specification title"
    )
    assert client.get("/api/search?q=99.999").json()["total"] == 0
    assert client.get("/api/search?q=23.501+pdu").json()["matched_documents"] == 1
    assert len(client.get("/api/search?q=pdu").json()["documents"][0]["sections"]) == 1


def test_refinement_is_an_intersection_of_actual_section_results(client):
    # "one" matches only section 1; pdu elsewhere in the same spec must not leak.
    r = client.get("/api/search", params=[("q", "pdu"), ("within", "one")]).json()
    assert r["total"] == 1
    assert [d["sections"][0]["number"] for d in r["results"]] == ["1"]
    # A specification-number hit is a document scope, so its sections are allowed.
    r = client.get("/api/search", params=[("q", "pdu"), ("within", "23.501")]).json()
    assert r["total"] == 4
    # The HTML checkbox submits the previous query independently of the new one.
    r = client.get(
        "/api/search", params=[("q", "pdu"), ("parents", "one"), ("refine", "true")]
    ).json()
    assert r["total"] == 1 and r["within"] == ["one"]
    assert (
        client.get(
            "/api/search", params=[("q", "pdu")] + [("within", "one")] * 5
        ).status_code
        == 422
    )


def test_global_five_results_can_come_from_one_specification(client):
    db = sqlite3.connect(module.DB_PATH)
    for i in range(5, 11):
        title = "PDU session detail " + str(i)
        body = "PDU session starts."
        db.execute(
            "INSERT INTO sections VALUES(?,?,?,?,?)", (i, 1, str(i), title, i + 3)
        )
        db.execute("INSERT INTO section_text VALUES(?,?)", (i, body))
        a, b = (
            Counter(module.normalize(title).split()),
            Counter(module.normalize(body).split()),
        )
        db.executemany(
            "INSERT INTO postings VALUES(?,?,?,?,?)",
            [(t, i, 1, a[t], b[t]) for t in a.keys() | b.keys()],
        )
        db.execute(
            "INSERT INTO section_features VALUES(?,?,?,?)",
            (i, "content", sum(b.values()), sum(a.values())),
        )
    db.commit()
    db.close()
    first = client.get("/api/search?q=pdu+session").json()
    second = client.get("/api/search?q=pdu+session&page=2").json()
    assert first["page_size"] == 5 and first["total"] == 10 and first["pages"] == 2
    assert len(first["results"]) == len(second["results"]) == 5
    assert {d["spec"] for d in first["results"]} == {"23.501"}
    a = {d["sections"][0]["id"] for d in first["results"]}
    b = {d["sections"][0]["id"] for d in second["results"]}
    assert not a & b
    scores = [d["score"] for d in first["results"] + second["results"]]
    assert scores == sorted(scores, reverse=True)
    html = client.get("/?q=pdu+session").text
    assert "Previews per document" not in html and 'name="previews"' not in html
    assert "Results per page" in html and 'value="5" selected' in html
    assert "page_size=5" in html and "Search within results" in html


def test_safe_bold_highlighting_in_titles_and_snippets(client):
    db = sqlite3.connect(module.DB_PATH)
    db.execute(
        "UPDATE section_text SET body='<script>alert(1)</script> PDU session.' WHERE section_id=1"
    )
    db.commit()
    db.close()
    html = client.get("/?q=pdu+session").text
    assert "<strong>PDU</strong>" in html and "<strong>session</strong>" in html
    assert "<script>" not in html and "&lt;script&gt;" in html
    for d in client.get("/api/search?q=pdu+session").json()["results"]:
        s = d["sections"][0]
        assert any(p["match"] for p in s["preview_parts"])
    assert client.get("/api/search?page_size=101").status_code == 422
