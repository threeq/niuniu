#!/usr/bin/env python3
"""geo_audit.py — deterministic engine for GEO citation-rate audits.

GEO (Generative Engine Optimization) measures how often an LLM *cites* your
brand / URL when answering domain questions — the metric SEO tools miss.

This script is the exact, testable core of the `geo-citation-audit` skill. It
does NOT reason; the agent drives the fuzzy parts (picking queries, deciding
which model CLIs to call). This engine only does the parts that must be
deterministic and reproducible:

  probe    detect which model CLIs are reachable on PATH
  init     emit a starter config.json
  collect  drive each model CLI for every query x round -> answers.jsonl
           (optional; the agent may instead collect via sub-agents and hand
            the engine a ready answers.jsonl)
  score    citation detection + report (geo-report.json + geo-report.md) and
           artifact registration — the heart of the differentiator
  selftest self-check the scoring math with synthetic answers (no model calls)

Zero third-party dependencies: Python 3.8+ standard library only. Works on
Windows and POSIX. Never hard-fails on a missing model — an unreachable CLI is
recorded as an error run, not a crash, so a partial fleet still yields a report.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import unicodedata
from datetime import datetime, timezone

# Best-guess non-interactive one-shot invocations for the CLI backends niuniu
# already ships. {query} is substituted with the (shell-safe) query string.
# Override per-model in config.json -> "commands" when your CLI differs.
DEFAULT_COMMANDS = {
    "claude": "claude -p {query}",
    "qwen": "qwen -p {query}",
    "codex": "codex exec {query}",
}


# ---------------------------------------------------------------------------
# citation detection (the deterministic core)
# ---------------------------------------------------------------------------

def _norm(s: str) -> str:
    """Casefold + NFKC-normalize so matching is punctuation/width/case tolerant."""
    return unicodedata.normalize("NFKC", s or "").casefold()


def domain_of(url: str) -> str:
    """Extract a bare domain from a URL/host string (drop scheme, www, path)."""
    u = (url or "").strip()
    u = re.sub(r"^[a-zA-Z][a-zA-Z0-9+.-]*://", "", u)  # scheme://
    u = u.split("/", 1)[0].split("?", 1)[0]            # path/query
    u = u.split("@")[-1].split(":", 1)[0]              # userinfo, port
    if u.startswith("www."):
        u = u[4:]
    return u.strip().strip(".")


def needle_set(config: dict) -> list:
    """Build the list of (label, normalized_needle) the brand is 'cited' by."""
    needles = []
    brand = (config.get("brand") or "").strip()
    if brand:
        needles.append(("brand", _norm(brand)))
    for a in config.get("aliases", []) or []:
        a = (a or "").strip()
        if a:
            needles.append(("alias", _norm(a)))
    for url in config.get("urls", []) or []:
        url = (url or "").strip()
        if not url:
            continue
        needles.append(("url", _norm(url)))
        dom = domain_of(url)
        if dom:
            needles.append(("domain", _norm(dom)))
    # De-dup by normalized value, keep first label.
    seen, out = set(), []
    for label, val in needles:
        if val and val not in seen:
            seen.add(val)
            out.append((label, val))
    return out


def detect_citation(answer: str, needles: list) -> dict:
    """Return {cited: bool, matched: [labels...]} for one answer text."""
    hay = _norm(answer)
    matched = sorted({label for label, val in needles if val in hay})
    return {"cited": bool(matched), "matched": matched}


def count_mentions(answer: str, terms: list) -> int:
    """Count how many of `terms` (share-of-voice competitors) appear at all."""
    hay = _norm(answer)
    return sum(1 for t in terms if t and _norm(t) in hay)


# ---------------------------------------------------------------------------
# scoring / report
# ---------------------------------------------------------------------------

def _rate(cited: int, total: int) -> float:
    return round(cited / total, 4) if total else 0.0


def score(config: dict, records: list) -> dict:
    """Compute the full citation-rate report from collected answer records."""
    needles = needle_set(config)
    competitors = [c for c in (config.get("competitors") or []) if c and c.strip()]
    gap_threshold = float(config.get("gap_threshold", 0.0))

    per_query, per_model = {}, {}
    voice = {config.get("brand", "brand"): 0}
    for c in competitors:
        voice[c] = 0
    total_runs = cited_runs = error_runs = 0

    for r in records:
        q = r.get("query", "")
        m = r.get("model", "unknown")
        ans = r.get("answer", "") or ""
        is_err = bool(r.get("error"))
        det = detect_citation(ans, needles)

        pq = per_query.setdefault(q, {"query": q, "runs": 0, "cited": 0, "errors": 0})
        pm = per_model.setdefault(m, {"model": m, "runs": 0, "cited": 0, "errors": 0})
        pq["runs"] += 1
        pm["runs"] += 1
        total_runs += 1
        if is_err:
            pq["errors"] += 1
            pm["errors"] += 1
            error_runs += 1
            continue
        if det["cited"]:
            pq["cited"] += 1
            pm["cited"] += 1
            cited_runs += 1
            voice[config.get("brand", "brand")] += 1
        for c in competitors:
            if _norm(c) in _norm(ans):
                voice[c] += 1

    scored_runs = total_runs - error_runs
    for d in list(per_query.values()) + list(per_model.values()):
        ok = d["runs"] - d["errors"]
        d["rate"] = _rate(d["cited"], ok)

    per_query_list = sorted(per_query.values(), key=lambda d: (d["rate"], d["query"]))
    per_model_list = sorted(per_model.values(), key=lambda d: d["model"])
    gap = [d for d in per_query_list if d["rate"] <= gap_threshold]

    total_voice = sum(voice.values())
    sov = [
        {"name": k, "mentions": v, "share": _rate(v, total_voice)}
        for k, v in sorted(voice.items(), key=lambda kv: -kv[1])
    ]

    return {
        "brand": config.get("brand", ""),
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "totals": {
            "queries": len(per_query),
            "models": len(per_model),
            "total_runs": total_runs,
            "scored_runs": scored_runs,
            "cited_runs": cited_runs,
            "error_runs": error_runs,
        },
        "overall_citation_rate": _rate(cited_runs, scored_runs),
        "per_model": per_model_list,
        "per_query": per_query_list,
        "gap_queries": [d["query"] for d in gap],
        "share_of_voice": sov if competitors else [],
    }


def _pct(x: float) -> str:
    return f"{x * 100:.1f}%"


def render_markdown(report: dict) -> str:
    t = report["totals"]
    L = []
    L.append(f"# GEO 引用率实测报告 · {report['brand'] or '(未命名品牌)'}")
    L.append("")
    L.append(f"> 生成时间 {report['generated_at']} · "
             f"{t['queries']} 个 query × {t['models']} 个模型 = "
             f"{t['total_runs']} 次问答（{t['error_runs']} 次失败已剔除）")
    L.append("")
    L.append(f"## 总体引用率：**{_pct(report['overall_citation_rate'])}**")
    L.append("")
    L.append(f"品牌/URL 在 {t['scored_runs']} 次有效问答中被引用 **{t['cited_runs']}** 次。")
    L.append("")

    L.append("## 分模型")
    L.append("")
    L.append("| 模型 | 有效次数 | 被引用 | 引用率 | 失败 |")
    L.append("|------|------:|------:|------:|----:|")
    for d in report["per_model"]:
        ok = d["runs"] - d["errors"]
        L.append(f"| {d['model']} | {ok} | {d['cited']} | {_pct(d['rate'])} | {d['errors']} |")
    L.append("")

    L.append("## 分 query（引用率升序 — 最缺口在最上）")
    L.append("")
    L.append("| Query | 有效次数 | 被引用 | 引用率 |")
    L.append("|-------|------:|------:|------:|")
    for d in report["per_query"]:
        ok = d["runs"] - d["errors"]
        L.append(f"| {d['query']} | {ok} | {d['cited']} | {_pct(d['rate'])} |")
    L.append("")

    if report["gap_queries"]:
        L.append("## 缺口 query（零引用 — 优先补内容）")
        L.append("")
        for q in report["gap_queries"]:
            L.append(f"- {q}")
        L.append("")

    if report["share_of_voice"]:
        L.append("## 声量占比 Share of Voice（品牌 vs 竞品）")
        L.append("")
        L.append("| 名称 | 被提及 | 占比 |")
        L.append("|------|-----:|-----:|")
        for s in report["share_of_voice"]:
            L.append(f"| {s['name']} | {s['mentions']} | {_pct(s['share'])} |")
        L.append("")

    L.append("---")
    L.append("*引用检测为大小写/全半角无关的子串匹配（品牌名、别名、URL、域名）；"
             "缺口 query 是 GEO 优化的落点：补权威内容、让模型学到你的品牌。*")
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
# collection (optional — drives real model CLIs)
# ---------------------------------------------------------------------------

def build_argv(template: str, query: str) -> list:
    """Turn a command template into a safe argv list (shell=False).

    The template is authored (trusted); the query is untrusted data. We
    shlex-split the template, then substitute the `{query}` token as a single
    argument — never as shell text — so a query with spaces, quotes, `&`, `;`
    or other metacharacters is passed verbatim and cannot break out.
    """
    parts = shlex.split(template, posix=True)
    argv, substituted = [], False
    for part in parts:
        if "{query}" in part:
            argv.append(part.replace("{query}", query))
            substituted = True
        else:
            argv.append(part)
    if not substituted:  # template had no {query} placeholder — append it
        argv.append(query)
    return argv


def probe(commands: dict) -> dict:
    out = {}
    for name, tmpl in commands.items():
        parts = shlex.split(tmpl, posix=True) if tmpl.strip() else []
        exe = parts[0] if parts else name
        out[name] = {"command": tmpl, "available": shutil.which(exe) is not None}
    return out


def collect(config: dict, out_path: str) -> dict:
    commands = {**DEFAULT_COMMANDS, **(config.get("commands") or {})}
    models = config.get("models") or list(commands.keys())
    rounds = int(config.get("rounds", 1))
    timeout = int(config.get("timeout_seconds", 120))
    queries = config.get("queries") or []
    avail = probe({m: commands.get(m, m) for m in models})

    n_ok = n_err = 0
    with open(out_path, "w", encoding="utf-8") as f:
        for q in queries:
            for m in models:
                tmpl = commands.get(m, m)
                for rnd in range(1, rounds + 1):
                    rec = {"query": q, "model": m, "round": rnd, "answer": "", "error": None}
                    if not avail.get(m, {}).get("available"):
                        rec["error"] = "cli-not-found"
                        n_err += 1
                    else:
                        try:
                            argv = build_argv(tmpl, q)
                            r = subprocess.run(argv, shell=False, capture_output=True,
                                               text=True, timeout=timeout)
                            rec["answer"] = (r.stdout or "").strip()
                            if r.returncode != 0 and not rec["answer"]:
                                rec["error"] = f"exit={r.returncode}: {(r.stderr or '').strip()[:200]}"
                                n_err += 1
                            else:
                                n_ok += 1
                        except subprocess.TimeoutExpired:
                            rec["error"] = "timeout"
                            n_err += 1
                        except Exception as e:  # pragma: no cover - defensive
                            rec["error"] = f"exception: {e}"
                            n_err += 1
                    f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    return {"answers": out_path, "ok": n_ok, "errors": n_err, "availability": avail}


# ---------------------------------------------------------------------------
# io helpers + CLI
# ---------------------------------------------------------------------------

def load_config(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def load_records(path: str) -> list:
    recs = []
    with open(path, "r", encoding="utf-8") as f:
        txt = f.read().strip()
    if not txt:
        return recs
    # Accept either JSONL or a JSON array of records.
    if txt.lstrip().startswith("["):
        return json.loads(txt)
    for line in txt.splitlines():
        line = line.strip()
        if line:
            recs.append(json.loads(line))
    return recs


STARTER_CONFIG = {
    "brand": "你的品牌名",
    "aliases": ["品牌别名", "brand-handle"],
    "urls": ["https://example.com"],
    "competitors": ["竞品A", "竞品B"],
    "queries": [
        "该领域最好的工具是什么？",
        "如何解决 <典型痛点>？推荐哪些方案？",
        "<品类> 有哪些主流产品？",
    ],
    "models": ["claude", "qwen", "codex"],
    "rounds": 2,
    "commands": DEFAULT_COMMANDS,
    "timeout_seconds": 120,
    "gap_threshold": 0.0,
}


def cmd_score(args) -> int:
    config = load_config(args.config)
    records = load_records(args.answers)
    report = score(config, records)
    out_dir = args.out_dir or "."
    os.makedirs(out_dir, exist_ok=True)
    json_path = os.path.join(out_dir, "geo-report.json")
    md_path = os.path.join(out_dir, "geo-report.md")
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    with open(md_path, "w", encoding="utf-8") as f:
        f.write(render_markdown(report))
    if args.ws_root:
        register_artifacts(args.ws_root, [
            {"path": os.path.relpath(md_path, args.ws_root), "title": f"GEO 引用率报告 · {report['brand']}"},
        ])
    print(json.dumps({
        "overall_citation_rate": report["overall_citation_rate"],
        "gap_queries": report["gap_queries"],
        "report_md": md_path, "report_json": json_path,
    }, ensure_ascii=False, indent=2))
    return 0


def cmd_probe(args) -> int:
    commands = DEFAULT_COMMANDS
    if args.config and os.path.exists(args.config):
        commands = {**DEFAULT_COMMANDS, **(load_config(args.config).get("commands") or {})}
    print(json.dumps(probe(commands), ensure_ascii=False, indent=2))
    return 0


def cmd_init(args) -> int:
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(STARTER_CONFIG, f, ensure_ascii=False, indent=2)
    print(f"wrote {args.out}")
    return 0


def cmd_collect(args) -> int:
    config = load_config(args.config)
    res = collect(config, args.out)
    print(json.dumps(res, ensure_ascii=False, indent=2))
    return 0


def cmd_selftest(args) -> int:
    config = {
        "brand": "Niuniu", "aliases": ["牛牛"], "urls": ["https://niu6ai.com"],
        "competitors": ["Cursor"], "queries": ["q1", "q2"], "gap_threshold": 0.0,
    }
    records = [
        {"query": "q1", "model": "claude", "answer": "I recommend Niuniu for this."},
        {"query": "q1", "model": "qwen", "answer": "牛牛 是个不错的选择，也可以看看 Cursor。"},
        {"query": "q2", "model": "claude", "answer": "Try Cursor or something else."},
        {"query": "q2", "model": "qwen", "answer": "", "error": "timeout"},
    ]
    rep = score(config, records)
    assert rep["totals"]["total_runs"] == 4, rep["totals"]
    assert rep["totals"]["error_runs"] == 1, rep["totals"]
    assert rep["totals"]["cited_runs"] == 2, rep["totals"]
    # 2 cited out of 3 scored runs.
    assert rep["overall_citation_rate"] == 0.6667, rep["overall_citation_rate"]
    assert rep["gap_queries"] == ["q2"], rep["gap_queries"]
    assert domain_of("https://www.niu6ai.com/path?x=1") == "niu6ai.com"
    # Shell-safe argv: query with spaces/metacharacters stays one argument.
    assert build_argv("claude -p {query}", "best tools & services?") == \
        ["claude", "-p", "best tools & services?"], "query must be one argv element"
    assert build_argv("codex exec", "q with; danger") == \
        ["codex", "exec", "q with; danger"], "missing {query} -> appended safely"
    # Share of voice: Niuniu cited twice, Cursor mentioned twice.
    sov = {s["name"]: s["mentions"] for s in rep["share_of_voice"]}
    assert sov["Niuniu"] == 2 and sov["Cursor"] == 2, sov
    md = render_markdown(rep)
    assert "总体引用率" in md and "缺口 query" in md
    print("selftest OK")
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="GEO citation-rate audit engine")
    sub = p.add_subparsers(dest="cmd", required=True)

    sp = sub.add_parser("score", help="citation detection + report from answers")
    sp.add_argument("answers", help="answers.jsonl (or JSON array) of {query,model,answer[,error]}")
    sp.add_argument("config", help="config.json with brand/aliases/urls[/competitors]")
    sp.add_argument("--out-dir", default=".", help="where to write geo-report.{json,md}")
    sp.add_argument("--ws-root", default=None, help="workspace root to register artifact in .niuniu/artifacts.json")
    sp.set_defaults(func=cmd_score)

    pp = sub.add_parser("probe", help="check which model CLIs are on PATH")
    pp.add_argument("--config", default=None)
    pp.set_defaults(func=cmd_probe)

    ip = sub.add_parser("init", help="write a starter config.json")
    ip.add_argument("--out", default="config.json")
    ip.set_defaults(func=cmd_init)

    cp = sub.add_parser("collect", help="drive model CLIs -> answers.jsonl (optional)")
    cp.add_argument("config")
    cp.add_argument("--out", default="answers.jsonl")
    cp.set_defaults(func=cmd_collect)

    stp = sub.add_parser("selftest", help="self-check scoring math (no model calls)")
    stp.set_defaults(func=cmd_selftest)

    args = p.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
