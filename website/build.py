#!/usr/bin/env python3
"""Builds the SSHYard website: static HTML rendered with Jinja2 for every
page in every language of i18n/, with the downloads of the latest GitHub
release, Open Graph images, sitemap, robots.txt and llms.txt.

    python3 website/build.py                         # into website/_site
    python3 website/build.py --serve                 # build for localhost, then serve
    python3 website/build.py --base-url URL --strict # what CI runs

See website/README.md.
"""

from __future__ import annotations

import argparse
import datetime as dt
import functools
import hashlib
import http.server
import json
import math
import os
import re
import shutil
import subprocess
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

from jinja2 import Environment, FileSystemLoader, StrictUndefined
from markupsafe import Markup
from PIL import Image

import og

HERE = Path(__file__).resolve().parent
REPO = HERE.parent
REPOSITORY = "Licoy/sshyard"
GITHUB = f"https://github.com/{REPOSITORY}"
AUTHOR = "https://github.com/Licoy"
LICENSE_URL = "https://www.gnu.org/licenses/agpl-3.0.html"
DEFAULT_BASE_URL = "https://licoy.github.io/sshyard"


@dataclass(frozen=True)
class Lang:
    code: str  # BCP 47, also the i18n/<code>.json name
    prefix: str  # URL directory; "" for the default language
    hreflang: tuple[str, ...]  # every hreflang value that points here
    og_locale: str
    font: str  # Source Han Sans region for the Open Graph images


LANGS = [
    Lang("en", "", ("en",), "en_US", "CN"),
    Lang("zh-Hans", "zh-hans", ("zh-Hans", "zh-CN", "zh-SG"), "zh_CN", "CN"),
    Lang("zh-Hant", "zh-hant", ("zh-Hant", "zh-TW", "zh-HK"), "zh_TW", "TW"),
    Lang("ja", "ja", ("ja",), "ja_JP", "JP"),
    Lang("ko", "ko", ("ko",), "ko_KR", "KR"),
]
DEFAULT = LANGS[0]


@dataclass(frozen=True)
class Page:
    key: str
    path: str  # under the language directory, "" or ending in "/"
    template: str
    platform: str | None = None


PLATFORMS = ("macos", "windows", "linux")
PAGES = [
    Page("home", "", "home.html"),
    Page("download", "download/", "download.html"),
    *(Page(f"download-{p}", f"download/{p}/", "download.html", p) for p in PLATFORMS),
]
PAGE = {p.key: p for p in PAGES}

# Release assets, as named by .github/release-download-notes.sh, with the
# name each has for a version when the release can't be read.
ASSETS = {
    "mac_arm64": (r"sshyard-.+-arm64\.dmg", "sshyard-{v}-arm64.dmg"),
    "mac_amd64": (r"sshyard-.+-amd64\.dmg", "sshyard-{v}-amd64.dmg"),
    "win_setup": (r"sshyard-setup-.+-amd64\.exe", "sshyard-setup-{v}-amd64.exe"),
    "win_portable": (r"sshyard-.+-windows-amd64-portable\.zip", "sshyard-{v}-windows-amd64-portable.zip"),
    "win_setup_arm64": (r"sshyard-setup-.+-arm64\.exe", "sshyard-setup-{v}-arm64.exe"),
    "win_portable_arm64": (r"sshyard-.+-windows-arm64-portable\.zip", "sshyard-{v}-windows-arm64-portable.zip"),
    "linux_deb": (r"sshyard_.+_amd64\.deb", "sshyard_{v}_amd64.deb"),
    "linux_tar": (r"sshyard-.+-linux-amd64\.tar\.gz", "sshyard-{v}-linux-amd64.tar.gz"),
    "linux_deb_arm64": (r"sshyard_.+_arm64\.deb", "sshyard_{v}_arm64.deb"),
    "linux_tar_arm64": (r"sshyard-.+-linux-arm64\.tar\.gz", "sshyard-{v}-linux-arm64.tar.gz"),
    "install_sh": (r"install\.sh", "install.sh"),
    "sha256sums": (r"SHA256SUMS", "SHA256SUMS"),
}
INSTALLERS = ("mac_arm64", "mac_amd64", "win_setup", "win_portable", "win_setup_arm64", "win_portable_arm64",
              "linux_deb", "linux_tar", "linux_deb_arm64", "linux_tar_arm64")
# Windows and Linux Arm64 builds start after 0.0.9: a release without them
# lists none, and the pages show only the files a release has.
OPTIONAL = {"win_setup_arm64", "win_portable_arm64", "linux_deb_arm64", "linux_tar_arm64"}
PLATFORM_ICONS = {"macos": "apple-line", "windows": "windows-line", "linux": "ubuntu-line"}

