CREATE INDEX publication_revision IF NOT EXISTS FOR (n:Publication) ON (n.claimRevision);
CREATE INDEX memory_claim_identity IF NOT EXISTS FOR (n:ClaimRevision) ON (n.claimID);
CREATE INDEX memory_validity IF NOT EXISTS FOR (n:ClaimRevision) ON (n.valid,n.status);
CREATE CONSTRAINT memory_entity_identity IF NOT EXISTS FOR (n:Entity) REQUIRE (n.projectID,n.id) IS UNIQUE;
