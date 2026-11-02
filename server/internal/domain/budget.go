package domain

import "encoding/json"

const (
	OrientationBudget    = 4096
	EvidenceBudget       = 16384
	MaxGraphDepth        = 2
	MaxGraphVisited      = 50
	MaxSupportReferences = 1000
)

// RetrievalWire measures the public JSON shape, including JSON escaping. HTTP
// adds one newline. Other transports must independently budget their framing.
func RetrievalWire(r RetrievalResult) ([]byte, error) {
	return json.Marshal(RetrievalWireValue(r))
}

// RetrievalWireValue is the public JSON value measured by RetrievalWire. Other
// renderers (CLI, MCP) encode this same value inside their own framing.
func RetrievalWireValue(r RetrievalResult) map[string]any {
	chunks := make([]any, 0, len(r.Chunks))
	for _, c := range r.Chunks {
		refs := make([]any, 0, len(c.Evidence))
		for _, e := range c.Evidence {
			refs = append(refs, map[string]any{"artifact_id": e.ArtifactID, "digest": e.Digest, "locator": e.Locator, "source_revision": e.SourceRevision, "origin": originWire(e.Origin), "applicability": applicabilityWire(e.Applicability)})
		}
		warnings := c.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		m := map[string]any{"record_id": c.RecordID, "revision": c.Revision, "text": c.Text, "claim_status": c.ClaimStatus, "origin_trust": c.OriginTrust, "evidence": refs, "warnings": warnings}
		if c.Applicability != nil {
			m["applicability"] = applicabilityWire(*c.Applicability)
		}
		if c.VerificationOutcome != nil {
			m["verification_outcome"] = *c.VerificationOutcome
		}
		if c.Passage != nil {
			m["passage"] = map[string]any{"start_byte": c.Passage.StartByte, "end_byte": c.Passage.EndByte, "source_bytes": c.Passage.SourceBytes}
		}
		chunks = append(chunks, m)
	}
	m := map[string]any{"schema_version": r.SchemaVersion, "chunks": chunks, "truncated": r.Truncated, "visited": r.Visited, "no_results": r.NoResults}
	if r.TruncationReason != "" {
		m["truncation_reason"] = r.TruncationReason
	}
	if r.Action != "" {
		m["action"] = r.Action
	}
	if r.Continuation != "" {
		m["continuation"] = r.Continuation
	}
	if r.TaskStateOmitted {
		m["task_state_omitted"] = true
	}
	if r.TaskCheckpoint != nil {
		c := r.TaskCheckpoint.Checkpoint
		m["task_checkpoint"] = map[string]any{"label": "task_state", "checkpoint": map[string]any{"id": c.ID, "revision": c.Revision, "scope": scopeWire(c.Scope), "objective": c.Objective, "completed": stringArray(c.Completed), "pending": stringArray(c.Pending), "questions": stringArray(c.Questions), "evidence_revision_ids": stringArray(c.EvidenceRevisionIDs), "updated_at": c.UpdatedAt}}
		if c.EvidenceReferencesOmitted {
			m["task_checkpoint"].(map[string]any)["checkpoint"].(map[string]any)["evidence_references_omitted"] = true
		}
	}
	return m
}
func stringArray(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func scopeWire(s Scope) map[string]any {
	m := map[string]any{"project_id": s.ProjectID}
	if s.TaskID != "" {
		m["task_id"] = s.TaskID
	}
	if s.SessionID != "" {
		m["session_id"] = s.SessionID
	}
	return m
}
func originWire(o Origin) map[string]any {
	m := map[string]any{"client_id": o.ClientID, "trust": o.Trust}
	if o.AttestationID != "" {
		m["attestation_id"] = o.AttestationID
	}
	return m
}
func applicabilityWire(a Applicability) map[string]any {
	m := map[string]any{}
	if a.RepositoryRevision != "" {
		m["repository_revision"] = a.RepositoryRevision
	}
	if a.DirtyDigest != "" {
		m["dirty_digest"] = a.DirtyDigest
	}
	if a.Environment != "" {
		m["environment"] = a.Environment
	}
	if a.Versions != nil {
		m["versions"] = a.Versions
	}
	if a.ValidFrom != nil {
		m["valid_from"] = a.ValidFrom
	}
	if a.ValidUntil != nil {
		m["valid_until"] = a.ValidUntil
	}
	return m
}
