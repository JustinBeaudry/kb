CREATE CONSTRAINT capture_policy_project IF NOT EXISTS FOR (n:CapturePolicy) REQUIRE n.projectID IS UNIQUE;
CREATE CONSTRAINT event_capture_key IF NOT EXISTS FOR (n:Event) REQUIRE n.captureKey IS UNIQUE;
CREATE CONSTRAINT task_checkpoint_owner IF NOT EXISTS FOR (n:TaskCheckpointOwner) REQUIRE (n.projectID,n.taskID) IS UNIQUE;
CREATE INDEX artifact_reconciliation IF NOT EXISTS FOR (n:Artifact) ON (n.state,n.checkedAt);
