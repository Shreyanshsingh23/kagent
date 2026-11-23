package feedback

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const sessionID = "11111111-1111-4111-8111-111111111111"

type testSession struct{ principal auth.Principal }

func (s testSession) Principal() auth.Principal { return s.principal }

// recordingAuthorizer records each check and denies the one it is told to.
type recordingAuthorizer struct {
	checks []string
	deny   string
}

func (a *recordingAuthorizer) Check(_ context.Context, _ auth.Principal, verb auth.Verb, resource auth.Resource) error {
	check := string(verb) + " " + resource.Type
	a.checks = append(a.checks, check)
	if check == a.deny {
		return errors.New("denied")
	}
	return nil
}

type testStore struct {
	upserted *apiv1.Feedback
	owner    string
	query    database.FeedbackQuery
	rows     []*apiv1.Feedback
	deleted  string
	err      error
}

func (s *testStore) UpsertSessionFeedback(_ context.Context, feedback *apiv1.Feedback, userID string) (*apiv1.Feedback, error) {
	s.upserted, s.owner = feedback, userID
	if s.err != nil {
		return nil, s.err
	}
	stored := *feedback
	stored.Creator = userID
	return &stored, nil
}

func (s *testStore) ListSessionFeedback(_ context.Context, query database.FeedbackQuery) ([]*apiv1.Feedback, error) {
	s.query = query
	rows := []*apiv1.Feedback{}
	for _, row := range s.rows {
		if row.Id > query.AfterID && len(rows) < query.Limit {
			rows = append(rows, row)
		}
	}
	return rows, s.err
}

func (s *testStore) DeleteSessionFeedback(_ context.Context, id, userID string) error {
	s.deleted, s.owner = id, userID
	return s.err
}

func aliceContext(t *testing.T) context.Context {
	return auth.AuthSessionTo(t.Context(), testSession{principal: auth.Principal{ID: "alice"}})
}

func TestCreateRatesTheCallersSession(t *testing.T) {
	store, authorizer := &testStore{}, &recordingAuthorizer{}
	service := NewService(store, authorizer)

	feedback, err := service.Create(aliceContext(t), CreateRequest{
		SessionID: strings.ToUpper(sessionID), TaskID: "task-1",
		Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, Comment: "wrong tool",
		IssueType: apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_TOOL,
	})

	require.NoError(t, err)
	require.Equal(t, "alice", store.owner)
	require.Equal(t, sessionID, store.upserted.SessionId, "the Session ID is canonical")
	require.Equal(t, "task-1", store.upserted.TaskId)
	require.NotEmpty(t, store.upserted.Id)
	require.Equal(t, "alice", feedback.Creator)
	require.Equal(t, []string{"create Feedback", "get Session"}, authorizer.checks)
}

func TestCreateRejectsInvalidRequestsBeforeStorage(t *testing.T) {
	scenarios := []struct {
		name    string
		request CreateRequest
	}{
		{name: "a malformed Session ID", request: CreateRequest{SessionID: "not-a-uuid", Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE}},
		{name: "no rating", request: CreateRequest{SessionID: sessionID}},
		{name: "an undefined rating", request: CreateRequest{SessionID: sessionID, Rating: 9}},
		{name: "an undefined issue type", request: CreateRequest{SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, IssueType: 9}},
		{name: "an issue type on a positive rating", request: CreateRequest{SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE, IssueType: apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_FACTUAL}},
		{name: "an oversized comment", request: CreateRequest{SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, Comment: strings.Repeat("é", maxCommentRunes+1)}},
	}
	for _, scenario := range scenarios {
		t.Run("Given a caller, when creating feedback with "+scenario.name+", assert that it is rejected before storage", func(t *testing.T) {
			store := &testStore{}
			_, err := NewService(store, &recordingAuthorizer{}).Create(aliceContext(t), scenario.request)
			require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%v", err)
			require.Nil(t, store.upserted)
		})
	}

	t.Run("Given a caller, when the comment is at the limit in multibyte characters, assert that it is accepted", func(t *testing.T) {
		_, err := NewService(&testStore{}, &recordingAuthorizer{}).Create(aliceContext(t), CreateRequest{
			SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, Comment: strings.Repeat("é", maxCommentRunes),
		})
		require.NoError(t, err)
	})
}

