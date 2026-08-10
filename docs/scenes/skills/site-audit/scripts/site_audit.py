#!/usr/bin/env python3
"""site_audit.py — deterministic engine for technical SEO / GEO site audits.

Given an HTML page (fetched from a URL, or handed in as a local file / stdin so
the agent can pre-fetch via the `fetch` MCP and stay fully offline), this engine
does the parts of a site audit that MUST be deterministic and reproducible:

  audit         run the full checklist over a page -> audit-report.{json,md}
                (title / meta / canonical / robots / headings / img alt /
                 mobile / Open Graph / JSON-LD structured data / speed signals),
                score it, and register the report artifact.
  validate-jsonld
                extract & validate every JSON-LD / schema.org block on a page
                (or a standalone .json file) against schema.org shape rules.
  selftest      self-check the checks + JSON-LD validation on synthetic input
                (no network).

The agent drives the fuzzy parts (which URL, whether findings are intentional,
turning findings into an action plan). This engine only reasons about HTML that
is already in hand — it never invents results.

Zero third-party dependencies: Python 3.8+ standard library only (html.parser,
urllib, json, re). Works on Windows and POSIX. A page it cannot fetch is
reported as an error finding, never a crash.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from datetime import datetime, timezone
from html.parser import HTMLParser
from urllib.parse import urljoin, urlparse

# Severity weights: how many points each failing check subtracts from 100.
# A warn costs half. P0 = breaks indexing/ranking, P3 = minor polish.
SEVERITY_WEIGHT = {"P0": 25, "P1": 12, "P2": 6, "P3": 2}

# schema.org types -> properties that must be present to be valid, and ones that
# are strongly recommended (their absence is a warning, not a failure).
SCHEMA_REQUIRED = {
    "Article": ["headline"],
    "NewsArticle": ["headline"],
    "BlogPosting": ["headline"],
    "Product": ["name"],
    "Organization": ["name"],
    "WebSite": ["name"],
    "FAQPage": ["mainEntity"],
    "HowTo": ["name", "step"],
    "BreadcrumbList": ["itemListElement"],
    "Recipe": ["name"],
    "Event": ["name", "startDate"],
    "Person": ["name"],
    "LocalBusiness": ["name"],
    "VideoObject": ["name", "thumbnailUrl", "uploadDate"],
    "WebPage": ["name"],
    "Review": ["reviewRating"],
}
SCHEMA_RECOMMENDED = {
    "Article": ["author", "datePublished", "image"],
    "NewsArticle": ["author", "datePublished", "image"],
    "BlogPosting": ["author", "datePublished", "image"],
    "Product": ["image", "description", "offers"],
    "Organization": ["url", "logo"],
    "Recipe": ["image", "recipeIngredient", "recipeInstructions"],
    "Event": ["location"],
    "LocalBusiness": ["address", "telephone"],
}


# ---------------------------------------------------------------------------
# HTML parsing (the deterministic core)
# ---------------------------------------------------------------------------

class PageParser(HTMLParser):
    """Collect the SEO-relevant surface of an HTML document in one pass."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.title_parts = []
        self._in_title = False
        self.metas = []          # list of {name/property: value, content}
        self.links = []          # list of {rel, href}
        self.html_lang = None
        self.headings = []       # list of (level:int, text:str)
        self._heading_level = 0
        self._heading_text = []
        self.imgs = []           # list of {alt: str|None, src}
        self.jsonld_raw = []     # raw text of each ld+json script block
        self._in_jsonld = False
        self._jsonld_buf = []
        self.charset = None
        self.paragraphs = []     # text of <p> blocks (AI-extraction signal)
        self._in_p = False
        self._p_text = []

    def handle_starttag(self, tag, attrs):
        a = {k.lower(): (v or "") for k, v in attrs}
        if tag == "html":
            self.html_lang = a.get("lang") or self.html_lang
        elif tag == "title":
            self._in_title = True
        elif tag == "meta":
            if a.get("charset"):
                self.charset = a["charset"]
            self.metas.append(a)
        elif tag == "link":
            self.links.append(a)
        elif tag in ("h1", "h2", "h3", "h4", "h5", "h6"):
            self._heading_level = int(tag[1])
            self._heading_text = []
        elif tag == "img":
            self.imgs.append({"alt": a.get("alt"), "src": a.get("src", "")})
        elif tag == "script" and a.get("type", "").lower() == "application/ld+json":
            self._in_jsonld = True
            self._jsonld_buf = []
        elif tag == "p":
            self._in_p = True
            self._p_text = []

    def handle_endtag(self, tag):
        if tag == "title":
            self._in_title = False
        elif tag in ("h1", "h2", "h3", "h4", "h5", "h6") and self._heading_level:
            text = " ".join("".join(self._heading_text).split())
            self.headings.append((self._heading_level, text))
            self._heading_level = 0
        elif tag == "script" and self._in_jsonld:
            self.jsonld_raw.append("".join(self._jsonld_buf))
            self._in_jsonld = False
        elif tag == "p" and self._in_p:
            text = " ".join("".join(self._p_text).split())
            if text:
                self.paragraphs.append(text)
            self._in_p = False

    def handle_data(self, data):
        if self._in_title:
            self.title_parts.append(data)
        if self._heading_level:
            self._heading_text.append(data)
        if self._in_jsonld:
            self._jsonld_buf.append(data)
        if self._in_p:
            self._p_text.append(data)

    # ld+json bodies can contain HTML entities / CDATA-ish content; keep raw.
    def handle_entityref(self, name):
        if self._in_jsonld:
            self._jsonld_buf.append(f"&{name};")

    def handle_charref(self, name):
        if self._in_jsonld:
            self._jsonld_buf.append(f"&#{name};")

    # -- convenience accessors -------------------------------------------------
    @property
    def title(self):
        return " ".join("".join(self.title_parts).split())

    def meta_content(self, key, value):
        """First <meta {key}={value}> content, case-insensitive."""
        value = value.lower()
        for m in self.metas:
            if (m.get(key, "").lower() == value):
                return m.get("content", "")
        return None

    def link_href(self, rel):
        rel = rel.lower()
        for l in self.links:
            rels = l.get("rel", "").lower().split()
            if rel in rels:
                return l.get("href", "")
        return None


