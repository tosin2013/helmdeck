package keystore

import (
	"context"
	"errors"
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
		key[i] = byte(i)
	}
	ks, err := New(db, key)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return ks
}

func TestCreateListGetDecrypt(t *testing.T) {
	ks := newTestStore(t)
	ctx := context.Background()

	rec, err := ks.Create(ctx, "openai", "prod", "sk-supersecret-1234")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Last4 != "1234" {
		t.Errorf("last4 = %q", rec.Last4)
	}
	if rec.Fingerprint == "" || len(rec.Fingerprint) != 16 {
		t.Errorf("fingerprint = %q", rec.Fingerprint)
	}

	got, err := ks.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Provider != "openai" || got.Label != "prod" {
		t.Errorf("get = %+v", got)
	}

	pt, err := ks.Decrypt(ctx, rec.ID)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if pt != "sk-supersecret-1234" {
		t.Errorf("plaintext mismatch: %q", pt)
	}

	list, err := ks.List(ctx, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", err, list)
	}
	if list[0].LastUsedAt == nil {
		t.Error("expected last_used_at populated after Decrypt")
	}
}

func TestCreateDuplicate(t *testing.T) {
	ks := newTestStore(t)
	ctx := context.Background()
	if _, err := ks.Create(ctx, "openai", "prod", "sk-a"); err != nil {
		t.Fatal(err)
	}
	_, err := ks.Create(ctx, "openai", "prod", "sk-b")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
}

func TestRotate(t *testing.T) {
	ks := newTestStore(t)
	ctx := context.Background()
	rec, _ := ks.Create(ctx, "anthropic", "prod", "sk-old-1111")
	old := rec.Fingerprint

	rotated, err := ks.Rotate(ctx, rec.ID, "sk-new-2222")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.ID != rec.ID {
		t.Error("id changed across rotation")
	}
	if rotated.Fingerprint == old {
		t.Error("fingerprint did not change")
	}
	if rotated.Last4 != "2222" {
		t.Errorf("last4 = %q", rotated.Last4)
	}

	pt, _ := ks.Decrypt(ctx, rec.ID)
	if pt != "sk-new-2222" {
		t.Errorf("decrypt after rotate = %q", pt)
	}
}

func TestDelete(t *testing.T) {
	ks := newTestStore(t)
	ctx := context.Background()
	rec, _ := ks.Create(ctx, "openai", "prod", "sk-x")
	if err := ks.Delete(ctx, rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ks.Get(ctx, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := ks.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}

func TestNewRequires32Bytes(t *testing.T) {
	db, _ := store.Open(":memory:")
	defer db.Close()
	if _, err := New(db, []byte("short")); err == nil {
		t.Error("expected error for short key")
	}
}

func TestParseAndGenerateMasterKey(t *testing.T) {
	hexKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseMasterKey(hexKey)
	if err != nil || len(b) != 32 {
		t.Errorf("parse: %v len=%d", err, len(b))
	}
}

func TestCiphertextDoesNotContainPlaintext(t *testing.T) {
	ks := newTestStore(t)
	ctx := context.Background()
	plaintext := "sk-needle-in-a-haystack"
	rec, _ := ks.Create(ctx, "openai", "prod", plaintext)
	// Read the raw row directly to confirm AES did its job.
	var ct []byte
	if err := ks.db.QueryRowContext(ctx, `SELECT ciphertext FROM provider_keys WHERE id = ?`, rec.ID).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if string(ct) == plaintext {
		t.Error("ciphertext equals plaintext")
	}
	for i := 0; i+len(plaintext) <= len(ct); i++ {
		if string(ct[i:i+len(plaintext)]) == plaintext {
			t.Error("plaintext substring found in ciphertext")
		}
	}
}
