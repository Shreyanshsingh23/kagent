package database

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func newFeedback(sessionID, taskID string, rating apiv1.FeedbackRating) *apiv1.Feedback {
	return &apiv1.Feedback{Id: uuid.NewString(), SessionId: sessionID, TaskId: taskID, Rating: rating}
}

func TestSessionFeedbackIsOneRatingPerReaderAndTarget(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	session, task := waitingTaskFixture(t, client)

	t.Run("Given a rated Session, when its owner rates it again, assert that the rating is replaced", func(t *testing.T) {
		first, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
		require.NoError(t, err)
		require.Equal(t, "alice", first.Creator)
		require.Equal(t, "revision", first.PreparedRevision)
		require.Equal(t, "team-a", first.Agent.GetNamespace())
		require.Equal(t, "assistant", first.Agent.GetName())

		again := newFeedback(session.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE)
		again.Comment = "the prompt is too long"
		again.IssueType = apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_INCOMPLETE
		replaced, err := client.UpsertSessionFeedback(ctx, again, "alice")
		require.NoError(t, err)
		require.Equal(t, first.Id, replaced.Id, "a repeated rating keeps the original entry")
		require.Equal(t, apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, replaced.Rating)
		require.Equal(t, "the prompt is too long", replaced.Comment)
		require.Equal(t, apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_INCOMPLETE, replaced.IssueType)
		require.True(t, first.CreatedAt.AsTime().Equal(replaced.CreatedAt.AsTime()))
		require.False(t, replaced.UpdatedAt.AsTime().Before(first.UpdatedAt.AsTime()))

		listed, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 10})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		require.Equal(t, replaced.Id, listed[0].Id)
	})

	t.Run("Given a rated Session, when its owner rates one reply, assert that the reply has its own rating", func(t *testing.T) {
		reply, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, string(task.ID), apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
		require.NoError(t, err)
		require.Equal(t, string(task.ID), reply.TaskId)

		listed, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 10})
		require.NoError(t, err)
		require.Len(t, listed, 2)
	})
}

func TestSessionFeedbackListFilters(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	first, _ := waitingTaskFixture(t, client)
	second, _ := waitingTaskFixture(t, client)
	positive, err := client.UpsertSessionFeedback(ctx, newFeedback(first.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
	require.NoError(t, err)
	negative, err := client.UpsertSessionFeedback(ctx, newFeedback(second.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE), "alice")
	require.NoError(t, err)

	scenarios := []struct {
		name  string
		query FeedbackQuery
		want  []string
	}{
		{name: "no filter", query: FeedbackQuery{}, want: []string{positive.Id, negative.Id}},
		{name: "one Session", query: FeedbackQuery{SessionID: second.Id}, want: []string{negative.Id}},
		{name: "one rating", query: FeedbackQuery{Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE}, want: []string{positive.Id}},
		{name: "the rated Agent", query: FeedbackQuery{Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}, want: []string{positive.Id, negative.Id}},
		{name: "another Agent", query: FeedbackQuery{Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "other"}}, want: nil},
	}
	for _, scenario := range scenarios {
		t.Run("Given two rated Sessions, when listing with "+scenario.name+", assert that only matching feedback is returned", func(t *testing.T) {
			query := scenario.query
			query.UserID, query.Limit = "alice", 10
			listed, err := client.ListSessionFeedback(ctx, query)
			require.NoError(t, err)
			ids := make([]string, 0, len(listed))
			for _, feedback := range listed {
				ids = append(ids, feedback.Id)
			}
			require.ElementsMatch(t, scenario.want, ids)
		})
	}

	t.Run("Given two rated Sessions, when paging one at a time, assert that pages follow ID order", func(t *testing.T) {
		page, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 1})
		require.NoError(t, err)
		require.Len(t, page, 1)
		next, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 1, AfterID: page[0].Id})
		require.NoError(t, err)
		require.Len(t, next, 1)
		require.Greater(t, next[0].Id, page[0].Id)
	})
}

