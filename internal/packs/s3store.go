package packs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config drives the S3-compatible ArtifactStore. It points at any
// service that speaks the S3 API — AWS S3, MinIO, Cloudflare R2,
// Backblaze B2 — because the only thing helmdeck depends on is the
// PutObject + presigned-GET surface.
//
// Endpoint should be the host[:port] without scheme; UseSSL controls
// whether the client speaks HTTPS. Region defaults to "us-east-1"
// (the value MinIO returns when no region is configured) so the
// signing path stays valid for non-AWS backends.
type S3Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Region          string
	UseSSL          bool

	// PresignTTL is how long generated GET URLs remain valid. Zero
	// means "use default" (15 minutes) — long enough for an agent to
	// fetch a screenshot, short enough that a leaked URL is not a
	// permanent disclosure.
	PresignTTL time.Duration

	// PublicEndpoint is the host clients should use in presigned
	// URLs when it differs from Endpoint (e.g. control plane talks
	// to MinIO over a docker-internal name, but agents fetch via a
	// public DNS name). Empty means "use Endpoint".
	PublicEndpoint string
}

// S3ArtifactStore implements ArtifactStore against an S3-compatible
// backend. It is safe for concurrent use; the underlying minio.Client
// already pools connections.
type S3ArtifactStore struct {
	cfg    S3Config
	client *minio.Client
	now    func() time.Time

	// Per-pack key index. Listing every object under a prefix on each
	// run would work but it costs a round trip; we keep an in-memory
	// index of keys we wrote ourselves so the common "list artifacts
	// produced by THIS pack run" path stays free. Cross-process state
	// (other replicas, prior runs) requires the bucket-list path,
	// which Get supports.
	indexMu sync.Mutex
	index   map[string][]Artifact
}

// NewS3ArtifactStore dials the configured backend and ensures the
// bucket exists. Returns an error if credentials are missing or the
// initial connectivity check fails — we want misconfiguration to
// surface at startup, not on the first pack run.
func NewS3ArtifactStore(ctx context.Context, cfg S3Config) (*S3ArtifactStore, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3store: endpoint and bucket required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("s3store: credentials required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3store: dial: %w", err)
	}
	// Verify the bucket exists. We deliberately do NOT auto-create —
	// bucket creation has security implications (default ACLs,
	// encryption settings) that are not the artifact store's call.
	exists, err := c.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3store: bucket exists check: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("s3store: bucket %q does not exist", cfg.Bucket)
	}
	return &S3ArtifactStore{
		cfg:    cfg,
		client: c,
		now:    func() time.Time { return time.Now().UTC() },
		index:  make(map[string][]Artifact),
	}, nil
}

// Put uploads content under <pack>/<rand>-<name> and returns the
// Artifact metadata with a freshly-generated presigned GET URL. The
// URL embeds Pack and CreatedAt so a stale entry can't be revived
// just by knowing the key.
func (s *S3ArtifactStore) Put(ctx context.Context, pack, name string, content []byte, contentType string) (Artifact, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return Artifact{}, err
	}
	key := pack + "/" + hex.EncodeToString(rnd[:]) + "-" + name
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, key, bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return Artifact{}, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	signed, err := s.presign(ctx, key)
	if err != nil {
		return Artifact{}, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	a := Artifact{
		Key:         key,
		URL:         signed,
		Size:        int64(len(content)),
		ContentType: contentType,
		CreatedAt:   s.now(),
		Pack:        pack,
	}
	s.indexMu.Lock()
	s.index[pack] = append(s.index[pack], a)
	s.indexMu.Unlock()
	return a, nil
}

// ListForPack returns artifacts produced by THIS process for pack.
// Cross-process listing would need a bucket scan; the engine only
// calls this immediately after a handler returns, so the in-memory
// index is the right cost/value tradeoff.
func (s *S3ArtifactStore) ListForPack(ctx context.Context, pack string) ([]Artifact, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	src := s.index[pack]
	out := make([]Artifact, len(src))
	copy(out, src)
	return out, nil
}

// Get streams an object back from the bucket. Used by the artifact
// download endpoint and by tests.
func (s *S3ArtifactStore) Get(ctx context.Context, key string) ([]byte, Artifact, error) {
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, Artifact{}, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		return nil, Artifact{}, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	body, err := io.ReadAll(obj)
	if err != nil {
		return nil, Artifact{}, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	return body, Artifact{
		Key:         key,
		Size:        stat.Size,
		ContentType: stat.ContentType,
		CreatedAt:   stat.LastModified,
	}, nil
}

// ListAll walks the entire bucket and returns metadata for every
// object. Used by the TTL janitor (T211b). The Pack field is parsed
// from the key prefix because S3 object metadata isn't carried by
// minio-go's recursive listing — that's fine because the engine's Put
// always namespaces by `<pack>/<rand>-<name>`.
func (s *S3ArtifactStore) ListAll(ctx context.Context) ([]Artifact, error) {
	var out []Artifact
	for obj := range s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return nil, &PackError{Code: CodeArtifactFailed, Message: obj.Err.Error(), Cause: obj.Err}
		}
		pack := ""
		if i := indexByte(obj.Key, '/'); i > 0 {
			pack = obj.Key[:i]
		}
		out = append(out, Artifact{
			Key:         obj.Key,
			Size:        obj.Size,
			ContentType: obj.ContentType,
			CreatedAt:   obj.LastModified,
			Pack:        pack,
		})
	}
	return out, nil
}

// Delete removes a single object. Idempotent — the janitor calls
// Delete on every expired key, and a 404 from the backend would just
// mean another janitor cycle (or operator) got there first.
func (s *S3ArtifactStore) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.cfg.Bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	// Drop the in-memory index entry too so a subsequent ListForPack
	// in this process doesn't return a stale handle.
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	for pack, list := range s.index {
		filtered := list[:0]
		for _, a := range list {
			if a.Key != key {
				filtered = append(filtered, a)
			}
		}
		s.index[pack] = filtered
	}
	return nil
}

// indexByte is strings.IndexByte without importing strings just for
// this. The key always uses '/' as the pack/name separator.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// presign builds a time-limited GET URL. If PublicEndpoint is set, we
// rewrite the host on the returned URL so agents reaching helmdeck
// from outside the docker network get a URL they can actually fetch.
func (s *S3ArtifactStore) presign(ctx context.Context, key string) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.cfg.Bucket, key, s.cfg.PresignTTL, url.Values{})
	if err != nil {
		return "", err
	}
	if s.cfg.PublicEndpoint != "" {
		u.Host = s.cfg.PublicEndpoint
	}
	return u.String(), nil
}