def parse_html(html: str) -> PageParser:
    p = PageParser()
    p.feed(html)
    p.close()
    return p


# ---------------------------------------------------------------------------
# fetching (optional — engine can also be handed pre-fetched HTML)
# ---------------------------------------------------------------------------

def fetch_page(target: str, timeout: int = 20) -> dict:
    """Fetch a URL (stdlib urllib) or read a local file.

    Returns {ok, url, status, headers, html, elapsed_ms, bytes, error}. Never
    raises for network/HTTP errors — they land in `error` so a partial audit can
    still run on whatever was retrieved.
    """
    # Local file path (or file:// URL) — pre-fetched HTML handed to the engine.
    if os.path.exists(target):
        with open(target, "r", encoding="utf-8", errors="replace") as f:
            html = f.read()
        return {"ok": True, "url": target, "status": 200, "headers": {},
                "html": html, "elapsed_ms": 0, "bytes": len(html.encode("utf-8")),
                "error": None, "source": "file"}

    import urllib.error
    import urllib.request
    req = urllib.request.Request(target, headers={
        "User-Agent": "niuniu-site-audit/1.0 (+https://niu6ai.com)",
        "Accept": "text/html,application/xhtml+xml",
    })
    t0 = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            elapsed = int((time.monotonic() - t0) * 1000)
            charset = resp.headers.get_content_charset() or "utf-8"
            html = raw.decode(charset, errors="replace")
            headers = {k.lower(): v for k, v in resp.headers.items()}
            return {"ok": True, "url": resp.geturl(), "status": resp.status,
                    "headers": headers, "html": html, "elapsed_ms": elapsed,
                    "bytes": len(raw), "error": None, "source": "url"}
    except urllib.error.HTTPError as e:
        elapsed = int((time.monotonic() - t0) * 1000)
        return {"ok": False, "url": target, "status": e.code, "headers": {},
                "html": "", "elapsed_ms": elapsed, "bytes": 0,
                "error": f"HTTP {e.code} {e.reason}", "source": "url"}
    except Exception as e:  # URLError, timeout, DNS, TLS...
        return {"ok": False, "url": target, "status": 0, "headers": {},
                "html": "", "elapsed_ms": 0, "bytes": 0,
                "error": f"{type(e).__name__}: {e}", "source": "url"}


