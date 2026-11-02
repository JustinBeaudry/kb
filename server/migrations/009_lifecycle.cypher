CREATE CONSTRAINT deletion_operation_id IF NOT EXISTS FOR (n:DeletionOperation) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT source_tombstone_identity IF NOT EXISTS FOR (n:SourceTombstone) REQUIRE n.identityDigest IS UNIQUE;
CREATE CONSTRAINT session_tombstone_identity IF NOT EXISTS FOR (n:SessionTombstone) REQUIRE n.identityDigest IS UNIQUE;
CREATE CONSTRAINT task_tombstone_identity IF NOT EXISTS FOR (n:TaskTombstone) REQUIRE n.identityDigest IS UNIQUE;
CREATE CONSTRAINT policy_revision_identity IF NOT EXISTS FOR (n:PolicyRevision) REQUIRE n.identityDigest IS UNIQUE;
CREATE CONSTRAINT policy_eligibility_identity IF NOT EXISTS FOR (n:PolicyEligibility) REQUIRE n.identityDigest IS UNIQUE;
CREATE CONSTRAINT prepared_derivation_id IF NOT EXISTS FOR (n:PreparedDerivation) REQUIRE n.id IS UNIQUE;
CREATE INDEX artifact_source_digest IF NOT EXISTS FOR (n:Artifact) ON (n.digest);
MATCH (o:OwnerState {id:'owner'}) SET o.lifecycleBaseline=coalesce(o.lifecycleBaseline,datetime().epochMillis*1000000);
MATCH (a:Artifact),(o:OwnerState {id:'owner'}) SET a.ingestedAt=coalesce(a.ingestedAt,o.lifecycleBaseline);
MATCH (t:TaskCheckpointOwner),(o:OwnerState {id:'owner'}) SET t.lastActivityAt=coalesce(t.lastActivityAt,o.lifecycleBaseline);

CREATE CONSTRAINT task_activity_identity IF NOT EXISTS FOR (n:TaskActivity) REQUIRE n.identityDigest IS UNIQUE;
MATCH (t:TaskCheckpointOwner),(o:OwnerState {id:'owner'}) MERGE (a:TaskActivity {projectID:t.projectID,taskID:t.taskID}) ON CREATE SET a.lastActivityAt=o.lifecycleBaseline;

CREATE INDEX source_tombstone_digest IF NOT EXISTS FOR (n:SourceTombstone) ON (n.digest);
CREATE INDEX policy_eligibility_artifact IF NOT EXISTS FOR (n:PolicyEligibility) ON (n.artifactID);
