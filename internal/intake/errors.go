package intake

import (
	"errors"
	"fmt"

	"github.com/vantt/multica-intake/internal/multica"
)

// Error is what a tool returns to the calling bot: a stable code, what went
// wrong, and what to do about it. The bot shows it to its human, so the text is
// plain and the fix is concrete.
type Error struct {
	Code string `json:"code"`
	Why  string `json:"why"`
	Fix  string `json:"fix"`
}

func (e *Error) Error() string {
	if e.Fix == "" {
		return e.Why
	}
	return e.Why + " Fix: " + e.Fix
}

func newErr(code, why, fix string) *Error { return &Error{Code: code, Why: why, Fix: fix} }

// Codes. They are part of the tool contract: never rename one.
const (
	CodeUnauthorized  = "unauthorized" // the PAT is missing, invalid, expired or revoked
	CodeBadInput      = "bad_input"    // an argument is missing or too long
	CodeNoWorkspace   = "unknown_workspace"
	CodeNoIntakeAgent = "no_intake_agent" // the workspace has no agent to receive the issue
	CodeNotFound      = "not_found"       // no such issue among the caller's own
	CodeClosed        = "issue_closed"
	CodeDuplicate     = "duplicate"     // an open issue with the same title exists (not the caller's)
	CodeUpstream      = "multica_error" // Multica answered with an error we did not expect
	CodeUnavailable   = "multica_unreachable"
	CodePartial       = "partially_done"
)

// fromMultica maps a Multica client error to a tool error.
func fromMultica(err error, what string) *Error {
	var me *multica.Error
	if errors.As(err, &me) {
		switch me.Status {
		case 401:
			return newErr(CodeUnauthorized, "Multica refused this account's token (expired, revoked or wrong).",
				"Ask the owner to create a new personal access token for this account and put it in this MCP server's Authorization header.")
		case 403:
			return newErr(CodeUpstream, fmt.Sprintf("Multica does not allow this account to %s: %s.", what, me.Message),
				"Ask the owner to check the account's membership and role in that workspace.")
		case 404:
			return newErr(CodeNotFound, fmt.Sprintf("Multica could not find it while trying to %s.", what), "Check the identifier, then call list_my_issues.")
		}
		return newErr(CodeUpstream, fmt.Sprintf("Multica answered HTTP %d while trying to %s: %s.", me.Status, what, me.Message), "Try again in a minute; if it keeps failing, tell the owner.")
	}
	return newErr(CodeUnavailable, fmt.Sprintf("Could not reach Multica while trying to %s.", what), "Try again in a minute; if it keeps failing, tell the owner to run `multica-intake doctor`.")
}
