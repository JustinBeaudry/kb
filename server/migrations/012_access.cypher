// Access audit: one rollup per client, project, capability and UTC day. Counts
// only. No request content, bearer or source text is ever stored here.
CREATE CONSTRAINT access_rollup_key IF NOT EXISTS FOR (a:AccessRollup) REQUIRE a.key IS UNIQUE;
CREATE INDEX access_rollup_day IF NOT EXISTS FOR (a:AccessRollup) ON (a.day);
// One marker per flushed batch, so a retried batch is never counted twice.
CREATE CONSTRAINT access_batch_id IF NOT EXISTS FOR (b:AccessBatch) REQUIRE b.id IS UNIQUE;
CREATE INDEX access_batch_at IF NOT EXISTS FOR (b:AccessBatch) ON (b.at);
