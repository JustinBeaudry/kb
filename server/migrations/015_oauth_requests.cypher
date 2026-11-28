// OAuth grant requests waiting for, or decided by, the owner.
CREATE CONSTRAINT oauth_request_id IF NOT EXISTS FOR (r:OAuthRequest) REQUIRE r.id IS UNIQUE;
CREATE INDEX oauth_request_approval IF NOT EXISTS FOR (r:OAuthRequest) ON (r.approvalCode);
CREATE INDEX oauth_request_expires IF NOT EXISTS FOR (r:OAuthRequest) ON (r.expiresAt);
