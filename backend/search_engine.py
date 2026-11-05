"""Field-aware lexical retrieval. No LLM-generated definitions or fixed spec boosts."""

import heapq
import re
import math
import threading
import time

from backend.highlight import Plan, evidence

from backend.text import normalize

STOP = {"the", "a", "an", "of", "for", "and", "in", "on", "to", "with"}
TECH = {
    "4g": ("4g", "lte", "eps", "eutran"),
    "5g": ("5g", "5gs", "5gc", "nr"),
    "3g": ("3g", "umts", "utran"),
    "2g": ("2g", "gsm", "geran"),
}
TECH_NAMES = {"lte": "4g", "eps": "4g", "umts": "3g", "gsm": "2g"}
FORMS = {
    "attach": ("attach", "attached", "attaches", "attaching"),
    "authenticate": ("authenticate", "authentication"),
    "register": ("register", "registration"),
}
for _forms in list(FORMS.values()):
    for _word in _forms:
        FORMS[_word] = _forms
EXCEPTIONS = {"reject", "rejected", "failure", "failed", "not", "detach", "detachment"}


def marks(items):
    return ",".join("?" for _ in items)


def coverage(groups, title):
    words = set(title.split())
    return sum(bool(words.intersection(g)) for g in groups)


def technology_in(title, tech):
    if not tech:
        return True
    return bool(set(title.split()).intersection(TECH[tech])) or (
        tech == "4g" and "e utran" in title
    )


NUMBER_QUERY = re.compile(
    r"^\s*(?:(?:3gpp|etsi)\s*)?(?:(?:ts|tr)\s*)?(\d{2}\.\d{2,3}(?:-\d+)?|\d{3}\s+\d{3}|\d{5,6}|0\d{3})(?=\s|$)\s*(.*)$",
    re.I,
)


def split_query(db, q):
    # Number selectors apply to the actual specification, never to citations.
    number = NUMBER_QUERY.match(q)
    ids = None
    rest = q
    if number:
        compact = re.sub(r"[.\s]", "", number[1])
        ids = [
            r[0]
            for r in db.execute(
                "SELECT document_id FROM document_search WHERE spec_compact=? OR etsi_number=?",
                [compact, compact],
            )
        ]
        rest = number[2]
    words = list(dict.fromkeys(normalize(rest).split()))
    if len(words) > 16:
        raise ValueError("Use at most 16 search words.")
    tech = next(
        (TECH_NAMES.get(w, w) for w in words if w in TECH or w in TECH_NAMES), None
    )
    meaningful = [
        w for w in words if w not in STOP and w not in TECH and w not in TECH_NAMES
    ]
    if not meaningful and not tech:
        meaningful = words if any(w not in STOP for w in words) else []
    groups = list(dict.fromkeys(FORMS.get(w, (w,)) for w in meaningful))
    if not groups and tech:
        groups = [TECH[tech]]
    return ids, groups, tech, words, normalize(rest)


def all_terms_intersect(groups, terms):
    return any(set(group).intersection(terms) for group in groups)


_stats_lock = threading.Lock()
_stats_cache = {}


def corpus_statistics(db):
    # Ranking statistics only, never hit/results caching. New documents are
    # searchable immediately; average lengths refresh within 60 seconds.
    path = db.execute("PRAGMA database_list").fetchone()[2]
    now = time.monotonic()
    with _stats_lock:
        cached = _stats_cache.get(path)
        if cached and now - cached[0] < 60:
            return cached[1]
        row = db.execute(
            "SELECT count(*),coalesce(avg(body_length),1),coalesce(avg(title_length),1) FROM section_features"
        ).fetchone()
        value = (max(row[0], 1), max(row[1], 1), max(row[2], 1))
        if len(_stats_cache) > 16:
            _stats_cache.clear()
        _stats_cache[path] = (now, value)
        return value


