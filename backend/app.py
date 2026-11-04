"""Read-only, server-rendered ETSI explorer. No script is shipped to the browser."""
import os
import re
import sqlite3
import unicodedata
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import urlencode

from fastapi import FastAPI, Query, Request
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse
from fastapi.templating import Jinja2Templates

ROOT = Path(__file__).resolve().parent.parent
DB_PATH = os.environ.get('EXPLORER_DB', str(ROOT / 'data/explorer.db'))
app = FastAPI(title='ETSI 3GPP Explorer')
templates = Jinja2Templates(directory=str(ROOT / 'frontend'))


def normalize(text):
    # Shared contract with Go: NFKD, remove combining marks, lowercase letters,
    # retain numbers; every other character becomes a word boundary.
    text = unicodedata.normalize('NFKD', text)
    return ' '.join(''.join(c.lower() if c.isalpha() or c.isdecimal() else
                           '' if unicodedata.category(c).startswith('M') else ' '
                           for c in text).split())


@contextmanager
def database():
    db = sqlite3.connect(Path(DB_PATH).resolve().as_uri() + '?mode=ro', uri=True, timeout=5)
    db.row_factory = sqlite3.Row
    db.execute('PRAGMA query_only=ON')
    db.execute('BEGIN')  # One consistent WAL snapshot for all queries in this response.
    try:
        yield db
    finally:
        db.close()


def preview(body, terms):
    # Bound work and response size. Show original text around the first match.
    pattern = re.compile('|'.join(re.escape(t) for t in terms), re.I)
    match = pattern.search(body) if terms else None
    start = max(0, match.start() - 100) if match else 0
    return ('…' if start else '') + body[start:start + 360].strip() + ('…' if start + 360 < len(body) else '')


