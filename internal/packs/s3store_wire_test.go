// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// s3store_wire_test.go covers the S3 ArtifactStore wire path against a
// minimal S3-protocol stub. The existing s3store_test.go has compile-
// time interface checks + an opt-in live integration test against a
// real MinIO (skipped in CI). PR E of the v0.25.0 reliability arc adds
// the missing layer: every operator deploying with MinIO/R2/B2/AWS S3
// depends on this code, and until now the in-CI test surface was 0%.
//
// Approach: an httptest.NewServer that speaks just enough of the S3
// REST + XML surface for the minio-go client to round-trip. We do not
// re-validate the signature — httptest accepts whatever Signature V4
// header minio-go sends. The tests focus on the operations helmdeck
// actually exercises: BucketExists (called at NewS3ArtifactStore time),
// PutObject, GetObject, ListObjectsV2 (the recursive lister), and
// RemoveObject. Presigned URLs are built client-side from the config
// + the SDK's signer, so they're tested via shape assertions rather
// than a round trip to the stub.

// stubS3Server is an httptest.NewServer that emulates the path-style
// S3 endpoints. Storage is in-memory and goroutine-safe. Each request
// is captured (path + method + headers) so tests can assert on
// signatures and request shape if a regression sneaks through.
type stubS3Server struct {
	mu      sync.Mutex
	objects map[string]stubS3Object
	calls   []stubS3Call
	// errMethod / errStatus / errBody — when set, every request whose
	// Method matches errMethod returns the given status + body. Persists
	// across the request (minio-go retries internally, so a one-shot
	// would clear on the first attempt and let the retry succeed).
	errMethod string
	errStatus int
	errBody   string
}

type stubS3Object struct {
	body        []byte
	contentType string
	lastMod     time.Time
}

type stubS3Call struct {
	Method string
	Path   string
	Query  string
}

// listBucketResult is the minimal XML envelope ListObjectsV2 returns.
// minio-go decodes this and yields one ObjectInfo per Contents entry.
type listBucketResult struct {
	XMLName  xml.Name       `xml:"ListBucketResult"`
	Name     string         `xml:"Name"`
	Prefix   string         `xml:"Prefix"`
	Contents []listObjEntry `xml:"Contents"`
	IsTrunc  bool           `xml:"IsTruncated"`
	NextTok  string         `xml:"NextContinuationToken,omitempty"`
	KeyCount int            `xml:"KeyCount"`
}

type listObjEntry struct {
	Key     string    `xml:"Key"`
	Size    int64     `xml:"Size"`
	LastMod time.Time `xml:"LastModified"`
	ETag    string    `xml:"ETag"`
}

func newStubS3Server(t *testing.T) *stubS3Server {
	t.Helper()
	return &stubS3Server{objects: make(map[string]stubS3Object)}
}

// start spins the httptest server, records its host, and registers
// the cleanup. Returns the host:port for use in S3Config.Endpoint.
func (s *stubS3Server) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Host
}

