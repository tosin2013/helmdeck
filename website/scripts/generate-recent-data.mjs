/**
 * Build-time script: scans website/blog/*.md, extracts frontmatter, sorts
 * by date desc, writes the top 8 posts as src/data/recent.json. Imported
 * by src/pages/index.tsx to render the "Recently shipped" homepage
 * section.
 *
 * Wired into npm run build via package.json (runs before docusaurus
 * build). Idempotent — safe to run repeatedly.
 *
 * Why: gives newest blog posts a strong internal link from the highest-
 * PageRank page on the site (homepage). Directly addresses the GSC
 * "Discovered – currently not indexed" failure mode where new content
 * lacks the discovery signal Google needs to allocate crawl budget.
 * Companion to PR #494 (Tier 1 + Tier 2 schema + meta hygiene).
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const blogDir = path.join(root, 'blog');
const outFile = path.join(root, 'src', 'data', 'recent.json');

const HEAD_LIMIT = 8;

function parseFrontmatter(raw) {
  if (!raw.startsWith('---\n')) return null;
  const end = raw.indexOf('\n---\n', 4);
  if (end === -1) return null;
  const yaml = raw.slice(4, end);
  const result = {};
  // Naive line-by-line parser sufficient for blog frontmatter shape.
  // Handles: `key: value`, `key: "quoted value"`, `key: [a, b, c]`,
  // and multiline values are NOT supported (we don't need them here).
  for (const line of yaml.split('\n')) {
    const m = line.match(/^([a-zA-Z_][a-zA-Z0-9_]*):\s*(.*)$/);
    if (!m) continue;
    const [, key, valRaw] = m;
    let val = valRaw.trim();
    if (val.startsWith('"') && val.endsWith('"')) val = val.slice(1, -1);
    if (val.startsWith('[') && val.endsWith(']')) {
      val = val.slice(1, -1).split(',').map((s) => s.trim().replace(/^["']|["']$/g, ''));
    }
    result[key] = val;
  }
  return result;
}

function dateFromFile(file, fm) {
  // Prefer explicit `date:` frontmatter; fall back to leading YYYY-MM-DD
  // in filename (Docusaurus blog convention).
  if (fm.date) return fm.date;
  const m = file.match(/^(\d{4}-\d{2}-\d{2})-/);
  return m ? m[1] : null;
}

async function main() {
  const files = (await fs.readdir(blogDir))
    .filter((f) => f.endsWith('.md') && !f.startsWith('_'));

  const posts = [];
  for (const file of files) {
    const raw = await fs.readFile(path.join(blogDir, file), 'utf8');
    const fm = parseFrontmatter(raw);
    if (!fm) continue;
    if (fm.draft === 'true' || fm.draft === true) continue;

    const date = dateFromFile(file, fm);
    if (!date) continue;
    if (!fm.slug || !fm.title) continue;

    posts.push({
      slug: fm.slug,
      title: fm.title,
      description: fm.description ?? '',
      date,
      tags: Array.isArray(fm.tags) ? fm.tags : [],
      permalink: `/blog/${fm.slug}`,
    });
  }

  posts.sort((a, b) => (a.date > b.date ? -1 : a.date < b.date ? 1 : 0));
  const top = posts.slice(0, HEAD_LIMIT);

  await fs.mkdir(path.dirname(outFile), {recursive: true});
  await fs.writeFile(
    outFile,
    JSON.stringify({generated: 'generate-recent-data.mjs', count: top.length, posts: top}, null, 2) + '\n',
  );

  console.log(`[generate-recent-data] wrote ${top.length} posts → ${path.relative(root, outFile)}`);
}

main().catch((err) => {
  console.error('[generate-recent-data] failed:', err);
  process.exit(1);
});
