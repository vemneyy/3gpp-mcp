import unicodedata


def normalize(text):
    text = unicodedata.normalize('NFKD', text)
    return ' '.join(''.join(c.lower() if c.isalpha() or c.isdecimal() else
                           '' if unicodedata.category(c).startswith('M') else ' '
                           for c in text).split())