func (s *stubS3Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls = append(s.calls, stubS3Call{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
	})
	errMethod, errStatus, errBody := s.errMethod, s.errStatus, s.errBody
	s.mu.Unlock()
	if errMethod == r.Method && errStatus > 0 {
		w.WriteHeader(errStatus)
		_, _ = io.WriteString(w, errBody)
		return
	}

	// Path is /<bucket>/<key...> or /<bucket>. Strip the bucket; tests
	// only ever use one bucket so we don't need to track it here.
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		// Service-level call (rare for us); not modeled.
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Bucket-level operations (no object key).
	if len(parts) == 1 || parts[1] == "" {
		switch r.Method {
		case http.MethodHead:
			// BucketExists.
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodGet:
			// ListObjectsV2 (?list-type=2).
			s.respondListV2(w, r)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Object-level operations.
	key := parts[1]
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		// minio-go uses AWS chunked-signature streaming for PUT when
		// X-Amz-Content-Sha256 is STREAMING-AWS4-HMAC-SHA256-PAYLOAD.
		// Each chunk is `<hex-size>;chunk-signature=...\r\n<data>\r\n`,
		// terminated by a zero-size chunk. Decode to recover the raw
		// bytes so GET round-trips cleanly.
		if r.Header.Get("X-Amz-Content-Sha256") == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
			body = decodeAWSChunked(body)
		}
		s.mu.Lock()
		s.objects[key] = stubS3Object{
			body:        body,
			contentType: r.Header.Get("Content-Type"),
			lastMod:     time.Now().UTC(),
		}
		s.mu.Unlock()
		w.Header().Set("ETag", `"deadbeef"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		s.mu.Lock()
		obj, ok := s.objects[key]
		s.mu.Unlock()
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
			return
		}
		if obj.contentType != "" {
			w.Header().Set("Content-Type", obj.contentType)
		}
		w.Header().Set("Last-Modified", obj.lastMod.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj.body)))
		w.Header().Set("ETag", `"deadbeef"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(obj.body)
	case http.MethodHead:
		s.mu.Lock()
		obj, ok := s.objects[key]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if obj.contentType != "" {
			w.Header().Set("Content-Type", obj.contentType)
		}
		w.Header().Set("Last-Modified", obj.lastMod.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj.body)))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// decodeAWSChunked unwraps an AWS Signature V4 chunked-signed body
// (Content-Encoding: aws-chunked). Each chunk is:
//
//	<hex-chunk-size>;chunk-signature=<sig>\r\n
//	<chunk-data>\r\n
//
// Terminated by a zero-sized chunk. Returns the concatenated raw data.
func decodeAWSChunked(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		i := bytesIndexCRLF(b)
		if i < 0 {
			break
		}
		header := string(b[:i])
		rest := b[i+2:]
		// Header is "<hex-size>;chunk-signature=..."
		sizeStr := header
		if semi := strings.Index(header, ";"); semi >= 0 {
			sizeStr = header[:semi]
		}
		var size int64
		_, _ = fmt.Sscanf(sizeStr, "%x", &size)
		if size == 0 {
			break
		}
		if int64(len(rest)) < size {
			break
		}
		out = append(out, rest[:size]...)
		// Skip data + trailing \r\n.
		b = rest[size:]
		if len(b) >= 2 {
			b = b[2:]
		}
	}
	return out
}

func bytesIndexCRLF(b []byte) int {
	for i := 0; i < len(b)-1; i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func (s *stubS3Server) respondListV2(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	s.mu.Lock()
	defer s.mu.Unlock()
	result := listBucketResult{Name: "test-bucket", Prefix: prefix}
	for key, obj := range s.objects {
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		result.Contents = append(result.Contents, listObjEntry{
			Key:     key,
			Size:    int64(len(obj.body)),
			LastMod: obj.lastMod,
			ETag:    `"deadbeef"`,
		})
	}
	result.KeyCount = len(result.Contents)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(result)
}

// newStubbedStore builds an S3ArtifactStore wired to a fresh stub
// server. Bucket name "test-bucket" matches the stub's hard-coded
// response so ListObjectsV2 round-trips cleanly.
func newStubbedStore(t *testing.T) (*S3ArtifactStore, *stubS3Server) {
	t.Helper()
	stub := newStubS3Server(t)
	endpoint := stub.start(t)
	store, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:        endpoint,
		Bucket:          "test-bucket",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		Region:          "us-east-1",
		UseSSL:          false,
		PresignTTL:      5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewS3ArtifactStore: %v", err)
	}
	return store, stub
}