# Screenshots from docs/, keyed as tour.items[].key, and the widths made.
SHOTS = {
    "terminal": "terminal-ai-assistant",
    "sftp": "sftp-file-manager",
    "monitor": "system-monitor",
    "settings": "ai-assistant-settings",
}
SHOT_WIDTHS = (960, 1600, 2400)

# AI brands shown beside "bring your own model", from internal/aibrands
# (LobeHub Icons, MIT): each reachable through an API SSHYard speaks.
AI_BRANDS = (
    ("openai", "OpenAI"), ("claude-color", "Claude"), ("gemini-color", "Gemini"),
    ("deepseek-color", "DeepSeek"), ("qwen-color", "Qwen"), ("kimi-color", "Kimi"),
    ("zhipu-color", "Zhipu"), ("mistral-color", "Mistral"), ("openrouter-color", "OpenRouter"),
    ("ollama", "Ollama"),
)

AI_CRAWLERS = (
    "GPTBot", "OAI-SearchBot", "ChatGPT-User", "ClaudeBot", "Claude-SearchBot", "Claude-User",
    "PerplexityBot", "Perplexity-User", "Google-Extended", "Applebot-Extended", "CCBot",
)

# Search engine ownership tokens, from the environment (repository
# variables in CI): meta name → variable.
VERIFICATION = {
    "google-site-verification": "GOOGLE_SITE_VERIFICATION",
    "msvalidate.01": "BING_SITE_VERIFICATION",
    "baidu-site-verification": "BAIDU_SITE_VERIFICATION",
    "naver-site-verification": "NAVER_SITE_VERIFICATION",
    "yandex-verification": "YANDEX_SITE_VERIFICATION",
}

MONTHS = ("January", "February", "March", "April", "May", "June", "July",
          "August", "September", "October", "November", "December")


def fail(msg: str) -> None:
    raise SystemExit(f"build.py: {msg}")


# ---------------------------------------------------------------- content


def load_catalogs() -> dict[str, dict]:
    cats = {}
    for lang in LANGS:
        path = HERE / "i18n" / f"{lang.code}.json"
        try:
            cats[lang.code] = json.loads(path.read_text("utf-8"))
        except (OSError, json.JSONDecodeError) as e:
            fail(f"{path.name}: {e}")
    for lang in LANGS[1:]:
        same_shape(cats[DEFAULT.code], cats[lang.code], lang.code)
    return cats


PLACEHOLDER = re.compile(r"\{(\w+)\}")


def same_shape(ref, other, where: str) -> None:
    """Fails unless other has exactly ref's keys, list lengths and
    placeholders, so no language misses a string."""
    if isinstance(ref, dict):
        if not isinstance(other, dict) or ref.keys() != other.keys():
            missing = set(ref) - set(other or {})
            extra = set(other or {}) - set(ref) if isinstance(other, dict) else set()
            fail(f"{where}: keys differ from en (missing {sorted(missing)}, extra {sorted(extra)})")
        for k in ref:
            same_shape(ref[k], other[k], f"{where}.{k}")
    elif isinstance(ref, list):
        if not isinstance(other, list) or len(ref) != len(other):
            fail(f"{where}: expected a list of {len(ref)} like en")
        for i, (a, b) in enumerate(zip(ref, other)):
            same_shape(a, b, f"{where}[{i}]")
    elif isinstance(ref, str):
        if not isinstance(other, str) or not other.strip():
            fail(f"{where}: expected a non-empty string")
        if sorted(PLACEHOLDER.findall(ref)) != sorted(PLACEHOLDER.findall(other)):
            fail(f"{where}: placeholders differ from en")


def fill(text: str, **values) -> str:
    """Replaces {name} placeholders, leaving unknown ones alone."""
    return PLACEHOLDER.sub(lambda m: str(values.get(m.group(1), m.group(0))), text)


def format_date(d: dt.date | None, code: str) -> str:
    if d is None:
        return ""
    if code == "en":
        return f"{MONTHS[d.month - 1]} {d.day}, {d.year}"
    if code == "ko":
        return f"{d.year}년 {d.month}월 {d.day}일"
    if code == "ja":
        return f"{d.year}年{d.month}月{d.day}日"
    return f"{d.year} 年 {d.month} 月 {d.day} 日"


def format_size(n: int | None) -> str:
    return f"{n / 1048576:.1f} MB" if n else ""


# ---------------------------------------------------------------- release


