"""Generation and topic context, derived from titles and the actual ancestor chain.

No preferred specification IDs or clause numbers. Technology banners alone are
weak evidence; an unqualified 'Attach' inherits its chapter's subject.
"""

from functools import lru_cache
from itertools import islice

from backend.text import normalize

TECH = {
    "4g": ("4g", "lte", "eps", "epc", "eutran", "eutra"),
    "5g": ("5g", "5gs", "5gc", "nr"),
    "3g": ("3g", "umts", "utran"),
    "2g": ("2g", "gsm", "geran"),
}
TECH_NAMES = {"lte": "4g", "eps": "4g", "umts": "3g", "gsm": "2g"}
BANNERS = {
    normalize(x)
    for x in [
        "LTE",
        "5G",
        "4G",
        "3G",
        "2G",
        "GSM",
        "UMTS",
        "Universal Mobile Telecommunications System (UMTS)",
        "Digital cellular telecommunications system (Phase 2+) (GSM)",
    ]
}
# Standardized topic qualifiers: lower specialized scenarios for a broad query,
# but remove the penalty when that qualifier is explicitly requested.
FACETS = {
    "interception": (
        ("lawful interception", "interception", "li at", "li x2"),
        {"lawful", "interception", "li", "xiri"},
    ),
    "non3gpp": (
        ("non 3gpp", "non3gpp", "fixed broadband", "s2a", "s2b", "pmip"),
        {"non", "non3gpp", "broadband", "s2a", "s2b", "pmip"},
    ),
    "fallback": (
        ("cs fallback", "circuit switched fallback", "csfb"),
        {"cs", "csfb", "fallback"},
    ),
    "srvcc": (("srvcc",), {"srvcc"}),
    "v2x": (("v2x",), {"v2x", "vehicular", "vehicle"}),
    "relay": (("relaying", "relay", "rn"), {"relaying", "relay", "rn"}),
    "glr": (("gateway location register", "glr"), {"glr", "gateway"}),
    "pool": (
        ("multiple core network", "multiple cn", "multiple core", "nnsf"),
        {"multiple", "pool", "pooling", "nnsf"},
    ),
    "dedicated": (("dedicated core", "dcn", "decor"), {"dedicated", "dcn", "decor"}),
    "disaster": (("disaster roaming",), {"disaster"}),
    "interworking": (
        ("interworking", "inter working"),
        {"interworking", "n26", "inter", "rat"},
    ),
    "testing": (
        ("conformance", "test", "tests", "testing"),
        {"conformance", "test", "tests", "testing"},
    ),
    "charging": (("charging", "billing", "cdr"), {"charging", "billing", "cdr"}),
}


def technology_in(text, tech):
    if not tech:
        return True
    words = text.split()
    if tech == "3g":
        return bool(set(words) & {"3g", "umts"}) or any(
            w == "utran" and (i == 0 or words[i - 1] != "e")
            for i, w in enumerate(words)
        )
    return bool(set(words).intersection(TECH[tech])) or (
        tech == "4g" and ("e utran" in text or "e utra" in text)
    )


@lru_cache(maxsize=4096)
def subject_title(title):
    fields = [normalize(field) for field in title.split(";")]
    return " ".join(field for field in fields if field not in BANNERS)


@lru_cache(maxsize=8192)
def facet_tags(text):
    padded = " " + text + " "
    return frozenset(
        name
        for name, (markers, _) in FACETS.items()
        if any(" " + m + " " in padded for m in markers)
    )


def context_quality(tech, heading, parents, doc_title, query_words):
    if not tech:
        return 0, False
    requested = set(query_words)
    subject = subject_title(doc_title)
    direct = technology_in(heading, tech)
    base = None
    for local in [heading, *reversed(parents)]:
        if technology_in(local, tech):
            base = 3
            break
        if tech == "4g":
            # An LTE banner must not turn GPRS/IMSI or GERAN/UTRAN attach into
            # the main E-UTRAN/EPS procedure. Keep these hits, but below it.
            words = set(local.split())
            legacy = (
                bool(words & {"gprs", "geran"})
                or ("utran" in words and "e utran" not in local)
                or "imsi attach" in local
            )
            intentional = bool(requested & {"gprs", "geran", "utran", "imsi"})
            if legacy and not intentional:
                base = 0
                break
    if base is None:
        base = (
            2
            if technology_in(subject, tech)
            else 1
            if technology_in(normalize(doc_title), tech)
            else 0
        )
    tags = set(facet_tags(subject)) | set(facet_tags(heading))
    for parent in parents:
        tags.update(facet_tags(parent))
    wanted = {name for name in tags if requested.intersection(FACETS[name][1])}
    if wanted:
        # An explicitly requested feature is the search intent, not noise.
        # A document carrying that technology and feature can cover inter-RAT
        # procedures whose own short heading has no generation marker.
        base = max(base, 3 if technology_in(normalize(doc_title), tech) else 1) + 1
    elif tags:
        base -= 2
    # A 5GS-focused document's interworking clause is secondary for a generic
    # 4G procedure query, even when that clause mentions E-UTRAN explicitly.
    if (
        not technology_in(subject, tech)
        and any(technology_in(subject, other) for other in TECH if other != tech)
        and not wanted
    ):
        base -= 1
    return max(0, min(base, 4)), direct


def ancestor_numbers(number):
    parts = number.split(".")
    return [".".join(parts[:i]) for i in range(1, len(parts))]


def with_ancestors(db, rows, eligible):
    """Batch indexed parent lookups; don't scan all headings in every document."""
    cache = {}
    iterator = iter(rows)
    while True:
        raw = list(islice(iterator, 256))
        if not raw:
            return
        batch = [r for r in raw if r[1] in eligible]
        if not batch:
            continue
        needed = {
            (r[1], n)
            for r in batch
            for n in ancestor_numbers(r[3])
            if (r[1], n) not in cache
        }
        for key in needed:
            cache[key] = ""
        needed = list(needed)
        for offset in range(0, len(needed), 300):
            keys = needed[offset : offset + 300]
            sql = (
                "WITH wanted(document_id,number) AS (VALUES "
                + ",".join("(?,?)" for _ in keys)
                + ") SELECT s.document_id,s.number,s.title FROM wanted w CROSS JOIN sections s ON s.document_id=w.document_id AND s.number=w.number"
            )
            for row in db.execute(sql, [v for key in keys for v in key]):
                cache[(row[0], row[1])] = normalize(row[2])
        for row in batch:
            yield (
                row,
                [
                    cache[(row[1], n)]
                    for n in ancestor_numbers(row[3])
                    if cache[(row[1], n)]
                ],
            )