def section_candidates(db, groups, conditions, args, tech):
    if not groups:
        return (), [], []
    terms = set(t for group in groups for t in group)
    stats = dict(
        db.execute(
            "SELECT term,section_count FROM term_stats WHERE term IN ("
            + marks(terms)
            + ")",
            list(terms),
        )
    )
    ordered = sorted(groups, key=lambda group: sum(stats.get(t, 0) for t in group))
    if any(not any(t in stats for t in group) for group in ordered):
        return (), [], []
    driver = [t for t in ordered[0] if t in stats]
    if len(driver) == 1:
        first = "SELECT section_id,document_id,title_hits,body_hits FROM postings WHERE term=?"
    else:
        first = (
            "SELECT section_id,document_id,sum(title_hits) title_hits,sum(body_hits) body_hits FROM postings WHERE term IN ("
            + marks(driver)
            + ") GROUP BY section_id"
        )
    params = list(driver)
    columns = ["p0.body_hits", "p0.title_hits"]
    for group in ordered[1:]:
        for field in ["body_hits", "title_hits"]:
            columns.append(
                "coalesce((SELECT sum(p."
                + field
                + ") FROM postings p WHERE p.section_id=p0.section_id AND p.term IN ("
                + marks(group)
                + ")),0)"
            )
            params.extend(group)
    tech_sql = "0"
    # tech appears before frequency columns in SELECT, so prepend its arguments.
    tech_args = []
    if tech:
        aliases = TECH[tech]
        tech_sql = (
            "EXISTS(SELECT 1 FROM postings z WHERE z.section_id=p0.section_id AND z.term IN ("
            + marks(aliases)
            + "))"
        )
        tech_args = list(aliases)
    params = [*driver, *tech_args, *params[len(driver) :]]
    kinds = (
        "'content','history'"
        if all_terms_intersect(groups, {"history", "changelog"})
        else "'content'"
    )
    sql = f"""WITH driver AS ({first}) SELECT p0.section_id,p0.document_id,s.title,s.number,
        f.body_length,f.title_length,{tech_sql} AS body_tech,{",".join(columns)}
        FROM driver p0 CROSS JOIN section_features f ON f.section_id=p0.section_id
        CROSS JOIN sections s ON s.id=p0.section_id WHERE f.kind IN ({kinds})"""
    for group in ordered[1:]:
        sql += (
            " AND EXISTS(SELECT 1 FROM postings p WHERE p.section_id=p0.section_id AND p.term IN ("
            + marks(group)
            + "))"
        )
        params.extend(group)
    if conditions:
        sql += (
            " AND p0.document_id IN (SELECT d.id FROM documents d WHERE "
            + conditions
            + ")"
        )
        params.extend(args)
    return (
        db.execute(sql, params),
        ordered,
        [sum(stats.get(t, 0) for t in g) for g in ordered],
    )


def bm25f(frequencies, dfs, length, title_length, statistics):
    count, avg_body, avg_title = statistics
    score = 0.0
    for i, df in enumerate(dfs):
        body_tf, title_tf = frequencies[2 * i : 2 * i + 2]
        df = min(count, max(df, 1))
        idf = math.log1p((count - df + 0.5) / (df + 0.5))
        weighted = body_tf / (0.35 + 0.65 * length / avg_body) + 4 * title_tf / (
            0.7 + 0.3 * title_length / avg_title
        )
        score += idf * 2.2 * weighted / (1.2 + weighted) if weighted else 0
    return score


def glossary_search(db, q="", page=1, limit=50):
    terms = list(dict.fromkeys(normalize(q).split()))
    if len(terms) > 16:
        raise ValueError("Use at most 16 search words.")
    params = []
    condition = ""
    if terms:
        condition = " WHERE g.id IN (SELECT p0.glossary_id FROM glossary_postings p0 "
        for i in range(1, len(terms)):
            condition += f"CROSS JOIN glossary_postings p{i} ON p{i}.glossary_id=p0.glossary_id AND p{i}.term=? "
            params.append(terms[i])
        condition += "WHERE p0.term=?)"
        params.append(terms[0])
    total = db.execute(
        "SELECT count(*) FROM glossary g" + condition, params
    ).fetchone()[0]
    rows = db.execute(
        """SELECT g.term,g.definition,g.kind,d.spec,d.url,s.number,s.page
      FROM glossary g JOIN documents d ON d.id=g.document_id JOIN sections s ON s.id=g.section_id"""
        + condition
        + " ORDER BY (g.term_norm=?) DESC,g.term_norm,d.spec LIMIT ? OFFSET ?",
        [*params, normalize(q), limit, (page - 1) * limit],
    )
    entries = [dict(r) for r in rows]
    for entry in entries:
        entry["url"] += "#page=" + str(entry["page"] + 1)
    return entries, total


