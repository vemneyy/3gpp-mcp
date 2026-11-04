import sqlite3
from collections import Counter
from pathlib import Path
import pytest
from fastapi.testclient import TestClient
from backend import app as module

@pytest.fixture
def client(tmp_path, monkeypatch):
    path=tmp_path/'test.db'
    db=sqlite3.connect(path)
    db.executescript((Path(__file__).parent/'schema.sql').read_text())
    db.execute("INSERT INTO documents VALUES(1,'23.501','3gpp','23','Architecture','19.0.0','https://www.etsi.org/deliver/test.pdf','hash')")
    db.execute("INSERT INTO documents VALUES(2,'04.08','gsm','04','GSM','8.0.0','https://www.etsi.org/deliver/gsm.pdf','hash')")
    for i,title,body,page in [(1,'PDU session','one mention',3),(2,'Body only','PDU session PDU session PDU session',4),(3,'Another','pdu session',5),(4,'<script>alert(1)</script>','pdu session',6)]:
        db.execute('INSERT INTO sections VALUES(?,?,?,?,?)',(i,1,str(i),title,page))
        db.execute('INSERT INTO section_text VALUES(?,?)',(i,body))
    for row in db.execute('SELECT s.id,s.title,t.body FROM sections s JOIN section_text t ON t.section_id=s.id').fetchall():
        a,b=Counter(module.normalize(row[1]).split()),Counter(module.normalize(row[2]).split())
        db.executemany('INSERT INTO postings VALUES(?,?,?,?,?)',[(t,row[0],1,a[t],b[t]) for t in a.keys()|b.keys()])
    db.commit();db.close()
    monkeypatch.setattr(module,'DB_PATH',str(path))
    return TestClient(module.app)

def test_normalization_and_ranking(client):
    a=client.get('/api/search',params={'q':'PDU-session','previews':2}).json()
    b=client.get('/api/search',params={'q':'ＰＤＵ / session','previews':2}).json()
    assert a['documents'][0]['sections']==b['documents'][0]['sections']
    doc=a['documents'][0]
    assert doc['matching_sections']==4
    assert [s['number'] for s in doc['sections']]==['1','2']
    assert doc['sections'][0]['url'].endswith('#page=4')

def test_filters_html_and_validation(client):
    assert client.get('/api/search',params={'q':'pdu','family':'gsm'}).json()['total']==0
    text=client.get('/',params={'q':'pdu','previews':50}).text
    assert '<script>' not in text and '&lt;script&gt;' in text
    assert client.get('/api/search?previews=0').status_code==422
    assert client.get('/api/search?q=%22%20OR%20*').status_code==200
    assert client.get('/health').status_code==200
    assert client.get('/index.html').status_code==200

def test_read_only_and_missing_index(client,tmp_path,monkeypatch):
    monkeypatch.setattr(module,'DB_PATH',str(tmp_path/'missing.db'))
    assert client.get('/api/search').status_code==503
    assert client.get('/').status_code==503
    assert not (tmp_path/'missing.db').exists()

def test_schema_is_shared():
    root=Path(__file__).parent.parent
    assert (root/'backend/schema.sql').read_bytes()==(root/'indexer/schema.sql').read_bytes()


def test_every_section_can_be_reached(client):
    first=client.get('/api/search',params={'q':'pdu','previews':1}).json()['documents'][0]
    assert first['sections'][0]['number']=='1'
    assert 'section_page=2' in first['next_sections_url']
    second=client.get('/api/search',params={'q':'pdu','previews':1,'spec':'23.501','section_page':2}).json()['documents'][0]
    assert second['sections'][0]['number']=='2'
    assert second['previous_sections_url']


def test_initial_page_is_light_and_toc_is_separate(client):
    result=client.get('/api/search').json()
    assert all(not d['sections'] for d in result['documents'])
    page=client.get('/').text
    assert 'name="family"' not in page and 'name="series"' not in page
    assert 'PDF links use' not in page and 'All words must' not in page
    assert len(page.encode())<5000
    toc=client.get('/document/23.501')
    assert toc.status_code==200 and 'PDU session' in toc.text
    assert client.get('/document/99.999').status_code==404


def test_search_reads_candidates_once_and_only_selected_text(client,monkeypatch):
    from contextlib import contextmanager
    statements=[]
    original=module.database
    @contextmanager
    def traced():
        with original() as db:
            db.set_trace_callback(statements.append)
            yield db
    monkeypatch.setattr(module,'database',traced)
    r=client.get('/api/search?q=pdu-session&previews=1')
    assert r.status_code==200
    candidate_sql=[s for s in statements if 'FROM postings p0' in s]
    assert len(candidate_sql)==1
    text_sql=[s for s in statements if 'JOIN section_text' in s]
    assert len(text_sql)==1 and 'IN (1)' in text_sql[0]


def test_rarest_word_drives_intersection(client,monkeypatch):
    from contextlib import contextmanager
    original=module.database
    statements=[]
    @contextmanager
    def traced():
        with original() as db:
            db.set_trace_callback(statements.append)
            yield db
    monkeypatch.setattr(module,'database',traced)
    assert client.get('/api/search?q=pdu+one').json()['total']==1
    sql=next(s for s in statements if 'FROM postings p0' in s)
    assert "WHERE p0.term='one'" in sql


def test_table_of_contents_pagination(client):
    db=sqlite3.connect(module.DB_PATH)
    db.executemany('INSERT INTO sections(id,document_id,number,title,page) VALUES(?,2,?,?,1)',[(100+i,str(i),'GSM heading '+str(i)) for i in range(103)])
    db.commit();db.close()
    first=client.get('/document/04.08').text
    second=client.get('/document/04.08?page=2').text
    assert 'GSM heading 99' in first and 'GSM heading 100' not in first
    assert 'GSM heading 100' in second and 'GSM heading 99' not in second
    assert '/document/04.08?page=2' in first
