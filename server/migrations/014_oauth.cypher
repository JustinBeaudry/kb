// OAuth dynamic client registrations (RFC 7591). Only redirect URIs and the
// client's self-reported name are kept. An unapproved registration expires.
CREATE CONSTRAINT oauth_registration_id IF NOT EXISTS FOR (r:OAuthRegistration) REQUIRE r.id IS UNIQUE;
CREATE INDEX oauth_registration_expires IF NOT EXISTS FOR (r:OAuthRegistration) ON (r.expiresAt);