def rank_documents(
    db, q, filters, args, allowed_scope=None, limit=0, collect_scope=False
):
    number_ids, groups, tech, words, phrase = split_query(db, q)
    if number_ids is not None:
        if not number_ids:
            return (
                {},
                {},
                words,
                [],
                0,
                {"whole": set(), "sections": set(), "docs": set()},
            )
        selector = "d.id IN (" + marks(number_ids) + ")"
        filters = filters + " AND " + selector if filters else selector
        args = [*args, *number_ids]
    # Metadata is small and separate from all section text. Fetch only documents
    # eligible for this request; a dictionary also avoids per-section SQL calls.
    where = filters or "1"
    metadata = {
        r["id"]: dict(r)
        for r in db.execute(
            """SELECT d.id,d.spec,d.family,d.series,d.title,d.version,d.url,e.title_norm
      FROM documents d JOIN document_search e ON e.document_id=d.id WHERE """
            + where,
            args,
        )
    }
    if allowed_scope is not None:
        metadata = {k: v for k, v in metadata.items() if k in allowed_scope["docs"]}
    hits = []
    total_hits = 0
    scope = (
        {"whole": set(), "sections": set(), "docs": set()} if collect_scope else None
    )

    def emit(did, sid, tier, score, title_hits, body_hits, reason):
        nonlocal total_hits
        if (
            allowed_scope is not None
            and did not in allowed_scope["whole"]
            and (sid is None or sid not in allowed_scope["sections"])
        ):
            return False
        total_hits += 1
        if scope is not None:
            scope["docs"].add(did)
            if sid is None:
                scope["whole"].add(did)
            else:
                scope["sections"].add(sid)
        if not limit:
            return True
        tie = tuple(-ord(c) for c in metadata[did]["spec"]) + (0,)
        key = (tier, round(score, 8), body_hits, tie, -(sid or 0))
        item = (key, did, sid, title_hits, body_hits, reason)
        if len(hits) < limit:
            heapq.heappush(hits, item)
        elif item > hits[0]:
            heapq.heapreplace(hits, item)
        return True

    if number_ids is not None and not groups:
        buckets = {}
        for did in metadata:
            if emit(did, None, 5, 1000, 0, 0, "Specification number"):
                buckets[did] = {"count": 0}
        return metadata, buckets, words, hits, total_hits, scope
    if not groups:
        return metadata, {}, words, [], 0, scope
    all_terms = set(t for group in groups for t in group)
    title_ids = {
        r[0]
        for r in db.execute(
            "SELECT DISTINCT document_id FROM document_postings WHERE term IN ("
            + marks(all_terms)
            + ")",
            list(all_terms),
        )
    }
    buckets = {}
    for did in title_ids.intersection(metadata):
        doc = metadata[did]
        cov = coverage(groups, doc["title_norm"])
        if cov == len(groups) and technology_in(doc["title_norm"], tech):
            phrase_bonus = (
                15
                if phrase and (" " + phrase + " ") in (" " + doc["title_norm"] + " ")
                else 0
            )
            if emit(did, None, 4, 110 + phrase_bonus, cov, 0, "Specification title"):
                buckets[did] = {"count": 0}
    title_max = len(groups)
    rows, ordered_groups, dfs = section_candidates(db, groups, filters, args, tech)
    statistics = corpus_statistics(db) if ordered_groups else (1, 1, 1)
    for row in rows:
        sid, did, title, number, length, title_length, body_tech = row[:7]
        frequencies = list(row[7:])
        tf = sum(frequencies[::2])
        if did not in metadata:
            continue
        doc = metadata[did]
        tech_title = technology_in(doc["title_norm"], tech)
        if tech and not tech_title and not body_tech:
            continue
        heading = normalize(title)
        cov = coverage(groups, heading)
        # Saturating frequency and length normalization prevent long appendices
        # from winning just because a token appears hundreds of times.
        bm25 = bm25f(frequencies, dfs, length, title_length, statistics)
        score = 2 * bm25 + 30 * cov / title_max + (30 if cov == title_max else 0)
        if cov:
            score += 8 / max(title_length, 1)
            score -= min(number.count("."), 6)
        if phrase and (" " + phrase + " ") in (" " + heading + " "):
            score += 12
        heading_words = set(heading.split())
        action = bool(
            set(words).intersection(
                {
                    "attach",
                    "attached",
                    "attaches",
                    "attaching",
                    "detach",
                    "handover",
                    "registration",
                    "register",
                    "authentication",
                    "authenticate",
                }
            )
        )
        if cov == title_max:
            # A bare procedure/concept query should find the overview/initial
            # procedure before individual messages, IEs or specialized cases.
            if action and heading_words.intersection(
                {"procedure", "initial", "overview"}
            ):
                score += 12
            neutral = STOP | (
                {
                    "procedure",
                    "procedures",
                    "initial",
                    "overview",
                    "general",
                    "basic",
                    "e",
                    "utran",
                }
                if action
                else set()
            )
            if tech:
                neutral.update(TECH[tech])
            extras = heading_words - all_terms - neutral
            score -= min(24, 3 * len(extras))
            if not length:
                score -= 12
        details = {"complete", "accept", "request", "result", "type", "needed", "state"}
        if (
            action
            and cov
            and not set(words).intersection(details)
            and not heading_words.intersection({"procedure", "initial"})
        ):
            if heading_words.intersection(details):
                score -= 12
        if tech:
            score += 22 if tech_title else -12
            if tech_title:
                named = sum(technology_in(doc["title_norm"], t) for t in TECH)
                score += 6 / max(named, 1)
        if (
            action
            and not set(words).intersection(EXCEPTIONS)
            and set(heading.split()).intersection(EXCEPTIONS)
        ):
            score -= 22
        # Test/report specifications stay searchable, but a procedure query
        # first shows normative procedure text, not conformance test cases.
        if (
            "test" not in words
            and "conformance" not in words
            and "conformance" in doc["title_norm"].split()
        ):
            score -= 12
        title_cov = coverage(groups, doc["title_norm"])
        score += 5 * title_cov / title_max
        if score <= 0:
            continue
        requested = [
            w for w in words if w not in STOP and w not in TECH and w not in TECH_NAMES
        ]
        exact_heading = heading == " ".join(requested or words)
        tier = 3 if exact_heading else 2 if cov == title_max else 1 if cov else 0
        if emit(
            did, sid, tier, score, cov, tf, "Section title" if cov else "Section text"
        ):
            bucket = buckets.setdefault(did, {"count": 0})
            bucket["count"] += 1
    return metadata, buckets, words, hits, total_hits, scope