func TestSessionFeedbackIsScopedToTheSessionOwner(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	session, _ := waitingTaskFixture(t, client)
	rated, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
	require.NoError(t, err)

	t.Run("Given alice's Session, when bob rates it, assert that it is not found", func(t *testing.T) {
		_, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE), "bob")
		require.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("Given alice's feedback, when bob lists feedback, assert that it is absent", func(t *testing.T) {
		listed, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "bob", Limit: 10})
		require.NoError(t, err)
		require.Empty(t, listed)
	})
	t.Run("Given alice's feedback, when bob deletes it, assert that it is not found and survives", func(t *testing.T) {
		require.ErrorIs(t, client.DeleteSessionFeedback(ctx, rated.Id, "bob"), ErrNotFound)
		listed, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 10})
		require.NoError(t, err)
		require.Len(t, listed, 1)
	})
	t.Run("Given alice's feedback, when alice deletes it twice, assert that the second delete is not found", func(t *testing.T) {
		require.NoError(t, client.DeleteSessionFeedback(ctx, rated.Id, "alice"))
		require.ErrorIs(t, client.DeleteSessionFeedback(ctx, rated.Id, "alice"), ErrNotFound)
	})
	t.Run("Given a missing Session, when it is rated, assert that it is not found", func(t *testing.T) {
		_, err := client.UpsertSessionFeedback(ctx, newFeedback(uuid.NewString(), "", apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSessionFeedbackRequiresAReplyFromTheSession(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	session, _ := waitingTaskFixture(t, client)
	_, otherTask := waitingTaskFixture(t, client)

	for _, taskID := range []string{"missing-task", string(otherTask.ID)} {
		t.Run("Given a Session, when a reply outside it is rated, assert that it is not found", func(t *testing.T) {
			_, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, taskID, apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestDeletingASessionDeletesItsFeedback(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	session, task := waitingTaskFixture(t, client)
	for _, taskID := range []string{"", string(task.ID)} {
		_, err := client.UpsertSessionFeedback(ctx, newFeedback(session.Id, taskID, apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
		require.NoError(t, err)
	}

	require.NoError(t, deleteSession(ctx, client, session.Id))

	listed, err := client.ListSessionFeedback(ctx, FeedbackQuery{UserID: "alice", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, listed, "the tombstone keeps no feedback")
	_, err = client.UpsertSessionFeedback(ctx, newFeedback(session.Id, "", apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE), "alice")
	require.ErrorIs(t, err, ErrNotFound, "a deleted Session cannot be rated")
}

func TestSessionFeedbackConstraints(t *testing.T) {
	db := setupTestDB(t)
	client := NewClient(db, "public")
	ctx := t.Context()
	sessionFixture(t, client, ctx, "team-a", "revision", "assistant", "kagent")
	session, _ := waitingTaskFixture(t, client)

	scenarios := []struct {
		name, rating, issueType, comment string
	}{
		{name: "an issue type on a positive rating", rating: "FEEDBACK_RATING_POSITIVE", issueType: "FEEDBACK_ISSUE_TYPE_TOOL"},
		{name: "an unspecified rating", rating: "FEEDBACK_RATING_UNSPECIFIED", issueType: "FEEDBACK_ISSUE_TYPE_UNSPECIFIED"},
		{name: "an oversized comment", rating: "FEEDBACK_RATING_NEGATIVE", issueType: "FEEDBACK_ISSUE_TYPE_UNSPECIFIED", comment: strings.Repeat("x", 2001)},
	}
	for _, scenario := range scenarios {
		t.Run("Given a Session, when storing "+scenario.name+", assert that PostgreSQL rejects it", func(t *testing.T) {
			_, err := db.Exec(ctx, `
				INSERT INTO session_feedback (id, session_id, history_id, user_id, rating, issue_type, comment,
				    agent_namespace, agent_name, prepared_revision, created_at, updated_at)
				SELECT $1, id, history_id, 'alice', $3, $4, $5, 'team-a', 'assistant', 'revision', now(), now()
				FROM session WHERE id = $2
			`, uuid.NewString(), session.Id, scenario.rating, scenario.issueType, scenario.comment)
			require.Error(t, err)
		})
	}
}
