// Owner public keys that have been replaced. A retired key can never be the
// owner key again.
CREATE CONSTRAINT owner_key_retired IF NOT EXISTS FOR (r:OwnerKeyRetired) REQUIRE r.key IS UNIQUE;
