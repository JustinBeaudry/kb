// Owner judgments keep their signing key and recorded time (Unix nanoseconds)
// as properties, so kb-admin judgments filters and pages in the graph. This
// backfills judgments written before kb kept them, reading both from the JSON
// data encoding/json wrote. It is idempotent and reruns on every schema
// initialization.
CREATE INDEX owner_judgment_recorded IF NOT EXISTS FOR (j:OwnerJudgment) ON (j.recordedAt, j.id);
CREATE INDEX owner_judgment_signer IF NOT EXISTS FOR (j:OwnerJudgment) ON (j.signerID);
MATCH (j:OwnerJudgment) WHERE j.recordedAt IS NULL AND j.data CONTAINS '"RecordedAt":"' AND j.data CONTAINS '"SignerID":"' WITH j, split(split(j.data,'"RecordedAt":"')[1],'"')[0] AS raw, toLower(split(split(j.data,'"SignerID":"')[1],'"')[0]) AS signer WHERE raw =~ '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})' WITH j, signer, datetime(raw) AS at SET j.signerID = signer, j.recordedAt = at.epochSeconds * 1000000000 + at.nanosecond;
