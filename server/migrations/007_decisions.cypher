CREATE INDEX decision_state IF NOT EXISTS FOR (d:Decision) ON (d.state);
CREATE CONSTRAINT decision_reply_event IF NOT EXISTS FOR (r:DecisionReply) REQUIRE r.eventID IS UNIQUE;
CREATE CONSTRAINT owner_judgment_id IF NOT EXISTS FOR (j:OwnerJudgment) REQUIRE j.id IS UNIQUE;
CREATE CONSTRAINT owner_challenge_id IF NOT EXISTS FOR (c:OwnerChallenge) REQUIRE c.id IS UNIQUE;
CREATE CONSTRAINT action_grant_id IF NOT EXISTS FOR (g:ActionGrant) REQUIRE g.id IS UNIQUE;
CREATE CONSTRAINT action_grant_token IF NOT EXISTS FOR (g:ActionGrant) REQUIRE g.tokenDigest IS UNIQUE;
CREATE CONSTRAINT decision_cursor_id IF NOT EXISTS FOR (c:DecisionCursor) REQUIRE c.id IS UNIQUE;
