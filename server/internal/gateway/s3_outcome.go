package gateway

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// A later retry/precondition result cannot settle an earlier lost response.
// Disable SDK retries on mutations, leaving explicit reconciliation to callers.
func singleMutationAttempt(o *s3.Options) { o.Retryer = aws.NopRetryer{} }

func putObject(ctx context.Context, client *s3.Client, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := client.PutObject(ctx, input, singleMutationAttempt)
	if err != nil && !conditionalConflict(err) {
		domain.MarkEffectUncertain(ctx)
	}
	return result, err
}
func deleteQualificationObject(ctx context.Context, client *s3.Client, input *s3.DeleteObjectInput) {
	if ctx.Err() != nil {
		return
	}
	if _, err := client.DeleteObject(ctx, input, singleMutationAttempt); err != nil {
		domain.MarkEffectUncertain(ctx)
	}
}
