package e2e_test

import (
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// Feedback does not depend on the harness, so one harness covers the API.
func TestSessionFeedback(t *testing.T) {
	t.Parallel()
	target := interactionTarget(t)
	fixture := newInteractionFixture(t, testHarness{name: "kagent", runtimeLabel: "kagent"}, target, startInteractionMock(t))
	_, _, task := fixture.send(t, "What is 2+2?")
	feedback := apiv1.NewFeedbackServiceClient(newControllerConn(t, target))

	session, err := feedback.CreateFeedback(fixture.ctx, &apiv1.CreateFeedbackRequest{
		SessionId: fixture.sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE,
	})
	require.NoError(t, err)
	require.Equal(t, "e2e", session.Feedback.Creator)
	require.NotEmpty(t, session.Feedback.PreparedRevision)

	reply, err := feedback.CreateFeedback(fixture.ctx, &apiv1.CreateFeedbackRequest{
		SessionId: fixture.sessionID, TaskId: string(task.ID), Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE,
		IssueType: apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_FACTUAL, Comment: "2+2 is 4",
	})
	require.NoError(t, err)
	again, err := feedback.CreateFeedback(fixture.ctx, &apiv1.CreateFeedbackRequest{
		SessionId: fixture.sessionID, TaskId: string(task.ID), Rating: apiv1.FeedbackRating_FEEDBACK_RATING_POSITIVE,
	})
	require.NoError(t, err)
	require.Equal(t, reply.Feedback.Id, again.Feedback.Id, "rating a reply again replaces its rating")

	listed, err := feedback.ListFeedback(fixture.ctx, &apiv1.ListFeedbackRequest{SessionId: fixture.sessionID})
	require.NoError(t, err)
	require.Len(t, listed.Feedback, 2)

	// A different user cannot see or rate the conversation.
	other := metadata.NewOutgoingContext(fixture.ctx, metadata.Pairs("x-user-id", "e2e-other", a2atype.SvcParamVersion, string(a2atype.Version)))
	_, err = feedback.CreateFeedback(other, &apiv1.CreateFeedbackRequest{
		SessionId: fixture.sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE,
	})
	require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	_, err = feedback.DeleteFeedback(other, &apiv1.DeleteFeedbackRequest{FeedbackId: session.Feedback.Id})
	require.Equal(t, codes.NotFound, status.Code(err), "%v", err)

	// A read-only share link may open the conversation but not rate it.
	share, err := fixture.sessions.CreateSessionShare(fixture.ctx, &apiv1.CreateSessionShareRequest{SessionId: fixture.sessionID})
	require.NoError(t, err)
	visitor := metadata.NewOutgoingContext(fixture.ctx, metadata.Pairs("x-user-id", "share-reader", "x-share-token", share.Token, a2atype.SvcParamVersion, string(a2atype.Version)))
	_, err = feedback.CreateFeedback(visitor, &apiv1.CreateFeedbackRequest{
		SessionId: fixture.sessionID, Rating: apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

	_, err = feedback.DeleteFeedback(fixture.ctx, &apiv1.DeleteFeedbackRequest{FeedbackId: session.Feedback.Id})
	require.NoError(t, err)
	listed, err = feedback.ListFeedback(fixture.ctx, &apiv1.ListFeedbackRequest{SessionId: fixture.sessionID})
	require.NoError(t, err)
	require.Len(t, listed.Feedback, 1)
	require.Equal(t, reply.Feedback.Id, listed.Feedback[0].Id)
}
