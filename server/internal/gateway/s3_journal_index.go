package gateway

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// journalLink is authenticated immutable metadata only. It never retains full
// manifest payloads or decrypted JournalRecords. Membership is committed only
// after the exact predecessor path reaches genesis or the prior indexed head.
type journalLink struct {
	body                         journalBody
	barrier                      domain.JournalBarrier
	operationID, operationDigest string
}
type journalOperation struct {
	barrier domain.JournalBarrier
	digest  string
}

func (g *S3DeletionJournal) readLink(ctx context.Context, head journalHead) (journalLink, error) {
	raw, _, err := g.get(ctx, head.body.RecordKey)
	if err != nil {
		return journalLink{}, err
	}
	if journalDigest(raw) != head.body.RecordDigest {
		return journalLink{}, domain.ErrUnavailable
	}
	body, err := g.open(raw, "record")
	if err != nil {
		return journalLink{}, err
	}
	if body.Sequence != head.body.Sequence || body.PreviousManifestDigest != head.body.PreviousManifestDigest {
		return journalLink{}, domain.ErrUnavailable
	}
	encoded, err := json.Marshal(body.Record)
	if err != nil {
		return journalLink{}, domain.ErrUnavailable
	}
	return journalLink{body: head.body, barrier: head.barrier, operationID: body.Record.OperationID, operationDigest: journalDigest(encoded)}, nil
}
func (g *S3DeletionJournal) previousHead(ctx context.Context, head journalHead) (journalHead, error) {
	digest := head.body.PreviousManifestDigest
	if link, ok := g.links[digest]; ok {
		if link.barrier.Sequence+1 != head.barrier.Sequence {
			return journalHead{}, domain.ErrUnavailable
		}
		return journalHead{body: link.body, barrier: link.barrier}, nil
	}
	raw, _, err := g.get(ctx, g.manifestKey(digest))
	if err != nil {
		return journalHead{}, err
	}
	if journalDigest(raw) != digest {
		return journalHead{}, domain.ErrUnavailable
	}
	body, err := g.open(raw, "manifest")
	if err != nil {
		return journalHead{}, err
	}
	if body.Sequence+1 != head.barrier.Sequence {
		return journalHead{}, domain.ErrUnavailable
	}
	return journalHead{body: body, barrier: domain.JournalBarrier{Namespace: g.namespace, Sequence: body.Sequence, Digest: digest}}, nil
}

