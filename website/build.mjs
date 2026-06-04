#!/usr/bin/env node
// Static documentation site generator for Laredo.
// Renders Markdown (docs + EDRs) to a small, fast static site with a categorized
// sidebar, EDR listing, a designed landing page, and interactive diagrams.
import { readdir, readFile, writeFile, mkdir, rm, cp, stat } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join, dirname, relative, posix } from 'node:path';
import { fileURLToPath } from 'node:url';
import matter from 'gray-matter';
import { unified } from 'unified';
import remarkParse from 'remark-parse';
import remarkGfm from 'remark-gfm';
import remarkRehype from 'remark-rehype';
import rehypeSlug from 'rehype-slug';
import rehypeAutolinkHeadings from 'rehype-autolink-headings';
import rehypeHighlight from 'rehype-highlight';
import rehypeStringify from 'rehype-stringify';

const SITE_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = dirname(SITE_DIR);
const EDRS_DIR = join(REPO_ROOT, 'docs', 'edr');
const DOCS_DIR = join(SITE_DIR, 'docs');
const TEMPLATES_DIR = join(SITE_DIR, 'templates');
const STATIC_DIR = join(SITE_DIR, 'static');
const DIST_DIR = join(SITE_DIR, 'dist');

// Site is published at https://zrz.io/laredo/ — every emitted link/asset is
// prefixed with this base path. Local preview (serve.mjs) strips it.
const BASE = '/laredo';
const GITHUB_SOURCE_BASE = 'https://github.com/zourzouvillys/laredo/blob/main';

const VALID_STATUSES = new Set(['proposed', 'accepted', 'deprecated', 'superseded']);
const EDR_FILE_RE = /^(\d{4})-([a-z0-9][a-z0-9-]*)\.md$/;
const ALIAS_RE = /^[a-z0-9][a-z0-9-]*$/;
const EDR_SLUG_RE = /^\d{4}-[a-z0-9-]+$/;

// Doc sidebar: categories in display order. Pages within a category are ordered
// by frontmatter `sidebar_position`, then title.
const CATEGORIES = [
  { dir: 'getting-started', label: 'Getting Started' },
  { dir: 'concepts', label: 'Concepts' },
  { dir: 'guides', label: 'Guides' },
  { dir: 'reference', label: 'Reference' },
  { dir: 'design', label: 'Design' },
  { dir: 'internals', label: 'Internals' },
  { dir: 'operations', label: 'Operations' },
];

// Top navigation. `key` marks the active item per page.
const NAV = [
  { key: 'overview', label: 'Overview', href: `${BASE}/` },
  { key: 'docs', label: 'Docs', href: `${BASE}/getting-started/quick-start/` },
  { key: 'edrs', label: 'Decision Records', href: `${BASE}/edr/` },
  { key: 'viz', label: 'Diagrams', href: `${BASE}/viz/` },
  { key: 'github', label: 'GitHub', href: 'https://github.com/zourzouvillys/laredo' },
];
function renderNav(activeKey) {
  return NAV.map((n) =>
    `<a href="${n.href}"${n.key === activeKey ? ' class="active"' : ''}>${escapeHtml(n.label)}</a>`,
  ).join('\n        ');
}

const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype, { allowDangerousHtml: true })
  .use(rehypeSlug)
  .use(rehypeAutolinkHeadings, { behavior: 'wrap', properties: { className: ['heading-link'] } })
  .use(rehypeHighlight, { detect: true, ignoreMissing: true })
  .use(rehypeStringify, { allowDangerousHtml: true });

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
function formatDate(d) {
  if (!d) return '';
  if (d instanceof Date) return d.toISOString().slice(0, 10);
  return String(d);
}

