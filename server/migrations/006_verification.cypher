CREATE CONSTRAINT verification_id IF NOT EXISTS FOR (v:Verification) REQUIRE v.id IS UNIQUE;
CREATE CONSTRAINT owner_decision_id IF NOT EXISTS FOR (d:OwnerDecision) REQUIRE d.id IS UNIQUE;
CREATE CONSTRAINT owner_decision_key IF NOT EXISTS FOR (d:OwnerDecision) REQUIRE (d.ownerID,d.key) IS UNIQUE;
CREATE CONSTRAINT excerpt_lineage_id IF NOT EXISTS FOR (l:ExcerptLineage) REQUIRE l.id IS UNIQUE;
CREATE INDEX publication_support IF NOT EXISTS FOR (p:Publication) ON (p.claimRevision);