// TestS3ArtifactStore_PutRoundTrip — happy path: Put uploads, returns
// an Artifact with a sensibly-shaped presigned URL, Get retrieves the
// same bytes back, content-type and size match.
func TestS3ArtifactStore_PutRoundTrip(t *testing.T) {
	store, stub := newStubbedStore(t)
	ctx := context.Background()

	content := []byte("Hello, world!")
	a, err := store.Put(ctx, "image.generate", "test.png", content, "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if a.Size != int64(len(content)) {
		t.Errorf("Size = %d; want %d", a.Size, len(content))
	}
	if a.ContentType != "image/png" {
		t.Errorf("ContentType = %q", a.ContentType)
	}
	if a.Pack != "image.generate" {
		t.Errorf("Pack = %q", a.Pack)
	}
	if !strings.HasPrefix(a.Key, "image.generate/") {
		t.Errorf("Key %q should start with image.generate/", a.Key)
	}
	if !strings.HasSuffix(a.Key, "-test.png") {
		t.Errorf("Key %q should end with -test.png", a.Key)
	}
	if a.URL == "" {
		t.Error("URL should be a non-empty presigned GET")
	}
	// Presigned URLs always carry the X-Amz-Signature query param
	// — without it, an agent fetching the URL would be denied by the
	// real backend.
	parsed, err := url.Parse(a.URL)
	if err != nil {
		t.Fatalf("URL not parseable: %v", err)
	}
	if parsed.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("presigned URL missing X-Amz-Signature: %s", a.URL)
	}
	// CreatedAt is set from the store's `now` clock; should be very
	// recent.
	if time.Since(a.CreatedAt) > time.Minute {
		t.Errorf("CreatedAt %v should be recent", a.CreatedAt)
	}

	// Read it back.
	body, meta, err := store.Get(ctx, a.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != string(content) {
		t.Errorf("Get returned %q; want %q", body, content)
	}
	if meta.Size != int64(len(content)) {
		t.Errorf("Get meta size = %d", meta.Size)
	}
	if meta.ContentType != "image/png" {
		t.Errorf("Get meta content-type = %q", meta.ContentType)
	}

	// Confirm the stub saw PUT then GET on the right paths.
	puts := 0
	gets := 0
	for _, c := range stub.calls {
		if c.Method == http.MethodPut && strings.HasSuffix(c.Path, "-test.png") {
			puts++
		}
		if c.Method == http.MethodGet && strings.HasSuffix(c.Path, "-test.png") {
			gets++
		}
	}
	if puts != 1 || gets != 1 {
		t.Errorf("stub saw puts=%d gets=%d; want 1+1", puts, gets)
	}
}

// TestS3ArtifactStore_BucketExistsFailureSurfacesAtConstruction —
// when the bucket-exists check returns 404, NewS3ArtifactStore must
// fail with a clear error pointing at the missing bucket. Without
// this, a misconfigured operator would see no error at startup and a
// confusing "404 on Put" later.
func TestS3ArtifactStore_BucketExistsFailureSurfacesAtConstruction(t *testing.T) {
	stub := newStubS3Server(t)
	// Pre-arm a 404 for the HEAD request the constructor makes.
	stub.errMethod = http.MethodHead
	stub.errStatus = http.StatusNotFound
	endpoint := stub.start(t)

	_, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:    endpoint,
		Bucket:      "missing-bucket",
		AccessKeyID: "k", SecretAccessKey: "s",
		UseSSL: false,
	})
	if err == nil {
		t.Fatal("NewS3ArtifactStore should reject when bucket doesn't exist")
	}
	if !strings.Contains(err.Error(), "missing-bucket") {
		t.Errorf("error %q should name the missing bucket", err)
	}
}