def read_release(path: str | None, strict: bool) -> dict:
    data = None
    if path:
        data = json.loads(Path(path).read_text("utf-8"))
    else:
        req = urllib.request.Request(
            f"https://api.github.com/repos/{REPOSITORY}/releases/latest",
            headers={"Accept": "application/vnd.github+json", "User-Agent": "sshyard-website"},
        )
        token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                data = json.load(r)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as e:
            if strict:
                fail(f"cannot read the latest release: {e}")
            print(f"warning: cannot read the latest release ({e}); using mygo.json", file=sys.stderr)
    if data is None:
        return fallback_release()

    tag = data["tag_name"]
    assets = {}
    for key, (pattern, _) in ASSETS.items():
        found = [a for a in data.get("assets", []) if re.fullmatch(pattern, a["name"])]
        if not found and key in OPTIONAL:
            continue
        if len(found) != 1:
            fail(f"release {tag}: expected one asset matching {pattern}, found {len(found)}")
        a = found[0]
        assets[key] = {"name": a["name"], "url": a["browser_download_url"], "size": a["size"]}
    published = dt.datetime.fromisoformat(data["published_at"].replace("Z", "+00:00"))
    return {"version": tag.removeprefix("v"), "tag": tag, "date": published.date(),
            "url": data["html_url"], "assets": assets}


def fallback_release() -> dict:
    v = json.loads((REPO / "mygo.json").read_text("utf-8"))["version"]
    tag = f"v{v}"
    assets = {}
    for key, (_, name) in ASSETS.items():
        name = name.format(v=v)
        assets[key] = {"name": name, "url": f"{GITHUB}/releases/download/{tag}/{name}", "size": None}
    return {"version": v, "tag": tag, "date": None, "url": f"{GITHUB}/releases/tag/{tag}", "assets": assets}


def size_range(release: dict) -> str:
    a = release["assets"]
    sizes = [a[k]["size"] for k in INSTALLERS if k in a and a[k]["size"]]
    if not sizes:
        return "10 MB"
    lo, hi = math.floor(min(sizes) / 1048576), math.ceil(max(sizes) / 1048576)
    return f"{lo} MB" if lo == hi else f"{lo}–{hi} MB"


def last_modified(release: dict) -> dt.date:
    """The later of the release and the last commit touching the site."""
    days = [release["date"]] if release["date"] else []
    try:
        out = subprocess.run(["git", "log", "-1", "--format=%cs", "--", "website", "docs"],
                             cwd=REPO, capture_output=True, text=True, check=True).stdout.strip()
        if out:
            days.append(dt.date.fromisoformat(out))
    except (OSError, subprocess.CalledProcessError, ValueError):
        pass
    return max(days) if days else dt.datetime.now(dt.timezone.utc).date()


# ---------------------------------------------------------------- assets


@functools.cache
def icon_svg(name: str) -> str:
    for d in (HERE / "icons", REPO / "internal" / "icons" / "svg"):
        p = d / f"{name}.svg"
        if p.exists():
            svg = p.read_text("utf-8").strip()
            return svg.replace("<svg ", '<svg class="i" aria-hidden="true" focusable="false" width="24" height="24" ', 1)
    fail(f"no icon {name}.svg in website/icons or internal/icons/svg")


@functools.cache
def file_icon_svg(name: str) -> str:
    """A Material Icon Theme file or folder icon, unchanged but for the
    attributes the page needs; the artwork keeps its own colours."""
    p = REPO / "internal" / "fileicons" / "svg" / f"{name}.svg"
    if not p.exists():
        fail(f"no file icon {name}.svg in internal/fileicons/svg")
    svg = p.read_text("utf-8").strip()
    return svg.replace("<svg ", f'<svg class="fi" data-fi="{name}" aria-hidden="true" focusable="false" width="16" height="16" ', 1)


def fingerprint(out: Path, rel: str, data: bytes) -> str:
    stem, dot, ext = rel.rpartition(".")
    name = f"{stem}.{hashlib.sha256(data).hexdigest()[:10]}.{ext}"
    (out / name).parent.mkdir(parents=True, exist_ok=True)
    (out / name).write_bytes(data)
    return name


def build_screenshots(out: Path) -> dict:
    shots = {}
    (out / "img").mkdir(parents=True, exist_ok=True)
    for key, name in SHOTS.items():
        src = Image.open(REPO / "docs" / f"{name}.webp")
        w, h = src.size
        widths = [x for x in SHOT_WIDTHS if x < w] + [w]
        srcset = []
        for width in widths:
            file = f"img/{name}-{width}.webp"
            img = src if width == w else src.resize((width, round(h * width / w)), Image.LANCZOS)
            img.save(out / file, "WEBP", quality=82, method=4)
            srcset.append((file, width))
        shots[key] = {"srcset": srcset, "width": w, "height": h, "src": srcset[min(1, len(srcset) - 1)][0]}
    return shots


