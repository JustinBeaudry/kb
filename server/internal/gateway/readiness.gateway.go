package gateway

import (
	"context"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (g *S3) Probe(ctx context.Context) error {
	if _, err := g.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(g.bucket)}); err != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (g *S3DeletionJournal) CheckHealth(ctx context.Context) (uint64, uint64, error) {
	head, err := g.CurrentHead(ctx)
	if err != nil {
		return 0, journalMaxWalk, err
	}
	if err = g.VerifyContinuity(ctx, domain.JournalBarrier{Namespace: head.Namespace}, head); err != nil {
		return head.Sequence, journalMaxWalk, err
	}
	return head.Sequence, journalMaxWalk, nil
}
