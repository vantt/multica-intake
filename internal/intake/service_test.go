package intake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vantt/multica-intake/internal/config"
)

func caller(t *testing.T, s *Service, tok string) Caller {
	t.Helper()
	c, err := s.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatalf("auth %s: %v", tok, err)
	}
	return c
}

func code(err error) string {
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Code
	}
	return ""
}

func TestAuthenticateRejectsBadToken(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	if _, err := s.Authenticate(context.Background(), "nope"); code(err) != CodeUnauthorized {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if _, err := s.Authenticate(context.Background(), ""); code(err) != CodeUnauthorized {
		t.Fatalf("want unauthorized for empty, got %v", err)
	}
}

func TestListWorkspacesOffersOnlyWorkspacesWithTheIntakeAgent(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	ws, err := s.ListWorkspaces(context.Background(), caller(t, s, "tok-maya"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Slug != "acme" || ws[0].IntakeAgent != "Phương" {
		t.Fatalf("got %+v", ws)
	}
	if len(ws[0].Projects) != 1 || ws[0].Projects[0].Name != "Website" || len(ws[0].Types) != 2 {
		t.Fatalf("projects/types: %+v", ws[0])
	}
}

func TestSubmitNeverSetsProjectTypeOrLabels(t *testing.T) {
	f, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	r, err := s.Submit(context.Background(), caller(t, s, "tok-maya"), SubmitInput{Workspace: "acme", Title: "  Landing   page ",
		Request: "Need a landing page", Hints: Hints{Project: "website", Type: "Sales"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Issue != "ACME-1" || r.AssignedTo != "Phương" || r.URL != "http://app/acme/issues/ACME-1" {
		t.Fatalf("result %+v", r)
	}
	body := f.created[0]
	for _, k := range []string{"project_id", "properties", "label_ids", "parent_issue_id"} {
		if _, ok := body[k]; ok {
			t.Fatalf("create body carries %s: %v", k, body)
		}
	}
	if body["title"] != "Landing page" || body["assignee_id"] != "a-pm" || body["assignee_type"] != "agent" || body["status"] != "todo" {
		t.Fatalf("create body %v", body)
	}
	desc := body["description"].(string)
	if !strings.Contains(desc, "- Project: Website\n") || !strings.Contains(desc, "- Type: Sales (not in this workspace)") {
		t.Fatalf("description:\n%s", desc)
	}
	meta := f.issues[0]["metadata"].(map[string]any)
	if meta["source"] != "mausbot" || meta["intake.hint_project"] != "Website" || meta["intake.hint_type"] != "Sales" {
		t.Fatalf("metadata %v", meta)
	}
	if f.agentHdrs != 0 {
		t.Fatal("a request carried X-Agent-ID / X-Task-ID")
	}
	if len(r.Notes) != 2 {
		t.Fatalf("notes %v", r.Notes)
	}
}

func TestSubmitValidatesEverythingAtOnce(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, func(c *config.Config) { c.MaxTitle = 5 })
	_, err := s.Submit(context.Background(), caller(t, s, "tok-maya"), SubmitInput{Workspace: "acme", Title: "too long title", Request: "", RequestID: "bad id!"})
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeBadInput || !strings.Contains(ie.Why, "title is longer") || !strings.Contains(ie.Why, "request is empty") || !strings.Contains(ie.Why, "request_id") {
		t.Fatalf("got %v", err)
	}
}

func TestSubmitUnknownWorkspaceAndNoAgent(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	c := caller(t, s, "tok-maya")
	if _, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "nope", Title: "t", Request: "r"}); code(err) != CodeNoWorkspace {
		t.Fatalf("want unknown_workspace, got %v", err)
	}
	if _, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "other", Title: "t", Request: "r"}); code(err) != CodeNoIntakeAgent {
		t.Fatalf("want no_intake_agent, got %v", err)
	}
}

func TestConfiguredWorkspacesNarrowTheList(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, func(c *config.Config) {
		c.Workspaces = []string{"other"}
		c.IntakeAgents = map[string]string{"other": "Someone"}
	})
	c := caller(t, s, "tok-maya")
	ws, _ := s.ListWorkspaces(context.Background(), c)
	if len(ws) != 1 || ws[0].Slug != "other" || ws[0].IntakeAgent != "Someone" {
		t.Fatalf("got %+v", ws)
	}
	if _, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "acme", Title: "t", Request: "r"}); code(err) != CodeNoWorkspace {
		t.Fatalf("acme should not be allowed: %v", err)
	}
}

