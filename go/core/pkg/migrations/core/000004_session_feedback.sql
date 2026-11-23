-- +goose Up

-- A reader's rating of a Session, or of one reply in it. Feedback is part of the
-- Session: explicit deletion removes it with the Session's other references, and
-- removing the Session row (idle expiry) cascades. The prepared revision is a
-- record of what was rated, not a reference, so it never blocks revision cleanup.
CREATE TABLE session_feedback (
    id                UUID        PRIMARY KEY,
    session_id        UUID        NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    history_id        UUID        NOT NULL,
    -- NULL rates the whole Session.
    task_id           TEXT        CHECK (task_id <> ''),
    user_id           TEXT        NOT NULL CHECK (user_id <> ''),
    rating            TEXT        NOT NULL CHECK (rating IN ('FEEDBACK_RATING_POSITIVE', 'FEEDBACK_RATING_NEGATIVE')),
    issue_type        TEXT        NOT NULL CHECK (issue_type IN ('FEEDBACK_ISSUE_TYPE_UNSPECIFIED',
        'FEEDBACK_ISSUE_TYPE_INSTRUCTIONS', 'FEEDBACK_ISSUE_TYPE_FACTUAL', 'FEEDBACK_ISSUE_TYPE_INCOMPLETE',
        'FEEDBACK_ISSUE_TYPE_TOOL', 'FEEDBACK_ISSUE_TYPE_OTHER')),
    comment           TEXT        NOT NULL CHECK (char_length(comment) <= 2000),
    agent_namespace   TEXT        NOT NULL,
    agent_name        TEXT        NOT NULL,
    prepared_revision TEXT        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    CHECK (issue_type = 'FEEDBACK_ISSUE_TYPE_UNSPECIFIED' OR rating = 'FEEDBACK_RATING_NEGATIVE'),
    -- A rated reply must belong to the Session's conversation, and its rating
    -- goes with it.
    CONSTRAINT session_feedback_task_fkey
        FOREIGN KEY (history_id, task_id) REFERENCES session_task(history_id, id) ON DELETE CASCADE
);
-- One rating per reader and target, so rating again replaces it.
CREATE UNIQUE INDEX session_feedback_target_idx
    ON session_feedback (session_id, user_id, COALESCE(task_id, ''));
CREATE INDEX session_feedback_owner_idx
    ON session_feedback (user_id, id);

-- +goose Down

DROP TABLE session_feedback;
