---
slug: /explanation/
title: Explanation
description: Understanding-oriented background on helmdeck's design and security model.
---

# Explanation

Understanding-oriented background. These pages step back from the immediate task and explain the *why* behind helmdeck's design — the trade-offs, the threat model, the constraints that shaped the architecture.

## Available explanations

- **[Why helmdeck](./why-helmdeck.md)** — the cost-and-correctness argument: helmdeck lets cheap or local LLMs do agentic work that otherwise needs frontier models. Per-task comparisons against Anthropic Computer Use, OpenAI Operator, Browser-use, Cursor, and Aider, with a "test it yourself" reproduction recipe.
- **[Security hardening](../SECURITY-HARDENING.md)** — how helmdeck isolates browser sessions and credentials, what the threat model assumes, and which knobs you can tighten further in production.

## See also

The 36 [Architecture Decision Records](/adrs) under Reference are also explanation-shaped — each one captures the context, decision, and consequences of one architectural choice. They're filed under Reference because they're a structured catalog, but read individually they're explanation material.

## What goes here

Explanation is *understanding-oriented*. Unlike how-to guides (which assume you know what you want and just need the steps), explanations assume you have time to think and want to understand the reasons behind the design. They are the most discursive of the four modes.
