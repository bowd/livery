#!/usr/bin/env python3
"""Assemble static GitHub Pages bundle into _site/ from site/, docs/, and .github/assets/."""
import html
import pathlib
import re
import shutil

ROOT = pathlib.Path(__file__).resolve().parent.parent
SITE_SRC = ROOT / "site"
DOCS_SRC = ROOT / "docs"
OUT = ROOT / "_site"

DOCS_PAGES = [
    ("architecture", "Architecture & Data Model", "architecture.md"),
    ("cli", "CLI Reference", "cli.md"),
    ("launchers-and-secrets", "Launchers, Secrets & Herdr", "launchers-and-secrets.md"),
    ("ui-and-skillshare", "Meta-UI & Nested Skillshare", "ui-and-skillshare.md"),
    ("prompt-guidelines", "Prompt & Skill Guidelines", "prompt-guidelines.md"),
]


def inline_md(text: str) -> str:
    # Extract inline code spans into placeholders first so bold/links wrapping `code` parse cleanly!
    codes = []

    def save_code(m):
        idx = len(codes)
        codes.append(f"<code>{html.escape(m.group(1))}</code>")
        return f"\x00CODE{idx}\x00"

    s = re.sub(r"`([^`]+)`", save_code, text)
    s = html.escape(s)
    s = re.sub(
        r"\[([^\]]+)\]\(([^)]+)\)",
        lambda m: f'<a href="{m.group(2)}" style="color:var(--blue);text-decoration:underline">{m.group(1)}</a>',
        s,
    )
    s = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", s)
    s = re.sub(r"\*([^*]+)\*", r"<em>\1</em>", s)
    for idx, c_html in enumerate(codes):
        s = s.replace(f"\x00CODE{idx}\x00", c_html)
    return s


def md_to_html(md_text: str) -> str:
    lines = md_text.splitlines()
    html_out = []
    in_code = False
    code_lang = ""
    code_buf = []
    in_ul = False
    in_ol = False
    in_table = False
    table_rows = []

    def close_lists():
        nonlocal in_ul, in_ol
        if in_ul:
            html_out.append("</ul>")
            in_ul = False
        if in_ol:
            html_out.append("</ol>")
            in_ol = False

    def flush_table():
        nonlocal in_table, table_rows
        if not in_table:
            return
        in_table = False
        if not table_rows:
            return
        header = table_rows[0]
        body = [r for r in table_rows[1:] if not all(set(c.strip()) <= set("-: ") for c in r)]
        html_out.append("<table><thead><tr>" + "".join(f"<th>{inline_md(c.strip())}</th>" for c in header) + "</tr></thead><tbody>")
        for row in body:
            html_out.append("<tr>" + "".join(f"<td>{inline_md(c.strip())}</td>" for c in row) + "</tr>")
        html_out.append("</tbody></table>")
        table_rows = []

    for raw in lines:
        if raw.startswith("```"):
            close_lists()
            flush_table()
            if not in_code:
                in_code = True
                code_lang = raw[3:].strip()
                code_buf = []
            else:
                in_code = False
                cls = f' class="language-{html.escape(code_lang)}"' if code_lang else ""
                html_out.append(f"<pre><code{cls}>{html.escape(chr(10).join(code_buf))}</code></pre>")
            continue
        if in_code:
            code_buf.append(raw)
            continue

        s = raw.strip()
        if not s:
            close_lists()
            flush_table()
            continue

        if s.startswith("|") and s.endswith("|"):
            close_lists()
            in_table = True
            # Split unescaped pipes
            cells = [c.replace(r"\|", "|") for c in re.split(r"(?<!\\)\|", s[1:-1])]
            table_rows.append(cells)
            continue
        else:
            flush_table()

        if s == "---":
            close_lists()
            html_out.append("<hr/>")
            continue

        m_h = re.match(r"^(#{1,4})\s+(.*)$", s)
        if m_h:
            close_lists()
            lvl = len(m_h.group(1))
            html_out.append(f"<h{lvl}>{inline_md(m_h.group(2))}</h{lvl}>")
            continue

        m_ul = re.match(r"^-\s+(.*)$", s)
        if m_ul:
            if in_ol:
                html_out.append("</ol>")
                in_ol = False
            if not in_ul:
                html_out.append("<ul>")
                in_ul = True
            html_out.append(f"<li>{inline_md(m_ul.group(1))}</li>")
            continue

        m_ol = re.match(r"^\d+\.\s+(.*)$", s)
        if m_ol:
            if in_ul:
                html_out.append("</ul>")
                in_ul = False
            if not in_ol:
                html_out.append("<ol>")
                in_ol = True
            html_out.append(f"<li>{inline_md(m_ol.group(1))}</li>")
            continue

        close_lists()
        html_out.append(f"<p>{inline_md(s)}</p>")

    close_lists()
    flush_table()
    return "\n".join(html_out)


