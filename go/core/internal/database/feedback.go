package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// FeedbackQuery selects one user's feedback. Empty filters match everything.
type FeedbackQuery struct {
	UserID    string
	SessionID string
	Agent     *apiv1.ResourceReference
	Rating    apiv1.FeedbackRating
	AfterID   string
	Limit     int
}

// UpsertSessionFeedback stores userID's rating of a live Session they own, or of
// one of its replies when feedback.TaskId is set, together with the Session's
// Agent and prepared revision at the time of rating. Rating the same target again
// replaces the rating, comment and issue type and records the current revision,
// keeping the original ID and creation time. A missing, deleted or unowned
// Session, or a task outside its conversation, returns ErrNotFound. The Session
// is share-locked so concurrent deletion either removes this feedback or
// prevents it.
func (c *Client) UpsertSessionFeedback(ctx context.Context, feedback *apiv1.Feedback, userID string) (*apiv1.Feedback, error) {
	id, err := uuid.Parse(feedback.GetId())
	if err != nil {
		return nil, fmt.Errorf("invalid feedback ID: %w", err)
	}
	sessionID, err := uuid.Parse(feedback.GetSessionId())
	if err != nil {
		return nil, fmt.Errorf("invalid feedback Session ID: %w", err)
	}
	var taskID *string
	if feedback.GetTaskId() != "" {
		taskID = new(feedback.GetTaskId())
	}
	var result *apiv1.Feedback
	err = c.withTx(ctx, func(tx pgx.Tx) error {
		row, err := queryOne(ctx, tx, `
			SELECT id, user_id, prepared_revision, state, data, operation, context_id,
			    source_checkpoint_id, history_id, operation_id, executor_id
			FROM session_record
			WHERE id = $1 AND user_id = $2 AND state <> 'RUNTIME_STATE_DELETED'
			FOR SHARE
		`, pgx.RowToStructByName[sessionRow], sessionID, userID)
		if err != nil {
			return notFoundOr(err)
		}
		session, err := toSession(row)
		if err != nil {
			return err
		}
		agent := session.GetAgent().GetRef()
		stored, err := queryOne(ctx, tx, `
			INSERT INTO session_feedback (id, session_id, history_id, task_id, user_id, rating, issue_type,
			    comment, agent_namespace, agent_name, prepared_revision, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, statement_timestamp(), statement_timestamp())
			ON CONFLICT (session_id, user_id, COALESCE(task_id, '')) DO UPDATE
			SET rating = EXCLUDED.rating, issue_type = EXCLUDED.issue_type, comment = EXCLUDED.comment,
			    prepared_revision = EXCLUDED.prepared_revision, updated_at = EXCLUDED.updated_at
			RETURNING id, session_id, task_id, user_id, rating, issue_type, comment,
			    agent_namespace, agent_name, prepared_revision, created_at, updated_at
		`,
			pgx.RowToStructByName[sessionFeedbackRow], id, row.ID, row.HistoryID, taskID, userID,
			feedback.GetRating().String(), feedback.GetIssueType().String(), feedback.GetComment(),
			agent.GetNamespace(), agent.GetName(), session.GetPreparedRevision(),
		)
		if isFeedbackTaskViolation(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		result, err = toFeedback(stored)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("rate Session %s: %w", sessionID, err)
	}
	return result, nil
}

// ListSessionFeedback returns up to query.Limit of query.UserID's feedback in
// ascending ID order after query.AfterID, optionally narrowed to one Session, one
// Agent, or one rating. Feedback of deleted Sessions has already been removed.
func (c *Client) ListSessionFeedback(ctx context.Context, query FeedbackQuery) ([]*apiv1.Feedback, error) {
	rating := ""
	if query.Rating != apiv1.FeedbackRating_FEEDBACK_RATING_UNSPECIFIED {
		rating = query.Rating.String()
	}
	rows, err := queryMany(ctx, c.db, `
		SELECT id, session_id, task_id, user_id, rating, issue_type, comment,
		    agent_namespace, agent_name, prepared_revision, created_at, updated_at
		FROM session_feedback
		WHERE user_id = $1
		  AND (NULLIF($2::text, '') IS NULL OR session_id = NULLIF($2::text, '')::uuid)
		  AND ($3::text = '' OR (agent_namespace = $3 AND agent_name = $4))
		  AND ($5::text = '' OR rating = $5)
		  AND (NULLIF($6::text, '') IS NULL OR id > NULLIF($6::text, '')::uuid)
		ORDER BY id
		LIMIT $7
	`,
		pgx.RowToStructByName[sessionFeedbackRow], query.UserID, query.SessionID,
		query.Agent.GetNamespace(), query.Agent.GetName(), rating, query.AfterID, int32(query.Limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list Session feedback: %w", err)
	}
	result := make([]*apiv1.Feedback, 0, len(rows))
	for _, row := range rows {
		feedback, err := toFeedback(row)
		if err != nil {
			return nil, err
		}
		result = append(result, feedback)
	}
	return result, nil
}

// DeleteSessionFeedback removes feedback given by userID. Missing feedback and
// feedback given by another user return ErrNotFound.
func (c *Client) DeleteSessionFeedback(ctx context.Context, id, userID string) error {
	tag, err := c.db.Exec(ctx, `
		DELETE FROM session_feedback WHERE id = $1 AND user_id = $2
	`, id, userID)
	if err != nil {
		return fmt.Errorf("delete Session feedback %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete Session feedback %s: %w", id, ErrNotFound)
	}
	return nil
}

type sessionFeedbackRow struct {
	ID               uuid.UUID
	SessionID        uuid.UUID
	TaskID           *string
	UserID           string
	Rating           string
	IssueType        string
	Comment          string
	AgentNamespace   string
	AgentName        string
	PreparedRevision string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// toFeedback decodes a stored rating and rejects enum values this binary does
// not know rather than reporting them as unspecified.
func toFeedback(row sessionFeedbackRow) (*apiv1.Feedback, error) {
	rating, ok := apiv1.FeedbackRating_value[row.Rating]
	if !ok || rating == int32(apiv1.FeedbackRating_FEEDBACK_RATING_UNSPECIFIED) {
		return nil, fmt.Errorf("feedback %s has unknown rating %q", row.ID, row.Rating)
	}
	issueType, ok := apiv1.FeedbackIssueType_value[row.IssueType]
	if !ok {
		return nil, fmt.Errorf("feedback %s has unknown issue type %q", row.ID, row.IssueType)
	}
	return &apiv1.Feedback{
		Id:               row.ID.String(),
		SessionId:        row.SessionID.String(),
		TaskId:           derefStr(row.TaskID),
		Creator:          row.UserID,
		Rating:           apiv1.FeedbackRating(rating),
		Comment:          row.Comment,
		IssueType:        apiv1.FeedbackIssueType(issueType),
		Agent:            &apiv1.ResourceReference{Namespace: row.AgentNamespace, Name: row.AgentName},
		PreparedRevision: row.PreparedRevision,
		CreatedAt:        timestamppb.New(row.CreatedAt),
		UpdatedAt:        timestamppb.New(row.UpdatedAt),
	}, nil
}

// isFeedbackTaskViolation reports whether a PostgreSQL error identifies a rated
// task that is not part of the Session's conversation.
func isFeedbackTaskViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "session_feedback_task_fkey"
}