func TestFeedbackAccessIsLimitedToTheSessionReader(t *testing.T) {
	create := func(ctx context.Context, s *Service) error {
		_, err := s.Create(ctx, CreateRequest{SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE})
		return err
	}
	list := func(ctx context.Context, s *Service) error { _, err := s.List(ctx, ListRequest{}); return err }
	remove := func(ctx context.Context, s *Service) error { return s.Delete(ctx, sessionID) }
	share := func(t *testing.T) context.Context {
		return auth.ShareContextTo(aliceContext(t), &auth.ShareContext{SessionID: sessionID, UserID: "alice"})
	}
	actor := func(t *testing.T) context.Context {
		return auth.AuthSessionTo(t.Context(), testSession{principal: auth.Principal{
			ID: "actor", RuntimeBinding: &auth.RuntimeBinding{SessionID: sessionID, CreatorID: "alice"},
		}})
	}

	scenarios := []struct {
		name string
		ctx  func(*testing.T) context.Context
		deny string
		call func(context.Context, *Service) error
		want serviceerrors.Code
	}{
		{name: "an unauthenticated caller creates feedback", ctx: func(t *testing.T) context.Context { return t.Context() }, call: create, want: serviceerrors.CodeUnauthenticated},
		{name: "a share link creates feedback", ctx: share, call: create, want: serviceerrors.CodePermissionDenied},
		{name: "a share link lists feedback", ctx: share, call: list, want: serviceerrors.CodePermissionDenied},
		{name: "a share link deletes feedback", ctx: share, call: remove, want: serviceerrors.CodePermissionDenied},
		{name: "an agent runtime rates its own Session", ctx: actor, call: create, want: serviceerrors.CodePermissionDenied},
		{name: "a caller without feedback access creates feedback", ctx: aliceContext, deny: "create Feedback", call: create, want: serviceerrors.CodePermissionDenied},
		{name: "a caller without Session access creates feedback", ctx: aliceContext, deny: "get Session", call: create, want: serviceerrors.CodePermissionDenied},
		{name: "a caller without list access lists feedback", ctx: aliceContext, deny: "list Feedback", call: list, want: serviceerrors.CodePermissionDenied},
		{name: "a caller without delete access deletes feedback", ctx: aliceContext, deny: "delete Feedback", call: remove, want: serviceerrors.CodePermissionDenied},
	}
	for _, scenario := range scenarios {
		t.Run("Given a stored Session, when "+scenario.name+", assert that it is refused before storage", func(t *testing.T) {
			store := &testStore{}
			err := scenario.call(scenario.ctx(t), NewService(store, &recordingAuthorizer{deny: scenario.deny}))
			require.True(t, serviceerrors.IsCode(err, scenario.want), "%v", err)
			require.Nil(t, store.upserted)
			require.Empty(t, store.deleted)
			require.Empty(t, store.query.UserID)
		})
	}
}

func TestStoreMissesAreNotFound(t *testing.T) {
	store := &testStore{err: database.ErrNotFound}
	service := NewService(store, &recordingAuthorizer{})

	_, err := service.Create(aliceContext(t), CreateRequest{SessionID: sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound), "%v", err)
	err = service.Delete(aliceContext(t), sessionID)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound), "%v", err)
	require.Equal(t, "alice", store.owner)
}

func TestListPagesTheCallersFeedback(t *testing.T) {
	store := &testStore{rows: []*apiv1.Feedback{
		{Id: "00000000-0000-7000-8000-000000000001"},
		{Id: "00000000-0000-7000-8000-000000000002"},
		{Id: "00000000-0000-7000-8000-000000000003"},
	}}
	service := NewService(store, &recordingAuthorizer{})
	agent := &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}

	first, err := service.List(aliceContext(t), ListRequest{
		SessionID: sessionID, Agent: agent, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, PageSize: 2,
	})
	require.NoError(t, err)
	require.Len(t, first.Feedback, 2)
	require.NotEmpty(t, first.NextPageToken)
	require.Equal(t, database.FeedbackQuery{
		UserID: "alice", SessionID: sessionID, Agent: agent, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE, Limit: 3,
	}, store.query)

	second, err := service.List(aliceContext(t), ListRequest{PageSize: 2, PageToken: first.NextPageToken})
	require.NoError(t, err)
	require.Len(t, second.Feedback, 1)
	require.Equal(t, "00000000-0000-7000-8000-000000000003", second.Feedback[0].Id)
	require.Empty(t, second.NextPageToken)

	for _, request := range []ListRequest{
		{PageSize: maxPageSize + 1},
		{PageToken: "not a token"},
		{SessionID: "not-a-uuid"},
		{Rating: 9},
	} {
		_, err := service.List(aliceContext(t), request)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%+v: %v", request, err)
	}
}
