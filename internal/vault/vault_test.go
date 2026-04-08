package vault

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/store"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := New(db, key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func TestStore_CreateAndGet(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, err := v.Create(ctx, CreateInput{
		Name:        "github-token",
		Type:        TypeAPIKey,
		HostPattern: "api.github.com",
		Plaintext:   []byte("ghp_abc123"),
		Metadata:    map[string]any{"created_for": "ci"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.ID == "" || !strings.HasPrefix(rec.ID, "cred_") {
		t.Errorf("bad id: %s", rec.ID)
	}
	if rec.Fingerprint == "" {
		t.Errorf("missing fingerprint")
	}

	got, err := v.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "github-token" || got.Type != TypeAPIKey || got.HostPattern != "api.github.com" {
		t.Errorf("get returned wrong record: %+v", got)
	}
	if got.Metadata["created_for"] != "ci" {
		t.Errorf("metadata not round-tripped: %+v", got.Metadata)
	}
}

func TestStore_DuplicateNameRejected(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	_, _ = v.Create(ctx, CreateInput{Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p")})
	_, err := v.Create(ctx, CreateInput{Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p")})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
}

func TestStore_ResolveDeniedWithoutGrant(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "gh", Type: TypeAPIKey, HostPattern: "api.github.com", Plaintext: []byte("secret"),
	})
	_, err := v.Resolve(ctx, Actor{Subject: "alice"}, "api.github.com", "/repos")
	if !errors.Is(err, ErrDenied) {
		t.Errorf("expected ErrDenied with no grant, got %v", err)
	}

	// After grant, the same actor should succeed.
	if err := v.Grant(ctx, rec.ID, Grant{ActorSubject: "alice"}); err != nil {
		t.Fatal(err)
	}
	res, err := v.Resolve(ctx, Actor{Subject: "alice"}, "api.github.com", "/repos")
	if err != nil {
		t.Fatalf("Resolve after grant: %v", err)
	}
	if string(res.Plaintext) != "secret" {
		t.Errorf("plaintext wrong: %q", res.Plaintext)
	}
	if res.Record.ID != rec.ID {
		t.Errorf("wrong credential resolved: %s", res.Record.ID)
	}
}

func TestStore_ResolveWildcardSubject(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p"),
	})
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "*"})
	if _, err := v.Resolve(ctx, Actor{Subject: "anyone"}, "h", "/"); err != nil {
		t.Errorf("wildcard grant should allow any subject: %v", err)
	}
}

func TestStore_ResolveClientFiltering(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p"),
	})
	// Grant only to claude-code clients.
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "alice", ActorClient: "claude-code"})

	if _, err := v.Resolve(ctx, Actor{Subject: "alice", Client: "claude-code"}, "h", "/"); err != nil {
		t.Errorf("matching client should be allowed: %v", err)
	}
	if _, err := v.Resolve(ctx, Actor{Subject: "alice", Client: "openclaw"}, "h", "/"); !errors.Is(err, ErrDenied) {
		t.Errorf("non-matching client should be denied, got %v", err)
	}
}

func TestStore_ResolveNoMatch(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	_, err := v.Resolve(ctx, Actor{Subject: "*"}, "nothing.example", "/")
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestStore_HostGlobMatch(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "any-gh", Type: TypeAPIKey, HostPattern: "*.github.com", Plaintext: []byte("globsecret"),
	})
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "*"})
	res, err := v.Resolve(ctx, Actor{Subject: "alice"}, "api.github.com", "/")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Plaintext) != "globsecret" {
		t.Errorf("wrong plaintext: %q", res.Plaintext)
	}
}

func TestStore_SpecificityWinsOverGlob(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	gloRec, _ := v.Create(ctx, CreateInput{
		Name: "glob", Type: TypeAPIKey, HostPattern: "*.example.com", Plaintext: []byte("glob"),
	})
	litRec, _ := v.Create(ctx, CreateInput{
		Name: "literal", Type: TypeAPIKey, HostPattern: "api.example.com", Plaintext: []byte("literal"),
	})
	_ = v.Grant(ctx, gloRec.ID, Grant{ActorSubject: "*"})
	_ = v.Grant(ctx, litRec.ID, Grant{ActorSubject: "*"})
	res, err := v.Resolve(ctx, Actor{Subject: "alice"}, "api.example.com", "/")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Plaintext) != "literal" {
		t.Errorf("literal pattern should win over glob, got %q", res.Plaintext)
	}
}