def build_ai_brands(out: Path) -> list[dict]:
    (out / "img" / "ai").mkdir(parents=True, exist_ok=True)
    brands = []
    for name, label in AI_BRANDS:
        files = {}
        for theme in ("light", "dark"):
            rel = f"img/ai/{name}-{theme}.png"
            shutil.copyfile(REPO / "internal" / "aibrands" / "png" / theme / f"{name}.png", out / rel)
            files[theme] = rel
        brands.append({"name": label, **files})
    return brands


def build_icons(out: Path) -> None:
    src = Image.open(REPO / "resources" / "icon.png").convert("RGBA")
    for size in (32, 192, 512):
        src.resize((size, size), Image.LANCZOS).save(out / f"icon-{size}.png", optimize=True)
    for size in (64,):
        src.resize((size, size), Image.LANCZOS).save(out / f"logo-{size}.png", optimize=True)
    src.save(out / "favicon.ico", sizes=[(16, 16), (32, 32), (48, 48)])
    # Apple touch icons are drawn full-bleed and rounded by the system:
    # the squircle's own fill under the logo, without the clear margin.
    box = src.getchannel("A").point(lambda a: 255 if a > 200 else 0).getbbox()
    tile = Image.new("RGBA", src.size, src.getpixel((src.width // 2, box[1] + 8)))
    tile.alpha_composite(src)
    tile.crop(box).resize((180, 180), Image.LANCZOS).convert("RGB").save(out / "apple-touch-icon.png", optimize=True)


# ---------------------------------------------------------------- site


class Site:
    def __init__(self, base_url: str, out: Path, release: dict, cats: dict, og_images: bool):
        self.base_url = base_url.rstrip("/")
        self.base_path = urlsplit(self.base_url).path.rstrip("/")
        self.out = out
        self.release = release
        self.cats = cats
        self.og_images = og_images
        self.lastmod = last_modified(release)
        self.size_range = size_range(release)
        self.verify = [(name, os.environ[var].strip()) for name, var in VERIFICATION.items()
                       if os.environ.get(var, "").strip()]
        self.env = Environment(loader=FileSystemLoader(HERE / "templates"), autoescape=True,
                               undefined=StrictUndefined, trim_blocks=True, lstrip_blocks=True,
                               keep_trailing_newline=True)
        self.env.filters["fill"] = fill
        self.env.globals.update(icon=lambda name: Markup(icon_svg(name)),
                                file_icon=lambda name: Markup(file_icon_svg(name)), github=GITHUB,
                                platforms=PLATFORMS, platform_icons=PLATFORM_ICONS, format_size=format_size)

    # URLs ---------------------------------------------------------

    def path(self, page: str | Page, lang: Lang = DEFAULT) -> str:
        """The root-relative URL of a page in a language."""
        page = PAGE[page] if isinstance(page, str) else page
        prefix = f"{lang.prefix}/" if lang.prefix else ""
        return f"{self.base_path}/{prefix}{page.path}"

    def abs(self, path: str) -> str:
        """The absolute URL of a root-relative one."""
        return f"{self.base_url}{path[len(self.base_path):]}" if path.startswith(self.base_path) else path

    def file(self, rel: str) -> str:
        return f"{self.base_path}/{rel}"

    def values(self, lang: Lang | None = None) -> dict:
        r = self.release
        return {"version": r["version"], "size": self.size_range,
                "date": format_date(r["date"], (lang or DEFAULT).code),
                "year": dt.datetime.now(dt.timezone.utc).year}

    # Build --------------------------------------------------------

    def build(self) -> None:
        if self.out.exists():
            shutil.rmtree(self.out)
        self.out.mkdir(parents=True)
        build_icons(self.out)
        self.shots = build_screenshots(self.out)
        self.ai_brands = build_ai_brands(self.out)
        self.css = self.file(fingerprint(self.out, "assets/site.css",
                                         self.env.get_template("site.css").render().encode()))
        self.js = self.file(fingerprint(self.out, "assets/site.js", (HERE / "static" / "site.js").read_bytes()))
        self.og = self.build_og()
        for lang in LANGS:
            for page in PAGES:
                self.render_page(lang, page)
        self.render_404()
        self.write_seo_files()
        (self.out / ".nojekyll").write_text("")

    def build_og(self) -> dict:
        images = {}
        (self.out / "og").mkdir()
        shot = REPO / "docs" / f"{SHOTS['terminal']}.webp"
        logo = REPO / "resources" / "icon.png"
        for lang in LANGS:
            t = self.cats[lang.code]
            v = self.values(lang)
            platforms = " · ".join(t["download"]["platforms"][p]["name"] for p in PLATFORMS)
            specs = {
                "home": (t["meta"]["og_title"], t["meta"]["description"], shot),
                "download": ([t["download"]["title"]], fill(t["download"]["version"], **v),
                             REPO / "docs" / f"{SHOTS['sftp']}.webp"),
            }
            for kind, (title, sub, image) in specs.items():
                rel = f"og/{kind}-{lang.code}.png"
                if self.og_images:
                    og.render(self.out / rel, region=lang.font, title=title, subtitle=sub,
                              tags=[platforms, t["glance"]["rows"][0][1], f"v{v['version']}"],
                              logo=logo, shot=image)
                images[(lang.code, kind)] = self.abs(self.file(rel))
        return images

    def alternates(self, page: Page) -> list[dict]:
        alts = []
        for lang in LANGS:
            for h in lang.hreflang:
                alts.append({"hreflang": h, "href": self.abs(self.path(page, lang))})
        alts.append({"hreflang": "x-default", "href": self.abs(self.path(page, DEFAULT))})
        return alts

    def site_data(self, lang: Lang, page: Page) -> str:
        t = self.cats[lang.code]
        a = self.release["assets"]
        dl = {k: {"url": a[k]["url"], "size": format_size(a[k]["size"])} for k in INSTALLERS if k in a}
        data = {
            "lang": lang.code,
            "base": self.base_path,
            "copy": t["copy"],
            "banner": {l.code: {**self.cats[l.code]["banner"], "name": self.cats[l.code]["name"]} for l in LANGS},
            "downloads": dl,
            "cta": t["hero"]["cta_for"],
            "labels": {**t["download"]["platforms"]["macos"]["files"], **t["download"]["platforms"]["windows"]["files"],
                       **t["download"]["platforms"]["linux"]["files"]},
            "linuxPage": self.path("download-linux", lang),
            "theme": {"auto": t["nav"]["theme_auto"], "light": t["nav"]["theme_light"], "dark": t["nav"]["theme_dark"]},
            "palette": t["palette"],
        }
        if page.key == "home":
            # The demo window's strings; its hosts, files and shell are in site.js.
            data["demo"] = {**t["demo"], "modes": t["ai"]["modes"], "card": t["ai"]["card"]}
        return Markup(script_json(data))

    def render_page(self, lang: Lang, page: Page) -> None:
        t = self.cats[lang.code]
        v = self.values(lang)
        if page.key == "home":
            title, desc = t["meta"]["title"], t["meta"]["description"]
        elif page.platform:
            p = t["download"]["platforms"][page.platform]
            title = fill(t["download"]["for_platform"], platform=p["name"])
            desc = fill(p["description"], **v)
        else:
            title, desc = t["meta"]["download_title"], fill(t["meta"]["download_description"], **v)
        path = self.path(page, lang)
        ctx = {
            "t": t, "lang": lang, "langs": LANGS, "cats": self.cats, "page": page, "v": v,
            "release": self.release, "title": title, "description": desc,
            "canonical": self.abs(path), "alternates": self.alternates(page),
            "url": lambda key, l=lang: self.path(key, l), "file": self.file,
            "lang_url": lambda l, p=page: self.path(p, l),
            "og_image": self.og[(lang.code, "home" if page.key == "home" else "download")],
            "og_locales": [l.og_locale for l in LANGS if l != lang],
            "css": self.css, "js": self.js, "shots": self.shots, "verify": self.verify,
            "ai_brands": self.ai_brands,
            "markdown": self.file(f"{lang.prefix + '/' if lang.prefix else ''}index.md") if page.key == "home" else None,
            "jsonld": Markup(script_json(self.jsonld(lang, page, title, desc))),
            "site_data": self.site_data(lang, page),
        }
        html = self.env.get_template(page.template).render(**ctx)
        dest = self.out / path[len(self.base_path) + 1:] / "index.html"
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(html, "utf-8")

    def render_404(self) -> None:
        lang, page = DEFAULT, PAGE["home"]
        t = self.cats[lang.code]
        notfound = {l.code: {**self.cats[l.code]["notfound"], "href": self.path("home", l), "prefix": l.prefix}
                    for l in LANGS}
        html = self.env.get_template("404.html").render(
            t=t, lang=lang, langs=LANGS, cats=self.cats, page=Page("404", "", "404.html"), v=self.values(),
            release=self.release, title=t["meta"]["notfound_title"], description=t["notfound"]["text"],
            canonical=None, alternates=[], url=lambda key, l=lang: self.path(key, l), file=self.file,
            lang_url=lambda l: self.path("home", l), og_image=self.og[(lang.code, "home")], og_locales=[],
            css=self.css, js=self.js, shots=self.shots, verify=[], markdown=None, jsonld=None,
            site_data=self.site_data(lang, page), notfound=Markup(script_json(notfound)),
        )
        (self.out / "404.html").write_text(html, "utf-8")

    # Structured data ----------------------------------------------

    def jsonld(self, lang: Lang, page: Page, title: str, desc: str) -> dict:
        t = self.cats[lang.code]
        v = self.values(lang)
        home = self.abs(self.path("home", lang))
        url = self.abs(self.path(page, lang))
        root = self.abs(self.path("home"))
        app = {
            "@type": "SoftwareApplication",
            "@id": f"{root}#software",
            "name": "SSHYard",
            "url": home,
            "description": t["meta"]["description"],
            "applicationCategory": "DeveloperApplication",
            "applicationSubCategory": "SSH client",
            "operatingSystem": "macOS 12+, Windows (x64, Arm64), Linux (x64, Arm64)",
            "softwareVersion": self.release["version"],
            "downloadUrl": self.abs(self.path("download", lang)),
            "installUrl": self.abs(self.path("download", lang)),
            "releaseNotes": self.release["url"],
            "offers": {"@type": "Offer", "price": "0", "priceCurrency": "USD"},
            "isAccessibleForFree": True,
            "license": LICENSE_URL,
            "image": self.abs(self.file("icon-512.png")),
            "screenshot": [self.abs(self.file(s["srcset"][-1][0])) for s in self.shots.values()],
            "featureList": [f"{f['title']}: {f['text']}" for f in t["features"]["items"]],
            "inLanguage": [l.code for l in LANGS],
            "author": {"@id": f"{AUTHOR}#person"},
            "sameAs": [GITHUB],
            "subjectOf": {"@type": "SoftwareSourceCode", "codeRepository": GITHUB,
                          "programmingLanguage": "Go", "license": LICENSE_URL},
        }
        if self.release["date"]:
            app["dateModified"] = self.release["date"].isoformat()
        graph = [
            {"@type": "WebSite", "@id": f"{root}#website", "url": root, "name": "SSHYard",
             "inLanguage": [l.code for l in LANGS], "publisher": {"@id": f"{AUTHOR}#person"}},
            {"@type": "Person", "@id": f"{AUTHOR}#person", "name": "Licoy", "url": AUTHOR},
            app,
            {"@type": "WebPage", "@id": f"{url}#webpage", "url": url, "name": title, "description": desc,
             "inLanguage": lang.code, "isPartOf": {"@id": f"{root}#website"}, "about": {"@id": f"{root}#software"},
             "dateModified": self.lastmod.isoformat(),
             "primaryImageOfPage": {"@type": "ImageObject",
                                    "url": self.og[(lang.code, "home" if page.key == "home" else "download")],
                                    "width": og.W, "height": og.H}},
        ]
        if page.key == "home":
            graph.append({"@type": "FAQPage", "@id": f"{url}#faq", "inLanguage": lang.code, "mainEntity": [
                {"@type": "Question", "name": q["q"],
                 "acceptedAnswer": {"@type": "Answer", "text": fill(q["a"], **v)}}
                for q in t["faq"]["items"]]})
        else:
            crumbs = [(t["nav"]["home"], home), (t["download"]["title"], self.abs(self.path("download", lang)))]
            if page.platform:
                crumbs.append((t["download"]["platforms"][page.platform]["name"], url))
            graph.append({"@type": "BreadcrumbList", "itemListElement": [
                {"@type": "ListItem", "position": i + 1, "name": n, "item": u} for i, (n, u) in enumerate(crumbs)]})
        return {"@context": "https://schema.org", "@graph": graph}

    # Crawlers -----------------------------------------------------

    def write_seo_files(self) -> None:
        lastmod = self.lastmod.isoformat()
        urls = []
        lines = ['<?xml version="1.0" encoding="UTF-8"?>',
                 '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">']
        for page in PAGES:
            alts = self.alternates(page)
            for lang in LANGS:
                loc = self.abs(self.path(page, lang))
                urls.append(loc)
                lines.append(f"  <url>\n    <loc>{loc}</loc>\n    <lastmod>{lastmod}</lastmod>")
                lines += [f'    <xhtml:link rel="alternate" hreflang="{a["hreflang"]}" href="{a["href"]}"/>' for a in alts]
                lines.append("  </url>")
        lines.append("</urlset>")
        (self.out / "sitemap.xml").write_text("\n".join(lines) + "\n", "utf-8")

        robots = ["User-agent: *", "Allow: /", ""]
        robots += ["# Search and answer engines are welcome to read and cite every page."]
        for bot in AI_CRAWLERS:
            robots += [f"User-agent: {bot}", "Allow: /", ""]
        robots.append(f"Sitemap: {self.abs(self.file('sitemap.xml'))}")
        (self.out / "robots.txt").write_text("\n".join(robots) + "\n", "utf-8")

        key = os.environ.get("INDEXNOW_KEY", "").strip()
        if key:
            if not re.fullmatch(r"[A-Za-z0-9-]{8,128}", key):
                fail("INDEXNOW_KEY must be 8 to 128 letters, digits or dashes")
            (self.out / f"{key}.txt").write_text(key, "utf-8")
            (self.out / "indexnow.json").write_text(json.dumps({
                "host": urlsplit(self.base_url).hostname, "key": key,
                "keyLocation": self.abs(self.file(f"{key}.txt")), "urlList": urls}, indent=2), "utf-8")

        manifest = {
            "name": "SSHYard", "short_name": "SSHYard", "description": self.cats[DEFAULT.code]["meta"]["description"],
            "start_url": self.path("home"), "scope": f"{self.base_path}/", "display": "browser",
            "background_color": "#F5F5F7", "theme_color": "#1984FC",
            "icons": [{"src": self.file(f"icon-{s}.png"), "sizes": f"{s}x{s}", "type": "image/png"} for s in (192, 512)],
        }
        (self.out / "manifest.webmanifest").write_text(json.dumps(manifest, ensure_ascii=False, indent=2), "utf-8")

        for lang in LANGS:
            md = self.markdown(lang)
            (self.out / lang.prefix / "index.md").write_text(md, "utf-8")
        (self.out / "llms.txt").write_text(self.llms(), "utf-8")
        (self.out / "llms-full.txt").write_text(
            self.markdown(DEFAULT) + "\n## Other languages\n\n" + "".join(
                f"- [{self.cats[l.code]['name']}]({self.abs(self.file((l.prefix + '/' if l.prefix else '') + 'index.md'))})\n"
                for l in LANGS[1:]), "utf-8")

    def markdown(self, lang: Lang) -> str:
        """The home page as Markdown, for answer engines and llms-full.txt."""
        t = self.cats[lang.code]
        v = self.values(lang)
        f = lambda s: fill(s, **v)
        out = [f"# SSHYard\n\n> {t['meta']['description']}\n\n{t['hero']['lead']}\n",
               f"## {t['glance']['title']}\n"]
        out += [f"- **{k}**: {f(val)}" for k, val in t["glance"]["rows"]]
        out.append(f"\n## {t['features']['title']}\n\n{t['features']['lead']}\n")
        for item in t["features"]["items"]:
            out.append(f"### {item['title']}\n\n{item['text']}\n")
            out += [f"- {p}" for p in item["points"]]
            out.append("")
        out.append(f"## {t['ai']['title']}\n\n{t['ai']['lead']}\n")
        out += [f"- **{m['label']}**: {m['text']}" for m in t["ai"]["modes"]]
        out.append(f"\n### {t['ai']['providers_title']}\n\n{t['ai']['providers_text']}\n")
        out.append(f"### {t['ai']['agents_title']}\n\n{t['ai']['agents_text']}\n")
        out += [f"- {p}" for p in t["ai"]["agents_points"]]
        out.append(f"\n## {t['security']['title']}\n\n{t['security']['lead']}\n")
        out += [f"- **{s['title']}**: {s['text']}" for s in t["security"]["steps"]]
        out += [f"- {p}" for p in t["security"]["points"]]
        line = f(t["download"]["version_date"] if self.release["date"] else t["download"]["version"])
        out.append(f"\n## {t['download']['title']}\n\n{line}. [{t['download']['all']}]({self.abs(self.path('download', lang))})\n")
        a = self.release["assets"]
        for p in PLATFORMS:
            info = t["download"]["platforms"][p]
            out.append(f"### {info['name']}\n\n{info['req']}\n")
            out += [f"- [{label}]({a[k]['url']})" + (f" ({format_size(a[k]['size'])})" if a[k]["size"] else "")
                    for k, label in info["files"].items() if k in a]
            out.append("")
        out.append(f"- [{t['download']['checksums']}]({a['sha256sums']['url']})\n")
        out.append(f"## {t['faq']['title']}\n")
        for q in t["faq"]["items"]:
            out.append(f"### {q['q']}\n\n{f(q['a'])}\n")
        out.append(f"---\n\n- {GITHUB}\n- {f(t['footer']['copyright'])}\n")
        # U+200B only marks where headings may wrap on the page.
        return "\n".join(out).replace("\u200b", "")

    def llms(self) -> str:
        t = self.cats[DEFAULT.code]
        v = self.values()
        a = self.release["assets"]
        out = [f"# SSHYard\n\n> {t['meta']['description']}\n"]
        out += [f"- {k}: {fill(val, **v)}" for k, val in t["glance"]["rows"]]
        out.append(f"- Latest version: {v['version']}" + (f" ({self.release['date'].isoformat()})" if self.release["date"] else ""))
        out.append("\n## Pages\n")
        for lang in LANGS:
            c = self.cats[lang.code]
            out.append(f"- [{c['name']}]({self.abs(self.path('home', lang))}): {c['meta']['description']}")
        out.append(f"- [Download]({self.abs(self.path('download'))}): {fill(t['meta']['download_description'], **v)}")
        out.append("\n## Downloads\n")
        for p in PLATFORMS:
            info = t["download"]["platforms"][p]
            out += [f"- [{info['name']} {label}]({a[k]['url']})" for k, label in info["files"].items() if k in a]
        out.append(f"- [SHA256SUMS]({a['sha256sums']['url']})")
        out.append("\n## Project\n")
        out.append(f"- [Source code on GitHub]({GITHUB})")
        out.append(f"- [Release notes]({self.release['url']})")
        out.append(f"- [License: GNU AGPL-3.0]({LICENSE_URL})")
        out.append("\n## Optional\n")
        out.append(f"- [Full description in Markdown]({self.abs(self.file('llms-full.txt'))})")
        return "\n".join(out) + "\n"


def script_json(data) -> str:
    """JSON safe to put inside a <script> element."""
    return (json.dumps(data, ensure_ascii=False, separators=(",", ":"), default=str)
            .replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026"))


# ---------------------------------------------------------------- main


def serve(out: Path, port: int) -> None:
    class Handler(http.server.SimpleHTTPRequestHandler):
        def __init__(self, *a, **kw):
            super().__init__(*a, directory=str(out), **kw)

        def send_error(self, code, message=None, explain=None):
            if code == 404 and (out / "404.html").exists():
                body = (out / "404.html").read_bytes()
                self.send_response(404)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return
            super().send_error(code, message, explain)

    Handler.extensions_map.update({".webmanifest": "application/manifest+json", ".md": "text/markdown; charset=utf-8"})
    # The default listen backlog of 5 drops connections when a browser opens many at once.
    http.server.ThreadingHTTPServer.request_queue_size = 128
    with http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler) as httpd:
        print(f"Serving http://127.0.0.1:{port}/ (Ctrl+C to stop)")
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            pass


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base-url", default=os.environ.get("SITE_URL") or DEFAULT_BASE_URL,
                    help="absolute URL the site is served from (env SITE_URL)")
    ap.add_argument("--out", default=str(HERE / "_site"), help="output directory")
    ap.add_argument("--release", help="GitHub release JSON to use instead of fetching the latest")
    ap.add_argument("--strict", action="store_true", help="fail instead of falling back when the release can't be read")
    ap.add_argument("--no-og", action="store_true", help="skip drawing Open Graph images (no font download)")
    ap.add_argument("--serve", nargs="?", const=8000, type=int, metavar="PORT",
                    help="build for http://127.0.0.1:PORT and serve it")
    args = ap.parse_args()

    base_url = f"http://127.0.0.1:{args.serve}" if args.serve else args.base_url
    if not re.fullmatch(r"https?://[^/\s]+(/[^\s]*)?", base_url):
        fail(f"--base-url must be an absolute http(s) URL, not {base_url!r}")
    out = Path(args.out).resolve()
    site = Site(base_url, out, read_release(args.release, args.strict), load_catalogs(), not args.no_og)
    site.build()
    pages = len(LANGS) * len(PAGES)
    print(f"Built {pages} pages in {len(LANGS)} languages for {site.base_url} into {out}"
          f" (release {site.release['tag']})")
    if args.serve:
        serve(out, args.serve)


if __name__ == "__main__":
    main()
