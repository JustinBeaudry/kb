// OAuth dynamic client registrations (RFC 7591). Only redirect URIs and the
// client's self-reported name are kept. An unapproved registration expires.
CREATE CONSTRAINT oauth_registration_id IF NOT EXISTS FOR (r:OAuthRegistration) REQUIRE r.id IS UNIQUE;
CREATE INDEX oauth_registration_expires IF NOT EXISTS FOR (r:OAuthRegistration) ON (r.expiresAt);
// One lock node per OAuth limit. Writing it serializes the count and create
// that enforce the limit across concurrent transactions and api instances.
CREATE CONSTRAINT oauth_limit_id IF NOT EXISTS FOR (l:OAuthLimit) REQUIRE l.id IS UNIQUE;
