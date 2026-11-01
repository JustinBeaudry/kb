package service

import (
	"bytes"
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
)

func TestHostExcerptUsesF4PolicyHashGenerationAndHealthChecks(t *testing.T) {
	s, r, o, _, m := captureFixture(t)
	_, owners := newIdentityFixture()
	owners.owner = OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey}
	host := NewHostCaptureService(s.tx, r, s.jobs, o, owners)
	m.Scope = domain.Scope{ProjectID: "project"}
	m.SourceLocator = "excerpt:opaque"
	a, err := host.StageHost(t.Context(), m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	_, err = host.CompleteHost(t.Context(), a.ID, a.Digest)
	require.NoError(t, err)
	ref := domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: "source"}
	ref, err = host.ReferenceHost(t.Context(), ref, a.Scope)
	require.NoError(t, err)
	assert.Equal(t, domain.TrustSubmitted, ref.Origin.Trust)
	assert.Contains(t, ref.Origin.ClientID, ownerActorPrefix)
	_, err = s.Complete(t.Context(), domain.Principal{Role: domain.RoleOwner, OwnerID: "owner"}, a.ID, a.Digest)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	r.policy.Revision = "changed"
	_, err = host.ReferenceHost(t.Context(), ref, a.Scope)
	assert.ErrorIs(t, err, domain.ErrStale)
}
func TestHostExcerptRequiresPersistedOwner(t *testing.T) {
	s, r, o, _, m := captureFixture(t)
	_, owners := newIdentityFixture()
	host := NewHostCaptureService(s.tx, r, s.jobs, o, owners)
	_, err := host.StageHost(t.Context(), m, bytes.NewBufferString("exit_code=0"))
	assert.ErrorIs(t, err, domain.ErrForbidden)
	assert.Empty(t, r.artifacts)
}

func TestUnknownObjectReadPreservesAvailability(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	_, owners := newIdentityFixture()
	owners.owner = OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey}
	host := NewHostCaptureService(s.tx, r, s.jobs, unavailableHostObjects{ObjectStore: o}, owners)
	_, _, err := host.ReadHost(t.Context(), a.ID)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.True(t, r.artifacts[a.ID].ObjectAvailable)
	delete(o.data, a.ObjectKey)
	host = NewHostCaptureService(s.tx, r, s.jobs, o, owners)
	_, _, err = host.ReadHost(t.Context(), a.ID)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	assert.False(t, r.artifacts[a.ID].ObjectAvailable)
}

type unavailableHostObjects struct{ ObjectStore }

func (unavailableHostObjects) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, domain.ErrUnavailable
}

func TestHostReadRejectsChangedSourceGeneration(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	_, owners := newIdentityFixture()
	owners.owner = OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey}
	objects := mutatingHostObjects{ObjectStore: o, change: func() { current := r.artifacts[a.ID]; current.Generation = "replacement"; r.artifacts[a.ID] = current }}
	host := NewHostCaptureService(s.tx, r, s.jobs, objects, owners)
	_, _, err := host.ReadHost(t.Context(), a.ID)
	assert.ErrorIs(t, err, domain.ErrStale)
}

type mutatingHostObjects struct {
	ObjectStore
	change func()
}

func (o mutatingHostObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := o.ObjectStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	return changedOnClose{ReadCloser: r, change: o.change}, nil
}

type changedOnClose struct {
	io.ReadCloser
	change func()
}

func (r changedOnClose) Close() error { err := r.ReadCloser.Close(); r.change(); return err }

func TestHostHealthSettlementRechecksPersistedOwner(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	_, owners := newIdentityFixture()
	owners.owner = OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey}
	host := NewHostCaptureService(s.tx, r, s.jobs, revokeOnOpenHostObjects{ObjectStore: o, revoke: func() { owners.owner = OwnerIdentity{} }}, owners)
	_, _, err := host.ReadHost(t.Context(), a.ID)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	assert.True(t, r.artifacts[a.ID].ObjectAvailable)
}

type revokeOnOpenHostObjects struct {
	ObjectStore
	revoke func()
}

func (o revokeOnOpenHostObjects) Open(context.Context, string) (io.ReadCloser, error) {
	o.revoke()
	return nil, domain.ErrIncomplete
}
