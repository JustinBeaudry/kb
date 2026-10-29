package domain

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRetrievalWireIncludesCompleteMetadata(t *testing.T) {
	now := time.Now().UTC()
	outcome := VerificationSupported
	r := RetrievalResult{SchemaVersion: "3", Continuation: "cursor", TaskStateOmitted: true, Chunks: []EvidenceChunk{{RecordID: "c", Revision: "r", Text: "<猫>\"\\", ClaimStatus: ClaimVerified, OriginTrust: TrustGatewayObserved, VerificationOutcome: &outcome, Evidence: []EvidenceRef{{ArtifactID: "a", Digest: "sha256", Locator: "bytes=0-1", SourceRevision: "s", Origin: Origin{ClientID: "client", Trust: TrustGatewayObserved, AttestationID: "att"}, Applicability: Applicability{RepositoryRevision: "rev", DirtyDigest: "dirty", Environment: "test", Versions: map[string]string{"go": "1.24"}, ValidFrom: &now, ValidUntil: &now}}}}}}
	wire, e := RetrievalWire(r)
	require.NoError(t, e)
	assert.True(t, json.Valid(wire))
	for _, value := range []string{"verification_outcome", "gateway_observed", "attestation_id", "repository_revision", "dirty_digest", "environment", "versions", "valid_from", "valid_until", "continuation", "task_state_omitted", "\\u003c"} {
		assert.Contains(t, string(wire), value)
	}
}
