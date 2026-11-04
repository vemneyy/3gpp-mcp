"""Field-aware lexical retrieval. No LLM-generated definitions or fixed spec boosts."""
import heapq
import re
from urllib.parse import urlencode

from backend.text import normalize

STOP = {'the', 'a', 'an', 'of', 'for', 'and', 'in', 'on', 'to', 'with'}
TECH = {
    '4g': ('4g', 'lte', 'eps', 'eutran'),
    '5g': ('5g', '5gs', '5gc', 'nr'),
    '3g': ('3g', 'umts', 'utran'),
    '2g': ('2g', 'gsm', 'geran'),
}
TECH_NAMES = {'lte': '4g', 'eps': '4g', 'umts': '3g', 'gsm': '2g'}
FORMS = {'attach': ('attach', 'attached', 'attaches', 'attaching'),
         'authenticate': ('authenticate', 'authentication'),
         'register': ('register', 'registration')}
for _forms in list(FORMS.values()):
    for _word in _forms:FORMS[_word]=_forms
EXCEPTIONS = {'reject', 'rejected', 'failure', 'failed', 'not', 'detach', 'detachment'}


def marks(items):
    return ','.join('?' for _ in items)


def coverage(groups, title):
    words = set(title.split())
    return sum(bool(words.intersection(g)) for g in groups)


def technology_in(title, tech):
    if not tech:
        return True
    return bool(set(title.split()).intersection(TECH[tech])) or (tech == '4g' and 'e utran' in title)


def split_query(db, q):
    # Number selectors apply to the actual specification, never to citations.
    number = re.match(r'^\s*(?:(?:3gpp|etsi)\s*)?(?:(?:ts|tr)\s*)?(\d{2}\.\d{2,3}(?:-\d+)?|\d{3}\s+\d{3}|\d{5,6}|0\d{3})(?=\s|$)\s*(.*)$', q, re.I)
    ids = None
    rest = q
    if number:
        compact = re.sub(r'[.\s]', '', number[1])
        ids = [r[0] for r in db.execute('SELECT document_id FROM document_search WHERE spec_compact=? OR etsi_number=?', [compact, compact])]
        rest = number[2]
    words = list(dict.fromkeys(normalize(rest).split()))
    if len(words) > 16:
        raise ValueError('Use at most 16 search words.')
    tech = next((TECH_NAMES.get(w, w) for w in words if w in TECH or w in TECH_NAMES), None)
    meaningful = [w for w in words if w not in STOP and w not in TECH and w not in TECH_NAMES]
    if not meaningful and not tech:
        meaningful = words if any(w not in STOP for w in words) else []
    groups = list(dict.fromkeys(FORMS.get(w, (w,)) for w in meaningful))
    if not groups and tech:
        groups = [TECH[tech]]
    return ids, groups, tech, words, normalize(rest)


def all_terms_intersect(groups, terms):
    return any(set(group).intersection(terms) for group in groups)


def section_candidates(db, groups, conditions, args, tech):
    if not groups:
        return ()
    terms = set(t for group in groups for t in group)
    stats = dict(db.execute('SELECT term,section_count FROM term_stats WHERE term IN (' + marks(terms) + ')', list(terms)))
    ordered = sorted(groups, key=lambda group: sum(stats.get(t, 0) for t in group))
    if any(not any(t in stats for t in group) for group in ordered):
        return ()
    driver = [t for t in ordered[0] if t in stats]
    if len(driver) == 1:
        first = 'SELECT section_id,document_id,title_hits,body_hits FROM postings WHERE term=?'
    else:
        first = 'SELECT section_id,document_id,sum(title_hits) title_hits,sum(body_hits) body_hits FROM postings WHERE term IN (' + marks(driver) + ') GROUP BY section_id'
    params = list(driver)
    # Only body frequency is needed for length-normalized scoring. Full title
    # coverage is calculated from the small section heading, not aggregated TF.
    frequency = 'p0.body_hits'
    for group in ordered[1:]:
        frequency += '+coalesce((SELECT sum(p.body_hits) FROM postings p WHERE p.section_id=p0.section_id AND p.term IN (' + marks(group) + ')),0)'
        params.extend(group)
    tech_sql = '0'
    if tech:
        aliases = TECH[tech]
        tech_sql = 'EXISTS(SELECT 1 FROM postings z WHERE z.section_id=p0.section_id AND z.term IN (' + marks(aliases) + '))'
        params.extend(aliases)
    kinds="'content','history'" if all_terms_intersect(groups, {'history','changelog'}) else "'content'"
    sql = f'''WITH driver AS ({first}) SELECT p0.section_id,p0.document_id,
      {frequency} AS body_hits,s.title,s.number,f.kind,f.body_length,f.title_length,{tech_sql} AS body_tech
      FROM driver p0 CROSS JOIN section_features f ON f.section_id=p0.section_id
      CROSS JOIN sections s ON s.id=p0.section_id WHERE f.kind IN ({kinds}) '''
    for group in ordered[1:]:
        sql += ' AND EXISTS(SELECT 1 FROM postings p WHERE p.section_id=p0.section_id AND p.term IN (' + marks(group) + '))'
        params.extend(group)
    if conditions:
        sql += ' AND p0.document_id IN (SELECT d.id FROM documents d WHERE ' + conditions + ')'
        params.extend(args)
    return db.execute(sql, params)


