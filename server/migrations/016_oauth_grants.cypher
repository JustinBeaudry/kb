// OAuth grants approved by the owner, their refresh tokens by digest, and the
// digests of refresh tokens already used, kept to detect reuse.
CREATE CONSTRAINT oauth_grant_id IF NOT EXISTS FOR (g:OAuthGrant) REQUIRE g.id IS UNIQUE;
CREATE CONSTRAINT oauth_grant_client IF NOT EXISTS FOR (g:OAuthGrant) REQUIRE g.clientID IS UNIQUE;
CREATE CONSTRAINT oauth_grant_refresh IF NOT EXISTS FOR (g:OAuthGrant) REQUIRE g.refreshDigest IS UNIQUE;
CREATE CONSTRAINT oauth_used_refresh IF NOT EXISTS FOR (u:OAuthUsedRefresh) REQUIRE u.digest IS UNIQUE;
CREATE INDEX oauth_grant_expires IF NOT EXISTS FOR (g:OAuthGrant) ON (g.expiresAt);
CREATE INDEX oauth_request_code IF NOT EXISTS FOR (r:OAuthRequest) ON (r.codeDigest);
