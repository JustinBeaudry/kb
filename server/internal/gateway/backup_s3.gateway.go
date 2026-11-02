package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3BackupStore is deliberately separate from S3 evidence storage: Delete is
// physical backup retention, while evidence generations retain permanent fences.
type S3BackupStore struct {
	client *s3.Client
	bucket string
}

func NewS3BackupStore(c S3Config) (*S3BackupStore, error) {
	g, e := NewS3(c)
	if e != nil {
		return nil, e
	}
	return &S3BackupStore{client: g.client, bucket: g.bucket}, nil
}
func validBackupStoreKey(k string) bool {
	return strings.HasPrefix(k, "backups/") && !strings.Contains(k, "..") && !strings.ContainsAny(k, "\r\n\x00\\")
}
func (g *S3BackupStore) PutImmutable(ctx context.Context, k string, r io.ReadSeeker, n int64, digest string) error {
	if !validBackupStoreKey(k) || r == nil || n < 0 || len(digest) != 64 {
		return domain.ErrInvalidInput
	}
	_, e := g.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(k), Body: r, ContentLength: aws.Int64(n), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"kb-cipher-sha256": digest}}, func(options *s3.Options) { options.RetryMaxAttempts = 1 })
	if e == nil {
		return nil
	}
	if !evidencePrecondition(e) {
		domain.MarkEffectUncertain(ctx)
		return domain.ErrUnavailable
	}
	body, e := g.Open(ctx, k)
	if e != nil {
		return e
	}
	h := sha256.New()
	actual, e := io.Copy(h, io.LimitReader(body, n+1))
	closeErr := body.Close()
	if e != nil || closeErr != nil {
		return domain.ErrUnavailable
	}
	if actual != n || hex.EncodeToString(h.Sum(nil)) != digest {
		return domain.ErrConflict
	}
	return nil
}
func (g *S3BackupStore) Open(ctx context.Context, k string) (io.ReadCloser, error) {
	if !validBackupStoreKey(k) {
		return nil, domain.ErrInvalidInput
	}
	out, e := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(k)})
	if e != nil {
		return nil, domain.ErrUnavailable
	}
	return &safeS3Reader{body: out.Body}, nil
}
func (g *S3BackupStore) List(ctx context.Context, prefix string) ([]string, error) {
	if !validBackupStoreKey(prefix) {
		return nil, domain.ErrInvalidInput
	}
	keys := []string{}
	pages := s3.NewListObjectsV2Paginator(g.client, &s3.ListObjectsV2Input{Bucket: aws.String(g.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1000)})
	for pages.HasMorePages() {
		out, e := pages.NextPage(ctx)
		if e != nil {
			return nil, domain.ErrUnavailable
		}
		if len(keys)+len(out.Contents) > 100000 {
			return nil, domain.ErrBudget
		}
		for _, item := range out.Contents {
			if item.Key == nil || !validBackupStoreKey(*item.Key) {
				return nil, domain.ErrUnavailable
			}
			keys = append(keys, *item.Key)
		}
	}
	return keys, nil
}
func (g *S3BackupStore) Delete(ctx context.Context, k string) error {
	if !validBackupStoreKey(k) {
		return domain.ErrInvalidInput
	}
	_, e := g.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(k)}, func(options *s3.Options) { options.RetryMaxAttempts = 1 })
	if e != nil {
		domain.MarkEffectUncertain(ctx)
		return domain.ErrUnavailable
	}
	return nil
}
