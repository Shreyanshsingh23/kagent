# Session feedback

`kagent.api.v1.FeedbackService` lets a Session's owner rate the Session, or one
reply in it, so runs that went wrong can be found later and compared across an
Agent's revisions.

## Model

Feedback is stored in the `session_feedback` table, one row per rater and target.
A target is the whole Session, or one of its tasks when `task_id` is set. Rating
the same target again replaces the rating, comment, and issue type in place,
keeping the original ID and creation time.

Each row records:

- the rating, positive or negative;
- an optional comment of at most 2000 characters;
- an issue type, allowed only on a negative rating;
- the Agent the Session runs and the Session's prepared revision when it was
  last rated.

The Agent and revision are captured as plain values, not references, so feedback
never keeps a revision from being cleaned up and filtering by Agent still works
after a revision is gone.

PostgreSQL enforces these rules: a rated task must belong to the Session's
conversation, enum values are stored by their full protobuf names, and the issue
type requires a negative rating.

## Access

Feedback belongs to the Session's owner. Creating feedback requires `create` on
`Feedback` and `get` on the Session, and the store only accepts a Session the
caller owns. Listing and deleting return only the caller's own feedback. A
read-only share link and an agent's runtime credentials cannot give, list, or
delete feedback.

## Lifecycle

Feedback follows its Session. Explicit deletion removes it along with the
Session's shares, and removing the Session row on idle expiry cascades to it.
Removing a rated task removes that task's feedback.
