"""Read-only server-rendered explorer; metadata, text and postings are separate."""
import os
import re
import sqlite3
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
from backend.text import normalize
from backend.search_engine import retrieve, glossary_search, FORMS, TECH, TECH_NAMES, STOP

DOC_COLUMNS = 'id,spec,family,series,title,version,url'


@contextmanager
def database():
    db = sqlite3.connect(Path(DB_PATH).resolve().as_uri() + '?mode=ro', uri=True, timeout=5)
    db.row_factory = sqlite3.Row
    try:
        db.execute('PRAGMA query_only=ON')
        db.execute('BEGIN')
        if db.execute('PRAGMA user_version').fetchone()[0] != 3:
            raise sqlite3.DatabaseError('Run etsi-indexer --db <path> --migrate to upgrade the index.')
        yield db
    finally:
        db.close()


def preview(body, terms):
    # Word boundaries prevent an attach query from centering on "detach".
    expanded=set()
    for word in terms:
        expanded.update(FORMS.get(word,(word,)))
        technology=TECH_NAMES.get(word,word)
        if technology in TECH:expanded.update(TECH[technology])
    if not expanded:return body[:360].strip()
    pattern=re.compile(r'(?<![^\W_])(?:'+'|'.join(re.escape(t) for t in sorted(expanded,key=len,reverse=True))+r')(?![^\W_])',re.I)
    matches=list(pattern.finditer(body))
    if not matches:return body[:360].strip()
    # Choose the smallest window covering as many distinct query words as fit;
    # walk linearly rather than rereading a long section for every occurrence.
    best_start,best_coverage,best_span=matches[0].start(),-1,10**9
    left=0;counts={}
    for right,match in enumerate(matches):
        word=match.group().lower();counts[word]=counts.get(word,0)+1
        while match.end()-matches[left].start()>260 and left<right:
            old=matches[left].group().lower();counts[old]-=1
            if not counts[old]:del counts[old]
            left+=1
        span=match.end()-matches[left].start()
        if len(counts)>best_coverage or len(counts)==best_coverage and span<best_span:
            best_start,best_coverage,best_span=matches[left].start(),len(counts),span
    start=max(0,best_start-70)
    return ('…' if start else '')+body[start:start+360].strip()+('…' if start+360<len(body) else '')