def fetch_robots(page_url: str, timeout: int = 10):
    """Return (allowed: bool|None, reason: str) for `*` on page_url per robots.txt.

    None => couldn't determine (no robots.txt, fetch failed). Uses stdlib
    urllib.robotparser. Skipped for local-file audits.
    """
    parsed = urlparse(page_url)
    if parsed.scheme not in ("http", "https"):
        return None, "not an http(s) URL"
    robots_url = f"{parsed.scheme}://{parsed.netloc}/robots.txt"
    import urllib.robotparser
    rp = urllib.robotparser.RobotFileParser()
    try:
        import urllib.request
        req = urllib.request.Request(robots_url, headers={"User-Agent": "niuniu-site-audit/1.0"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            rp.parse(resp.read().decode("utf-8", errors="replace").splitlines())
    except Exception as e:
        return None, f"robots.txt unavailable ({type(e).__name__})"
    allowed = rp.can_fetch("*", page_url)
    return allowed, robots_url


# ---------------------------------------------------------------------------
# JSON-LD / schema.org validation
# ---------------------------------------------------------------------------

def _types_of(node: dict) -> list:
    t = node.get("@type")
    if isinstance(t, list):
        return [str(x) for x in t]
    return [str(t)] if t else []


def iter_schema_nodes(obj):
    """Yield every dict that carries an @type, unwrapping @graph and lists."""
    if isinstance(obj, list):
        for it in obj:
            yield from iter_schema_nodes(it)
    elif isinstance(obj, dict):
        if "@graph" in obj and isinstance(obj["@graph"], list):
            for it in obj["@graph"]:
                yield from iter_schema_nodes(it)
        if _types_of(obj):
            yield obj


def _has(node: dict, prop: str) -> bool:
    v = node.get(prop)
    if v is None:
        return False
    if isinstance(v, str):
        return v.strip() != ""
    if isinstance(v, (list, dict)):
        return len(v) > 0
    return True


def validate_node(node: dict) -> dict:
    """Validate one schema.org node; return {types, errors, warnings}."""
    types = _types_of(node)
    errors, warnings = [], []

    ctx = node.get("@context")
    if ctx is not None:
        ctx_str = json.dumps(ctx, ensure_ascii=False).lower()
        if "schema.org" not in ctx_str:
            warnings.append("@context 未指向 schema.org")

    for t in types:
        for prop in SCHEMA_REQUIRED.get(t, []):
            if not _has(node, prop):
                errors.append(f"{t} 缺少必填属性 `{prop}`")
        for prop in SCHEMA_RECOMMENDED.get(t, []):
            if not _has(node, prop):
                warnings.append(f"{t} 建议补充 `{prop}`（利于富媒体结果/AI 抽取）")

        # Nested shape checks for the highest-value types.
        if t == "FAQPage":
            main = node.get("mainEntity")
            items = main if isinstance(main, list) else ([main] if main else [])
            if not items:
                errors.append("FAQPage.mainEntity 为空（应为一组 Question）")
            for q in items:
                if not isinstance(q, dict):
                    continue
                if "Question" not in _types_of(q):
                    warnings.append("FAQPage.mainEntity 项的 @type 应为 Question")
                if not _has(q, "name"):
                    errors.append("Question 缺少 `name`（问题文本）")
                ans = q.get("acceptedAnswer")
                if not (isinstance(ans, dict) and _has(ans, "text")):
                    errors.append("Question.acceptedAnswer 缺少含 `text` 的 Answer")
        if t == "HowTo":
            steps = node.get("step")
            steps = steps if isinstance(steps, list) else ([steps] if steps else [])
            for s in steps:
                if isinstance(s, dict) and not (_has(s, "text") or _has(s, "name")):
                    warnings.append("HowTo.step 建议含 `text` 或 `name`")
        if t == "BreadcrumbList":
            items = node.get("itemListElement")
            if not (isinstance(items, list) and items):
                errors.append("BreadcrumbList.itemListElement 应为非空数组")

    if not types:
        errors.append("节点缺少 @type")
    return {"types": types, "errors": errors, "warnings": warnings}


def validate_jsonld_blocks(raw_blocks: list) -> dict:
    """Parse & validate a list of raw ld+json script bodies."""
    blocks = []
    total_err = total_warn = valid_nodes = 0
    for i, raw in enumerate(raw_blocks):
        entry = {"index": i, "parse_ok": False, "nodes": [], "error": None}
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as e:
            entry["error"] = f"JSON 语法错误：{e}"
            total_err += 1
            blocks.append(entry)
            continue
        entry["parse_ok"] = True
        nodes = list(iter_schema_nodes(data))
        if not nodes:
            entry["error"] = "未找到带 @type 的 schema.org 节点"
            total_warn += 1
        for n in nodes:
            res = validate_node(n)
            entry["nodes"].append(res)
            total_err += len(res["errors"])
            total_warn += len(res["warnings"])
            if not res["errors"]:
                valid_nodes += 1
        blocks.append(entry)
    return {
        "block_count": len(raw_blocks),
        "valid_node_count": valid_nodes,
        "error_count": total_err,
        "warning_count": total_warn,
        "blocks": blocks,
    }


# ---------------------------------------------------------------------------
# the page checklist
# ---------------------------------------------------------------------------

def _finding(check, status, severity, evidence, fix=""):
    return {"check": check, "status": status, "severity": severity,
            "evidence": evidence, "fix": fix}


def run_checks(page: PageParser, fetched: dict, robots) -> list:
    """Produce the ordered list of checklist findings for one page."""
    F = []

    # --- crawlability -------------------------------------------------------
    robots_meta = (page.meta_content("name", "robots") or "").lower()
    xrobots = (fetched.get("headers", {}) or {}).get("x-robots-tag", "").lower()
    noindex = "noindex" in robots_meta or "noindex" in xrobots
    if noindex:
        F.append(_finding("可索引性 robots", "fail", "P0",
                          f"检测到 noindex（meta='{robots_meta}' header='{xrobots}'）",
                          "若该页应被搜索引擎收录，移除 noindex；若是有意屏蔽可忽略此项。"))
    else:
        F.append(_finding("可索引性 robots", "pass", "P0",
                          f"无 noindex（meta robots='{robots_meta or '—'}')", ""))

    allowed, robots_reason = (robots if isinstance(robots, tuple) else (robots, ""))
    if allowed is False:
        F.append(_finding("robots.txt 允许抓取", "fail", "P0",
                          f"robots.txt 禁止抓取该 URL（{robots_reason}）",
                          "在 robots.txt 放开该路径，否则搜索引擎/生成式引擎都抓不到。"))
    elif allowed is True:
        F.append(_finding("robots.txt 允许抓取", "pass", "P0", "robots.txt 允许 `*` 抓取", ""))
    else:
        F.append(_finding("robots.txt 允许抓取", "warn", "P3",
                          f"未能判定（{robots_reason}）", "手工确认 robots.txt。"))

    # --- canonical ----------------------------------------------------------
    canonical = page.link_href("canonical")
    if canonical:
        F.append(_finding("canonical 链接", "pass", "P1", f"canonical = {canonical}", ""))
    else:
        F.append(_finding("canonical 链接", "warn", "P1", "未声明 <link rel=canonical>",
                          "加 canonical 指向该页规范 URL，避免重复内容分散权重。"))

    # --- title --------------------------------------------------------------
    title = page.title
    if not title:
        F.append(_finding("标题 <title>", "fail", "P0", "页面无 <title>",
                          "补一个含目标关键词、30–60 字符的标题。"))
    elif not (10 <= len(title) <= 65):
        F.append(_finding("标题 <title>", "warn", "P2",
                          f"标题长度 {len(title)}（建议约 30–60）：{title!r}",
                          "调整到 ~30–60 字符：太短信息不足，太长会被 SERP 截断。"))
    else:
        F.append(_finding("标题 <title>", "pass", "P0", f"{title!r}（{len(title)} 字符）", ""))

    # --- meta description ---------------------------------------------------
    desc = page.meta_content("name", "description")
    if not desc:
        F.append(_finding("meta description", "warn", "P1", "无 meta description",
                          "写 50–160 字符、含关键词并能诱导点击的描述。"))
    elif not (50 <= len(desc) <= 160):
        F.append(_finding("meta description", "warn", "P2",
                          f"长度 {len(desc)}（建议 50–160）",
                          "调整到 50–160 字符，避免 SERP 截断或信息过少。"))
    else:
        F.append(_finding("meta description", "pass", "P1", f"{len(desc)} 字符", ""))

    # --- lang / charset / viewport ------------------------------------------
    if page.html_lang:
        F.append(_finding("html lang", "pass", "P2", f"lang = {page.html_lang}", ""))
    else:
        F.append(_finding("html lang", "warn", "P2", "<html> 缺少 lang 属性",
                          "加 <html lang=\"...\">，帮助引擎判定语言与地区。"))

    if page.charset or page.meta_content("http-equiv", "content-type"):
        F.append(_finding("字符集 charset", "pass", "P3", f"charset = {page.charset or 'via http-equiv'}", ""))
    else:
        F.append(_finding("字符集 charset", "warn", "P3", "未声明 charset",
                          "在 <head> 顶部加 <meta charset=\"utf-8\">。"))

    viewport = page.meta_content("name", "viewport")
    if viewport:
        F.append(_finding("移动端 viewport", "pass", "P1", f"viewport = {viewport}", ""))
    else:
        F.append(_finding("移动端 viewport", "fail", "P1", "无 viewport meta",
                          "加 <meta name=viewport content=\"width=device-width, initial-scale=1\">。"))

    # --- headings -----------------------------------------------------------
    h1s = [t for lvl, t in page.headings if lvl == 1]
    if len(h1s) == 1:
        F.append(_finding("H1 标题", "pass", "P1", f"1 个 H1：{h1s[0]!r}", ""))
    elif len(h1s) == 0:
        F.append(_finding("H1 标题", "fail", "P1", "页面无 H1",
                          "补一个描述页面主题的 H1（利于 SEO 与 AI 抽取主旨）。"))
    else:
        F.append(_finding("H1 标题", "warn", "P2", f"{len(h1s)} 个 H1",
                          "保留单一 H1，其余降级为 H2/H3，保持清晰层级。"))

    # heading hierarchy: flag a jump of >1 level (e.g. H2 -> H4)
    levels = [lvl for lvl, _ in page.headings]
    skips = [(levels[i - 1], levels[i]) for i in range(1, len(levels))
             if levels[i] - levels[i - 1] > 1]
    if skips:
        F.append(_finding("标题层级连续性", "warn", "P3",
                          f"存在跳级：{skips[:3]}",
                          "标题层级逐级递进（H2→H3），别跳级，利于结构解析。"))
    else:
        F.append(_finding("标题层级连续性", "pass", "P3",
                          f"{len(page.headings)} 个标题层级连续", ""))

    # --- images / alt -------------------------------------------------------
    imgs = page.imgs
    if imgs:
        missing = [im for im in imgs if not (im.get("alt") or "").strip()]
        if missing:
            F.append(_finding("图片 alt 覆盖", "warn", "P2",
                              f"{len(missing)}/{len(imgs)} 张图片缺 alt",
                              "为有信息价值的图片补 alt 文本（无障碍 + 图片搜索）。"))
        else:
            F.append(_finding("图片 alt 覆盖", "pass", "P2", f"{len(imgs)} 张图片均有 alt", ""))
    else:
        F.append(_finding("图片 alt 覆盖", "pass", "P3", "页面无 <img>", ""))

    # --- Open Graph / social ------------------------------------------------
    og_title = page.meta_content("property", "og:title")
    if og_title:
        F.append(_finding("Open Graph 社交卡片", "pass", "P3",
                          "含 og:title 等 OG 标签", ""))
    else:
        F.append(_finding("Open Graph 社交卡片", "warn", "P3", "无 Open Graph 标签",
                          "补 og:title/og:description/og:image，改善社媒与部分引擎抓取。"))

    # --- structured data ----------------------------------------------------
    if page.jsonld_raw:
        v = validate_jsonld_blocks(page.jsonld_raw)
        types = sorted({t for b in v["blocks"] for n in b["nodes"] for t in n["types"]})
        if v["error_count"] == 0:
            F.append(_finding("结构化数据 JSON-LD", "pass", "P1",
                              f"{v['block_count']} 块 / 类型 {types or '—'} / 无错误", ""))
        else:
            errs = [e for b in v["blocks"] for n in b["nodes"] for e in n["errors"]]
            errs += [b["error"] for b in v["blocks"] if b.get("error")]
            F.append(_finding("结构化数据 JSON-LD", "fail", "P1",
                              f"{v['error_count']} 个错误：{'; '.join(errs[:4])}",
                              "修正 JSON-LD：补必填属性 / 修 JSON 语法，见 validate-jsonld 详表。"))
    else:
        F.append(_finding("结构化数据 JSON-LD", "warn", "P1", "页面无 JSON-LD 结构化数据",
                          "按页面类型补 JSON-LD（Article/FAQPage/Product…），显著利于富媒体结果与 AI 引用。"))

    # --- AI-extraction friendliness (GEO) -----------------------------------
    lead = page.paragraphs[0] if page.paragraphs else ""
    has_faq = any("Question" in n["types"]
                  for b in validate_jsonld_blocks(page.jsonld_raw)["blocks"]
                  for n in b["nodes"]) if page.jsonld_raw else False
    if lead and len(lead) >= 40:
        F.append(_finding("AI 抽取友好度（首段直答）", "pass", "P2",
                          f"首段 {len(lead)} 字，可作精选摘要/AI 抽取来源", ""))
    else:
        F.append(_finding("AI 抽取友好度（首段直答）", "warn", "P2",
                          "首段过短或缺失，AI 难以直接摘录",
                          "开篇用一段直接回答页面核心问题，便于生成式引擎抽取引用。"))
    if has_faq:
        F.append(_finding("问答式结构（利于引用）", "pass", "P3", "含 FAQPage/Question 结构", ""))

    # --- speed signals ------------------------------------------------------
    if fetched.get("source") == "url":
        ms = fetched.get("elapsed_ms", 0)
        kb = fetched.get("bytes", 0) / 1024
        if ms > 3000:
            F.append(_finding("加载速度信号", "warn", "P2",
                              f"HTML 首字节+下载 {ms}ms / {kb:.0f}KB",
                              "首屏 HTML 应尽快返回；排查 TTFB、压缩、CDN。"))
        else:
            F.append(_finding("加载速度信号", "pass", "P3",
                              f"HTML {ms}ms / {kb:.0f}KB", ""))
        if kb > 500:
            F.append(_finding("HTML 体积", "warn", "P3", f"HTML {kb:.0f}KB 偏大",
                              "精简内联脚本/样式，考虑拆分与懒加载。"))

    return F


# ---------------------------------------------------------------------------
# scoring + report
# ---------------------------------------------------------------------------

def score_findings(findings: list) -> dict:
    score = 100.0
    counts = {"pass": 0, "warn": 0, "fail": 0}
    by_sev = {"P0": 0, "P1": 0, "P2": 0, "P3": 0}
    for f in findings:
        counts[f["status"]] = counts.get(f["status"], 0) + 1
        if f["status"] == "fail":
            score -= SEVERITY_WEIGHT.get(f["severity"], 0)
            by_sev[f["severity"]] = by_sev.get(f["severity"], 0) + 1
        elif f["status"] == "warn":
            score -= SEVERITY_WEIGHT.get(f["severity"], 0) / 2
            by_sev[f["severity"]] = by_sev.get(f["severity"], 0) + 1
    score = max(0, round(score))
    grade = ("A" if score >= 90 else "B" if score >= 75 else
             "C" if score >= 60 else "D" if score >= 40 else "E")
    return {"score": score, "grade": grade, "counts": counts, "issues_by_severity": by_sev}


def build_report(target: str, fetched: dict, findings: list) -> dict:
    summary = score_findings(findings)
    return {
        "target": target,
        "final_url": fetched.get("url", target),
        "http_status": fetched.get("status"),
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "score": summary["score"],
        "grade": summary["grade"],
        "counts": summary["counts"],
        "issues_by_severity": summary["issues_by_severity"],
        "findings": findings,
    }


_STATUS_ICON = {"pass": "✅ 通过", "warn": "⚠️ 告警", "fail": "❌ 失败"}


def render_report_md(report: dict) -> str:
    L = []
    L.append(f"# 站点审计报告 · {report['final_url']}")
    L.append("")
    L.append(f"> 生成时间 {report['generated_at']} · HTTP {report['http_status']} · "
             f"得分 **{report['score']}/100（{report['grade']}）**")
    L.append("")
    c = report["counts"]
    L.append(f"通过 {c.get('pass', 0)} · 告警 {c.get('warn', 0)} · 失败 {c.get('fail', 0)}")
    L.append("")

    order = {"fail": 0, "warn": 1, "pass": 2}
    sev_order = {"P0": 0, "P1": 1, "P2": 2, "P3": 3}
    rows = sorted(report["findings"],
                  key=lambda f: (order.get(f["status"], 9), sev_order.get(f["severity"], 9)))
    L.append("## 审计清单（按状态/严重度排序 — 最需修的在最上）")
    L.append("")
    L.append("| 项目 | 状态 | 严重度 | 证据 | 修复建议 |")
    L.append("|------|------|:----:|------|----------|")
    for f in rows:
        ev = (f["evidence"] or "").replace("|", "\\|").replace("\n", " ")
        fix = (f["fix"] or "—").replace("|", "\\|").replace("\n", " ")
        L.append(f"| {f['check']} | {_STATUS_ICON.get(f['status'], f['status'])} | "
                 f"{f['severity']} | {ev} | {fix} |")
    L.append("")
    L.append("---")
    L.append("*P0 阻断收录/排名，P1 重要，P2 中等，P3 细节。证据来自对页面 HTML 的确定性解析，"
             "抓不到或无法判定的项已如实标注，不臆断。*")
    L.append("")
    return "\n".join(L)


def register_artifacts(ws_root: str, items: list) -> None:
    """Append user-facing artifacts to <ws_root>/.niuniu/artifacts.json."""
    d = os.path.join(ws_root, ".niuniu")
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, "artifacts.json")
    data = {"artifacts": []}
    if os.path.exists(path):
        try:
            with open(path, "r", encoding="utf-8") as f:
                data = json.load(f)
            if not isinstance(data.get("artifacts"), list):
                data = {"artifacts": []}
        except Exception:
            data = {"artifacts": []}
    have = {a.get("path") for a in data["artifacts"]}
    for it in items:
        if it["path"] not in have:
            data["artifacts"].append(it)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


# ---------------------------------------------------------------------------
# CLI commands
# ---------------------------------------------------------------------------

def _read_input_html(target: str, timeout: int):
    """Resolve `target` to (html, fetched_meta). `-` reads HTML from stdin."""
    if target == "-":
        html = sys.stdin.read()
        return html, {"ok": True, "url": "(stdin)", "status": 200, "headers": {},
                      "html": html, "elapsed_ms": 0, "bytes": len(html.encode("utf-8")),
                      "error": None, "source": "file"}
    fetched = fetch_page(target, timeout=timeout)
    return fetched.get("html", ""), fetched


def cmd_audit(args) -> int:
    html, fetched = _read_input_html(args.target, args.timeout)
    if not fetched["ok"]:
        report = {
            "target": args.target, "final_url": args.target,
            "http_status": fetched.get("status"),
            "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "score": 0, "grade": "E", "counts": {"pass": 0, "warn": 0, "fail": 1},
            "issues_by_severity": {"P0": 1},
            "findings": [_finding("页面抓取", "fail", "P0",
                                  f"抓取失败：{fetched.get('error')}",
                                  "确认 URL 可访问；或用 fetch MCP 抓取后把 HTML 存成本地文件再审计。")],
        }
    else:
        page = parse_html(html)
        robots = None
        if fetched.get("source") == "url" and not args.no_robots:
            robots = fetch_robots(fetched["url"], timeout=min(args.timeout, 10))
        findings = run_checks(page, fetched, robots)
        report = build_report(args.target, fetched, findings)

    out_dir = args.out_dir or "."
    os.makedirs(out_dir, exist_ok=True)
    json_path = os.path.join(out_dir, "audit-report.json")
    md_path = os.path.join(out_dir, "audit-report.md")
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    with open(md_path, "w", encoding="utf-8") as f:
        f.write(render_report_md(report))
    if args.ws_root:
        register_artifacts(args.ws_root, [
            {"path": os.path.relpath(md_path, args.ws_root),
             "title": f"站点审计报告 · {report['final_url']}"},
        ])
    print(json.dumps({
        "score": report["score"], "grade": report["grade"],
        "counts": report["counts"], "report_md": md_path, "report_json": json_path,
    }, ensure_ascii=False, indent=2))
    return 0


def cmd_validate_jsonld(args) -> int:
    target = args.target
    if target != "-" and os.path.exists(target) and target.endswith(".json"):
        # Standalone JSON-LD file (e.g. an about-to-be-embedded snippet).
        with open(target, "r", encoding="utf-8", errors="replace") as f:
            raw_blocks = [f.read()]
    else:
        html, fetched = _read_input_html(target, args.timeout)
        if not fetched["ok"]:
            print(json.dumps({"error": fetched.get("error")}, ensure_ascii=False))
            return 1
        raw_blocks = parse_html(html).jsonld_raw
    result = validate_jsonld_blocks(raw_blocks)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    # Non-zero exit when there are hard errors, so callers can gate on it.
    return 1 if result["error_count"] else 0


def cmd_selftest(args) -> int:
    good = """<!doctype html><html lang="zh"><head>
      <meta charset="utf-8">
      <title>牛牛站点审计 — 本地 SEO/GEO 技术体检工具</title>
      <meta name="description" content="牛牛站点审计用纯本地引擎检查标题、meta、canonical、robots、结构化数据与移动友好度，产出可执行的修复清单，帮助内容既被搜索排名也被 AI 引用。">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <link rel="canonical" href="https://niu6ai.com/site-audit">
      <meta property="og:title" content="牛牛站点审计">
      <script type="application/ld+json">
      {"@context":"https://schema.org","@type":"Article","headline":"站点审计","author":{"@type":"Person","name":"牛牛"},"datePublished":"2026-07-02","image":"https://niu6ai.com/x.png"}
      </script>
    </head><body>
      <h1>站点审计</h1>
      <p>站点审计是一次面向 SEO 与 GEO 的技术体检：逐项检查页面是否能被搜索引擎与生成式引擎正确抓取、理解、引用。</p>
      <h2>检查项</h2><img src="a.png" alt="示意图">
    </body></html>"""
    page = parse_html(good)
    fetched = {"source": "file", "url": "test", "status": 200, "headers": {}}
    findings = run_checks(page, fetched, None)
    rep = build_report("test", fetched, findings)
    assert page.title.startswith("牛牛站点审计"), page.title
    assert page.html_lang == "zh"
    assert len([f for f in findings if f["check"] == "标题 <title>" and f["status"] == "pass"]) == 1
    assert rep["score"] >= 80, rep["score"]
    assert rep["counts"]["fail"] == 0, [f for f in findings if f["status"] == "fail"]

    # A deliberately broken page must surface P0/P1 fails.
    bad = """<html><head><title></title>
      <meta name="robots" content="noindex">
      <script type="application/ld+json">{ not valid json }</script>
      <script type="application/ld+json">{"@context":"https://schema.org","@type":"Product"}</script>
    </head><body><img src="x.png"><p>短</p></body></html>"""
    page2 = parse_html(bad)
    f2 = run_checks(page2, {"source": "file", "headers": {}}, None)
    fails = {f["check"]: f for f in f2 if f["status"] == "fail"}
    assert "可索引性 robots" in fails, "noindex must fail"
    assert "标题 <title>" in fails, "empty title must fail"
    assert "结构化数据 JSON-LD" in fails, "bad json-ld must fail"

    # JSON-LD validator: syntax error + missing required prop both caught.
    v = validate_jsonld_blocks([
        "{ not json }",
        '{"@context":"https://schema.org","@type":"Article","headline":"ok","author":"x","datePublished":"2026-01-01","image":"y"}',
        '{"@context":"https://schema.org","@type":"FAQPage","mainEntity":[{"@type":"Question","name":"Q?","acceptedAnswer":{"@type":"Answer","text":"A"}}]}',
        '{"@context":"https://schema.org","@type":"Product"}',
    ])
    assert v["error_count"] >= 2, v["error_count"]      # bad json + Product missing name
    assert v["valid_node_count"] >= 2, v["valid_node_count"]  # Article + FAQPage valid

    # @graph unwrapping.
    g = validate_jsonld_blocks(['{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"n"},{"@type":"Organization","name":"o","url":"u","logo":"l"}]}'])
    assert g["valid_node_count"] == 2, g

    md = render_report_md(rep)
    assert "站点审计报告" in md and "审计清单" in md
    print("selftest OK")
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Technical SEO / GEO site-audit engine (stdlib only)")
    sub = p.add_subparsers(dest="cmd", required=True)

    ap = sub.add_parser("audit", help="full checklist over a page -> audit-report.{json,md}")
    ap.add_argument("target", help="URL, local HTML file, or '-' for HTML on stdin")
    ap.add_argument("--out-dir", default=".", help="where to write audit-report.{json,md}")
    ap.add_argument("--ws-root", default=None, help="workspace root to register artifact in .niuniu/artifacts.json")
    ap.add_argument("--timeout", type=int, default=20, help="fetch timeout seconds")
    ap.add_argument("--no-robots", action="store_true", help="skip robots.txt fetch")
    ap.set_defaults(func=cmd_audit)

    vp = sub.add_parser("validate-jsonld", help="extract & validate JSON-LD / schema.org on a page or .json file")
    vp.add_argument("target", help="URL, HTML file, standalone .json file, or '-' for stdin")
    vp.add_argument("--timeout", type=int, default=20)
    vp.set_defaults(func=cmd_validate_jsonld)

    stp = sub.add_parser("selftest", help="self-check checks + JSON-LD validation (no network)")
    stp.set_defaults(func=cmd_selftest)

    args = p.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
