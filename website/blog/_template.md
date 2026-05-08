---
slug: replace-with-kebab-case-slug
title: Replace with the headline (concrete number or sharp claim, not a category)
authors: [tosin]
tags: [field-report]
description: One-sentence summary that reads well in social-card previews and search results. Lead with the finding, not the context.
image: /img/social-card.png
date: 2099-12-31
draft: true
---

<!--
HOW TO USE THIS TEMPLATE

The helmdeck blog is open to community contributions — see
CONTRIBUTING.md §"Other contribution types" for the workflow.

We especially welcome **independent reproductions** of claims in
maintainer posts: if you re-ran a comparison and got different
numbers, please share. That's more valuable than a fresh marketing
pitch.

1. Copy this file to website/blog/<YYYY-MM-DD>-<slug>.md.
2. Update every frontmatter field above. Set `date` to the publish target,
   not today (Docusaurus orders posts by date, not file mtime).
3. If you're not already in website/blog/authors.yml, add yourself
   there with a short kebab-case key. The `authors:` array uses that
   key.
4. Leave `draft: true` while you iterate. Docusaurus skips draft posts
   in production builds — they only appear with `npm run start`.
   Maintainer review happens on `draft: true`; you flip to `false`
   once approved, in a follow-up commit.
5. Pick tags from the small standing taxonomy below — keep the list small
   so readers can subscribe by tag without losing signal:

     cost                — quantified cost / ROI / pricing comparisons
     mcp                 — MCP protocol, transports, client integrations
     weak-models         — small open-weight model behaviour and patterns
     agent-architecture  — design rationale, structural choices, ADR-flavored
     friction            — bug stories, surprise interactions, what bit you
     field-report        — install / upgrade / production observation
     release-notes       — version-pegged change summaries (companion to /changelog)
     security            — auth, sandbox, vault, audit, threat model
     reproduction        — independent re-run of a prior post's claims

6. Replace the body sections below. Keep this shape because it works:
     - Hook (one or two sentences with a concrete number or sharp claim)
     - Context (1 paragraph — what was the work that surfaced this?)
     - Finding (1–3 paragraphs, with a code or terminal block if it shows)
     - Why-it-matters-to-the-reader (1 paragraph — generalize, don't navel-gaze)
     - CTA (link to docs page or PR for the deeper dive)

7. Length: aim for ~600–900 words for a field report, ~1200–2000 for a
   design / cost analysis. Longer than that, write a /docs/explanation/
   page and link from a short blog hook.

8. When the post is ready: open a PR with `draft: false`, the maintainer
   reviews + merges, and helmdeck.dev/blog publishes on the next deploy.

9. If your post is reporting on a comparison or benchmark, include the
   exact prompts / commands / hardware so others can reproduce. Posts
   without a reproducer are still welcome but less load-bearing.
-->

## Hook

One or two sentences with a concrete number or sharp claim. The reader
should know within 10 seconds whether this post is worth their time.

## Context

What was the work that surfaced this? One paragraph. Link to the
PR / commit / issue that produced the finding so a curious reader can
audit the source.

## Finding

The substance. Show, don't tell.

```text
# code or terminal block where applicable
```

If the finding has a metric (cost, time, retry count, accuracy), put
the before/after numbers in a small markdown table.

| Scenario | Approx cost / time / N |
|---|---|
| Before | … |
| After | … |

## Why this matters to you

Generalize from the specific finding to the broader audience. What
should a reader who isn't already deep in helmdeck do differently after
reading this? What's the takeaway that survives outside this codebase?

## See also

- The PR: <https://github.com/tosin2013/helmdeck/pull/...>
- Related docs: [/docs/...](/docs/...)
- The longer reference (if any): [/docs/explanation/...](/docs/explanation/...)
