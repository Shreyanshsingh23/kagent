// Package feedback records readers' ratings of their own Sessions.
package feedback

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
	maxCommentRunes = 2000
)

type store interface {
	UpsertSessionFeedback(context.Context, *apiv1.Feedback, string) (*apiv1.Feedback, error)
	ListSessionFeedback(context.Context, database.FeedbackQuery) ([]*apiv1.Feedback, error)
	DeleteSessionFeedback(context.Context, string, string) error
}

type Service struct {
	store      store
	authorizer auth.Authorizer
}

// CreateRequest identifies the rated Session, or one of its replies when TaskID
// is set, and the rating given.
type CreateRequest struct {
	SessionID string
	TaskID    string
	Rating    apiv1.FeedbackRating
	Comment   string
	IssueType apiv1.FeedbackIssueType
}

// ListRequest narrows the caller's feedback. Empty filters match everything.
type ListRequest struct {
	SessionID string
	Agent     *apiv1.ResourceReference
	Rating    apiv1.FeedbackRating
	PageSize  int
	PageToken string
}

type ListResult struct {
	Feedback      []*apiv1.Feedback
	NextPageToken string
}

func NewService(store store, authorizer auth.Authorizer) *Service {
	return &Service{store: store, authorizer: authorizer}
}

// Create rates a Session the caller owns, replacing the caller's earlier rating
// of the same Session or reply. Creating feedback is authorized on its own and
// also requires read access to the Session; the store then limits it to the
// Session's owner.
func (s *Service) Create(ctx context.Context, request CreateRequest) (*apiv1.Feedback, error) {
	sessionID, err := validateCreate(request)
	if err != nil {
		return nil, err
	}
	userID, err := s.authorize(ctx, auth.VerbCreate, auth.Resource{Type: "Feedback"})
	if err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, auth.VerbGet, auth.Resource{Type: "Session", Name: sessionID}); err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to generate feedback identifier", err)
	}
	feedback, err := s.store.UpsertSessionFeedback(ctx, &apiv1.Feedback{
		Id: id.String(), SessionId: sessionID, TaskId: request.TaskID,
		Rating: request.Rating, Comment: request.Comment, IssueType: request.IssueType,
	}, userID)
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Session or reply not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to save feedback", err)
	}
	return feedback, nil
}

// List returns the caller's feedback in ascending ID order.
func (s *Service) List(ctx context.Context, request ListRequest) (ListResult, error) {
	sessionID := request.SessionID
	if sessionID != "" {
		var err error
		if sessionID, err = validateIdentity(sessionID); err != nil {
			return ListResult{}, err
		}
	}
	if !validRating(request.Rating, true) {
		return ListResult{}, serviceerrors.NewInvalidArgument("rating is invalid", nil)
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if pageSize < 0 || pageSize > maxPageSize {
		return ListResult{}, serviceerrors.NewInvalidArgument(fmt.Sprintf("page limit must be between 1 and %d", maxPageSize), nil)
	}
	afterID, err := decodePageToken(request.PageToken)
	if err != nil {
		return ListResult{}, serviceerrors.NewInvalidArgument("page token is invalid", err)
	}
	userID, err := s.authorize(ctx, auth.VerbList, auth.Resource{Type: "Feedback"})
	if err != nil {
		return ListResult{}, err
	}
	rows, err := s.store.ListSessionFeedback(ctx, database.FeedbackQuery{
		UserID: userID, SessionID: sessionID, Agent: request.Agent, Rating: request.Rating,
		AfterID: afterID, Limit: pageSize + 1,
	})
	if err != nil {
		return ListResult{}, serviceerrors.NewInternal("Failed to list feedback", err)
	}
	result := ListResult{Feedback: rows}
	if len(rows) > pageSize {
		result.Feedback = rows[:pageSize]
		result.NextPageToken = encodePageToken(rows[pageSize-1].GetId())
	}
	return result, nil
}

// Delete removes feedback the caller gave.
func (s *Service) Delete(ctx context.Context, feedbackID string) error {
	feedbackID, err := validateIdentity(feedbackID)
	if err != nil {
		return err
	}
	userID, err := s.authorize(ctx, auth.VerbDelete, auth.Resource{Type: "Feedback", Name: feedbackID})
	if err != nil {
		return err
	}
	err = s.store.DeleteSessionFeedback(ctx, feedbackID, userID)
	if errors.Is(err, database.ErrNotFound) {
		return serviceerrors.NewNotFound("Feedback not found", err)
	}
	if err != nil {
		return serviceerrors.NewInternal("Failed to delete feedback", err)
	}
	return nil
}

// authorize returns the caller's identity after checking the policy. Feedback is
// a person's opinion of their own conversation: share links only grant reading
// a Session, and an agent's runtime credentials must not rate the Session it
// serves.
func (s *Service) authorize(ctx context.Context, verb auth.Verb, resource auth.Resource) (string, error) {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return "", serviceerrors.NewUnauthenticated("Failed to get authenticated principal", nil)
	}
	if _, shared := auth.ShareContextFrom(ctx); shared {
		return "", serviceerrors.NewPermissionDenied("Session shares do not grant feedback access", nil)
	}
	principal := session.Principal()
	if principal.RuntimeBinding != nil {
		return "", serviceerrors.NewPermissionDenied("Runtime credentials cannot give feedback", nil)
	}
	if err := s.authorizer.Check(ctx, principal, verb, resource); err != nil {
		return "", serviceerrors.NewPermissionDenied("Not authorized", err)
	}
	return principal.ID, nil
}

// validateCreate enforces the request contract for callers that bypass the gRPC
// validation interceptor, and returns the canonical Session ID.
func validateCreate(request CreateRequest) (string, error) {
	sessionID, err := validateIdentity(request.SessionID)
	if err != nil {
		return "", err
	}
	if !validRating(request.Rating, false) {
		return "", serviceerrors.NewInvalidArgument("rating must be positive or negative", nil)
	}
	if _, ok := apiv1.FeedbackIssueType_name[int32(request.IssueType)]; !ok {
		return "", serviceerrors.NewInvalidArgument("issue_type is invalid", nil)
	}
	if request.IssueType != apiv1.FeedbackIssueType_FEEDBACK_ISSUE_TYPE_UNSPECIFIED &&
		request.Rating != apiv1.FeedbackRating_FEEDBACK_RATING_NEGATIVE {
		return "", serviceerrors.NewInvalidArgument("issue_type may only be set on a negative rating", nil)
	}
	if len([]rune(request.Comment)) > maxCommentRunes {
		return "", serviceerrors.NewInvalidArgument(fmt.Sprintf("comment must be at most %d characters", maxCommentRunes), nil)
	}
	return sessionID, nil
}

// validRating reports whether rating is a defined value, allowing unspecified
// only where it means "any rating".
func validRating(rating apiv1.FeedbackRating, allowUnspecified bool) bool {
	if rating == apiv1.FeedbackRating_FEEDBACK_RATING_UNSPECIFIED {
		return allowUnspecified
	}
	_, ok := apiv1.FeedbackRating_name[int32(rating)]
	return ok
}

func validateIdentity(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", serviceerrors.NewInvalidArgument("identifier is invalid", err)
	}
	return parsed.String(), nil
}

func encodePageToken(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodePageToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	id, err := uuid.Parse(string(raw))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