// ---- link rewriting -----------------------------------------------------
// Docs are authored with relative `.md` links (so they render on GitHub) and a
// few Docusaurus-style absolute links (`/guides/x`). Rewrite both to the site's
// base-prefixed pretty URLs. `webDir` is the doc's directory within docs/
// (posix, '' for root) used to resolve relative links.
function rewriteLinks(html, webDir) {
  // Relative .md links → resolved pretty URL.
  html = html.replace(
    /href="(?!https?:|\/|#)([^"]+?)\.md([#?][^"]*)?"/g,
    (_m, path, suffix = '') => {
      const resolved = posix.normalize(posix.join(webDir, path)).replace(/^\.\//, '');
      return `href="${BASE}/${resolved}/${suffix}"`;
    },
  );
  // Explicit ../ or ./ already covered above; also handle leading "./".
  // Absolute, site-internal links (start with '/', not '//', not already base).
  html = html.replace(/href="(\/(?!\/)[^"#?]*)([#?][^"]*)?"/g, (m, path, suffix = '') => {
    if (path.startsWith(BASE + '/') || path === BASE) return m; // already based
    if (/\.[a-z0-9]{1,5}$/i.test(path)) return `href="${BASE}${path}${suffix}"`; // file w/ ext
    const clean = path.endsWith('/') ? path : path + '/';
    return `href="${BASE}${clean}${suffix}"`;
  });
  return html;
}

// EDR cross-links: relative `.md` resolved from edrs dir. Cross-EDR → site page;
// site-absolute (/guides/x) → base-prefixed; anything else .md → GitHub source.
function rewriteEdrLinks(html) {
  html = html.replace(
    /href="(\.\.?)\/([^"#?]+?)\.md([#?][^"]*)?"/g,
    (_m, prefix, path, suffix = '') => {
      if (prefix === '.' && EDR_SLUG_RE.test(path)) return `href="${BASE}/edr/${path}/${suffix}"`;
      const resolved = prefix === '..' ? path : `docs/edr/${path}`;
      return `href="${GITHUB_SOURCE_BASE}/${resolved}.md${suffix}"`;
    },
  );
  // Site-absolute links inside EDR prose.
  html = html.replace(/href="(\/(?!\/)[^"#?]*)([#?][^"]*)?"/g, (m, path, suffix = '') => {
    if (path.startsWith(BASE + '/') || path === BASE) return m;
    if (/\.[a-z0-9]{1,5}$/i.test(path)) return `href="${BASE}${path}${suffix}"`;
    const clean = path.endsWith('/') ? path : path + '/';
    return `href="${BASE}${clean}${suffix}"`;
  });
  return html;
}

function extractToc(html) {
  const items = [];
  const re = /<h([234])[^>]*\bid="([^"]+)"[^>]*>([\s\S]*?)<\/h\1>/g;
  let m;
  while ((m = re.exec(html)) !== null) {
    items.push({ level: Number(m[1]), id: m[2], text: m[3].replace(/<[^>]+>/g, '').trim() });
  }
  return items;
}
function renderToc(items) {
  if (!items.length) return '';
  return items.map((i) =>
    `<li class="level-${i.level}"><a href="#${escapeHtml(i.id)}">${escapeHtml(i.text)}</a></li>`).join('');
}

function statusBadge(s) { return `<span class="badge badge-${escapeHtml(s)}">${escapeHtml(s)}</span>`; }
function tagBadges(fm) {
  const t = Array.isArray(fm.tags) ? fm.tags : [];
  return t.map((x) => `<span class="tag">${escapeHtml(x)}</span>`).join(' ');
}
function authors(fm) {
  const l = Array.isArray(fm.authors) ? fm.authors : [];
  return l.map((a) => escapeHtml(String(a))).join(', ');
}

async function renderTemplate(name, vars) {
  const tpl = await readFile(join(TEMPLATES_DIR, name), 'utf8');
  return tpl.replace(/\{\{\s*([\w.-]+)\s*\}\}/g, (_, k) => (k in vars ? vars[k] : ''));
}
function applyLayout(layout, { title, nav, body, sidebar = '' }) {
  return layout
    .replaceAll('{{title}}', title)
    .replaceAll('{{nav}}', nav)
    .replaceAll('{{sidebar}}', sidebar)
    .replaceAll('{{body}}', body)
    .replaceAll('{{base}}', BASE);
}

