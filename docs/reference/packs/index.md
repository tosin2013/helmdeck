---
title: Pack reference (per-pack)
description: One page per shipped capability pack — CLI invocation, UI affordance, vault credentials, error codes.
slug: /reference/packs/
---

# Pack reference

One page per shipped pack. Each page covers what the pack does, the input/output schema, vault credentials it depends on, how to invoke it from the CLI today and from the Management UI when that lands, the closed set of typed error codes it can return, and how it composes with other packs via session chaining.

For a quick-lookup summary across all 38 packs (just the input/output contract), see **[`PACKS.md`](/PACKS)**. For agent-facing prompt guidance, see **[`SKILLS.md`](/integrations/SKILLS)**. This per-pack reference is the deep view.

## By family

| Family | Packs | Status |
|---|---|---|
| **browser** | [browser.screenshot_url](browser/screenshot-url.md) · [browser.interact](browser/interact.md) | ✅ Documented |
| **web** | `web.scrape` · `web.scrape_spa` · `web.test` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fweb) |
| **repo** | `repo.fetch` · `repo.map` · `repo.push` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Frepo) |
| **github** | `github.create_issue` · `github.list_issues` · `github.list_prs` · `github.post_comment` · `github.create_release` · `github.search` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fgithub) |
| **slides** | `slides.render` · `slides.narrate` · `slides.notes` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fslides) |
| **doc** | `doc.ocr` · `doc.parse` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fdoc) |
| **desktop** | `desktop.run_app_and_screenshot` + 16 REST primitives | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fdesktop) |
| **vision** | `vision.click_anywhere` · `vision.extract_visible_text` · `vision.fill_form_by_label` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fvision) |
| **fs** | `fs.read` · `fs.write` · `fs.list` · `fs.patch` · `fs.delete` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Ffs) |
| **shell** | `cmd.run` · `git.commit` · `git.diff` · `git.log` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fshell) |
| **http** | `http.fetch` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fhttp) |
| **research / content** | `research.deep` · `content.ground` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Fresearch) |
| **language** | `python.run` · `node.run` | 🟡 [Tracking issue](https://github.com/tosin2013/helmdeck/labels/pack-family%2Flanguage) |

## Contributing a pack page

Use **[`_template.md` on GitHub](https://github.com/tosin2013/helmdeck/blob/main/docs/reference/packs/_template.md)** as your starting point. The browser pages above are worked examples — same structure, same depth.

A typical pack page takes 30–60 minutes to write end-to-end against a running install:

1. Read the handler in `internal/packs/builtin/<file>.go` for the actual error codes and any session-coupling.
2. Pick up the inputs/outputs from the [`PACKS.md`](/PACKS) row and the agent guidance from [`SKILLS.md`](/integrations/SKILLS).
3. Run a real CLI invocation against your local stack (see the [install tutorial](/tutorials/install-cli)) and paste the redacted output.
4. Open a PR linking to the family tracking issue.
