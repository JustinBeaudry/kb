package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3Config struct{ Endpoint, Region, Bucket, AccessKey, SecretKey string }

func (S3Config) String() string   { return "S3Config([redacted])" }
func (S3Config) GoString() string { return "S3Config([redacted])" }

type S3 struct {
	client *s3.Client
	bucket string
}

func NewS3(c S3Config) (*S3, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || domain.ValidateIDs(c.Region, c.Bucket, c.AccessKey, c.SecretKey) != nil {
		return nil, domain.ErrInvalidInput
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return nil, domain.ErrInvalidInput
	}
	client := s3.New(s3.Options{Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: &http.Client{Timeout: 30 * time.Second}, RetryMaxAttempts: 2})
	return &S3{client: client, bucket: c.Bucket}, nil
}
func (g *S3) Put(ctx context.Context, key string, body io.Reader, digest string) error {
	sum, err := hex.DecodeString(digest)
	if err != nil || len(sum) != 32 || key == "" || body == nil {
		return domain.ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(body, domain.MaxArtifactBytes+1))
	if err != nil {
		return domain.ErrUnavailable
	}
	if int64(len(data)) > domain.MaxArtifactBytes {
		return domain.ErrBudget
	}
	_, err = g.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum))})
	if err != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (g *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			return nil, domain.ErrIncomplete
		}
		return nil, domain.ErrUnavailable
	}
	return &safeS3Reader{body: out.Body}, nil
}
func (g *S3) Delete(ctx context.Context, key string) error {
	_, err := g.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return domain.ErrUnavailable
	}
	return nil
}

// Body failures can also include request URLs in vendor errors. Normalize the
// entire reader lifecycle, not just GetObject's initial response.
type safeS3Reader struct{ body io.ReadCloser }

func (r *safeS3Reader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, domain.ErrUnavailable
	}
	return n, err
}
func (r *safeS3Reader) Close() error {
	if r.body.Close() != nil {
		return domain.ErrUnavailable
	}
	return nil
}
