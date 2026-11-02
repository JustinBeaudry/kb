package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func evidencePrecondition(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict")
}
func (g *S3) matchExistingEvidence(ctx context.Context, key string, expected []byte) error {
	out, err := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return domain.ErrUnavailable
	}
	if out.Metadata["kb-deleted"] == "1" {
		_ = out.Body.Close()
		return domain.ErrDeleted
	}
	data, readErr := io.ReadAll(io.LimitReader(out.Body, domain.MaxArtifactBytes+1))
	closeErr := out.Body.Close()
	if readErr != nil || closeErr != nil {
		return domain.ErrUnavailable
	}
	if !bytes.Equal(data, expected) {
		return domain.ErrConflict
	}
	return nil
}
func (g *S3) writeEvidenceTombstone(ctx context.Context, key string) error {
	_, err := putObject(ctx, g.client, &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader(nil), ContentLength: aws.Int64(0), Metadata: map[string]string{"kb-deleted": "1"}})
	if err != nil {
		return domain.ErrUnavailable
	}
	out, err := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return domain.ErrUnavailable
	}
	body, readErr := io.ReadAll(io.LimitReader(out.Body, 1))
	closeErr := out.Body.Close()
	if readErr != nil || closeErr != nil || len(body) != 0 || out.Metadata["kb-deleted"] != "1" {
		return domain.ErrUnavailable
	}
	return nil
}

// qualifyEvidenceFence proves create preconditions serialize with a durable
// same-key tombstone using synthetic random probe keys only. Actual evidence
// keys are never physically removed; probe cleanup is deliberately separate.
func (g *S3) qualifyEvidenceFence(ctx context.Context) error {
	g.qualificationMu.Lock()
	defer g.qualificationMu.Unlock()
	if g.qualified {
		return nil
	}
	key := "qualification/evidence-fence/" + rand.Text()
	defer func() {
		deleteQualificationObject(ctx, g.client, &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	}()
	outcomes := make(chan error, 2)
	var group sync.WaitGroup
	for _, data := range []string{"first", "second"} {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := putObject(ctx, g.client, &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader([]byte(data)), IfNoneMatch: aws.String("*")})
			outcomes <- err
		}()
	}
	group.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if evidencePrecondition(err) {
			conflict++
		} else {
			return domain.ErrUnavailable
		}
	}
	if success != 1 || conflict != 1 {
		return domain.ErrUnavailable
	}
	if err := g.writeEvidenceTombstone(ctx, key); err != nil {
		return err
	}
	_, err := putObject(ctx, g.client, &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("late")), IfNoneMatch: aws.String("*")})
	if !evidencePrecondition(err) {
		return domain.ErrUnavailable
	}
	if err = g.matchExistingEvidence(ctx, key, nil); !errors.Is(err, domain.ErrDeleted) {
		return domain.ErrUnavailable
	}
	g.qualified = true
	return nil
}