def retrieve(db, q, filters, args, page_size, page, within=()):
    allowed = None
    for parent in within:
        _, _, _, _, _, allowed = rank_documents(
            db, parent, filters, args, allowed, collect_scope=True
        )
        if not allowed["docs"]:
            return [], 0, 0
    metadata, buckets, words, hits, total, _ = rank_documents(
        db, q, filters, args, allowed, page * page_size
    )
    selected = sorted(hits, reverse=True)[(page - 1) * page_size : page * page_size]
    results = []
    wanted = []
    for key, did, sid, th, bh, reason in selected:
        doc = {k: v for k, v in metadata[did].items() if k != "title_norm"}
        doc.update(
            sections=[],
            score=round(key[0] * 1_000_000 + min(key[1], 999_999), 6),
            lexical_score=key[1],
            match_reason=reason,
        )
        if sid is not None:
            doc["sections"] = [
                dict(
                    id=sid,
                    title_hits=th,
                    body_hits=bh,
                    rank=1,
                    match_reason=reason,
                    score=key[1],
                )
            ]
            wanted.append(sid)
        results.append(doc)
    contents = {}
    if wanted:
        contents = {
            r["id"]: dict(r)
            for r in db.execute(
                """SELECT s.id,s.number,s.title,s.page,t.body
          FROM sections s JOIN section_text t ON t.section_id=s.id WHERE s.id IN ("""
                + marks(wanted)
                + ")",
                wanted,
            )
        }
    plan = Plan(" ".join(words), FORMS, TECH, TECH_NAMES, STOP)
    for doc in results:
        doc["title_parts"] = plan.parts(doc["title"])
        doc["spec_parts"] = (
            [dict(text=doc["spec"], match=True)]
            if NUMBER_QUERY.match(q)
            else plan.parts(doc["spec"])
        )
        doc["expanded_matches"] = []
        if not doc["sections"]:
            doc["evidence"] = evidence(plan, "", "", doc["title"])
        for section in doc["sections"]:
            content = contents[section["id"]]
            section.update({k: v for k, v in content.items() if k != "body"})
            proof = evidence(plan, content["body"], section["title"], doc["title"])
            section["preview"] = proof["text"] if proof else ""
            section["preview_parts"] = proof["parts"] if proof else []
            section["preview_source"] = proof["source"] if proof else None
            section["title_parts"] = plan.parts(section["title"])
            doc["expanded_matches"] = proof["expanded"] if proof else []
            parents = [
                ".".join(section["number"].split(".")[:i])
                for i in range(1, len(section["number"].split(".")))
            ]
            section["parents"] = (
                [
                    dict(r)
                    for r in db.execute(
                        "SELECT number,title FROM sections WHERE document_id=? AND number IN ("
                        + marks(parents)
                        + ") ORDER BY id",
                        [doc["id"], *parents],
                    )
                ]
                if parents
                else []
            )
        if doc.get("evidence"):
            doc["expanded_matches"] = doc["evidence"]["expanded"]
    return results, total, len(buckets)
