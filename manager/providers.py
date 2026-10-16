"""Provider adapters with exact matching for requested regional variants."""
import io
import json
import re
from urllib.parse import urljoin, urlparse
import zipfile
from locales import language, locale, matches, tag


def accepted_language(lang, wanted):
    return matches(tag(lang), wanted)


def identity(matches, episode):
    if episode:
        return {"series", "season", "episode"} <= matches or {"hash", "season", "episode"} <= matches
    return "hash" in matches or "imdb_id" in matches or {"title", "year"} <= matches


def settings(path):
    if not path:
        return {"providers": ["opensubtitlescom", "gestdown", "yifysubtitles"], "credentials": {}}
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def candidates(item, lang, work, config_path, rejected, health):
    from subliminal import Episode, Movie, ProviderPool, compute_score, scan_video
    from subliminal.cache import region
    from subliminal.core import provider_manager

    config = settings(config_path)
    region.configure("dogpile.cache.memory", replace_existing_backend=True)
    video = scan_video(item["Path"])
    if item.get("Source") == "sonarr":
        if not isinstance(video, Episode):
            return
        video.series = item["Title"]
    elif item.get("Source") == "radarr":
        if not isinstance(video, Movie):
            return
        video.title = item["Title"]
        video.year = item.get("Year") or video.year
        video.imdb_id = item.get("IMDBID") or video.imdb_id
    wanted = locale(lang)
    providers = [p for p in config.get("providers", []) if p != "yifysubtitles"]
    credentials = config.get("credentials", {})
    if "opensubtitlescom" in providers:
        credentials.setdefault("opensubtitlescom", {}).setdefault("max_result_pages", 2)
    with ProviderPool(providers=providers, provider_configs=credentials) as pool:
        found = []
        for provider in providers:
            try:
                if provider == "opensubtitlescom" and isinstance(video, Movie) and video.imdb_id:
                    # Search the actual movie rather than progressively broad
                    # title searches which can return unrelated films.
                    subtitles = pool[provider].query({wanted}, imdb_id=video.imdb_id)
                elif provider == "opensubtitlescom":
                    # The supported-language set can omit regional variants
                    # even when the converter and API accept the requested tag.
                    subtitles = pool[provider].list_subtitles(video, {wanted})
                else:
                    plugin = provider_manager[provider].plugin
                    supported = plugin.check_languages({wanted})
                    subtitles = pool[provider].list_subtitles(video, supported) if plugin.check(video) and supported else []
                health[provider] = "searched"
                for subtitle in subtitles:
                    if not accepted_language(subtitle.language, lang) or getattr(subtitle, "foreign_only", False):
                        continue
                    matches = subtitle.get_matches(video)
                    if isinstance(video, Movie) and hasattr(subtitle, "movie_imdb_id"):
                        # Upstream's imdb_match flag describes the query, not
                        # necessarily the result's actual movie identity.
                        if not video.imdb_id or subtitle.movie_imdb_id != video.imdb_id:
                            matches.discard("imdb_id")
                    if re.search(r"commentary|director.?s comments", str(getattr(subtitle, "release", "")), re.I):
                        continue
                    if identity(matches, isinstance(video, Episode)):
                        found.append((compute_score(subtitle, video), subtitle, matches))
            except Exception:
                health[provider] = "unavailable"
                continue
        found.sort(key=lambda entry: entry[0], reverse=True)
        downloads = 0
        for score, subtitle, matches in found:
            source = f"{subtitle.provider_name}:{subtitle.id}"
            if rejected("", source):
                continue
            if downloads >= 5:
                health[subtitle.provider_name] = "candidates_pending"
                break
            downloads += 1
            try:
                if not pool.download_subtitle(subtitle) or not subtitle.content:
                    health[subtitle.provider_name] = "download_unavailable"
                    continue
                output = work / f"provider-{downloads}.srt"
                output.write_text(subtitle.text, encoding="utf-8")
                yield output, {"source": "provider", "provider": subtitle.provider_name,
                               "source_id": source, "language_evidence": str(subtitle.language),
                               "matches": sorted(matches), "score": score}
            except Exception:
                health[subtitle.provider_name] = "download_unavailable"
                continue
    if "yifysubtitles" in config.get("providers", []) and isinstance(video, Movie) and video.imdb_id:
        yield from yify(video.imdb_id, work, rejected, health, lang)


def safe_yify_url(value):
    url = urljoin("https://yifysubtitles.ch", value)
    parsed = urlparse(url)
    if parsed.scheme != "https" or parsed.hostname != "yifysubtitles.ch":
        raise ValueError("untrusted_provider_link")
    return url


def limited_get(session, url):
    response = session.get(safe_yify_url(url), timeout=20, allow_redirects=False, stream=True)
    if response.status_code == 404:
        return b""
    response.raise_for_status()
    if response.status_code != 200:
        raise ValueError("provider_redirect")
    data = bytearray()
    for chunk in response.iter_content(65536):
        data.extend(chunk)
        if len(data) > 10_000_000:
            raise ValueError("provider_response_too_large")
    return bytes(data)


def yify(imdb, work, rejected, health, lang):
    import requests
    from bs4 import BeautifulSoup
    if not re.fullmatch(r"tt\d+", imdb):
        return
    with requests.Session() as session:
        session.headers["User-Agent"] = "Subtitler/ownership (subtitle management)"
        try:
            page = BeautifulSoup(limited_get(session, f"/movie-imdb/{imdb}"), "html.parser")
            health["yifysubtitles"] = "searched"
            rows = page.select("table.other-subs tbody tr")
            downloaded = 0
            for row in rows:
                cells = row.find_all("td")
                if len(cells) < 3 or not matches(language(cells[1].get_text(strip=True)), lang):
                    continue
                link = cells[2].find("a", href=True)
                if not link:
                    continue
                source = "yifysubtitles:" + safe_yify_url(link["href"])
                if rejected("", source):
                    continue
                if downloaded >= 3:
                    health["yifysubtitles"] = "candidates_pending"
                    break
                downloaded += 1
                detail = BeautifulSoup(limited_get(session, link["href"]), "html.parser")
                button = detail.select_one("a.download-subtitle[href]")
                if not button:
                    health["yifysubtitles"] = "download_unavailable"
                    continue
                with zipfile.ZipFile(io.BytesIO(limited_get(session, button["href"]))) as archive:
                    files = [f for f in archive.infolist() if f.filename.lower().endswith(".srt") and f.file_size < 5_000_000]
                    if len(files) != 1:
                        health["yifysubtitles"] = "download_unavailable"
                        continue
                    output = work / f"yify-{downloaded}.srt"
                    output.write_bytes(archive.read(files[0]))
                    yield output, {"source": "provider", "provider": "yifysubtitles", "source_id": source,
                                   "matches": ["imdb_id"], "language_evidence": language(cells[1].get_text(strip=True))}
        except Exception:
            health["yifysubtitles"] = "unavailable"
            return
