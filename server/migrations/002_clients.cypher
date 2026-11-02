// Digest lookup is unique and indexed. Owner public identity lives on OwnerState.
CREATE CONSTRAINT client_token_digest IF NOT EXISTS FOR (n:Client) REQUIRE n.tokenDigest IS UNIQUE;
