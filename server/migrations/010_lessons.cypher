// Qualified collector attestations bind one exact artifact upload. Agents have no write path.
CREATE CONSTRAINT execution_attestation_id IF NOT EXISTS FOR (n:ExecutionAttestation) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT execution_attestation_artifact IF NOT EXISTS FOR (n:ExecutionAttestation) REQUIRE n.artifactID IS UNIQUE;
// Cross-project discovery filters published lessons by sharing scope before eligibility.
CREATE INDEX publication_sharing IF NOT EXISTS FOR (p:Publication) ON (p.sharingScope,p.valid);
CREATE INDEX verification_revision IF NOT EXISTS FOR (v:Verification) ON (v.claimRevision);
