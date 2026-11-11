//go:build e2e

// End-to-end test against a real Multica. Never point it at production: it creates issues.
//
//	MI_E2E_URL=http://127.0.0.1:18310 MI_E2E_WORKSPACE=acme MI_E2E_AGENT=Triage \
//	MI_E2E_TOKEN_A=mul_… MI_E2E_TOKEN_B=mul_… go test -tags e2e -run E2E ./internal/intake/
//
// Token A and B belong to two member accounts of the workspace (two bots).
package intake

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vantt/multica-intake/internal/config"
)

func TestE2E(t *testing.T) {
	env := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Skip(k + " not set")
		}
		return v
	}
	c := config.Default()
	c.MulticaURL, c.IntakeAgent, c.Source = env("MI_E2E_URL"), env("MI_E2E_AGENT"), "e2e"
	ws := env("MI_E2E_WORKSPACE")
	s := New(c, nil)
	ctx := context.Background()
	a, err := s.Authenticate(ctx, env("MI_E2E_TOKEN_A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Authenticate(ctx, env("MI_E2E_TOKEN_B"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWorkspaces(ctx, a)
	if err != nil || len(list) == 0 {
		t.Fatalf("list_workspaces: %v %v", list, err)
	}
	title := fmt.Sprintf("e2e %d", time.Now().UnixNano())
	rid := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	r, err := s.Submit(ctx, a, SubmitInput{Workspace: ws, Title: title, Request: "e2e request", RequestID: rid,
		Hints: Hints{Project: "does-not-exist"}})
	if err != nil || r.Issue == "" || r.AssignedTo == "" {
		t.Fatalf("submit: %+v %v", r, err)
	}
	again, err := s.Submit(ctx, a, SubmitInput{Workspace: ws, Title: title + " x", Request: "again", RequestID: rid})
	if err != nil || !again.AlreadySent || again.Issue != r.Issue {
		t.Fatalf("request_id retry: %+v %v", again, err)
	}
	dup, err := s.Submit(ctx, a, SubmitInput{Workspace: ws, Title: title, Request: "same title"})
	if err != nil || !dup.AlreadySent || dup.Issue != r.Issue {
		t.Fatalf("same title: %+v %v", dup, err)
	}
	st, err := s.Status(ctx, a, r.Issue)
	if err != nil || st.State != "open" || st.Project != "" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, err := s.Status(ctx, b, r.Issue); code(err) != CodeNotFound {
		t.Fatalf("B sees A's issue: %v", err)
	}
	if _, err := s.Reply(ctx, b, r.Issue, "hi"); code(err) != CodeNotFound {
		t.Fatalf("B replies on A's issue: %v", err)
	}
	if rr, err := s.Reply(ctx, a, r.Issue, "more context"); err != nil || !rr.Posted {
		t.Fatalf("reply: %+v %v", rr, err)
	}
	mine, err := s.ListMine(ctx, a, ListInput{Workspace: ws})
	found := false
	for _, m := range mine {
		found = found || m.Issue == r.Issue
	}
	if err != nil || !found {
		t.Fatalf("list_my_issues: %v %v", mine, err)
	}
	other, _ := s.ListMine(ctx, b, ListInput{Workspace: ws, State: "all"})
	for _, m := range other {
		if m.Issue == r.Issue {
			t.Fatal("B lists A's issue")
		}
	}
}