async function writePageAt(relPath, content) {
  const full = join(DIST_DIR, relPath, 'index.html');
  await mkdir(dirname(full), { recursive: true });
  await writeFile(full, content);
}
function redirectHtml(toPath) {
  const safe = escapeHtml(toPath);
  return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta http-equiv="refresh" content="0; url=${safe}"><link rel="canonical" href="${safe}">
<meta name="robots" content="noindex,nofollow"><title>Redirecting…</title></head>
<body><p>Redirecting to <a href="${safe}">${safe}</a>…</p></body></html>\n`;
}

// ---- docs ---------------------------------------------------------------
async function walkMd(dir, baseRel = '') {
  const out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const rel = baseRel ? posix.join(baseRel, entry.name) : entry.name;
    if (entry.isDirectory()) out.push(...await walkMd(join(dir, entry.name), rel));
    else if (entry.name.endsWith('.md')) out.push(rel);
  }
  return out;
}

async function loadDocs() {
  if (!existsSync(DOCS_DIR)) return [];
  const rels = await walkMd(DOCS_DIR);
  const docs = [];
  for (const rel of rels) {
    if (rel === 'intro.md') continue; // the landing page owns the overview
    const raw = await readFile(join(DOCS_DIR, rel), 'utf8');
    const { data, content } = matter(raw);
    if (!data.title) throw new Error(`docs/${rel}: frontmatter 'title' is required`);
    const slug = rel.replace(/\.md$/, '');
    const dir = posix.dirname(slug) === '.' ? '' : posix.dirname(slug);
    docs.push({ rel, slug, dir, title: data.title, order: data.sidebar_position ?? 100, markdown: content });
  }
  return docs;
}

function renderSidebar(docs, activeSlug) {
  const byDir = new Map();
  for (const d of docs) byDir.set(d.dir, [...(byDir.get(d.dir) || []), d]);
  const blocks = [];
  for (const cat of CATEGORIES) {
    const items = (byDir.get(cat.dir) || []).sort((a, b) => a.order - b.order || a.title.localeCompare(b.title));
    if (!items.length) continue;
    const links = items.map((d) =>
      `<li><a href="${BASE}/${d.slug}/"${d.slug === activeSlug ? ' class="active"' : ''}>${escapeHtml(d.title)}</a></li>`).join('');
    blocks.push(`<div class="sb-group"><p class="sb-label">${escapeHtml(cat.label)}</p><ul>${links}</ul></div>`);
  }
  return blocks.join('\n');
}

async function buildDocPage(doc, docs, layout) {
  const html = rewriteLinks(String(await processor.process(doc.markdown)), doc.dir);
  const body = await renderTemplate('doc.html', {
    title: escapeHtml(doc.title),
    content: html,
    toc: renderToc(extractToc(html)),
    sidebar: renderSidebar(docs, doc.slug),
    source_path: `website/docs/${doc.rel}`,
    base: BASE,
  });
  const page = applyLayout(layout, {
    title: `${escapeHtml(doc.title)} · Laredo`,
    nav: renderNav('docs'),
    body,
  });
  await writePageAt(doc.slug, page);
}

// ---- EDRs ---------------------------------------------------------------
function validateFrontmatter(filename, fm) {
  const errs = [];
  if (typeof fm.id !== 'number' || !Number.isInteger(fm.id) || fm.id < 1) errs.push(`id must be a positive integer (got ${JSON.stringify(fm.id)})`);
  if (!fm.title || typeof fm.title !== 'string') errs.push('title is required');
  if (!fm.status || !VALID_STATUSES.has(fm.status)) errs.push(`status must be one of ${[...VALID_STATUSES].join('|')}`);
  if (!fm.date) errs.push('date is required (YYYY-MM-DD)');
  if (fm.status === 'superseded' && !fm.superseded_by) errs.push('status=superseded requires superseded_by');
  if (fm.status === 'proposed' && !fm.proposed_until) errs.push('status=proposed requires proposed_until');
  if (fm.aliases != null && (!Array.isArray(fm.aliases) || fm.aliases.some((a) => !ALIAS_RE.test(a)))) errs.push('aliases must be slug strings');
  const m = filename.match(EDR_FILE_RE);
  if (m && Number(m[1]) !== fm.id) errs.push(`filename number ${m[1]} does not match id ${fm.id}`);
  if (errs.length) throw new Error(`${filename}:\n  - ${errs.join('\n  - ')}`);
}
async function loadEdrs() {
  if (!existsSync(EDRS_DIR)) return [];
  const files = (await readdir(EDRS_DIR)).filter((f) => EDR_FILE_RE.test(f)).sort();
  const edrs = [];
  for (const file of files) {
    const { data, content } = matter(await readFile(join(EDRS_DIR, file), 'utf8'));
    validateFrontmatter(file, data);
    edrs.push({ file, slug: file.replace(/\.md$/, ''), frontmatter: data, markdown: content });
  }
  return edrs;
}
function edrUrl(slug) { return `${BASE}/edr/${slug}/`; }
function edrLink(id, byId) {
  const t = byId.get(id), padded = String(id).padStart(4, '0');
  return t ? `<a href="${edrUrl(t.slug)}">EDR-${padded}: ${escapeHtml(t.frontmatter.title)}</a>` : `EDR-${padded}`;
}
function renderRelations(fm, byId) {
  const p = [];
  if (fm.supersedes != null) p.push(`<li>Supersedes ${edrLink(fm.supersedes, byId)}</li>`);
  if (fm.superseded_by != null) p.push(`<li>Superseded by ${edrLink(fm.superseded_by, byId)}</li>`);
  return p.length ? `<ul class="relations">${p.join('')}</ul>` : '';
}
async function buildEdrPage(edr, byId, layout) {
  const fm = edr.frontmatter;
  const html = rewriteEdrLinks(String(await processor.process(edr.markdown)));
  const idPadded = String(fm.id).padStart(4, '0');
  const body = await renderTemplate('edr.html', {
    id_padded: idPadded,
    title: escapeHtml(fm.title),
    status_badge: statusBadge(fm.status),
    date: escapeHtml(formatDate(fm.date)),
    authors: authors(fm),
    tags: tagBadges(fm),
    relations: renderRelations(fm, byId),
    content: html,
    toc: renderToc(extractToc(html)),
    source_path: `docs/edr/${edr.file}`,
    base: BASE,
  });
  const page = applyLayout(layout, {
    title: `EDR-${idPadded}: ${escapeHtml(fm.title)} · Laredo`,
    nav: renderNav('edrs'),
    body,
  });
  await writePageAt(`edr/${edr.slug}`, page);
  // redirects: padded id, numeric id, aliases → canonical
  const targets = new Set([idPadded, String(fm.id), ...(fm.aliases ?? [])]);
  targets.delete(edr.slug);
  for (const t of targets) await writePageAt(`edr/${t}`, redirectHtml(edrUrl(edr.slug)));
}
async function buildEdrIndex(edrs, layout) {
  const sorted = [...edrs].sort((a, b) => {
    const da = formatDate(a.frontmatter.date), db = formatDate(b.frontmatter.date);
    return da !== db ? db.localeCompare(da) : b.frontmatter.id - a.frontmatter.id;
  });
  const rows = sorted.map((e) => {
    const fm = e.frontmatter, href = edrUrl(e.slug);
    return `<tr><td class="num"><a href="${href}">${String(fm.id).padStart(4, '0')}</a></td>
      <td><a href="${href}">${escapeHtml(fm.title)}</a></td>
      <td>${statusBadge(fm.status)}</td><td class="date">${escapeHtml(formatDate(fm.date))}</td>
      <td>${tagBadges(fm)}</td></tr>`;
  }).join('\n');
  const body = await renderTemplate('index.html', { count: String(sorted.length), rows, base: BASE });
  await writePageAt('edr', applyLayout(layout, { title: 'Decision Records · Laredo', nav: renderNav('edrs'), body }));
}

async function buildHome(layout) {
  const body = await readFile(join(TEMPLATES_DIR, 'home.html'), 'utf8');
  const page = applyLayout(layout, { title: 'Laredo · Real-time data sync', nav: renderNav('overview'), body });
  await writeFile(join(DIST_DIR, 'index.html'), page);
}

async function copyStatic() {
  if (!existsSync(STATIC_DIR)) return;
  for (const e of await readdir(STATIC_DIR)) await cp(join(STATIC_DIR, e), join(DIST_DIR, e), { recursive: true });
}

async function main() {
  await rm(DIST_DIR, { recursive: true, force: true });
  await mkdir(DIST_DIR, { recursive: true });
  const layout = await readFile(join(TEMPLATES_DIR, 'layout.html'), 'utf8');

  const docs = await loadDocs();
  for (const doc of docs) await buildDocPage(doc, docs, layout);

  const edrs = await loadEdrs();
  const byId = new Map(edrs.map((e) => [e.frontmatter.id, e]));
  for (const edr of edrs) await buildEdrPage(edr, byId, layout);
  await buildEdrIndex(edrs, layout);

  await buildHome(layout);
  await copyStatic();

  console.log(`Built landing + ${docs.length} docs + ${edrs.length} EDR(s) → ${DIST_DIR}`);
}
main().catch((err) => { console.error(`\nBuild failed:\n${err.message}\n`); process.exit(1); });