def lookup(q='', family='', series='', previews=1, page=1, spec='', section_page=1, toc=False):
    terms = list(dict.fromkeys(normalize(q).split()))
    if len(terms) > 16:
        raise ValueError('Use at most 16 search words.')
    conditions, args = [], []
    for field, value in [('family', family), ('series', series), ('spec', spec)]:
        if value:
            conditions.append(f'd.{field}=?')
            args.append(value)
    filters = ' AND '.join(conditions)
    with database() as db:
        if not terms:
            where = filters or '1'
            total = db.execute(f'SELECT count(*) FROM documents d WHERE {where}', args).fetchone()[0]
            docs = [dict(r) for r in db.execute(f'SELECT {DOC_COLUMNS} FROM documents d WHERE {where} ORDER BY spec LIMIT 25 OFFSET ?', [*args, (page-1)*25])]
            for doc in docs:
                doc['sections'] = []
                if toc:
                    doc['total_sections'] = db.execute('SELECT count(*) FROM sections WHERE document_id=?', [doc['id']]).fetchone()[0]
                    doc['sections'] = [dict(r) for r in db.execute('SELECT number,title,page FROM sections WHERE document_id=? ORDER BY id LIMIT 100 OFFSET ?', [doc['id'], (section_page-1)*100])]
                    doc['toc_previous'] = f'/document/{doc["spec"]}?page={section_page-1}' if section_page>1 else None
                    doc['toc_next'] = f'/document/{doc["spec"]}?page={section_page+1}' if section_page*100<doc['total_sections'] else None
        else:
            docs,total=retrieve(db,q,filters,args,previews,page,section_page,preview)
        glossary_entries,glossary_total=glossary_search(db,q,limit=5) if terms and not set(terms).issubset(STOP) and not re.search(r'\b\d{2}\.\d{2,3}\b|\b\d{5,6}\b',q) else ([],0)
        for doc in docs:
            doc['toc_url'] = '/document/' + doc['spec']
            params = dict(q=q, family=family, series=series, previews=previews, spec=doc['spec'])
            doc['next_sections_url'] = '/?' + urlencode(dict(params, section_page=section_page+1)) if doc.get('matching_sections',0)>section_page*previews else None
            doc['previous_sections_url'] = '/?' + urlencode(dict(params, section_page=section_page-1)) if terms and section_page>1 else None
            for section in doc['sections']:
                section['url'] = doc['url'] + '#page=' + str(section['page']+1)
        return dict(documents=docs, total=total, catalog=[], glossary_entries=glossary_entries, glossary_total=glossary_total, glossary_mode=False, q=q, family=family, series=series, previews=previews, page=page, spec=spec, section_page=section_page, toc=toc, pages=(total+24)//25)


@app.get('/styles.css', include_in_schema=False)
def styles():
    return FileResponse(ROOT / 'frontend/styles.css', media_type='text/css')


@app.get('/health')
def health():
    try:
        with database() as db:
            db.execute('SELECT id FROM documents LIMIT 1').fetchone()
        return {'status': 'ok', 'schema': 3}
    except sqlite3.Error:
        return JSONResponse({'status':'index unavailable; create or migrate the database'}, status_code=503)


def render(request, result):
    result['page_url'] = lambda n: '/?' + urlencode(dict(q=result['q'], family=result['family'], series=result['series'], previews=result['previews'], page=n, spec=result['spec'], section_page=result['section_page']))
    return templates.TemplateResponse(request=request, name='index.html', context=result)


@app.get('/glossary', response_class=HTMLResponse)
@app.get('/api/glossary')
def glossary(request: Request, q: str = Query('',max_length=256), page: int = Query(1,ge=1,le=100000)):
    try:
        with database() as db:
            entries,total=glossary_search(db,q,page)
        if request.url.path.startswith('/api/'):
            return dict(entries=entries,total=total,page=page,pages=(total+49)//50)
        result=dict(documents=[],glossary_entries=entries,glossary_total=total,glossary_mode=True,q=q,total=total,
                    page=page,pages=(total+49)//50,previews=1,spec='',family='',series='',section_page=1,toc=False)
        result['page_url']=lambda n:'/glossary?'+urlencode(dict(q=q,page=n))
        return templates.TemplateResponse(request=request,name='index.html',context=result)
    except ValueError as exc:
        return JSONResponse({'detail':str(exc)},status_code=422)
    except sqlite3.Error:
        return JSONResponse({'detail':'Glossary unavailable; run indexer --migrate.'},status_code=503)


@app.get('/document/{spec}', response_class=HTMLResponse)
def document(request: Request, spec: str, page: int = Query(1, ge=1, le=100000)):
    try:
        result = lookup(spec=spec, toc=True, section_page=page)
        if not result['documents']:
            return HTMLResponse('Document not found', status_code=404)
        return render(request, result)
    except sqlite3.Error:
        return JSONResponse({'detail':'Index unavailable; run indexer --migrate.'}, status_code=503)


@app.get('/', response_class=HTMLResponse)
@app.get('/index.html', response_class=HTMLResponse)
@app.get('/api/search')
def search(request: Request, q: str = Query('', max_length=256), family: str = Query('', pattern='^(|3gpp|gsm)$'), series: str = Query('', pattern=r'^(|[0-9]{2})$'), previews: int = Query(1, ge=1, le=50), page: int = Query(1, ge=1, le=100000), spec: str = Query('', max_length=32, pattern=r'^(|[0-9]{2}\.[0-9]{2,3}(-[0-9]+)?)$'), section_page: int = Query(1, ge=1, le=100000)):
    try:
        result = lookup(q, family, series, previews, page, spec, section_page)
    except ValueError as exc:
        return JSONResponse({'detail': str(exc)}, status_code=422)
    except sqlite3.Error:
        if request.url.path.startswith('/api/'):
            return JSONResponse({'detail':'Index unavailable; create or migrate the database with indexer --migrate.'}, status_code=503)
        return templates.TemplateResponse(request=request, name='index.html', context={'error':'Index unavailable. Create or migrate the database with indexer --migrate.'}, status_code=503)
    return result if request.url.path.startswith('/api/') else render(request, result)