def glossary_search(db, q='', page=1, limit=50):
    terms = list(dict.fromkeys(normalize(q).split()))
    if len(terms) > 16:
        raise ValueError('Use at most 16 search words.')
    params = []
    condition = ''
    if terms:
        condition = ' WHERE g.id IN (SELECT p0.glossary_id FROM glossary_postings p0 '
        for i in range(1, len(terms)):
            condition += f'CROSS JOIN glossary_postings p{i} ON p{i}.glossary_id=p0.glossary_id AND p{i}.term=? '
            params.append(terms[i])
        condition += 'WHERE p0.term=?)'
        params.append(terms[0])
    total = db.execute('SELECT count(*) FROM glossary g' + condition, params).fetchone()[0]
    rows = db.execute('''SELECT g.term,g.definition,g.kind,d.spec,d.url,s.number,s.page
      FROM glossary g JOIN documents d ON d.id=g.document_id JOIN sections s ON s.id=g.section_id''' + condition +
      ' ORDER BY (g.term_norm=?) DESC,g.term_norm,d.spec LIMIT ? OFFSET ?', [*params, normalize(q), limit, (page-1)*limit])
    entries = [dict(r) for r in rows]
    for entry in entries:
        entry['url'] += '#page=' + str(entry['page']+1)
    return entries, total