func TestStore_PathPrefixMatch(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	r1, _ := v.Create(ctx, CreateInput{
		Name: "all", Type: TypeAPIKey, HostPattern: "h", PathPattern: "", Plaintext: []byte("all"),
	})
	r2, _ := v.Create(ctx, CreateInput{
		Name: "specific", Type: TypeAPIKey, HostPattern: "h", PathPattern: "/api/v2/", Plaintext: []byte("v2"),
	})
	_ = v.Grant(ctx, r1.ID, Grant{ActorSubject: "*"})
	_ = v.Grant(ctx, r2.ID, Grant{ActorSubject: "*"})

	res, _ := v.Resolve(ctx, Actor{Subject: "alice"}, "h", "/api/v2/users")
	if string(res.Plaintext) != "v2" {
		t.Errorf("longer path prefix should win, got %q", res.Plaintext)
	}
	res2, _ := v.Resolve(ctx, Actor{Subject: "alice"}, "h", "/")
	if string(res2.Plaintext) != "all" {
		t.Errorf("fallthrough to empty-path credential should work, got %q", res2.Plaintext)
	}
}

func TestStore_RotatePreservesIdentity(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("v1"),
	})
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "*"})
	oldFP := rec.Fingerprint

	rotated, err := v.Rotate(ctx, rec.ID, []byte("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != rec.ID {
		t.Errorf("id changed on rotate: %s -> %s", rec.ID, rotated.ID)
	}
	if rotated.Fingerprint == oldFP {
		t.Errorf("fingerprint should change on rotate")
	}
	res, _ := v.Resolve(ctx, Actor{Subject: "alice"}, "h", "/")
	if string(res.Plaintext) != "v2" {
		t.Errorf("rotated plaintext not returned: %q", res.Plaintext)
	}
}

func TestStore_DeleteCascadesACL(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p"),
	})
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "alice"})
	if err := v.Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	grants, _ := v.Grants(ctx, rec.ID)
	if len(grants) != 0 {
		t.Errorf("ACL should cascade-delete, got %d grants", len(grants))
	}
}

func TestStore_UsageLogRecords(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, _ := v.Create(ctx, CreateInput{
		Name: "x", Type: TypeAPIKey, HostPattern: "h", Plaintext: []byte("p"),
	})
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "alice"})

	// Successful resolve.
	_, _ = v.Resolve(ctx, Actor{Subject: "alice"}, "h", "/")
	// Denied resolve (no grant for bob).
	_, _ = v.Resolve(ctx, Actor{Subject: "bob"}, "h", "/")
	// no_match resolve.
	_, _ = v.Resolve(ctx, Actor{Subject: "alice"}, "elsewhere", "/")

	entries, err := v.Usage(ctx, rec.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2 entries against this credential id (allowed + denied);
	// the no_match is logged with empty credential_id, not this one.
	if len(entries) != 2 {
		t.Fatalf("expected 2 usage entries, got %d: %+v", len(entries), entries)
	}
	results := map[string]int{}
	for _, e := range entries {
		results[e.Result]++
	}
	if results["allowed"] != 1 || results["denied"] != 1 {
		t.Errorf("usage results wrong: %v", results)
	}
}

func TestStore_HostMatchEdgeCases(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"github.com", "github.com", true},
		{"github.com", "api.github.com", false},
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "deep.api.github.com", true},
		{"*.github.com", "github.com", false},
		{"api.*.com", "api.github.com", true},
		{"*", "anything.example", true},
		{"", "github.com", false},
	}
	for _, tc := range cases {
		got := matchHost(tc.pattern, tc.host)
		if got != tc.want {
			t.Errorf("matchHost(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestStore_RejectsInvalidType(t *testing.T) {
	v := newTestStore(t)
	_, err := v.Create(context.Background(), CreateInput{
		Name: "x", Type: "weird", HostPattern: "h", Plaintext: []byte("p"),
	})
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestParseMasterKey(t *testing.T) {
	hex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	b, err := ParseMasterKey(hex)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 32 || b[0] != 0x01 || b[31] != 0x20 {
		t.Errorf("decoded wrong: %x", b)
	}
	if _, err := ParseMasterKey("notenough"); err == nil {
		t.Error("expected error for short hex")
	}
}
