---
slug: an-error-your-agent-can-recover-from
title: "unknown provider: minimax: an error your agent couldn't recover from"
authors: [tosin]
tags: [friction]
description: 'A pack failed with handler_failed and an opaque "unknown provider: minimax" message, and the agent — told nothing actionable — just guessed another bad model. The fix wasn''t a new provider; it was making the error caller-fixable and giving the agent a list to pick from.'
image: /img/social-card.png
date: 2026-05-28
draft: false
---

A `content.ground` call failed like this:

```
handler_failed: claim extractor dispatch: unknown provider: minimax: unknown provider: minimax
```

The agent had picked `model: "minimax/abab6.5"`. It's a reasonable-looking guess — MiniMax is a real provider, and OpenRouter's model catalog literally lists `minimax/minimax-m2.7`. But helmdeck's gateway has no `minimax` provider: MiniMax is reachable only *through* OpenRouter, as `openrouter/minimax/minimax-m2.7`. Drop the `openrouter/` prefix and you land on a provider that doesn't exist.

That part is a normal mistake. What made it bad was the *shape* of the failure.

## handler_failed is a dead end

helmdeck's packs return [typed error codes](/adrs/008-typed-error-codes-for-weak-model-reliability) so an agent can branch on the failure instead of parsing prose. `handler_failed` is the code reserved for *buried exceptions* — a handler panicked or returned something uncategorized. By contract it means "something broke inside; not your fault, not your fix."

So when the gateway's "unknown provider" error got wrapped as `handler_failed`, we told the agent exactly the wrong thing. A bad model string is the *most* caller-fixable failure there is — but the code said "unrecoverable," carried no hint about what *was* valid, and (thanks to a double-wrap bug) repeated itself. Faced with that, a model does the worst possible thing: it shrugs and guesses *another* model. We were manufacturing hallucinated retries.

## Two changes: classify it, and offer a list

The fix has a reactive half and a proactive half.

**Reactive — make the error caller-fixable.** A shared helper now classifies a gateway dispatch failure. If it's an unknown provider or a malformed model string, it becomes `invalid_input` — the code that means "you can fix this and retry" — with a message that says how:

```
invalid_input: claim extractor dispatch: unknown provider: minimax —
pick a configured model from the helmdeck://models resource (or GET
/v1/models); use the full provider/model id, e.g.
openrouter/minimax/minimax-m2.7, not minimax/…
```

Everything else still maps to `handler_failed`. And the detail now lives in one place (the message), so it doesn't print twice.

**Proactive — give the agent the actual list.** There was no way to *discover* valid chat models the way `helmdeck://voices` and `helmdeck://image-models` already let agents discover TTS voices and image models. So there's a new MCP resource, `helmdeck://models`, backed by the gateway's live registry — every routable `provider/model` ID, including `openrouter/minimax/minimax-m2.7`. The error points at it; so do the pipeline-builder tool and the agent skill. The agent reads it and picks a real model up front.

## The thing worth generalizing

We didn't add MiniMax as a provider. The bug was never "MiniMax isn't supported" — it's reachable, just under a different name. The bug was that the failure didn't tell anyone that.

The lesson is about error design for agents specifically: an error code is a *contract about recoverability*, and putting a caller-fixable failure under a not-your-fault code is worse than no code at all, because a capable model will trust the contract and act on it — by giving up and guessing. When a failure is the caller's to fix, say so, and say what "fixed" looks like. The cheapest way to stop a model hallucinating an answer is to hand it the real one.

See the [content.ground reference](/reference/packs/content/ground) for the model input and error codes, and [ADR 043](/adrs/043-actionable-gateway-model-errors) for the decision.
