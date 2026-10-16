"""Language tags and regional matching; no configured-language preferences."""
import re


def locale(value):
    from babelfish import Language
    value = str(value or "").strip().replace("_", "-")
    match = re.fullmatch(r"([a-zA-Z]{2,3})(?:-([a-zA-Z]{4}))?(?:-([a-zA-Z]{2}))?", value)
    if not match:
        raise ValueError("invalid_subtitle_language")
    base, script, country = match.groups()
    try:
        parsed = Language.fromalpha2(base.lower()) if len(base) == 2 else Language(base.lower())
        if parsed.alpha3 in {"und", "mul", "zxx"}:
            raise ValueError("invalid_subtitle_language")
        return Language(parsed.alpha3, country=country.upper() if country else None,
                        script=script.title() if script else None)
    except Exception:
        raise ValueError("invalid_subtitle_language") from None


def tag(parsed):
    try:
        base = parsed.alpha2
    except Exception:
        base = parsed.alpha3
    return "-".join([base] + ([str(parsed.script)] if parsed.script else [])
                    + ([str(parsed.country)] if parsed.country else []))


def normalize(value):
    return tag(locale(value))


def base_language(value):
    return normalize(value).split("-")[0]


def language(value, title=""):
    """Read tolerant media metadata without inventing missing locale evidence."""
    from babelfish import Language
    raw = str(value or "").strip().replace("_", "-")
    title = str(title or "")
    if raw.lower() in {"und", "mul", "zxx"}:
        return ""
    if raw.lower() == "pob":
        return "pt-BR"
    try:
        parsed = locale(raw)
    except ValueError:
        try:
            parsed = Language.fromalpha3b(raw.lower()) if len(raw) == 3 else Language.fromname(raw.title())
        except Exception:
            if re.search(r"portuguese|portugu[eê]s", raw, re.I):
                parsed = Language("por")
            else:
                return ""
    if parsed.alpha3 == "por":
        hint = raw + " " + title
        if re.search(r"brazil|brasil|pt[-_ ]br", hint, re.I):
            return "pt-BR"
        if re.search(r"portugal|european|iberian|pt[-_ ]pt", hint, re.I):
            return "pt-PT"
    return tag(parsed)


def matches(actual, wanted):
    """Generic requests accept variants; regional/script requests require evidence."""
    try:
        a, w = locale(actual), locale(wanted)
    except ValueError:
        return False
    return (a.alpha3 == w.alpha3 and (not w.country or a.country == w.country)
            and (not w.script or a.script == w.script))


def ambiguous(actual, wanted):
    try:
        a, w = locale(actual), locale(wanted)
    except ValueError:
        return False
    return (a.alpha3 == w.alpha3 and (not a.country or not w.country or a.country == w.country)
            and (not a.script or not w.script or a.script == w.script) and not matches(actual, wanted))


def display_name(value):
    parsed = locale(value)
    parts = [parsed.name]
    if parsed.script:
        parts.append(parsed.script.name)
    if parsed.country:
        parts.append(parsed.country.name)
    return ", ".join(parts) + " (" + tag(parsed) + "). Preserve this locale; do not substitute another regional variant"


def ocr_language(value):
    parsed = locale(value)
    if parsed.alpha3 == "zho":
        if str(parsed.script) == "Hans":
            return "chi_sim"
        return "chi_tra" if str(parsed.script) == "Hant" or str(parsed.country) in {"TW", "HK", "MO"} else "chi_sim"
    return parsed.alpha3


def text_language_supported(value):
    from pathlib import Path
    from langdetect.detector_factory import PROFILES_DIRECTORY
    return any(base_language(p.name) == base_language(value) for p in Path(PROFILES_DIRECTORY).iterdir())
