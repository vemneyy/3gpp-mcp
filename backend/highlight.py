"""Original-text offsets shared by snippets and HTML highlighting.

All HTML is built by Jinja from plain segments. No user/source text becomes markup.
"""

import unicodedata

from backend.text import normalize


def tokens(text):
    word = []
    start = end = 0
    for pos, char in enumerate(text):
        if ord(char) < 128:
            if char.isalnum():
                if not word:
                    start = pos
                word.append(char.lower())
                end = pos + 1
            elif word:
                yield "".join(word), start, end
                word = []
            continue
        decomposed = unicodedata.normalize("NFKD", char)
        for c in decomposed:
            if unicodedata.category(c).startswith("M"):
                if word:
                    end = pos + 1
                continue
            if c.isalpha() or c.isdecimal():
                if not word:
                    start = pos
                word.append(c.lower())
                end = pos + 1
            elif word:
                yield "".join(word), start, end
                word = []
    if word:
        yield "".join(word), start, end


class Plan:
    def __init__(self, q, forms=None, tech=None, tech_names=None, stop=()):
        self.words = list(
            dict.fromkeys(w for w in normalize(q).split() if w not in stop)
        )
        self.lookup = {}
        self.core = set()
        self.literal = set(self.words)
        for i, word in enumerate(self.words):
            aliases = (forms or {}).get(word, (word,))
            key = (tech_names or {}).get(word, word)
            is_tech = key in (tech or {})
            if is_tech:
                aliases = tuple(set(aliases) | set(tech[key]))
            else:
                self.core.add(i)
            for alias in aliases:
                self.lookup.setdefault(alias, []).append((i, word, alias == word))
        if not self.core:
            self.core = set(range(len(self.words)))

    def matches(self, text):
        for word, start, end in tokens(text):
            if word in self.lookup:
                yield dict(word=word, start=start, end=end, groups=self.lookup[word])

    def parts(self, text):
        out = []
        cursor = 0
        for match in self.matches(text):
            if match["start"] > cursor:
                out.append(dict(text=text[cursor : match["start"]], match=False))
            # Compatibility decompositions can create multiple tokens at one
            # original character; don't duplicate the original character.
            start = max(cursor, match["start"])
            if match["end"] > start:
                out.append(dict(text=text[start : match["end"]], match=True))
            cursor = max(cursor, match["end"])
        if cursor < len(text):
            out.append(dict(text=text[cursor:], match=False))
        return out

    def fragment(self, text, width=420):
        matches = list(self.matches(text))
        if not matches:
            return None  # Never return an arbitrary unmatched beginning.
        left = 0
        counts = {}
        best = None
        for right, match in enumerate(matches):
            for group, _, literal in match["groups"]:
                key = (group, literal)
                counts[key] = counts.get(key, 0) + 1
            while match["end"] - matches[left]["start"] > width - 100 and left < right:
                for group, _, literal in matches[left]["groups"]:
                    key = (group, literal)
                    counts[key] -= 1
                    if not counts[key]:
                        del counts[key]
                left += 1
            direct = {group for group, literal in counts if literal}
            covered = {group for group, _ in counts}
            span = match["end"] - matches[left]["start"]
            quality = (
                len(direct & self.core),
                len(covered & self.core),
                len(direct),
                len(covered),
                -span,
            )
            if best is None or quality > best[0]:
                best = quality, matches[left]["start"], match["end"]
        _, first, last = best
        start = max(0, first - 65)
        end = max(last, min(len(text), start + width))
        # Trim at word boundaries without truncating an actual match.
        while start > 0 and start < first and text[start - 1].isalnum():
            start += 1
        while end < len(text) and end > last and text[end - 1].isalnum():
            end -= 1
        excerpt = (
            ("…" if start else "")
            + text[start:end].strip()
            + ("…" if end < len(text) else "")
        )
        excerpt_matches = list(self.matches(excerpt))
        if not excerpt_matches:
            raise AssertionError("Snippet lost its matching token")
        expansions = sorted(
            {
                (query, m["word"])
                for m in excerpt_matches
                for _, query, literal in m["groups"]
                if not literal
            }
        )
        return dict(
            text=excerpt,
            parts=self.parts(excerpt),
            quality=best[0][:4],
            expanded=[dict(query=a, matched=b) for a, b in expansions],
        )


def evidence(plan, body, section_title, document_title):
    fields = [
        ("body", body, 2),
        ("section_title", section_title, 1),
        ("document_title", document_title, 0),
    ]
    candidates = []
    for source, text, preference in fields:
        fragment = plan.fragment(text)
        if fragment:
            candidates.append(((*fragment["quality"], preference), source, fragment))
    if not candidates:
        return None
    _, source, fragment = max(candidates, key=lambda c: c[0])
    fragment["source"] = source
    fragment.pop("quality")
    return fragment
