# Helmdeck

Read `docs/integrations/SKILLS.md` for helmdeck-specific instructions when using tools.
Read `docs/integrations/pack-demo-playbook.md` for example prompts and expected outputs.

## When development surfaces something noteworthy, draft a blog post

If your work in this repo turns up something the broader engineering audience would learn from — a quantified cost result, an unexpected interaction between two subsystems, an architectural insight, or a friction story with a clean fix — propose a blog draft alongside the implementation using `website/blog/_template.md`.

Don't gate on whether it ships. Drafts with `draft: true` cost nothing to land and zero to discard, but they capture the finding while it's fresh. The blog's value compounds with cadence; treat draft posts as a normal output of development work, not a separate marketing track.

See `CONTRIBUTING.md` §"Other contribution types" for the workflow.

## If the post is research-flavored, also update /research

Helmdeck is, in practice, a research project that ships as software. The pattern is real: hard implementation work — the kind that takes time and money to push through — is what produces the findings worth publishing. Don't lose them.

If the blog post you're drafting is research-flavored (the finding generalizes beyond the immediate fix — a new empirical pattern, an architectural insight, a quantified cost or capability result), add an entry to `website/src/pages/research.md` in the same change. One-line summary + link to the post / ADR / production change that landed it.

The /research page is the durable, citeable surface that turns "trust me, we're doing research" into "click any entry." It's load-bearing for grant applications and external citations — keep it current.