func TestSubmitIsIdempotent(t *testing.T) {
	f, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	c := caller(t, s, "tok-maya")
	a, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "acme", Title: "One", Request: "r", RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "acme", Title: "Two", Request: "r", RequestID: "req-1"})
	if err != nil || !b.AlreadySent || b.Issue != a.Issue {
		t.Fatalf("same request_id: %+v %v", b, err)
	}
	d, err := s.Submit(context.Background(), c, SubmitInput{Workspace: "acme", Title: "One", Request: "again"})
	if err != nil || !d.AlreadySent || d.Issue != a.Issue {
		t.Fatalf("same open title: %+v %v", d, err)
	}
	if len(f.issues) != 1 {
		t.Fatalf("created %d issues", len(f.issues))
	}
	// Someone else's open issue with the same title is a duplicate error, not "yours".
	ada := caller(t, s, "tok-ada")
	if _, err := s.Submit(context.Background(), ada, SubmitInput{Workspace: "acme", Title: "One", Request: "r"}); code(err) != CodeDuplicate {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestOnlyOwnIssuesAreVisible(t *testing.T) {
	f, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	maya, ada, own := caller(t, s, "tok-maya"), caller(t, s, "tok-ada"), caller(t, s, "tok-own")
	r, err := s.Submit(context.Background(), maya, SubmitInput{Workspace: "acme", Title: "Maya's", Request: "r"})
	if err != nil {
		t.Fatal(err)
	}
	// The owner's issue tagged with the same source is still not Maya's.
	f.issues = append(f.issues, map[string]any{"id": "ix", "workspace_id": "w1", "identifier": "ACME-99", "title": "owner", "status": "todo",
		"status_category": "todo", "creator_type": "member", "creator_id": "u-own", "metadata": map[string]any{"source": "mausbot"}, "properties": map[string]any{}})
	if _, err := s.Status(context.Background(), maya, "ACME-99"); code(err) != CodeNotFound {
		t.Fatalf("owner's issue visible to maya: %v", err)
	}
	if _, err := s.Status(context.Background(), ada, r.Issue); code(err) != CodeNotFound {
		t.Fatalf("maya's issue visible to ada: %v", err)
	}
	if _, err := s.Reply(context.Background(), ada, r.Issue, "hi"); code(err) != CodeNotFound {
		t.Fatalf("ada could reply on maya's issue: %v", err)
	}
	l, _ := s.ListMine(context.Background(), ada, ListInput{})
	if len(l) != 0 {
		t.Fatalf("ada sees %v", l)
	}
	l, _ = s.ListMine(context.Background(), maya, ListInput{State: "all"})
	if len(l) != 1 || l[0].Issue != r.Issue {
		t.Fatalf("maya sees %v", l)
	}
	_ = own
	for _, bad := range []string{"", "../x", "ACME-1/comments", strings.Repeat("A", 100)} {
		if _, err := s.Status(context.Background(), maya, bad); code(err) != CodeNotFound {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestStatusRejectedAndReply(t *testing.T) {
	f, srv := newFake(t)
	s := testService(t, srv.URL, nil)
	maya := caller(t, s, "tok-maya")
	r, _ := s.Submit(context.Background(), maya, SubmitInput{Workspace: "acme", Title: "X", Request: "r", Hints: Hints{Type: "tech · feature"}})
	// Phương asks a question.
	f.comments["i1"] = append(f.comments["i1"], map[string]any{"id": "c0", "author_type": "agent", "author_id": "a-pm", "content": "Which audience?", "type": "comment", "created_at": "t"})
	st, err := s.Status(context.Background(), maya, r.Issue)
	if err != nil || st.State != "open" || !st.NeedsReply || st.Comments[0].Author != "Phương" || st.Comments[0].Kind != "agent" {
		t.Fatalf("status %+v %v", st, err)
	}
	rr, err := s.Reply(context.Background(), maya, r.Issue, "Students")
	if err != nil || rr.Notify != "Phương" {
		t.Fatalf("reply %+v %v", rr, err)
	}
	if got := f.comments["i1"][1]["content"]; got != "[@Phương](mention://agent/a-pm) Students" {
		t.Fatalf("reply content %q", got)
	}
	st, _ = s.Status(context.Background(), maya, r.Issue)
	if st.NeedsReply || st.Comments[1].Kind != "you" {
		t.Fatalf("after reply %+v", st)
	}
	// Multica classifies, then the agent rejects.
	f.issues[0]["project_id"] = "p1"
	f.issues[0]["properties"] = map[string]any{"prop-type": "o2"}
	f.comments["i1"] = append(f.comments["i1"], map[string]any{"id": "c9", "author_type": "agent", "author_id": "a-pm", "content": "Rejected: no budget given.", "type": "comment", "created_at": "t"})
	f.issues[0]["status"], f.issues[0]["status_category"] = "cancelled", "cancelled"
	st, _ = s.Status(context.Background(), maya, r.Issue)
	if st.State != "rejected" || st.Rejection != "Rejected: no budget given." || st.Project != "Website" || st.Type != "Marketing · Copy" {
		t.Fatalf("rejected %+v", st)
	}
	if _, err := s.Reply(context.Background(), maya, r.Issue, "please"); code(err) != CodeClosed {
		t.Fatalf("reply on closed: %v", err)
	}
	l, _ := s.ListMine(context.Background(), maya, ListInput{})
	if len(l) != 0 {
		t.Fatalf("open list should be empty: %v", l)
	}
	l, _ = s.ListMine(context.Background(), maya, ListInput{State: "closed"})
	if len(l) != 1 || l[0].State != "rejected" {
		t.Fatalf("closed list %v", l)
	}
}

func TestVietnameseDescription(t *testing.T) {
	f, srv := newFake(t)
	s := testService(t, srv.URL, func(c *config.Config) { c.Locale = "vi" })
	if _, err := s.Submit(context.Background(), caller(t, s, "tok-maya"), SubmitInput{Workspace: "acme", Title: "A", Request: "B"}); err != nil {
		t.Fatal(err)
	}
	d := f.created[0]["description"].(string)
	if !strings.Contains(d, "## Gợi ý của người gửi") || !strings.Contains(d, "- Project: không có") || !strings.Contains(d, "Gửi bởi Maya (MausBot)") {
		t.Fatalf("vi description:\n%s", d)
	}
}

func TestListWorkspacesSaysWhyItIsEmpty(t *testing.T) {
	_, srv := newFake(t)
	s := testService(t, srv.URL, func(c *config.Config) { c.IntakeAgent = "Phương (PM)" })
	_, err := s.ListWorkspaces(context.Background(), caller(t, s, "tok-maya"))
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeNoIntakeAgent || !strings.Contains(ie.Why, "acme (no agent named") {
		t.Fatalf("got %v", err)
	}
}
