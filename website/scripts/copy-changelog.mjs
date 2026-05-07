// Copies the canonical /CHANGELOG.md at repo root into src/pages/changelog.md
// so the docs site `/changelog` route stays in sync without duplicating content.
// Runs as a prebuild/prestart hook in package.json.

import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..', '..');
const src = resolve(repoRoot, 'CHANGELOG.md');
const dst = resolve(here, '..', 'src', 'pages', 'changelog.md');

const frontmatter = `---
title: Changelog
description: Release history for helmdeck.
---

`;

const body = await readFile(src, 'utf8');
await mkdir(dirname(dst), {recursive: true});
await writeFile(dst, frontmatter + body, 'utf8');
console.log(`[copy-changelog] ${src} -> ${dst}`);