def render_doc_page(slug: str, title: str, body_html: str) -> str:
    sidebar_links = "\n".join(
        f'<a href="/docs/{s}.html" class="{"active" if s == slug else ""}">{html.escape(t)}</a>'
        for s, t, _ in DOCS_PAGES
    )
    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{html.escape(title)} · livery docs</title>
  <link rel="icon" type="image/png" sizes="32x32" href="/assets/favicon-32.png" />
  <link rel="icon" href="/favicon.ico" sizes="any" />
  <link rel="apple-touch-icon" sizes="180x180" href="/assets/apple-touch-icon.png" />
  <link rel="stylesheet" href="/styles.css" />
</head>
<body>
  <header class="nav-wrap">
    <div class="container nav">
      <a href="/" class="brand">
        <img src="/assets/livery-icon.png" alt="livery" width="28" height="28" />
        <span>livery</span>
      </a>
      <nav class="nav-links" aria-label="Primary">
        <a href="/#why">Why Livery</a>
        <a href="/#features">Features</a>
        <a href="/#gallery">Screenshots</a>
        <a href="/#quickstart">Quick Start</a>
        <a href="/docs/architecture.html" class="active">Docs</a>
      </nav>
      <div class="nav-actions">
        <button id="theme-toggle" class="btn icon-toggle" aria-label="Toggle color theme" title="Switch theme"></button>
        <a href="https://github.com/bowd/livery" class="btn btn-primary" target="_blank" rel="noopener">GitHub</a>
      </div>
    </div>
  </header>

  <div class="container docs-shell">
    <aside class="docs-sidebar">
      <div class="docs-nav-title">Documentation</div>
      <nav class="docs-nav">
        {sidebar_links}
      </nav>
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--line)">
        <a href="/" class="btn" style="width:100%;font-size:13px">← Back to Overview</a>
      </div>
    </aside>

    <article class="docs-article">
      {body_html}
    </article>
  </div>

  <footer class="footer">
    <div class="container footer-inner">
      <span><strong>livery</strong> (<code>liv</code>) · MIT License © bowd</span>
      <div class="footer-links">
        <a href="/">Home</a>
        <a href="https://github.com/bowd/livery">GitHub</a>
        <a href="https://github.com/bowd/livery/releases">Releases</a>
      </div>
    </div>
  </footer>

  <script src="/app.js"></script>
</body>
</html>
"""


def main():
    if OUT.exists():
        shutil.rmtree(OUT)
    OUT.mkdir(parents=True)
    (OUT / "assets" / "ui").mkdir(parents=True)
    (OUT / "docs").mkdir(parents=True)

    # Copy core static files + CNAME + .nojekyll + install.sh
    for name in ["index.html", "styles.css", "app.js", "CNAME"]:
        shutil.copy2(SITE_SRC / name, OUT / name)
    (OUT / ".nojekyll").write_text("")
    shutil.copy2(ROOT / "install.sh", OUT / "install.sh")

    # Copy UI brand icons & favicons
    ui_assets = ROOT / "internal" / "ui" / "assets"
    shutil.copy2(ui_assets / "favicon.ico", OUT / "favicon.ico")
    for fn in ["livery-icon.png", "favicon-32.png", "apple-touch-icon.png"]:
        shutil.copy2(ui_assets / fn, OUT / "assets" / fn)

    # Copy showcase GIFs & Retina screenshots without duplicating in git!
    gh_assets = ROOT / ".github" / "assets"
    for fn in ["demo.gif", "live-walkthrough.gif", "livery-logo-card.png"]:
        if (gh_assets / fn).exists():
            shutil.copy2(gh_assets / fn, OUT / "assets" / fn)
    if (SITE_SRC / "og-card.png").exists():
        shutil.copy2(SITE_SRC / "og-card.png", OUT / "assets" / "og-card.png")
    for img in (gh_assets / "ui").glob("*.png"):
        shutil.copy2(img, OUT / "assets" / "ui" / img.name)

    # Render docs/*.md -> _site/docs/*.html (+ /docs/index.html redirect/copy)
    for slug, title, md_file in DOCS_PAGES:
        raw_md = (DOCS_SRC / md_file).read_text()
        # Fix relative markdown links like (cli.md) -> (/docs/cli.html)
        raw_md = re.sub(r"\(([a-z0-9-]+)\.md\)", r"(/docs/\1.html)", raw_md)
        body = md_to_html(raw_md)
        page_html = render_doc_page(slug, title, body)
        (OUT / "docs" / f"{slug}.html").write_text(page_html)
        if slug == "architecture":
            (OUT / "docs" / "index.html").write_text(page_html)

    print("Built _site successfully:", sum(1 for _ in OUT.rglob("*") if _.is_file()), "files")


if __name__ == "__main__":
    main()
