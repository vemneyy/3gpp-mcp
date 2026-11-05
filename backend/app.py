"""Server-rendered search. Globally ranked hits, explicit evidence, no JS."""

import os
import sqlite3
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import urlencode

from fastapi import FastAPI, Query, Request
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse
from fastapi.templating import Jinja2Templates

from backend.text import normalize
from backend.highlight import Plan
from backend.search_engine import (
    retrieve,
    glossary_search,
    FORMS,
    TECH,
    TECH_NAMES,
    STOP,
)

ROOT = Path(__file__).resolve().parent.parent
DB_PATH = os.environ.get("EXPLORER_DB", str(ROOT / "data/explorer.db"))
app = FastAPI(title="ETSI 3GPP Explorer")
templates = Jinja2Templates(directory=str(ROOT / "frontend"))
DOC_COLUMNS = "id,spec,family,series,title,version,url"


@contextmanager
def database():
    db = sqlite3.connect(
        Path(DB_PATH).resolve().as_uri() + "?mode=ro", uri=True, timeout=5
    )
    db.row_factory = sqlite3.Row
    try:
        db.execute("PRAGMA query_only=ON")
        db.execute("BEGIN")
        if db.execute("PRAGMA user_version").fetchone()[0] != 3:
            raise sqlite3.DatabaseError("Run indexer --migrate to upgrade the index.")
        yield db
    finally:
        db.close()


def lookup(
    q="",
    family="",
    series="",
    page_size=5,
    page=1,
    spec="",
    toc=False,
    toc_page=1,
    within=(),
):
    if len(normalize(q).split()) > 16:
        raise ValueError("Use at most 16 search words.")
    if len(within) > 4:
        raise ValueError("At most four refinement steps are supported.")
    conditions, args = [], []
    for field, value in [("family", family), ("series", series), ("spec", spec)]:
        if value:
            conditions.append(f"d.{field}=?")
            args.append(value)
    filters = " AND ".join(conditions)
    with database() as db:
        if normalize(q):
            docs, total, matched_documents = retrieve(
                db, q, filters, args, page_size, page, within
            )
        elif within:
            raise ValueError("Enter a term to search within these results.")
        else:
            where = filters or "1"
            total = db.execute(
                f"SELECT count(*) FROM documents d WHERE {where}", args
            ).fetchone()[0]
            matched_documents = total
            docs = [
                dict(r)
                for r in db.execute(
                    f"SELECT {DOC_COLUMNS} FROM documents d WHERE {where} ORDER BY spec LIMIT ? OFFSET ?",
                    [*args, page_size, (page - 1) * page_size],
                )
            ]
            for doc in docs:
                doc.update(
                    sections=[],
                    title_parts=[dict(text=doc["title"], match=False)],
                    spec_parts=[dict(text=doc["spec"], match=False)],
                )
                if toc:
                    doc["total_sections"] = db.execute(
                        "SELECT count(*) FROM sections WHERE document_id=?", [doc["id"]]
                    ).fetchone()[0]
                    doc["sections"] = [
                        dict(r)
                        for r in db.execute(
                            "SELECT number,title,page FROM sections WHERE document_id=? ORDER BY id LIMIT 100 OFFSET ?",
                            [doc["id"], (toc_page - 1) * 100],
                        )
                    ]
                    doc["toc_previous"] = (
                        f"/document/{doc['spec']}?page={toc_page - 1}"
                        if toc_page > 1
                        else None
                    )
                    doc["toc_next"] = (
                        f"/document/{doc['spec']}?page={toc_page + 1}"
                        if toc_page * 100 < doc["total_sections"]
                        else None
                    )
        for doc in docs:
            doc["toc_url"] = "/document/" + doc["spec"]
            doc["result_url"] = doc["url"]
            for section in doc["sections"]:
                section["url"] = doc["url"] + "#page=" + str(section["page"] + 1)
                doc["result_url"] = section["url"]
    pages = (total + page_size - 1) // page_size
    return dict(
        documents=docs,
        results=docs,
        total=total,
        matched_documents=matched_documents,
        q=q,
        family=family,
        series=series,
        spec=spec,
        toc=toc,
        page_size=page_size,
        page=page,
        pages=pages,
        within=list(within),
        glossary_entries=[],
        glossary_mode=False,
        first=(page - 1) * page_size + 1 if docs else 0,
        last=(page - 1) * page_size + len(docs) if docs else 0,
        page_numbers=list(range(max(1, page - 3), min(pages, page + 3) + 1)),
    )


