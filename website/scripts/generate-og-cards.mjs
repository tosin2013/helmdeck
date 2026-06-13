/**
 * Ad-hoc script: generates per-blog-post OpenGraph PNG cards at
 * 1200×630. Reads website/blog/*.md frontmatter, builds an SVG
 * template per post, converts to PNG via @resvg/resvg-js, writes to
 * website/static/img/og/<slug>.png.
 *
 * Run via: `npm run og:generate` (manual; not part of `npm run build`).
 *
 * Why: PR #494's SEO audit found 33 blog posts using the site-wide
 * social-card.png fallback. Per-post cards reduce templated-visual
 * signal — both Google's quality model and human social-share previews
 * benefit from distinctive imagery. Companion to the homepage
 * "Recently shipped" section that gives the same posts internal
 * link signal.
 *
 * Frontmatter contract: the script reads `slug`, `title`, `date` (or
 * filename date), and `tags`. Pre-existing `image:` values are NOT
 * overwritten — only posts whose `image:` is absent or set to the
 * site-wide fallback get a generated card; hand-crafted images are
 * preserved.
 *
 * Output stability: filename = `<slug>.png`. Re-running overwrites
 * but never deletes — orphaned PNGs from renamed posts can be removed
 * manually.
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import {Resvg} from '@resvg/resvg-js';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const blogDir = path.join(root, 'blog');
const outDir = path.join(root, 'static', 'img', 'og');
const SITE_CARD_PATH = '/img/social-card.png';

const W = 1200;
const H = 630;
const MARGIN = 60;
const TITLE_LINE_HEIGHT = 70;
const TITLE_MAX_LINES = 4;
const TITLE_FONT_SIZE = 52;
const TITLE_CHARS_PER_LINE = 32; // approx for the font size; tuned by eye

const BRAND = 'Helmdeck';
const URL = 'helmdeck.dev';

const COLORS = {
  bg: '#0f172a',          // slate-900
  accent: '#22d3ee',      // cyan-400
  text: '#f1f5f9',        // slate-100
  muted: '#94a3b8',       // slate-400
  tagBg: '#1e293b',       // slate-800
  tagText: '#cbd5e1',     // slate-300
};

function escapeXml(s) {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&apos;');
}

function wrapText(text, charsPerLine, maxLines) {
  const words = text.split(/\s+/);
  const lines = [];
  let cur = '';
  for (const w of words) {
    if (!cur) {
      cur = w;
    } else if ((cur + ' ' + w).length <= charsPerLine) {
      cur += ' ' + w;
    } else {
      lines.push(cur);
      cur = w;
      if (lines.length === maxLines - 1) break;
    }
  }
  if (cur) lines.push(cur);
  if (words.length > 0 && lines.length < maxLines) {
    // Already finalized
  } else if (lines.length === maxLines) {
    // Truncate last line with ellipsis if there are remaining words
    const usedWordCount = lines.join(' ').split(/\s+/).length;
    if (usedWordCount < words.length) {
      const last = lines[lines.length - 1];
      // Trim to fit ellipsis
      const truncated = last.length > charsPerLine - 1
        ? last.slice(0, charsPerLine - 1).trimEnd() + '…'
        : last + '…';
      lines[lines.length - 1] = truncated;
    }
  }
  return lines.slice(0, maxLines);
}

function formatDate(iso) {
  const [y, m, d] = iso.split('-');
  const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  return `${months[parseInt(m, 10) - 1]} ${parseInt(d, 10)}, ${y}`;
}

function buildSvg({title, date, tags}) {
  const lines = wrapText(title, TITLE_CHARS_PER_LINE, TITLE_MAX_LINES);
  const titleHeight = lines.length * TITLE_LINE_HEIGHT;
  // Vertically center title block in the area between header (140px from
  // top) and footer (110px from bottom).
  const blockTop = 140;
  const blockBottom = H - 110;
  const blockMid = (blockTop + blockBottom) / 2;
  const titleStartY = Math.round(blockMid - titleHeight / 2 + TITLE_LINE_HEIGHT * 0.75);

  const tagsLine = tags.slice(0, 3).map((t) => t.toUpperCase()).join('  ·  ');
  const dateStr = formatDate(date);

  const titleTspans = lines
    .map((line, i) => `<tspan x="${MARGIN}" dy="${i === 0 ? 0 : TITLE_LINE_HEIGHT}">${escapeXml(line)}</tspan>`)
    .join('');

  return `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <defs>
    <linearGradient id="bgGrad" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="${COLORS.bg}"/>
      <stop offset="1" stop-color="#1e293b"/>
    </linearGradient>
  </defs>

  <rect width="${W}" height="${H}" fill="url(#bgGrad)"/>

  <!-- accent bar -->
  <rect x="0" y="0" width="12" height="${H}" fill="${COLORS.accent}"/>

  <!-- header: brand + URL -->
  <text x="${MARGIN}" y="92" font-family="ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="38" font-weight="700" fill="${COLORS.text}" letter-spacing="-0.5">
    ${BRAND}
  </text>
  <text x="${W - MARGIN}" y="92" text-anchor="end" font-family="ui-monospace, 'SF Mono', Menlo, Consolas, monospace" font-size="22" fill="${COLORS.muted}">
    ${URL}
  </text>

  <!-- title (wrapped) -->
  <text x="${MARGIN}" y="${titleStartY}" font-family="ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="${TITLE_FONT_SIZE}" font-weight="700" fill="${COLORS.text}" letter-spacing="-1">
    ${titleTspans}
  </text>

  <!-- footer: tags + date -->
  ${tagsLine ? `<text x="${MARGIN}" y="${H - 50}" font-family="ui-monospace, 'SF Mono', Menlo, Consolas, monospace" font-size="20" font-weight="600" fill="${COLORS.tagText}" letter-spacing="1.5">${escapeXml(tagsLine)}</text>` : ''}
  <text x="${W - MARGIN}" y="${H - 50}" text-anchor="end" font-family="ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif" font-size="22" fill="${COLORS.muted}">${escapeXml(dateStr)}</text>
</svg>`;
}

function parseFrontmatter(raw) {
  if (!raw.startsWith('---\n')) return null;
  const end = raw.indexOf('\n---\n', 4);
  if (end === -1) return null;
  const yaml = raw.slice(4, end);
  const result = {};
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
  if (fm.date) return fm.date;
  const m = file.match(/^(\d{4}-\d{2}-\d{2})-/);
  return m ? m[1] : null;
}

async function main() {
  await fs.mkdir(outDir, {recursive: true});

  const files = (await fs.readdir(blogDir))
    .filter((f) => f.endsWith('.md') && !f.startsWith('_'));

  let generated = 0;
  let skipped = 0;
  for (const file of files) {
    const filepath = path.join(blogDir, file);
    const raw = await fs.readFile(filepath, 'utf8');
    const fm = parseFrontmatter(raw);
    if (!fm) {
      console.warn(`SKIP  ${file} (no frontmatter)`);
      skipped++;
      continue;
    }
    if (fm.draft === 'true' || fm.draft === true) {
      console.warn(`SKIP  ${file} (draft)`);
      skipped++;
      continue;
    }

    const slug = fm.slug;
    const title = fm.title;
    const date = dateFromFile(file, fm);
    const tags = Array.isArray(fm.tags) ? fm.tags : [];

    if (!slug || !title || !date) {
      console.warn(`SKIP  ${file} (missing slug/title/date)`);
      skipped++;
      continue;
    }

    // Preserve hand-crafted images (anything not the site-wide card).
    if (fm.image && fm.image !== SITE_CARD_PATH && !fm.image.startsWith('/img/og/')) {
      console.log(`KEEP  ${file} → image: ${fm.image} (hand-crafted, not overwritten)`);
      skipped++;
      continue;
    }

    const svg = buildSvg({title, date, tags});
    const resvg = new Resvg(svg, {
      background: COLORS.bg,
      fitTo: {mode: 'width', value: W},
      font: {
        loadSystemFonts: true,
        defaultFontFamily: 'sans-serif',
      },
    });
    const pngData = resvg.render().asPng();
    const pngPath = path.join(outDir, `${slug}.png`);
    await fs.writeFile(pngPath, pngData);

    // Update the blog post frontmatter to reference the new card.
    const newImageLine = `image: /img/og/${slug}.png`;
    let updated;
    if (fm.image !== undefined) {
      // Replace existing image: line
      updated = raw.replace(/^image:.*$/m, newImageLine);
    } else {
      // Insert image: line before closing --- of frontmatter
      const closeIdx = raw.indexOf('\n---\n', 4);
      updated = raw.slice(0, closeIdx) + `\n${newImageLine}` + raw.slice(closeIdx);
    }
    if (updated !== raw) {
      await fs.writeFile(filepath, updated);
    }

    generated++;
    console.log(`OK    ${file} → /img/og/${slug}.png (${pngData.length} bytes)`);
  }

  console.log(`\n[summary] generated: ${generated}  skipped: ${skipped}  total: ${files.length}`);
}

main().catch((err) => {
  console.error('[generate-og-cards] failed:', err);
  process.exit(1);
});
