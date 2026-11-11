// Package multica is a small client for the Multica REST API, covering only
// the calls multica-intake needs. Every call is made with the caller's own
// personal access token (PAT), so Multica records the caller as the actor.
//
// The client never sends X-Agent-ID or X-Task-ID: without them Multica always
// resolves the actor to the PAT's member (handler.resolveActor), never to an agent.
package multica

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 8 << 20 // cap every response at 8 MiB

// Client talks to one Multica API with one PAT.
type Client struct {
	base  string
	token string
	http  *http.Client
	ua    string
}

// New returns a client for base (e.g. http://127.0.0.1:8080) authenticated by token.
func New(base, token string, timeout time.Duration, userAgent string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, ua: userAgent,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // never follow a redirect with the PAT attached
		}}}
}

// Error is a non-2xx answer from Multica.
type Error struct {
	Status  int
	Code    string // Multica's machine code, when it sent one (e.g. active_duplicate_issue)
	Message string
	Issue   json.RawMessage // the conflicting issue on a 409 active_duplicate_issue
}

func (e *Error) Error() string { return fmt.Sprintf("multica: HTTP %d: %s", e.Status, e.Message) }

// IsStatus reports whether err is a Multica answer with that HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

func (c *Client) do(ctx context.Context, method, path, ws string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ws != "" {
		req.Header.Set("X-Workspace-ID", ws)
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("multica: %s %s: %w", method, path, scrub(err, c.token))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("multica: reading %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Code  string          `json:"code"`
			Error string          `json:"error"`
			Issue json.RawMessage `json:"issue"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &Error{Status: resp.StatusCode, Code: e.Code, Message: msg, Issue: e.Issue}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("multica: unexpected answer from %s: %w", path, err)
	}
	return nil
}

// scrub removes the token from an error text, in case a transport echoes a header.
func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "«redacted»"))
}

// User is the account behind the PAT.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Me returns the PAT's account. A 401 means the PAT is invalid, expired or revoked.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	return u, c.do(ctx, http.MethodGet, "/api/me", "", nil, &u)
}

// Workspace is one workspace the account is a member of.
type Workspace struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	IssuePrefix string  `json:"issue_prefix"`
}

// Workspaces lists the account's workspaces.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var ws []Workspace
	return ws, c.do(ctx, http.MethodGet, "/api/workspaces", "", nil, &ws)
}

// Project is a project in a workspace.
type Project struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
}

// Projects lists a workspace's projects.
func (c *Client) Projects(ctx context.Context, ws string) ([]Project, error) {
	var r struct {
		Projects []Project `json:"projects"`
	}
	return r.Projects, c.do(ctx, http.MethodGet, "/api/projects", ws, nil, &r)
}

// Option is one choice of a select property.
type Option struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Property is a custom issue property definition.
type Property struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	Config      struct {
		Options []Option `json:"options"`
	} `json:"config"`
}

// Properties lists a workspace's issue properties.
func (c *Client) Properties(ctx context.Context, ws string) ([]Property, error) {
	var r struct {
		Properties []Property `json:"properties"`
	}
	return r.Properties, c.do(ctx, http.MethodGet, "/api/properties", ws, nil, &r)
}

// Agent is a Multica agent.
type Agent struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArchivedAt any    `json:"archived_at"`
}

// Agents lists a workspace's agents.
func (c *Client) Agents(ctx context.Context, ws string) ([]Agent, error) {
	var a []Agent
	return a, c.do(ctx, http.MethodGet, "/api/agents", ws, nil, &a)
}

// Member is a workspace member.
type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// Members lists a workspace's members.
func (c *Client) Members(ctx context.Context, ws string) ([]Member, error) {
	var m []Member
	return m, c.do(ctx, http.MethodGet, "/api/workspaces/"+url.PathEscape(ws)+"/members", ws, nil, &m)
}

// Issue is the subset of an issue multica-intake reads.
type Issue struct {
	ID           string                     `json:"id"`
	WorkspaceID  string                     `json:"workspace_id"`
	Identifier   string                     `json:"identifier"`
	Title        string                     `json:"title"`
	Description  *string                    `json:"description"`
	Status       string                     `json:"status"`
	StatusCat    string                     `json:"status_category"`
	StatusName   string                     `json:"status_name"`
	AssigneeType *string                    `json:"assignee_type"`
	AssigneeID   *string                    `json:"assignee_id"`
	CreatorType  string                     `json:"creator_type"`
	CreatorID    string                     `json:"creator_id"`
	ProjectID    *string                    `json:"project_id"`
	CreatedAt    string                     `json:"created_at"`
	UpdatedAt    string                     `json:"updated_at"`
	Metadata     map[string]any             `json:"metadata"`
	Properties   map[string]json.RawMessage `json:"properties"`
}

// CreateIssue is the body of POST /api/issues. Only these fields are ever sent:
// no project, no properties (Type), no labels — classification is Multica's job.
type CreateIssue struct {
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	Status       string  `json:"status"`
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
}

// CreateIssue creates an issue in ws.
func (c *Client) CreateIssue(ctx context.Context, ws string, in CreateIssue) (Issue, error) {
	var is Issue
	return is, c.do(ctx, http.MethodPost, "/api/issues", ws, in, &is)
}

// SetMetadata sets one metadata key on an issue (single-key atomic in Multica).
func (c *Client) SetMetadata(ctx context.Context, ws, issueID, key string, value any) error {
	return c.do(ctx, http.MethodPut, "/api/issues/"+url.PathEscape(issueID)+"/metadata/"+url.PathEscape(key), ws,
		map[string]any{"value": value}, nil)
}

// Issue gets one issue by UUID or identifier (SENC-12).
func (c *Client) Issue(ctx context.Context, ws, idOrIdentifier string) (Issue, error) {
	var is Issue
	return is, c.do(ctx, http.MethodGet, "/api/issues/"+url.PathEscape(idOrIdentifier), ws, nil, &is)
}

// IssueFilter narrows ListIssues.
type IssueFilter struct {
	CreatorID string
	Metadata  map[string]any
	OpenOnly  bool
	Limit     int
}

// ListIssues lists issues in ws matching f.
func (c *Client) ListIssues(ctx context.Context, ws string, f IssueFilter) ([]Issue, error) {
	q := url.Values{}
	if f.CreatorID != "" {
		q.Set("creator_id", f.CreatorID)
	}
	if len(f.Metadata) > 0 {
		b, _ := json.Marshal(f.Metadata)
		q.Set("metadata", string(b))
	}
	if f.OpenOnly {
		q.Set("open_only", "true")
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprint(f.Limit))
	}
	var r struct {
		Issues []Issue `json:"issues"`
	}
	return r.Issues, c.do(ctx, http.MethodGet, "/api/issues?"+q.Encode(), ws, nil, &r)
}

// Comment is one comment on an issue.
type Comment struct {
	ID         string `json:"id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	Content    string `json:"content"`
	Type       string `json:"type"`
	CreatedAt  string `json:"created_at"`
}

// Comments lists an issue's comments, oldest first.
func (c *Client) Comments(ctx context.Context, ws, issueID string) ([]Comment, error) {
	var cs []Comment
	return cs, c.do(ctx, http.MethodGet, "/api/issues/"+url.PathEscape(issueID)+"/comments", ws, nil, &cs)
}

// AddComment posts a comment as the PAT's account.
func (c *Client) AddComment(ctx context.Context, ws, issueID, content string) (Comment, error) {
	var cm Comment
	return cm, c.do(ctx, http.MethodPost, "/api/issues/"+url.PathEscape(issueID)+"/comments", ws,
		map[string]any{"content": content}, &cm)
}

// Health checks that the API answers (no token needed).
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET /health answered %d", resp.StatusCode)
	}
	return nil
}
