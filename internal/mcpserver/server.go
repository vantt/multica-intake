// Package mcpserver exposes the intake service as an MCP server, over
// Streamable HTTP (each caller authenticates every request with its own
// Multica PAT) or over stdio (one PAT read from a file).
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vantt/multica-intake/internal/intake"
	"github.com/vantt/multica-intake/internal/version"
)

// Instructions are sent to the client at initialize; they are the contract the
// bot reads before its first call.
const Instructions = `multica-intake lets you hand work to a Multica team and follow it. You cannot change anything once it is sent.

1. list_workspaces: where you may send work, with each workspace's projects and Types.
2. submit_issue: send one piece of work. Put your best guess of project and Type in hints; they are only suggestions — the workspace's intake agent decides, and may ask you questions.
3. get_issue_status / list_my_issues: follow your issues. When needs_reply is true, the team asked you something: answer with reply_to_issue.
4. A rejected issue comes with rejection_reason. Tell your human, agree what is missing, then submit a new issue. Never resubmit the same text unchanged.

Your human must approve each submit_issue and reply_to_issue. Only your own issues are visible to you.`

// Identify returns the caller of a tool call.
type Identify func(ctx context.Context, req *mcp.CallToolRequest) (intake.Caller, error)

// New builds the MCP server with the five tools.
func New(svc *intake.Service, identify Identify) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "multica-intake", Title: "Multica intake", Version: version.Get().Version},
		&mcp.ServerOptions{Instructions: Instructions})
	f := false

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List the Multica workspaces you may send work to, with their projects and Types (use them as hints for submit_issue).",
		Annotations: &mcp.ToolAnnotations{Title: "List workspaces", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workspacesOut, error) {
		c, err := identify(ctx, req)
		if err != nil {
			return nil, workspacesOut{}, toolErr(err)
		}
		ws, err := svc.ListWorkspaces(ctx, c)
		if err != nil {
			return nil, workspacesOut{}, toolErr(err)
		}
		return nil, workspacesOut{Workspaces: ws}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "submit_issue",
		Description: "Send one piece of work to a Multica workspace. It is created with no project and no Type, assigned to the workspace's intake agent, " +
			"which classifies it (your hints are only suggestions) and may ask you questions. Returns the issue identifier to follow.",
		Annotations: &mcp.ToolAnnotations{Title: "Submit an issue", DestructiveHint: &f, OpenWorldHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in intake.SubmitInput) (*mcp.CallToolResult, intake.SubmitResult, error) {
		c, err := identify(ctx, req)
		if err != nil {
			return nil, intake.SubmitResult{}, toolErr(err)
		}
		r, err := svc.Submit(ctx, c, in)
		if err != nil {
			return nil, intake.SubmitResult{}, toolErr(err)
		}
		return nil, r, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_issue_status",
		Description: "Read one of your issues: state (open, done, rejected), status, the project and Type Multica chose, who has it, and its comments. needs_reply=true means the team asked you something.",
		Annotations: &mcp.ToolAnnotations{Title: "Get issue status", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in issueRef) (*mcp.CallToolResult, intake.IssueStatus, error) {
		c, err := identify(ctx, req)
		if err != nil {
			return nil, intake.IssueStatus{}, toolErr(err)
		}
		r, err := svc.Status(ctx, c, in.Issue)
		if err != nil {
			return nil, intake.IssueStatus{}, toolErr(err)
		}
		return nil, r, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "reply_to_issue",
		Description: "Answer a question on one of your open issues. The reply is posted as a comment and wakes the agent that has the issue.",
		Annotations: &mcp.ToolAnnotations{Title: "Reply to an issue", DestructiveHint: &f, OpenWorldHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in replyIn) (*mcp.CallToolResult, intake.ReplyResult, error) {
		c, err := identify(ctx, req)
		if err != nil {
			return nil, intake.ReplyResult{}, toolErr(err)
		}
		r, err := svc.Reply(ctx, c, in.Issue, in.Text)
		if err != nil {
			return nil, intake.ReplyResult{}, toolErr(err)
		}
		return nil, r, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_my_issues",
		Description: "List the issues you submitted, newest activity first. Default: the open ones.",
		Annotations: &mcp.ToolAnnotations{Title: "List my issues", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in intake.ListInput) (*mcp.CallToolResult, myIssuesOut, error) {
		c, err := identify(ctx, req)
		if err != nil {
			return nil, myIssuesOut{}, toolErr(err)
		}
		r, err := svc.ListMine(ctx, c, in)
		if err != nil {
			return nil, myIssuesOut{}, toolErr(err)
		}
		return nil, myIssuesOut{Issues: r}, nil
	})
	return s
}

type workspacesOut struct {
	Workspaces []intake.WorkspaceInfo `json:"workspaces"`
}

type myIssuesOut struct {
	Issues []intake.IssueSummary `json:"issues"`
}

type issueRef struct {
	Issue string `json:"issue" jsonschema:"issue identifier, e.g. SENC-12, as returned by submit_issue"`
}

type replyIn struct {
	Issue string `json:"issue" jsonschema:"issue identifier, e.g. SENC-12"`
	Text  string `json:"text" jsonschema:"your answer, in plain words; markdown allowed"`
}

// toolError is returned from a handler so the SDK reports isError with the
// text {"error":{"code","why","fix"}} and no structured output.
type toolError struct{ body string }

func (t toolError) Error() string { return t.body }

// toolErr turns an error into the JSON body the model reads.
func toolErr(err error) error {
	var ie *intake.Error
	if !errors.As(err, &ie) {
		ie = &intake.Error{Code: "internal", Why: "multica-intake failed unexpectedly.", Fix: "Try again; if it keeps failing, tell the owner to check the multica-intake log."}
	}
	b, _ := json.Marshal(map[string]any{"error": ie})
	return toolError{body: string(b)}
}

// FromBearer identifies HTTP callers from the TokenInfo set by Handler.
func FromBearer(_ context.Context, req *mcp.CallToolRequest) (intake.Caller, error) {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		if c, ok := req.Extra.TokenInfo.Extra["caller"].(intake.Caller); ok {
			return c, nil
		}
	}
	return intake.Caller{}, &intake.Error{Code: intake.CodeUnauthorized, Why: "This call carried no Multica token.", Fix: "Send `Authorization: Bearer <personal access token>`."}
}

// Static identifies every stdio call as one caller.
func Static(c intake.Caller) Identify {
	return func(context.Context, *mcp.CallToolRequest) (intake.Caller, error) { return c, nil }
}

// Handler is the HTTP surface: /mcp (bearer-authenticated MCP) and /healthz.
func Handler(svc *intake.Service, maxBody int64) http.Handler {
	server := New(svc, FromBearer)
	mcpH := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxBody})
	verifier := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		c, err := svc.Authenticate(ctx, token)
		if err != nil {
			var ie *intake.Error
			if errors.As(err, &ie) && ie.Code == intake.CodeUnauthorized {
				return nil, fmt.Errorf("%w: %s %s", auth.ErrInvalidToken, ie.Why, ie.Fix)
			}
			return nil, errors.New("cannot reach Multica to check the token; try again")
		}
		return &auth.TokenInfo{UserID: c.User.ID, Expiration: time.Now().Add(time.Minute), Extra: map[string]any{"caller": c}}, nil
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{})(mcpH))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version.Get()})
	})
	return mux
}
