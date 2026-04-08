package packs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func dummyHandler(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewPackRegistry()
	if err := r.Register(&Pack{Name: "echo", Version: "v1", Handler: dummyHandler}); err != nil {
		t.Fatal(err)
	}
	p, err := r.Get("echo", "v1")
	if err != nil || p.Name != "echo" {
		t.Errorf("get = %v %v", p, err)
	}
}

func TestRegistryLatestResolution(t *testing.T) {
	r := NewPackRegistry()
	for _, v := range []string{"v1", "v3", "v2"} {
		_ = r.Register(&Pack{Name: "echo", Version: v, Handler: dummyHandler})
	}
	p, err := r.Get("echo", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "v3" {
		t.Errorf("latest = %q, want v3", p.Version)
	}
	p, err = r.Get("echo", "latest")
	if err != nil || p.Version != "v3" {
		t.Errorf("latest alias = %q %v", p.Version, err)
	}
}

func TestRegistryNotFound(t *testing.T) {
	r := NewPackRegistry()
	_, err := r.Get("missing", "")
	if !errors.Is(err, ErrPackNotFound) {
		t.Errorf("err = %v", err)
	}
	_ = r.Register(&Pack{Name: "x", Version: "v1", Handler: dummyHandler})
	_, err = r.Get("x", "v9")
	if !errors.Is(err, ErrPackNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestRegistryRejectsBadVersion(t *testing.T) {
	r := NewPackRegistry()
	bad := []string{"", "1", "1.0", "vX", "v0", "v-1"}
	for _, v := range bad {
		if err := r.Register(&Pack{Name: "x", Version: v, Handler: dummyHandler}); err == nil {
			t.Errorf("version %q accepted", v)
		}
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewPackRegistry()
	_ = r.Register(&Pack{Name: "x", Version: "v1", Handler: dummyHandler})
	_ = r.Register(&Pack{Name: "x", Version: "v2", Handler: dummyHandler})
	r.Unregister("x", "v1")
	if _, err := r.Get("x", "v1"); !errors.Is(err, ErrPackNotFound) {
		t.Error("v1 still present after unregister")
	}
	if _, err := r.Get("x", ""); err != nil {
		t.Error("v2 should still resolve as latest")
	}
	r.Unregister("x", "v2")
	if len(r.List()) != 0 {
		t.Errorf("list = %d, want 0 after removing all versions", len(r.List()))
	}
}

func TestRegistryListSorted(t *testing.T) {
	r := NewPackRegistry()
	_ = r.Register(&Pack{Name: "echo", Version: "v2", Description: "echo", Handler: dummyHandler})
	_ = r.Register(&Pack{Name: "echo", Version: "v1", Handler: dummyHandler})
	_ = r.Register(&Pack{Name: "abc", Version: "v1", Handler: dummyHandler})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	if list[0].Name != "abc" || list[1].Name != "echo" {
		t.Errorf("not sorted by name: %+v", list)
	}
	if list[1].Latest != "v2" {
		t.Errorf("latest = %q", list[1].Latest)
	}
	if list[1].Versions[0] != "v1" || list[1].Versions[1] != "v2" {
		t.Errorf("versions not oldest-first: %v", list[1].Versions)
	}
}
