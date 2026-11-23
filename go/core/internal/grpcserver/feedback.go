package grpcserver

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/feedback"
)

type feedbackServer struct {
	apiv1.UnimplementedFeedbackServiceServer
	service *feedback.Service
}

func (s *feedbackServer) CreateFeedback(ctx context.Context, request *apiv1.CreateFeedbackRequest) (*apiv1.CreateFeedbackResponse, error) {
	result, err := s.service.Create(ctx, feedback.CreateRequest{
		SessionID: request.GetSessionId(), TaskID: request.GetTaskId(),
		Rating: request.GetRating(), Comment: request.GetComment(), IssueType: request.GetIssueType(),
	})
	return &apiv1.CreateFeedbackResponse{Feedback: result}, err
}

func (s *feedbackServer) ListFeedback(ctx context.Context, request *apiv1.ListFeedbackRequest) (*apiv1.ListFeedbackResponse, error) {
	page := request.GetPage()
	result, err := s.service.List(ctx, feedback.ListRequest{
		SessionID: request.GetSessionId(), Agent: request.GetAgent(), Rating: request.GetRating(),
		PageSize: int(page.GetLimit()), PageToken: page.GetPageToken(),
	})
	return &apiv1.ListFeedbackResponse{
		Feedback: result.Feedback,
		Page:     &apiv1.PageResponse{NextPageToken: result.NextPageToken},
	}, err
}

func (s *feedbackServer) DeleteFeedback(ctx context.Context, request *apiv1.DeleteFeedbackRequest) (*apiv1.DeleteFeedbackResponse, error) {
	err := s.service.Delete(ctx, request.GetFeedbackId())
	return &apiv1.DeleteFeedbackResponse{}, err
}
