// Opaque application IDs survive dump/load. Neo4j elementId is never an API ID.
CREATE CONSTRAINT owner_state_id IF NOT EXISTS FOR (n:OwnerState) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT client_id IF NOT EXISTS FOR (n:Client) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT event_id IF NOT EXISTS FOR (n:Event) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT event_idempotency IF NOT EXISTS FOR (n:Event) REQUIRE (n.clientID, n.idempotencyKey) IS UNIQUE;
CREATE CONSTRAINT artifact_id IF NOT EXISTS FOR (n:Artifact) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT claim_id IF NOT EXISTS FOR (n:Claim) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT claim_revision_id IF NOT EXISTS FOR (n:ClaimRevision) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT checkpoint_id IF NOT EXISTS FOR (n:Checkpoint) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT job_id IF NOT EXISTS FOR (n:Job) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT decision_id IF NOT EXISTS FOR (n:Decision) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT verification_id IF NOT EXISTS FOR (n:Verification) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT publication_id IF NOT EXISTS FOR (n:Publication) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT tombstone_id IF NOT EXISTS FOR (n:Tombstone) REQUIRE n.id IS UNIQUE;
CREATE INDEX job_due IF NOT EXISTS FOR (n:Job) ON (n.status,n.dueAt);
CREATE INDEX claim_scope IF NOT EXISTS FOR (n:ClaimRevision) ON (n.projectID,n.taskID);
CREATE FULLTEXT INDEX claim_text IF NOT EXISTS FOR (n:ClaimRevision) ON EACH [n.text];
MERGE (n:OwnerState {id:'owner'}) ON CREATE SET n.authEpoch=0,n.deletionEpoch=0,n.revisionEpoch=0;