// TestS3ArtifactStore_PutUpstreamErrorSurfacesPackError — when the
// upstream PutObject fails (500, network error, etc.), the store must
// wrap the failure as a *PackError with CodeArtifactFailed so the
// engine's typed-error contract holds.
func TestS3ArtifactStore_PutUpstreamErrorSurfacesPackError(t *testing.T) {
	store, stub := newStubbedStore(t)
	stub.errMethod = http.MethodPut
	stub.errStatus = http.StatusInternalServerError
	stub.errBody = `<Error><Code>InternalError</Code><Message>boom</Message></Error>`

	_, err := store.Put(context.Background(), "p", "x.bin", []byte("x"), "application/octet-stream")
	var perr *PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T (%v); want *PackError", err, err)
	}
	if perr.Code != CodeArtifactFailed {
		t.Errorf("code = %q; want %q", perr.Code, CodeArtifactFailed)
	}
}

// TestS3ArtifactStore_GetUnknownKeyIsPackError — Get on a missing key
// surfaces a typed *PackError. The artifact-download HTTP handler in
// internal/api/artifacts.go uses this to translate to 404; if the
// store returned a raw Go error, the handler would surface 500.
func TestS3ArtifactStore_GetUnknownKeyIsPackError(t *testing.T) {
	store, _ := newStubbedStore(t)
	_, _, err := store.Get(context.Background(), "no/such/key")
	var perr *PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T (%v); want *PackError", err, err)
	}
	if perr.Code != CodeArtifactFailed {
		t.Errorf("code = %q; want %q", perr.Code, CodeArtifactFailed)
	}
}

// TestS3ArtifactStore_ListForPackInMemoryIndex — ListForPack reads
// the in-process index (NOT the bucket), so a key written via Put is
// listed back even with a stub that returns an empty
// ListObjectsV2. Documents the contract that ListForPack is a
// cross-handler-within-this-run lookup, not a bucket scan.
func TestS3ArtifactStore_ListForPackInMemoryIndex(t *testing.T) {
	store, _ := newStubbedStore(t)
	ctx := context.Background()
	a1, _ := store.Put(ctx, "image.generate", "a.png", []byte("aaa"), "image/png")
	a2, _ := store.Put(ctx, "image.generate", "b.png", []byte("bbb"), "image/png")
	// A different pack's artifacts must not leak in.
	_, _ = store.Put(ctx, "screenshot_url", "c.png", []byte("ccc"), "image/png")

	list, err := store.ListForPack(ctx, "image.generate")
	if err != nil {
		t.Fatalf("ListForPack: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListForPack = %d entries; want 2", len(list))
	}
	keysByID := map[string]bool{list[0].Key: true, list[1].Key: true}
	if !keysByID[a1.Key] || !keysByID[a2.Key] {
		t.Errorf("ListForPack = %v; missing %s or %s", keysByID, a1.Key, a2.Key)
	}
}

// TestS3ArtifactStore_ListAllParsesPackPrefix — ListAll walks every
// bucket object and derives the Pack field from the key prefix (the
// `<pack>/<rand>-<name>` convention Put established). This is the
// TTL janitor's only entry point; if the prefix parse breaks, the
// janitor either deletes the wrong artifacts or stops working.
func TestS3ArtifactStore_ListAllParsesPackPrefix(t *testing.T) {
	store, _ := newStubbedStore(t)
	ctx := context.Background()
	_, _ = store.Put(ctx, "image.generate", "a.png", []byte("aaa"), "image/png")
	_, _ = store.Put(ctx, "screenshot_url", "b.png", []byte("bbb"), "image/png")

	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll = %d; want 2", len(all))
	}
	// Pack derivation: the key segment before the first '/'.
	gotPacks := map[string]bool{}
	for _, a := range all {
		gotPacks[a.Pack] = true
	}
	for _, want := range []string{"image.generate", "screenshot_url"} {
		if !gotPacks[want] {
			t.Errorf("ListAll did not return pack %q (parsed packs: %v)", want, gotPacks)
		}
	}
}

