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
        db.execute('INSERT INTO sections VALUES(?,?,?,?,?,?,?,?)',(i,1,str(i),title,body,page,module.normalize(title),module.normalize(body)))
    for row in db.execute('SELECT id,title_norm,body_norm FROM sections').fetchall():
        a,b=Counter(row[1].split()),Counter(row[2].split())
        db.executemany('INSERT INTO term_counts VALUES(?,?,?,?)',[(t,row[0],a[t],b[t]) for t in a.keys()|b.keys()])
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
