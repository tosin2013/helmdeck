#!/usr/bin/env node
// Builds website/static/llms-full.txt by concatenating every Markdown
// documentation file in the repo (docs/, website/src/pages/, website/blog/)
// with a per-file header that names the source path and the canonical URL.
//
// The output is a single large text file that an LLM can ingest in one
// pass without crawling the site page-by-page. The curated, human-sized
// index sits next to it as website/static/llms.txt.
//
// Invoked from `npm run build` ahead of `docusaurus build` so the result
// is picked up by Docusaurus's static-asset pipeline.

import { readFile, writeFile, readdir } from 'node:fs/promises';
import { join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const websiteRoot = join(__dirname, '..');
const repoRoot = join(websiteRoot, '..');
const docsDir = join(repoRoot, 'docs');
const pagesDir = join(websiteRoot, 'src', 'pages');
const blogDir = join(websiteRoot, 'blog');
const outputPath = join(websiteRoot, 'static', 'llms-full.txt');

const SITE_URL = 'https://helmdeck.dev';

async function walk(dir) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw err;
  }
  const out = [];
  for (const e of entries) {
    const p = join(dir, e.name);
    if (e.isDirectory()) {
      out.push(...(await walk(p)));
    } else if (e.isFile() && e.name.endsWith('.md')) {
      out.push(p);
    }
  }
  return out;
}

function isDraft(content) {
  const m = content.match(/^---\n([\s\S]*?)\n---/);
  if (!m) return false;
  return /^draft:\s*true\s*$/m.test(m[1]);
}

function docsUrl(absPath) {
  let rel = relative(docsDir, absPath).replace(/\.md$/, '');
  rel = rel.replace(/\/index$/, '');
  return `${SITE_URL}/${rel}`;
}

function pageUrl(absPath) {
  let rel = relative(pagesDir, absPath).replace(/\.md$/, '');
  rel = rel.replace(/\/index$/, '');
  return `${SITE_URL}/${rel}`;
}

function blogUrl(absPath) {
  const rel = relative(blogDir, absPath).replace(/\.md$/, '');
  return `${SITE_URL}/blog/${rel}`;
}

async function collect(files, urlFn, sections) {
  let included = 0;
  for (const f of files.sort()) {
    const content = await readFile(f, 'utf8');
    if (isDraft(content)) continue;
    const url = urlFn(f);
    const rel = relative(repoRoot, f);
    sections.push(`================================================================
# ${rel}
# Source: ${url}
================================================================

${content.trim()}

`);
    included += 1;
  }
  return included;
}

async function main() {
  const docsFiles = await walk(docsDir);
  const pageFiles = await walk(pagesDir);
  const blogFiles = await walk(blogDir);

  const sections = [`# Helmdeck — full documentation corpus

Generated from the helmdeck repo at build time. Each section below is the verbatim Markdown source of one documentation page, prefixed by its repo-relative path and canonical URL on https://helmdeck.dev.

For a curated, human-sized index of the same docs, see https://helmdeck.dev/llms.txt

`];

  const docsCount = await collect(docsFiles, docsUrl, sections);
  const pagesCount = await collect(pageFiles, pageUrl, sections);
  const blogCount = await collect(blogFiles, blogUrl, sections);

  await writeFile(outputPath, sections.join(''), 'utf8');

  const bytes = (await readFile(outputPath)).length;
  const total = docsCount + pagesCount + blogCount;
  console.log(
    `[generate-llms-full] wrote ${relative(repoRoot, outputPath)} (${(bytes / 1024).toFixed(0)} KB, ${total} files: docs=${docsCount} pages=${pagesCount} blog=${blogCount})`,
  );
}

main().catch((err) => {
  console.error('[generate-llms-full] error:', err);
  process.exit(1);
});