// pathLocked stages links without trusting partial/canceled traversal. The
// caller holds metadataMu throughout provider reads and commits only on success.
func (g *S3DeletionJournal) pathLocked(ctx context.Context, head journalHead, stop domain.JournalBarrier) ([]journalLink, journalHead, error) {
	if !validBarrier(stop, g.namespace) || stop.Sequence > head.barrier.Sequence {
		return nil, journalHead{}, domain.ErrUnavailable
	}
	links := []journalLink{}
	seen := map[string]string{}
	for walked := 0; walked <= journalMaxWalk; walked++ {
		if head.barrier.Sequence == stop.Sequence {
			if stop.Digest != "" && head.barrier.Digest != stop.Digest {
				return nil, journalHead{}, domain.ErrUnavailable
			}
			return links, head, nil
		}
		if head.barrier.Sequence == 0 {
			return nil, journalHead{}, domain.ErrUnavailable
		}
		link, ok := g.links[head.barrier.Digest]
		var err error
		if !ok {
			link, err = g.readLink(ctx, head)
			if err != nil {
				return nil, journalHead{}, err
			}
		}
		if old, ok := seen[link.operationID]; ok && old != link.operationDigest {
			return nil, journalHead{}, domain.ErrUnavailable
		}
		seen[link.operationID] = link.operationDigest
		links = append(links, link)
		head, err = g.previousHead(ctx, head)
		if err != nil {
			return nil, journalHead{}, err
		}
	}
	return nil, journalHead{}, domain.ErrBudget
}
func (g *S3DeletionJournal) commitLinksLocked(links []journalLink, anchor journalHead) error {
	if g.links == nil {
		g.links = map[string]journalLink{}
	}
	additions := 0
	for _, link := range links {
		if _, ok := g.links[link.barrier.Digest]; !ok {
			additions++
		}
	}
	if anchor.barrier.Sequence == 0 {
		if _, ok := g.links[anchor.barrier.Digest]; !ok {
			additions++
		}
	}
	// The unique digest bound includes any previously authenticated side branch.
	if len(g.links)+additions > journalMaxWalk+1 {
		return domain.ErrBudget
	}
	for _, link := range links {
		g.links[link.barrier.Digest] = link
	}
	if anchor.barrier.Sequence == 0 {
		g.links[anchor.barrier.Digest] = journalLink{body: anchor.body, barrier: anchor.barrier}
	}
	return nil
}
func (g *S3DeletionJournal) ensureIndexLocked(ctx context.Context, head journalHead) error {
	if g.indexed && g.indexedHead == head.barrier {
		return nil
	}
	stop := domain.JournalBarrier{Namespace: g.namespace}
	if g.indexed {
		if head.barrier.Sequence < g.indexedHead.Sequence {
			return domain.ErrStale
		}
		stop = g.indexedHead
	}
	links, anchor, err := g.pathLocked(ctx, head, stop)
	if err != nil {
		return err
	}
	pendingOperations := map[string]string{}
	for _, link := range links {
		if old, ok := g.operations[link.operationID]; ok && old.digest != link.operationDigest {
			return domain.ErrUnavailable
		}
		if old, ok := pendingOperations[link.operationID]; ok && old != link.operationDigest {
			return domain.ErrUnavailable
		}
		pendingOperations[link.operationID] = link.operationDigest
	}
	if err = g.commitLinksLocked(links, anchor); err != nil {
		return err
	}
	if !g.indexed {
		g.chain = []domain.JournalBarrier{anchor.barrier}
		g.operations = map[string]journalOperation{}
	}
	// Only this exact freshly authenticated head's verified predecessor prefix
	// contributes operation membership; cache unions and orphan branches do not.
	for i := len(links) - 1; i >= 0; i-- {
		link := links[i]
		g.chain = append(g.chain, link.barrier)
		if _, exists := g.operations[link.operationID]; !exists {
			g.operations[link.operationID] = journalOperation{barrier: link.barrier, digest: link.operationDigest}
		}
	}
	g.indexedHead = head.barrier
	g.indexed = true
	return nil
}
func (g *S3DeletionJournal) ensureIndex(ctx context.Context, head journalHead) error {
	g.metadataMu.Lock()
	defer g.metadataMu.Unlock()
	return g.ensureIndexLocked(ctx, head)
}
func (g *S3DeletionJournal) readIndexedRecord(ctx context.Context, link journalLink) (domain.JournalRecord, error) {
	raw, _, err := g.get(ctx, link.body.RecordKey)
	if err != nil {
		return domain.JournalRecord{}, err
	}
	if journalDigest(raw) != link.body.RecordDigest {
		return domain.JournalRecord{}, domain.ErrUnavailable
	}
	body, err := g.open(raw, "record")
	if err != nil {
		return domain.JournalRecord{}, err
	}
	if body.Sequence != link.barrier.Sequence || body.PreviousManifestDigest != link.body.PreviousManifestDigest {
		return domain.JournalRecord{}, domain.ErrUnavailable
	}
	encoded, err := json.Marshal(body.Record)
	if err != nil || body.Record.OperationID != link.operationID || journalDigest(encoded) != link.operationDigest {
		return domain.JournalRecord{}, domain.ErrUnavailable
	}
	return *body.Record, nil
}
func (g *S3DeletionJournal) pageLinks(ctx context.Context, head journalHead, after domain.JournalBarrier, limit int) ([]journalLink, domain.JournalBarrier, error) {
	g.metadataMu.Lock()
	defer g.metadataMu.Unlock()
	if err := g.ensureIndexLocked(ctx, head); err != nil {
		return nil, domain.JournalBarrier{}, err
	}
	if after.Sequence > head.barrier.Sequence || !validBarrier(after, g.namespace) {
		return nil, domain.JournalBarrier{}, domain.ErrUnavailable
	}
	anchor := g.chain[after.Sequence]
	if after.Digest != "" && after != anchor {
		return nil, domain.JournalBarrier{}, domain.ErrUnavailable
	}
	end := min(head.barrier.Sequence, after.Sequence+uint64(limit))
	links := make([]journalLink, 0, end-after.Sequence)
	for seq := after.Sequence + 1; seq <= end; seq++ {
		links = append(links, g.links[g.chain[seq].Digest])
	}
	return links, g.chain[end], nil
}
func (g *S3DeletionJournal) Append(ctx context.Context, record domain.JournalRecord) (domain.JournalBarrier, error) {
	if err := validateJournalRecord(record); err != nil {
		return domain.JournalBarrier{}, err
	}
	encoded, _ := json.Marshal(record)
	identity := journalDigest(encoded)
	for attempt := 0; attempt < journalMaxRetries; attempt++ {
		head, err := g.freshHead(ctx)
		if err != nil {
			return domain.JournalBarrier{}, err
		}
		g.metadataMu.Lock()
		err = g.ensureIndexLocked(ctx, head)
		operation, exists := g.operations[record.OperationID]
		g.metadataMu.Unlock()
		if errors.Is(err, domain.ErrStale) {
			continue
		}
		if err != nil {
			return domain.JournalBarrier{}, err
		}
		if exists {
			if operation.digest != identity {
				return domain.JournalBarrier{}, domain.ErrConflict
			}
			return operation.barrier, nil
		}
		if head.body.Sequence >= journalMaxWalk {
			return domain.JournalBarrier{}, domain.ErrBudget
		}
		next := head.body.Sequence + 1
		raw, err := g.seal(journalBody{Kind: "record", Sequence: next, PreviousManifestDigest: head.barrier.Digest, Record: &record})
		if err != nil {
			return domain.JournalBarrier{}, err
		}
		digest := journalDigest(raw)
		key := g.recordKey(digest)
		if err = g.immutable(ctx, key, raw); err != nil {
			return domain.JournalBarrier{}, err
		}
		manifest, err := g.seal(journalBody{Kind: "manifest", Sequence: next, PreviousManifestDigest: head.barrier.Digest, RecordDigest: digest, RecordKey: key})
		if err != nil {
			return domain.JournalBarrier{}, err
		}
		digest = journalDigest(manifest)
		if err = g.immutable(ctx, g.manifestKey(digest), manifest); err != nil {
			return domain.JournalBarrier{}, err
		}
		if err = g.put(ctx, g.headKey(), manifest, head.etag); errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err != nil {
			return domain.JournalBarrier{}, err
		}
		fresh, err := g.freshHead(ctx)
		if err != nil {
			return domain.JournalBarrier{}, domain.ErrUnknownOutcome
		}
		barrier := domain.JournalBarrier{Namespace: g.namespace, Sequence: next, Digest: digest}
		if err = g.ensureIndex(ctx, fresh); err != nil {
			return domain.JournalBarrier{}, domain.ErrUnknownOutcome
		}
		g.metadataMu.Lock()
		found := barrier.Sequence < uint64(len(g.chain)) && g.chain[barrier.Sequence] == barrier
		g.metadataMu.Unlock()
		if !found {
			return domain.JournalBarrier{}, domain.ErrUnknownOutcome
		}
		return barrier, nil
	}
	return domain.JournalBarrier{}, domain.ErrUnavailable
}
func (g *S3DeletionJournal) ReadSince(ctx context.Context, after domain.JournalBarrier) ([]domain.JournalRecord, error) {
	if !validBarrier(after, g.namespace) {
		return nil, domain.ErrUnavailable
	}
	head, err := g.freshHead(ctx)
	if err != nil {
		return nil, err
	}
	if after.Sequence > head.barrier.Sequence {
		return nil, domain.ErrUnavailable
	}
	if head.barrier.Sequence-after.Sequence > domain.MaxDeletionEntries {
		return nil, domain.ErrBudget
	}
	links, _, err := g.pageLinks(ctx, head, after, domain.MaxDeletionEntries)
	if err != nil {
		return nil, err
	}
	records := make([]domain.JournalRecord, 0, len(links))
	for _, link := range links {
		record, err := g.readIndexedRecord(ctx, link)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}
func (g *S3DeletionJournal) VerifyContinuity(ctx context.Context, after, target domain.JournalBarrier) error {
	if !validBarrier(after, g.namespace) || !validBarrier(target, g.namespace) {
		return domain.ErrUnavailable
	}
	head, err := g.freshHead(ctx)
	if err != nil {
		return err
	}
	if head.barrier != target {
		return domain.ErrStale
	}
	if after == target {
		return nil
	}
	g.metadataMu.Lock()
	defer g.metadataMu.Unlock()
	if g.indexed && target.Sequence < uint64(len(g.chain)) && g.chain[target.Sequence] == target && after.Sequence <= target.Sequence {
		if (after.Sequence == 0 && after.Digest == "") || g.chain[after.Sequence] == after {
			return nil
		}
		return domain.ErrUnavailable
	}
	links, anchor, err := g.pathLocked(ctx, head, after)
	if err != nil {
		return err
	}
	return g.commitLinksLocked(links, anchor)
}
func (g *S3DeletionJournal) ReadPage(ctx context.Context, after, target domain.JournalBarrier, limit int) ([]domain.JournalRecord, domain.JournalBarrier, error) {
	if limit < 1 || limit > 100 || !validBarrier(after, g.namespace) || !validBarrier(target, g.namespace) {
		return nil, domain.JournalBarrier{}, domain.ErrInvalidInput
	}
	head, err := g.freshHead(ctx)
	if err != nil {
		return nil, domain.JournalBarrier{}, err
	}
	if head.barrier != target {
		return nil, domain.JournalBarrier{}, domain.ErrStale
	}
	links, next, err := g.pageLinks(ctx, head, after, limit)
	if err != nil {
		return nil, domain.JournalBarrier{}, err
	}
	records := make([]domain.JournalRecord, 0, len(links))
	for _, link := range links {
		record, err := g.readIndexedRecord(ctx, link)
		if err != nil {
			return nil, domain.JournalBarrier{}, err
		}
		records = append(records, record)
	}
	current, err := g.freshHead(ctx)
	if err != nil {
		return nil, domain.JournalBarrier{}, err
	}
	if current.barrier != target {
		return nil, domain.JournalBarrier{}, domain.ErrStale
	}
	return records, next, nil
}