// TestS3ArtifactStore_DeleteRemovesAndUpdatesIndex — Delete removes
// the object from the bucket AND drops the corresponding in-memory
// index entry. Without the index update, a follow-up ListForPack
// would return a stale handle whose presigned URL still points at the
// deleted object.
func TestS3ArtifactStore_DeleteRemovesAndUpdatesIndex(t *testing.T) {
	store, _ := newStubbedStore(t)
	ctx := context.Background()
	a, _ := store.Put(ctx, "p", "doomed.png", []byte("x"), "image/png")

	if err := store.Delete(ctx, a.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Index should no longer carry this key.
	list, _ := store.ListForPack(ctx, "p")
	for _, e := range list {
		if e.Key == a.Key {
			t.Errorf("Delete left stale index entry: %s", a.Key)
		}
	}
	// Get against the deleted key now fails.
	if _, _, err := store.Get(ctx, a.Key); err == nil {
		t.Error("Get after Delete should fail")
	}
}

// TestS3ArtifactStore_DeleteUpstreamErrorSurfacesPackError —
// the same typed-error contract holds for Delete as for Put / Get.
func TestS3ArtifactStore_DeleteUpstreamErrorSurfacesPackError(t *testing.T) {
	store, stub := newStubbedStore(t)
	stub.errMethod = http.MethodDelete
	stub.errStatus = http.StatusInternalServerError
	stub.errBody = `<Error><Code>InternalError</Code></Error>`

	err := store.Delete(context.Background(), "p/x")
	var perr *PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T (%v); want *PackError", err, err)
	}
	if perr.Code != CodeArtifactFailed {
		t.Errorf("code = %q; want %q", perr.Code, CodeArtifactFailed)
	}
}

// TestS3ArtifactStore_PublicEndpointRewritesPresignedHost —
// PublicEndpoint is the seam that lets a control plane talk to MinIO
// over a docker-internal name while agents fetch via a public DNS
// name. If the rewrite breaks, agents reach for the internal name
// and connection-refuse. This pins the host swap.
func TestS3ArtifactStore_PublicEndpointRewritesPresignedHost(t *testing.T) {
	stub := newStubS3Server(t)
	endpoint := stub.start(t)
	store, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:    endpoint,
		Bucket:      "test-bucket",
		AccessKeyID: "k", SecretAccessKey: "s",
		Region:         "us-east-1",
		PresignTTL:     time.Hour,
		PublicEndpoint: "artifacts.example.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	a, err := store.Put(context.Background(), "p", "x.png", []byte("x"), "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	parsed, err := url.Parse(a.URL)
	if err != nil {
		t.Fatalf("URL not parseable: %v", err)
	}
	if parsed.Host != "artifacts.example.com" {
		t.Errorf("presigned URL host = %q; want artifacts.example.com (PublicEndpoint should rewrite)", parsed.Host)
	}
}

// TestS3ArtifactStore_PresignTTLDefaulted — zero TTL falls back to
// the documented 15-minute default. A regression that ships PresignTTL=0
// through to PresignedGetObject would either return a forever-valid URL
// (a security regression) or an immediately-expired one.
func TestS3ArtifactStore_PresignTTLDefaulted(t *testing.T) {
	stub := newStubS3Server(t)
	endpoint := stub.start(t)
	store, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:    endpoint,
		Bucket:      "test-bucket",
		AccessKeyID: "k", SecretAccessKey: "s",
		PresignTTL: 0, // explicit zero
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if store.cfg.PresignTTL != 15*time.Minute {
		t.Errorf("PresignTTL default = %v; want 15m", store.cfg.PresignTTL)
	}
}

// TestS3ArtifactStore_RegionDefaultedToUSEast1 — the documented
// default. Non-AWS backends (MinIO) need a region for the signing
// path even when they don't enforce one.
func TestS3ArtifactStore_RegionDefaultedToUSEast1(t *testing.T) {
	stub := newStubS3Server(t)
	endpoint := stub.start(t)
	store, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:    endpoint,
		Bucket:      "test-bucket",
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if store.cfg.Region != "us-east-1" {
		t.Errorf("Region default = %q; want us-east-1", store.cfg.Region)
	}
}
