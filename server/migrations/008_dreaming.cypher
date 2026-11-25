CREATE CONSTRAINT model_reservation_id IF NOT EXISTS FOR (m:ModelReservation) REQUIRE m.id IS UNIQUE;
CREATE INDEX model_reservation_day IF NOT EXISTS FOR (m:ModelReservation) ON (m.day, m.state);
CREATE CONSTRAINT finding_id IF NOT EXISTS FOR (f:Finding) REQUIRE f.id IS UNIQUE;
CREATE CONSTRAINT finding_digest IF NOT EXISTS FOR (f:Finding) REQUIRE f.digest IS UNIQUE;
CREATE CONSTRAINT dream_notice_id IF NOT EXISTS FOR (n:DreamNotice) REQUIRE n.id IS UNIQUE;
CREATE CONSTRAINT dream_cursor_project IF NOT EXISTS FOR (d:DreamCursor) REQUIRE d.projectID IS UNIQUE;
CREATE INDEX dream_claim_ingested IF NOT EXISTS FOR (c:ClaimRevision) ON (c.ingestedAt);
