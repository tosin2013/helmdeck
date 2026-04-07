# 20. Pack: `web.fill_form`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Form submission against authenticated SaaS apps requires session management, vault credential injection, field detection, value entry, submission, and confirmation — a high-failure-rate workflow for weak models (PRD §6.6).

## Decision
Ship `web.fill_form` as a built-in pack.

**Input:** `{ url: string, credential_id?: string, field_map: object<string, string>, submit_selector?: string, confirm_selector?: string }`
**Output:** `{ success: boolean, final_url: string, confirmation_text?: string }`
**Errors:** `auth_failed`, `not_found`, `schema_mismatch`, `timeout`, `network_error`

The handler optionally injects credentials, navigates to the form, maps each `field_map` key to an input via CSS selector / `name` / `id` / `aria-label`, types values, clicks submit, and waits for the confirm selector or URL change. Returns success only if confirmation is observed.

## Consequences
**Positive:** form submission becomes a single declarative call; credential never exposed.
**Negative:** dynamic forms with conditional fields require per-site overrides.

## Related PRD Sections
§6.6 Capability Packs, §14 Credential Vault
