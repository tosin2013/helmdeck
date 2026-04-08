package inject

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/cdp"
	cdpfake "github.com/tosin2013/helmdeck/internal/cdp/fake"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

func newInjector(t *testing.T) (*Injector, *vault.Store) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	v, err := vault.New(db, key)
	if err != nil {
		t.Fatal(err)
	}
	return New(v, slog.New(slog.NewTextHandler(io.Discard, nil))), v
}

func TestInject_NoVaultIsNoOp(t *testing.T) {
	inj := New(nil, nil)
	c := &cdpfake.Client{}
	res, err := inj.Inject(context.Background(), c, "https://github.com/foo", vault.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "skipped" {
		t.Errorf("expected skipped, got %s", res.Action)
	}
}

func TestInject_NoMatch(t *testing.T) {
	inj, _ := newInjector(t)
	c := &cdpfake.Client{}
	res, _ := inj.Inject(context.Background(), c, "https://nothing.example", vault.Actor{Subject: "alice"})
	if res.Action != "no_match" {
		t.Errorf("expected no_match, got %s", res.Action)
	}
}

func TestInject_DeniedWhenNoGrant(t *testing.T) {
	inj, v := newInjector(t)
	_, _ = v.Create(context.Background(), vault.CreateInput{
		Name: "gh", Type: vault.TypeCookie, HostPattern: "github.com",
		Plaintext: []byte(`[{"name":"session","value":"abc"}]`),
	})
	c := &cdpfake.Client{}
	res, _ := inj.Inject(context.Background(), c, "https://github.com/", vault.Actor{Subject: "alice"})
	if res.Action != "denied" {
		t.Errorf("expected denied, got %s", res.Action)
	}
}

func TestInject_CookieInstall(t *testing.T) {
	inj, v := newInjector(t)
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "gh", Type: vault.TypeCookie, HostPattern: "github.com",
		Plaintext: []byte(`[{"name":"session","value":"abc","secure":true}]`),
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "alice"})

	c := &cdpfake.Client{}
	res, err := inj.Inject(context.Background(), c, "https://github.com/foo", vault.Actor{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "cookies_installed" {
		t.Errorf("expected cookies_installed, got %s", res.Action)
	}
	if !res.Matched || res.CredentialID != rec.ID {
		t.Errorf("matched/credential_id wrong: %+v", res)
	}
	if len(c.CookiesSet) != 1 || len(c.CookiesSet[0]) != 1 {
		t.Fatalf("expected 1 SetCookies call with 1 cookie: %+v", c.CookiesSet)
	}
	got := c.CookiesSet[0][0]
	if got.Name != "session" || got.Value != "abc" || !got.Secure {
		t.Errorf("cookie wrong: %+v", got)
	}
	if got.Domain != "github.com" {
		t.Errorf("domain not defaulted to host: %s", got.Domain)
	}
}

func TestInject_LoginAutofill(t *testing.T) {
	inj, v := newInjector(t)
	plaintext, _ := json.Marshal(map[string]string{"username": "alice", "password": "hunter2"})
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "site", Type: vault.TypeLogin, HostPattern: "example.com",
		Plaintext: plaintext,
		Metadata: map[string]any{
			"form_fields": map[string]any{
				"input[name=email]":    "username",
				"input[name=password]": "password",
			},
		},
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "alice"})

	c := &cdpfake.Client{}
	res, err := inj.Inject(context.Background(), c, "https://example.com/login", vault.Actor{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "form_autofilled" {
		t.Errorf("expected form_autofilled, got %s", res.Action)
	}
	if len(c.AutofillCalls) != 1 {
		t.Fatalf("expected 1 AutofillForm call, got %d", len(c.AutofillCalls))
	}
	fields := c.AutofillCalls[0]
	if fields["input[name=email]"] != "alice" || fields["input[name=password]"] != "hunter2" {
		t.Errorf("autofill fields wrong: %+v", fields)
	}
}

func TestInject_LoginMissingMetadataSkips(t *testing.T) {
	inj, v := newInjector(t)
	plaintext, _ := json.Marshal(map[string]string{"username": "alice"})
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "site", Type: vault.TypeLogin, HostPattern: "example.com",
		Plaintext: plaintext,
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "alice"})
	c := &cdpfake.Client{}
	res, _ := inj.Inject(context.Background(), c, "https://example.com/", vault.Actor{Subject: "alice"})
	if res.Action != "skipped" {
		t.Errorf("expected skipped without form_fields metadata, got %s", res.Action)
	}
	if len(c.AutofillCalls) != 0 {
		t.Errorf("AutofillForm should not be called: %+v", c.AutofillCalls)
	}
}

func TestInject_APIKeyTypeIsSkipped(t *testing.T) {
	inj, v := newInjector(t)
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "k", Type: vault.TypeAPIKey, HostPattern: "api.example.com", Plaintext: []byte("token"),
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "alice"})
	c := &cdpfake.Client{}
	res, _ := inj.Inject(context.Background(), c, "https://api.example.com/", vault.Actor{Subject: "alice"})
	if res.Action != "skipped" {
		t.Errorf("api_key type should be skipped, got %s", res.Action)
	}
	if len(c.CookiesSet) != 0 || len(c.AutofillCalls) != 0 {
		t.Error("api_key should not touch cookies or autofill")
	}
}

func TestInject_NoHostInURLSkips(t *testing.T) {
	inj, _ := newInjector(t)
	c := &cdpfake.Client{}
	res, _ := inj.Inject(context.Background(), c, "data:text/html,<h1>x</h1>", vault.Actor{Subject: "alice"})
	if res.Action != "skipped" {
		t.Errorf("data: URL should skip, got %s", res.Action)
	}
}

func TestInject_CookieDomainExplicit(t *testing.T) {
	inj, v := newInjector(t)
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "gh", Type: vault.TypeCookie, HostPattern: "*.github.com",
		Plaintext: []byte(`[{"name":"s","value":"v","domain":".github.com"}]`),
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "alice"})
	c := &cdpfake.Client{}
	_, _ = inj.Inject(context.Background(), c, "https://api.github.com/foo", vault.Actor{Subject: "alice"})
	if c.CookiesSet[0][0].Domain != ".github.com" {
		t.Errorf("explicit domain should not be overridden: %s", c.CookiesSet[0][0].Domain)
	}
}

// compile-time assertion: fake.Client must satisfy cdp.Client after T503.
var _ cdp.Client = (*cdpfake.Client)(nil)
