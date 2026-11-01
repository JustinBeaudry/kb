package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// qualify fails closed when an S3 implementation ignores either precondition.
// Each adapter process qualifies its independently configured namespace before
// reading or writing the authoritative head. This local protocol probe does not
// establish provider durability, administrative rollback protection or key
// custody; O2 must qualify the actual production provider and credentials.
func (g *S3DeletionJournal) qualify(ctx context.Context) error {
	g.qualificationMu.Lock()
	defer g.qualificationMu.Unlock()
	if g.qualified {
		return nil
	}
	var id [24]byte
	if _, err := rand.Read(id[:]); err != nil {
		return domain.ErrUnavailable
	}
	key := g.root() + "qualification/" + hex.EncodeToString(id[:])
	// Probe bodies are authenticated encrypted metadata, never key/prose material.
	first, err := g.seal(journalBody{Kind: "manifest"})
	if err != nil {
		return err
	}
	second, err := g.seal(journalBody{Kind: "manifest"})
	if err != nil {
		return err
	}
	// Cleanup is best effort for an isolated random probe, never a deletion receipt.
	defer func() {
		_, _ = g.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	}()
	if err = g.put(ctx, key, first, ""); err != nil {
		return domain.ErrUnavailable
	}
	data, etag, err := g.get(ctx, key)
	if err != nil || !bytes.Equal(data, first) {
		return domain.ErrUnavailable
	}
	if err = g.put(ctx, key, second, ""); !errors.Is(err, domain.ErrConflict) {
		return domain.ErrUnavailable
	}
	if err = g.put(ctx, key, second, `"unmatched-qualification-etag"`); !errors.Is(err, domain.ErrConflict) {
		return domain.ErrUnavailable
	}
	// Exactly one concurrent writer may replace the observed ETag.
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); outcomes <- g.put(ctx, key, second, etag) }()
	}
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for outcome := range outcomes {
		switch {
		case outcome == nil:
			success++
		case errors.Is(outcome, domain.ErrConflict):
			conflict++
		default:
			return domain.ErrUnavailable
		}
	}
	if success != 1 || conflict != 1 {
		return domain.ErrUnavailable
	}
	data, _, err = g.get(ctx, key)
	if err != nil || !bytes.Equal(data, second) {
		return domain.ErrUnavailable
	}
	g.qualified = true
	return nil
}
