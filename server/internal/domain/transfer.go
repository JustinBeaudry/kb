package domain

// ExportHead identifies one claim head in stable claim-ID order. Ordering by
// the immutable claim ID (not the revision) keeps export resumption stable
// when a claim receives a new revision between chunks.
type ExportHead struct{ ClaimID, Revision string }
