// Package inject bridges the credential vault and the CDP browser
// client (T503, ADR 007). It is the in-process layer that turns
// "navigate to https://github.com/foo/bar" into "look up github.com
// in the vault, install the matching cookies (or autofill the login
// form), and only then dispatch the navigation".
//
// The injector is the *only* component outside the vault package that
// holds plaintext secrets — by design, the secret transits inject for
// the duration of one Inject call and is not retained anywhere else.
// Pack handlers reach this layer via the existing
// /api/v1/browser/navigate endpoint, which calls Inject before
// chromedp.Navigate. The placeholder-token egress gateway (T504) and
// the repo packs (T505/T506) consume vault credentials directly,
// bypassing this layer.
//
// Credential type semantics:
//
//	cookie  -> SetCookies (browser session reuse)
//	login   -> AutofillForm (post-navigation, depends on the page DOM)
//	api_key -> no-op (these are HTTP headers, handled by T504)
//	oauth   -> no-op (T504 + token refresh in T502 follow-on)
//	ssh     -> no-op (file-system credential, handled by T505)
package inject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// Injector is the bridge object. It holds a vault store reference
// and is safe for concurrent use because the vault and the cdp
// clients it operates on are themselves goroutine-safe.
type Injector struct {
	vault  *vault.Store
	logger *slog.Logger
}

// New constructs an Injector. v may be nil — in that case Inject is
// a no-op so callers don't need to special-case missing vaults.
func New(v *vault.Store, logger *slog.Logger) *Injector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Injector{vault: v, logger: logger}
}

// Result describes what an Inject call did. Empty when no credential
// matched (the common case for unauthenticated navigation).
type Result struct {
	Matched      bool
	CredentialID string
	Type         vault.CredentialType
	Action       string // "cookies_installed" | "form_autofilled" | "skipped" | "no_match" | "denied"
}

// Inject looks up a credential matching targetURL's host/path for
// actor and applies it to the given cdp.Client.
//
// For cookie credentials: SetCookies is called BEFORE returning so
// the caller's subsequent Navigate sees the session.
//
// For login credentials: the caller is expected to navigate to the
// target URL first; this method does NOT navigate, it only sets up
// the credential payload. After navigation, the caller invokes
// AutofillForm via the returned Result. The reason for the split is
// that the form selectors live with the caller (a pack manifest or
// the navigate handler's config) and the page DOM doesn't exist
// until the navigation completes.
//
// To keep the caller's flow simple, when the credential type is
// "login" we DO call cdp.AutofillForm if the credential metadata
// includes a "form_fields" object mapping selectors to plaintext
// payload field names. The metadata layout is:
//
//	{
//	  "form_fields": {
//	    "input[name=email]":    "username",
//	    "input[name=password]": "password"
//	  }
//	}
//
// The plaintext payload is parsed as JSON {"username":"...","password":"..."}.
//
// Inject does NOT navigate. It is safe to call before or after
// Navigate; cookie credentials want before, login credentials want
// after. The browser handler in the API layer encodes that order.
func (i *Injector) Inject(ctx context.Context, c cdp.Client, targetURL string, actor vault.Actor) (Result, error) {
	if i == nil || i.vault == nil {
		return Result{Action: "skipped"}, nil
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return Result{}, fmt.Errorf("inject: parse url: %w", err)
	}
	if u.Host == "" {
		return Result{Action: "skipped"}, nil
	}
	res, err := i.vault.Resolve(ctx, actor, u.Hostname(), u.Path)
	switch {
	case errors.Is(err, vault.ErrNoMatch):
		return Result{Action: "no_match"}, nil
	case errors.Is(err, vault.ErrDenied):
		return Result{Action: "denied"}, nil
	case err != nil:
		return Result{}, fmt.Errorf("inject: vault resolve: %w", err)
	}
	out := Result{
		Matched:      true,
		CredentialID: res.Record.ID,
		Type:         res.Record.Type,
	}
	switch res.Record.Type {
	case vault.TypeCookie:
		cookies, err := parseCookiePayload(res.Plaintext, u.Hostname())
		if err != nil {
			return out, fmt.Errorf("inject: parse cookies: %w", err)
		}
		if err := c.SetCookies(ctx, cookies); err != nil {
			return out, fmt.Errorf("inject: set cookies: %w", err)
		}
		out.Action = "cookies_installed"
		i.logger.Info("vault: cookies installed",
			"credential_id", res.Record.ID, "host", u.Hostname(), "count", len(cookies))
	case vault.TypeLogin:
		fields, err := buildAutofillFields(res.Record.Metadata, res.Plaintext)
		if err != nil {
			return out, fmt.Errorf("inject: build autofill fields: %w", err)
		}
		if len(fields) == 0 {
			out.Action = "skipped"
			break
		}
		if err := c.AutofillForm(ctx, fields); err != nil {
			return out, fmt.Errorf("inject: autofill: %w", err)
		}
		out.Action = "form_autofilled"
		i.logger.Info("vault: form autofilled",
			"credential_id", res.Record.ID, "host", u.Hostname(), "fields", len(fields))
	default:
		// api_key / oauth / ssh — these are not browser-injectable.
		// The placeholder-token gateway (T504) and the repo packs
		// (T505) consume them directly.
		out.Action = "skipped"
	}
	return out, nil
}

// parseCookiePayload decodes the vault plaintext for a cookie
// credential. Two payload shapes are accepted:
//
// 1. JSON array of {name, value, domain?, path?, secure?, ...}
// 2. JSON array of {name, value} — domain defaults to fallbackHost
//
// The domain default lets operators store browser cookies without
// duplicating the host pattern in every entry.
func parseCookiePayload(plaintext []byte, fallbackHost string) ([]cdp.Cookie, error) {
	var raw []cdp.Cookie
	if err := json.Unmarshal(plaintext, &raw); err != nil {
		return nil, err
	}
	for i := range raw {
		if raw[i].Domain == "" {
			raw[i].Domain = fallbackHost
		}
	}
	return raw, nil
}

// buildAutofillFields returns selector→value pairs by combining the
// credential's metadata.form_fields map with the plaintext JSON
// {field_name: value} payload.
//
// metadata.form_fields maps selectors to payload field names:
//
//	{"input[name=email]": "username", "input[name=password]": "password"}
//
// plaintext is JSON like:
//
//	{"username": "alice", "password": "hunter2"}
//
// Selectors whose payload field is missing get dropped.
func buildAutofillFields(metadata map[string]any, plaintext []byte) (map[string]string, error) {
	var payload map[string]string
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, err
	}
	rawFields, ok := metadata["form_fields"]
	if !ok {
		return nil, nil
	}
	form, ok := rawFields.(map[string]any)
	if !ok {
		return nil, errors.New("metadata.form_fields must be an object")
	}
	out := make(map[string]string, len(form))
	for selector, fieldNameAny := range form {
		fieldName, ok := fieldNameAny.(string)
		if !ok {
			continue
		}
		if val, ok := payload[fieldName]; ok && val != "" {
			out[selector] = val
		}
	}
	return out, nil
}
