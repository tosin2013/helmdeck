package packs

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestS3ArtifactStoreSatisfiesInterface is a compile-time guarantee
// that the S3 store can drop in wherever ArtifactStore is expected.
// If this stops compiling, the engine wiring will silently fall
// back to the in-memory store at runtime — exactly the kind of
// regression a unit test should catch.
func TestS3ArtifactStoreSatisfiesInterface(t *testing.T) {
	var _ ArtifactStore = (*S3ArtifactStore)(nil)
}

func TestS3ConfigValidation(t *testing.T) {
	cases := map[string]S3Config{
		"missing endpoint": {Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"missing bucket":   {Endpoint: "e", AccessKeyID: "k", SecretAccessKey: "s"},
		"missing key":      {Endpoint: "e", Bucket: "b", SecretAccessKey: "s"},
		"missing secret":   {Endpoint: "e", Bucket: "b", AccessKeyID: "k"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3ArtifactStore(context.Background(), cfg); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

// TestS3ArtifactStoreLive runs the full Put → ListForPack → Get →
// presigned URL round trip against a real S3-compatible backend.
// Skipped unless HELMDECK_S3_TEST_ENDPOINT is set so CI doesn't need
// MinIO to pass; run locally with:
//
//	docker run -d -p 9000:9000 minio/minio server /data
//	mc alias set local http://localhost:9000 minioadmin minioadmin
//	mc mb local/helmdeck-test
//	HELMDECK_S3_TEST_ENDPOINT=localhost:9000 \
//	  HELMDECK_S3_TEST_BUCKET=helmdeck-test \
//	  HELMDECK_S3_TEST_KEY=minioadmin \
//	  HELMDECK_S3_TEST_SECRET=minioadmin \
//	  go test ./internal/packs -run TestS3ArtifactStoreLive
func TestS3ArtifactStoreLive(t *testing.T) {
	endpoint := os.Getenv("HELMDECK_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("HELMDECK_S3_TEST_ENDPOINT not set")
	}
	store, err := NewS3ArtifactStore(context.Background(), S3Config{
		Endpoint:        endpoint,
		Bucket:          os.Getenv("HELMDECK_S3_TEST_BUCKET"),
		AccessKeyID:     os.Getenv("HELMDECK_S3_TEST_KEY"),
		SecretAccessKey: os.Getenv("HELMDECK_S3_TEST_SECRET"),
		PresignTTL:      5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	a, err := store.Put(ctx, "testpack", "hello.txt", []byte("hello world"), "text/plain")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if a.URL == "" || a.Size != 11 || a.ContentType != "text/plain" {
		t.Errorf("artifact = %+v", a)
	}
	list, _ := store.ListForPack(ctx, "testpack")
	if len(list) != 1 || list[0].Key != a.Key {
		t.Errorf("list = %+v", list)
	}
	body, meta, err := store.Get(ctx, a.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "hello world" || meta.Size != 11 {
		t.Errorf("body=%q meta=%+v", body, meta)
	}
}