def render(request, result):
    result["page_url"] = lambda n: (
        "/?"
        + urlencode(
            dict(
                q=result["q"],
                family=result["family"],
                series=result["series"],
                page_size=result["page_size"],
                page=n,
                spec=result["spec"],
                within=result["within"],
            ),
            doseq=True,
        )
    )
    return templates.TemplateResponse(
        request=request, name="index.html", context=result
    )


@app.get("/styles.css", include_in_schema=False)
def styles():
    return FileResponse(ROOT / "frontend/styles.css", media_type="text/css")


@app.get("/health")
def health():
    try:
        with database() as db:
            db.execute("SELECT id FROM documents LIMIT 1").fetchone()
        return {"status": "ok", "schema": 3}
    except sqlite3.Error:
        return JSONResponse(
            {"status": "index unavailable; create or migrate the database"},
            status_code=503,
        )


@app.get("/glossary", response_class=HTMLResponse)
@app.get("/api/glossary")
def glossary(
    request: Request,
    q: str = Query("", max_length=256),
    page: int = Query(1, ge=1, le=100000),
    page_size: int = Query(5, ge=1, le=100),
):
    try:
        with database() as db:
            entries, total = glossary_search(db, q, page, page_size)
        plan = Plan(q, FORMS, TECH, TECH_NAMES, STOP)
        for entry in entries:
            entry["term_parts"] = plan.parts(entry["term"])
            entry["definition_parts"] = plan.parts(entry["definition"])
        if request.url.path.startswith("/api/"):
            return dict(
                entries=entries,
                total=total,
                page=page,
                page_size=page_size,
                pages=(total + page_size - 1) // page_size,
            )
        pages = (total + page_size - 1) // page_size
        result = dict(
            documents=[],
            glossary_entries=entries,
            glossary_mode=True,
            q=q,
            total=total,
            page=page,
            pages=pages,
            page_size=page_size,
            spec="",
            within=[],
            toc=False,
            first=(page - 1) * page_size + 1 if entries else 0,
            last=(page - 1) * page_size + len(entries) if entries else 0,
            page_numbers=list(range(max(1, page - 3), min(pages, page + 3) + 1)),
        )
        result["page_url"] = lambda n: (
            "/glossary?" + urlencode(dict(q=q, page=n, page_size=page_size))
        )
        return templates.TemplateResponse(
            request=request, name="index.html", context=result
        )
    except ValueError as exc:
        return JSONResponse({"detail": str(exc)}, status_code=422)
    except sqlite3.Error:
        return JSONResponse(
            {"detail": "Glossary unavailable; run indexer --migrate."}, status_code=503
        )


@app.get("/document/{spec}", response_class=HTMLResponse)
def document(request: Request, spec: str, page: int = Query(1, ge=1, le=100000)):
    try:
        result = lookup(spec=spec, toc=True, toc_page=page)
        if not result["documents"]:
            return HTMLResponse("Document not found", status_code=404)
        return render(request, result)
    except sqlite3.Error:
        return JSONResponse(
            {"detail": "Index unavailable; run indexer --migrate."}, status_code=503
        )


@app.get("/", response_class=HTMLResponse)
@app.get("/index.html", response_class=HTMLResponse)
@app.get("/api/search")
def search(
    request: Request,
    q: str = Query("", max_length=256),
    family: str = Query("", pattern="^(|3gpp|gsm)$"),
    series: str = Query("", pattern=r"^(|[0-9]{2})$"),
    page_size: int = Query(5, ge=1, le=100),
    page: int = Query(1, ge=1, le=100000),
    spec: str = Query(
        "", max_length=32, pattern=r"^(|[0-9]{2}\.[0-9]{2,3}(-[0-9]+)?)$"
    ),
    within: list[str] = Query(default=[], max_length=4),
    parents: list[str] = Query(default=[], max_length=5),
    refine: bool = False,
):
    try:
        scope = parents if refine else within
        if any(len(term) > 256 for term in scope):
            raise ValueError("A refinement query may contain at most 256 characters.")
        result = lookup(q, family, series, page_size, page, spec, within=scope)
    except ValueError as exc:
        return JSONResponse({"detail": str(exc)}, status_code=422)
    except sqlite3.Error:
        if request.url.path.startswith("/api/"):
            return JSONResponse(
                {
                    "detail": "Index unavailable; create or migrate the database with indexer --migrate."
                },
                status_code=503,
            )
        return templates.TemplateResponse(
            request=request,
            name="index.html",
            context={
                "error": "Index unavailable. Create or migrate the database with indexer --migrate."
            },
            status_code=503,
        )
    return result if request.url.path.startswith("/api/") else render(request, result)