def retrieve(db, q, filters, args, previews, page, section_page, preview_fn):
    number_ids, groups, tech, words, phrase = split_query(db, q)
    if number_ids is not None:
        if not number_ids:
            return [], 0
        selector = 'd.id IN (' + marks(number_ids) + ')'
        filters = filters + ' AND ' + selector if filters else selector
        args = [*args, *number_ids]
    # Metadata is small and separate from all section text. Fetch only documents
    # eligible for this request; a dictionary also avoids per-section SQL calls.
    where = filters or '1'
    metadata = {r['id']: dict(r) for r in db.execute('''SELECT d.id,d.spec,d.family,d.series,d.title,d.version,d.url,e.title_norm
      FROM documents d JOIN document_search e ON e.document_id=d.id WHERE ''' + where, args)}
    if number_ids is not None and not groups:
        docs = sorted(metadata.values(), key=lambda d: d['spec'])
        for d in docs:
            d.update(sections=[], match_reason='Specification number')
            d.pop('title_norm', None)
        return docs[(page-1)*25:page*25], len(docs)
    if not groups:
        return [], 0
    all_terms = set(t for group in groups for t in group)
    title_ids = {r[0] for r in db.execute('SELECT DISTINCT document_id FROM document_postings WHERE term IN (' + marks(all_terms) + ')', list(all_terms))}
    buckets = {}
    for did in title_ids.intersection(metadata):
        doc = metadata[did]
        cov = coverage(groups, doc['title_norm'])
        if cov == len(groups) and technology_in(doc['title_norm'], tech):
            phrase_bonus = 15 if phrase and (' '+phrase+' ') in (' '+doc['title_norm']+' ') else 0
            buckets[did] = {'count': 0, 'heap': [], 'best': 110+phrase_bonus, 'metadata': True}
    keep = section_page * previews
    title_max = len(groups)
    for row in section_candidates(db, groups, filters, args, tech):
        sid,did,tf,title,number,kind,length,title_length,body_tech = row
        if did not in metadata:
            continue
        doc = metadata[did]
        tech_title = technology_in(doc['title_norm'], tech)
        if tech and not tech_title and not body_tech:
            continue
        heading = normalize(title)
        cov = coverage(groups, heading)
        # Saturating frequency and length normalization prevent long appendices
        # from winning just because a token appears hundreds of times.
        bm25 = tf / (tf + 1.2*(0.25+0.75*max(length, 1)/300)) if tf else 0
        score = 8*bm25 + 30*cov/title_max + (30 if cov == title_max else 0)
        if cov:
            score += 8 / max(title_length, 1)
            score -= min(number.count('.'), 6)
        if phrase and (' '+phrase+' ') in (' '+heading+' '):
            score += 12
        heading_words=set(heading.split())
        if cov==title_max:
            # A bare procedure/concept query should find the overview/initial
            # procedure before individual messages, IEs or specialized cases.
            if heading_words.intersection({'procedure','initial','overview'}):score+=12
            neutral=STOP|{'procedure','procedures','initial','overview','general','basic','e','utran'}
            if tech:neutral.update(TECH[tech])
            extras=heading_words-all_terms-neutral
            score-=min(24,3*len(extras))
            if not length:score-=12
        details={'complete','accept','request','result','type','needed','state'}
        if cov and not set(words).intersection(details) and not heading_words.intersection({'procedure','initial'}):
            if heading_words.intersection(details):score-=12
        if tech:
            score += 22 if tech_title else -12
            if tech_title:
                named=sum(technology_in(doc['title_norm'],t) for t in TECH)
                score+=6/max(named,1)
        if not set(words).intersection(EXCEPTIONS) and set(heading.split()).intersection(EXCEPTIONS):
            score -= 22
        # Test/report specifications stay searchable, but a procedure query
        # first shows normative procedure text, not conformance test cases.
        if 'test' not in words and 'conformance' not in words and 'conformance' in doc['title_norm'].split():
            score -= 12
        title_cov = coverage(groups, doc['title_norm'])
        score += 5*title_cov/title_max
        if score<=0:continue
        key = (round(score, 8), -sid)
        bucket = buckets.setdefault(did, {'count': 0, 'heap': [], 'best': score, 'metadata': False})
        bucket['count'] += 1
        bucket['best'] = max(bucket['best'], score)
        item = (key, sid, cov, tf, 'Section title' if cov else 'Section text')
        if len(bucket['heap']) < keep:
            heapq.heappush(bucket['heap'], item)
        elif item > bucket['heap'][0]:
            heapq.heapreplace(bucket['heap'], item)
    ordered = sorted(buckets, key=lambda did: (-buckets[did]['best'], metadata[did]['spec']))
    docs = []
    wanted = []
    for did in ordered[(page-1)*25:page*25]:
        bucket = buckets[did]
        doc = metadata[did]
        doc.pop('title_norm', None)
        doc.update(matching_sections=bucket['count'], sections=[], score=round(bucket['best'], 3),
                   match_reason='Specification title' if bucket['metadata'] else 'Section text')
        chosen = sorted(bucket['heap'], reverse=True)[(section_page-1)*previews:section_page*previews]
        for rank, (key, sid, th, bh, reason) in enumerate(chosen, (section_page-1)*previews+1):
            doc['sections'].append(dict(id=sid, title_hits=th, body_hits=bh, rank=rank, match_reason=reason, score=key[0]))
            wanted.append(sid)
        if chosen and not bucket['metadata']:
            doc['match_reason'] = chosen[0][4]
        docs.append(doc)
    contents = {}
    for offset in range(0, len(wanted), 500):
        batch = wanted[offset:offset+500]
        contents.update({r['id']: dict(r) for r in db.execute('''SELECT s.id,s.number,s.title,s.page,t.body
          FROM sections s JOIN section_text t ON t.section_id=s.id WHERE s.id IN (''' + marks(batch) + ')', batch)})
    for doc in docs:
        for section in doc['sections']:
            content = contents[section['id']]
            section.update({k: v for k,v in content.items() if k != 'body'})
            section['preview'] = preview_fn(content['body'], words)
    return docs, len(buckets)