def lookup(q='', family='', series='', previews=3, page=1, spec='', section_page=1):
    terms = list(dict.fromkeys(normalize(q).split()))
    if len(terms) > 16:
        raise ValueError('Use at most 16 search words.')
    filters, args = [], []
    for field, value in [('family', family), ('series', series), ('spec', spec)]:
        if value:
            filters.append(f'd.{field}=?')
            args.append(value)
    where = ' AND '.join(filters) or '1'
    with database() as db:
        catalog = [dict(r) for r in db.execute('SELECT family,series,count(*) AS count FROM documents GROUP BY family,series ORDER BY family,series')]
        if not terms:
            total = db.execute(f'SELECT count(*) FROM documents d WHERE {where}', args).fetchone()[0]
            docs = [dict(r) for r in db.execute(f'SELECT d.* FROM documents d WHERE {where} ORDER BY spec LIMIT 25 OFFSET ?', [*args, (page-1)*25])]
            for doc in docs:
                doc['sections'] = [dict(r) for r in db.execute('SELECT number,title,page FROM sections WHERE document_id=? ORDER BY id', [doc['id']])]
        else:
            # Never accept FTS syntax from the user. All words must match the same section.
            expression = ' AND '.join('"' + t + '"' for t in terms)
            placeholders = ','.join('?' for _ in terms)
            cte = f'''WITH counts AS MATERIALIZED (
              SELECT section_id AS doc,sum(title_hits) AS title_hits,sum(body_hits) AS body_hits
              FROM term_counts WHERE term IN ({placeholders}) GROUP BY section_id), hits AS MATERIALIZED (
              SELECT s.id,s.document_id,c.title_hits,c.body_hits
              FROM search JOIN sections s ON s.id=search.rowid
              JOIN counts c ON c.doc=s.id JOIN documents d ON d.id=s.document_id
              WHERE search MATCH ? AND {where}), ranked AS (
              SELECT *,row_number() OVER(PARTITION BY document_id ORDER BY (title_hits>0) DESC,body_hits DESC,title_hits DESC,id) AS rank
              FROM hits)'''
            params = [*terms, expression, *args]
            total = db.execute(cte + ' SELECT count(DISTINCT document_id) FROM hits', params).fetchone()[0]
            docs = [dict(r) for r in db.execute(cte + ''' SELECT d.*,r.title_hits,r.body_hits,
              (SELECT count(*) FROM hits h WHERE h.document_id=d.id) AS matching_sections
              FROM ranked r JOIN documents d ON d.id=r.document_id WHERE rank=1
              ORDER BY (r.title_hits>0) DESC,r.body_hits DESC,r.title_hits DESC,d.spec LIMIT 25 OFFSET ?''', [*params,(page-1)*25])]
            ids = [d['id'] for d in docs]
            sections = {}
            if ids:
                rows = db.execute(cte + ' SELECT s.*,r.title_hits,r.body_hits,r.rank FROM ranked r JOIN sections s ON s.id=r.id WHERE r.rank>? AND r.rank<=? AND r.document_id IN (' + ','.join('?' for _ in ids) + ') ORDER BY r.document_id,r.rank', [*params,(section_page-1)*previews,section_page*previews,*ids])
                for row in rows:
                    section = dict(row)
                    section['preview'] = preview(section.pop('body'), terms)
                    section.pop('body_norm'); section.pop('title_norm')
                    sections.setdefault(section['document_id'], []).append(section)
            for doc in docs:
                doc['sections'] = sections.get(doc['id'], [])
        for doc in docs:
            doc.pop('sha256', None)
            doc['next_sections_url'] = '/?' + urlencode(dict(q=q, family=family, series=series, previews=previews, spec=doc['spec'], section_page=section_page+1)) if doc.get('matching_sections',0)>section_page*previews else None
            doc['previous_sections_url'] = '/?' + urlencode(dict(q=q, family=family, series=series, previews=previews, spec=doc['spec'], section_page=section_page-1)) if section_page>1 else None
            for s in doc['sections']:
                s['url'] = doc['url'] + '#page=' + str(s['page'] + 1)
        return dict(documents=docs, total=total, catalog=catalog, q=q, family=family, series=series, previews=previews, page=page, spec=spec, section_page=section_page, pages=(total+24)//25)


@app.get('/styles.css', include_in_schema=False)
def styles():
    return FileResponse(ROOT / 'frontend/styles.css', media_type='text/css')


@app.get('/health')
def health():
    try:
        with database() as db:
            db.execute('SELECT id FROM documents LIMIT 1').fetchone()
        return {'status': 'ok'}
    except sqlite3.Error:
        return JSONResponse({'status':'index unavailable'}, status_code=503)


@app.get('/', response_class=HTMLResponse)
@app.get('/index.html', response_class=HTMLResponse)
@app.get('/api/search')
def search(request: Request, q: str = Query('', max_length=256), family: str = Query('', pattern='^(|3gpp|gsm)$'), series: str = Query('', pattern=r'^(|[0-9]{2})$'), previews: int = Query(3, ge=1, le=50), page: int = Query(1, ge=1, le=100000), spec: str = Query('', max_length=32, pattern=r'^(|[0-9]{2}\.[0-9]{2,3}(-[0-9]+)?)$'), section_page: int = Query(1, ge=1, le=100000)):
    try:
        result = lookup(q, family, series, previews, page, spec, section_page)
    except ValueError as exc:
        return JSONResponse({'detail': str(exc)}, status_code=422)
    except sqlite3.Error:
        if request.url.path.startswith('/api/'):
            return JSONResponse({'detail':'Index unavailable; run the indexer.'}, status_code=503)
        return templates.TemplateResponse(request=request, name='index.html', context={'error':'Index unavailable. Run the indexer first.'}, status_code=503)
    if request.url.path.startswith('/api/'):
        return result
    result['page_url'] = lambda n: '/?' + urlencode(dict(q=q, family=family, series=series, previews=previews, page=n, spec=spec, section_page=section_page))
    return templates.TemplateResponse(request=request, name='index.html', context=result)
