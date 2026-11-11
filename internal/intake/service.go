// Package intake holds the rules of multica-intake: a caller (a chat bot with
// its own Multica account) may submit work to Multica and follow it, and do
// nothing else.
//
//   - Classification is Multica's job. An issue is created with no project, no
//     Type and no labels, assigned to the workspace's intake agent; the caller's
//     project/Type guesses go into the description as suggestions only.
//   - A caller only ever sees the issues it created through this server: created
//     by its own account AND tagged metadata.source = <source>.
//   - There is no tool to edit, reassign, move, close or delete anything.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vantt/multica-intake/internal/audit"
	"github.com/vantt/multica-intake/internal/config"
	"github.com/vantt/multica-intake/internal/multica"
	"github.com/vantt/multica-intake/internal/version"
)

// Metadata keys written on every submitted issue.
const (
	MetaSource      = "source"
	MetaHintProject = "intake.hint_project"
	MetaHintType    = "intake.hint_type"
	MetaRequestID   = "intake.request_id"
)

// Caller is an authenticated caller: its PAT and the account behind it.
type Caller struct {
	Token string
	User  multica.User
}

// Service implements the five tools.
type Service struct {
	cfg   config.Config
	audit *audit.Logger
	ua    string

	mu    sync.Mutex
	whoIs map[string]whoEntry // sha256(token) → account, cached briefly
}

type whoEntry struct {
	user multica.User
	exp  time.Time
}

const whoTTL = 2 * time.Minute

// New returns a service for cfg (already validated).
func New(cfg config.Config, a *audit.Logger) *Service {
	return &Service{cfg: cfg, audit: a, whoIs: map[string]whoEntry{},
		ua: "multica-intake/" + version.Get().Version}
}

func (s *Service) client(token string) *multica.Client {
	return multica.New(s.cfg.MulticaURL, token, s.cfg.TimeoutDuration(), s.ua)
}

// Authenticate resolves a PAT to its account (GET /api/me), cached for 2 minutes
// so a revoked token stops working within that window.
func (s *Service) Authenticate(ctx context.Context, token string) (Caller, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Caller{}, newErr(CodeUnauthorized, "No Multica token was sent.", "Configure this MCP server with the header `Authorization: Bearer <personal access token>`.")
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if e, ok := s.whoIs[key]; ok && time.Now().Before(e.exp) {
		s.mu.Unlock()
		return Caller{Token: token, User: e.user}, nil
	}
	s.mu.Unlock()
	u, err := s.client(token).Me(ctx)
	if err != nil {
		return Caller{}, fromMultica(err, "identify the account")
	}
	s.mu.Lock()
	if len(s.whoIs) > 1000 {
		s.whoIs = map[string]whoEntry{}
	}
	s.whoIs[key] = whoEntry{user: u, exp: time.Now().Add(whoTTL)}
	s.mu.Unlock()
	return Caller{Token: token, User: u}, nil
}

// ---------- workspaces ----------

// ProjectInfo is a project the caller may suggest.
type ProjectInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// WorkspaceInfo is one workspace the caller may submit to.
type WorkspaceInfo struct {
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	IntakeAgent string        `json:"intake_agent"`
	Projects    []ProjectInfo `json:"projects"`
	Types       []string      `json:"types"`
}

// allowed returns the workspaces the caller may use: its memberships, narrowed
// to the configured list when one is set.
func (s *Service) allowed(ctx context.Context, c Caller) ([]multica.Workspace, error) {
	all, err := s.client(c.Token).Workspaces(ctx)
	if err != nil {
		return nil, fromMultica(err, "list workspaces")
	}
	if len(s.cfg.Workspaces) == 0 {
		return all, nil
	}
	want := map[string]bool{}
	for _, w := range s.cfg.Workspaces {
		want[strings.ToLower(w)] = true
	}
	var out []multica.Workspace
	for _, w := range all {
		if want[strings.ToLower(w.Slug)] {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *Service) pickWorkspace(ctx context.Context, c Caller, name string) (multica.Workspace, []multica.Workspace, error) {
	ws, err := s.allowed(ctx, c)
	if err != nil {
		return multica.Workspace{}, nil, err
	}
	n := strings.ToLower(strings.TrimSpace(name))
	for _, w := range ws {
		if strings.ToLower(w.Slug) == n || strings.ToLower(w.Name) == n {
			return w, ws, nil
		}
	}
	return multica.Workspace{}, ws, newErr(CodeNoWorkspace,
		fmt.Sprintf("%q is not a workspace this account can submit to. Allowed: %s.", name, slugList(ws)),
		"Call list_workspaces and use one of its slugs.")
}

func slugList(ws []multica.Workspace) string {
	if len(ws) == 0 {
		return "none (the account is in no allowed workspace; tell the owner)"
	}
	var s []string
	for _, w := range ws {
		s = append(s, w.Slug)
	}
	return strings.Join(s, ", ")
}

func (s *Service) intakeAgent(ctx context.Context, cl *multica.Client, w multica.Workspace) (multica.Agent, error) {
	name := s.cfg.AgentFor(w.Slug)
	agents, err := cl.Agents(ctx, w.ID)
	if err != nil {
		return multica.Agent{}, fromMultica(err, "list agents")
	}
	for _, a := range agents {
		if a.ArchivedAt == nil && strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name)) {
			return a, nil
		}
	}
	return multica.Agent{}, newErr(CodeNoIntakeAgent,
		fmt.Sprintf("Workspace %s has no agent named %q to receive issues.", w.Slug, name),
		"Tell the owner: set the right agent with `multica-intake config set intake_agents."+w.Slug+" \"<agent name>\"`.")
}

func (s *Service) typeProperty(ctx context.Context, cl *multica.Client, ws string) (*multica.Property, error) {
	if s.cfg.TypeProperty == "" {
		return nil, nil
	}
	props, err := cl.Properties(ctx, ws)
	if err != nil {
		return nil, fromMultica(err, "list properties")
	}
	for i := range props {
		if !props[i].Archived && strings.EqualFold(props[i].Name, s.cfg.TypeProperty) {
			return &props[i], nil
		}
	}
	return nil, nil
}

// ListWorkspaces is the list_workspaces tool.
func (s *Service) ListWorkspaces(ctx context.Context, c Caller) (out []WorkspaceInfo, err error) {
	defer s.log("list_workspaces", c, "", "", time.Now(), &err)
	ws, err := s.allowed(ctx, c)
	if err != nil {
		return nil, err
	}
	cl := s.client(c.Token)
	out = []WorkspaceInfo{}
	var skipped []string
	for _, w := range ws {
		agent, aerr := s.intakeAgent(ctx, cl, w)
		if aerr != nil {
			if code := (*Error)(nil); !errors.As(aerr, &code) || code.Code != CodeNoIntakeAgent {
				return nil, aerr
			}
			skipped = append(skipped, w.Slug+" (no agent named "+strconv.Quote(s.cfg.AgentFor(w.Slug))+")")
			continue // a workspace nobody receives in is not offered
		}
		info := WorkspaceInfo{Slug: w.Slug, Name: w.Name, IntakeAgent: agent.Name, Projects: []ProjectInfo{}, Types: []string{}}
		if w.Description != nil {
			info.Description = clip(*w.Description, 500)
		}
		projects, err := cl.Projects(ctx, w.ID)
		if err != nil {
			return nil, fromMultica(err, "list projects")
		}
		for _, p := range projects {
			if p.Status == "cancelled" || p.Status == "completed" {
				continue
			}
			pi := ProjectInfo{Name: p.Title}
			if p.Description != nil {
				pi.Description = clip(*p.Description, 300)
			}
			info.Projects = append(info.Projects, pi)
		}
		tp, err := s.typeProperty(ctx, cl, w.ID)
		if err != nil {
			return nil, err
		}
		if tp != nil {
			for _, o := range tp.Config.Options {
				info.Types = append(info.Types, o.Name)
			}
		}
		out = append(out, info)
	}
	if len(out) == 0 {
		why := "This account can submit to no workspace."
		if len(skipped) > 0 {
			why = "None of this account's workspaces has the intake agent: " + strings.Join(skipped, ", ") + "."
		}
		return nil, newErr(CodeNoIntakeAgent, why,
			"Tell the owner: add this account to a workspace, or set the agent's exact name with `multica-intake config set intake_agent \"<name>\"`.")
	}
	return out, nil
}

// ---------- submit ----------

// Hints are the caller's guesses. They are written into the description and
// metadata, never into the issue's fields.
type Hints struct {
	Project string `json:"project,omitempty" jsonschema:"suggested project name (from list_workspaces); only a suggestion"`
	Type    string `json:"type,omitempty" jsonschema:"suggested Type (from list_workspaces); only a suggestion"`
}

// SubmitInput is the submit_issue arguments.
type SubmitInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug from list_workspaces"`
	Title     string `json:"title" jsonschema:"short title of the work, as the human would say it"`
	Request   string `json:"request" jsonschema:"what is wanted, with the context and what done looks like; markdown"`
	Hints     Hints  `json:"hints,omitzero" jsonschema:"optional suggestions; Multica decides the real project and Type"`
	RequestID string `json:"request_id,omitempty" jsonschema:"optional id you choose; sending the same id again returns the issue already created instead of a second one"`
}

// SubmitResult is what submit_issue returns.
type SubmitResult struct {
	Issue       string   `json:"issue"`
	URL         string   `json:"url,omitempty"`
	Workspace   string   `json:"workspace"`
	AssignedTo  string   `json:"assigned_to"`
	Status      string   `json:"status"`
	AlreadySent bool     `json:"already_sent"`
	Notes       []string `json:"notes"`
}

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,80}$`)

// Submit is the submit_issue tool.
func (s *Service) Submit(ctx context.Context, c Caller, in SubmitInput) (res SubmitResult, err error) {
	started := time.Now()
	wsSlug, ident := in.Workspace, ""
	defer func() { s.log("submit_issue", c, wsSlug, ident, started, &err) }()

	title := strings.Join(strings.Fields(in.Title), " ")
	req := strings.TrimSpace(in.Request)
	var bad []string
	if title == "" {
		bad = append(bad, "title is empty")
	}
	if utf8.RuneCountInString(title) > s.cfg.MaxTitle {
		bad = append(bad, fmt.Sprintf("title is longer than %d characters", s.cfg.MaxTitle))
	}
	if req == "" {
		bad = append(bad, "request is empty: say what is wanted and what done looks like")
	}
	if utf8.RuneCountInString(req) > s.cfg.MaxRequest {
		bad = append(bad, fmt.Sprintf("request is longer than %d characters", s.cfg.MaxRequest))
	}
	if in.RequestID != "" && !requestIDRe.MatchString(in.RequestID) {
		bad = append(bad, "request_id may only hold letters, digits and . _ : - (at most 80)")
	}
	if len(bad) > 0 {
		return res, newErr(CodeBadInput, strings.Join(bad, "; ")+".", "Fix the arguments and call submit_issue again.")
	}

	w, _, err := s.pickWorkspace(ctx, c, in.Workspace)
	if err != nil {
		return res, err
	}
	wsSlug = w.Slug
	cl := s.client(c.Token)

	if in.RequestID != "" {
		prev, err := cl.ListIssues(ctx, w.ID, multica.IssueFilter{CreatorID: c.User.ID, Limit: 5,
			Metadata: map[string]any{MetaSource: s.cfg.Source, MetaRequestID: in.RequestID}})
		if err != nil {
			return res, fromMultica(err, "look for an earlier submission")
		}
		if len(prev) > 0 {
			ident = prev[0].Identifier
			return SubmitResult{Issue: ident, URL: s.issueURL(w, ident), Workspace: w.Slug, Status: prev[0].Status,
				AssignedTo: "", AlreadySent: true, Notes: []string{"This request_id was already submitted; nothing new was created."}}, nil
		}
	}

	agent, err := s.intakeAgent(ctx, cl, w)
	if err != nil {
		return res, err
	}

	notes := []string{}
	hp, ht := clip(strings.TrimSpace(in.Hints.Project), 100), clip(strings.TrimSpace(in.Hints.Type), 100)
	hpKnown, htKnown := true, true
	if hp != "" {
		projects, err := cl.Projects(ctx, w.ID)
		if err != nil {
			return res, fromMultica(err, "list projects")
		}
		hpKnown = false
		for _, p := range projects {
			if strings.EqualFold(p.Title, hp) {
				hpKnown, hp = true, p.Title
			}
		}
		if !hpKnown {
			notes = append(notes, fmt.Sprintf("Project hint %q is not a project in %s; it is kept as text and the intake agent will choose.", hp, w.Slug))
		}
	}
	if ht != "" {
		tp, err := s.typeProperty(ctx, cl, w.ID)
		if err != nil {
			return res, err
		}
		htKnown = false
		if tp != nil {
			for _, o := range tp.Config.Options {
				if strings.EqualFold(o.Name, ht) {
					htKnown, ht = true, o.Name
				}
			}
		}
		if !htKnown {
			notes = append(notes, fmt.Sprintf("Type hint %q is not a Type in %s; it is kept as text and the intake agent will choose.", ht, w.Slug))
		}
	}

	desc := s.describe(req, c.User.Name, hp, hpKnown, ht, htKnown)
	at := "agent"
	issue, err := cl.CreateIssue(ctx, w.ID, multica.CreateIssue{Title: title, Description: desc, Status: "todo",
		AssigneeType: &at, AssigneeID: &agent.ID})
	if err != nil {
		var me *multica.Error
		if errors.As(err, &me) && me.Status == 409 && me.Code == "active_duplicate_issue" {
			var dup multica.Issue
			_ = json.Unmarshal(me.Issue, &dup)
			if dup.CreatorID == c.User.ID && fmt.Sprint(dup.Metadata[MetaSource]) == s.cfg.Source {
				ident = dup.Identifier
				return SubmitResult{Issue: ident, URL: s.issueURL(w, ident), Workspace: w.Slug, Status: dup.Status,
					AlreadySent: true, Notes: []string{"You already have an open issue with this title; nothing new was created."}}, nil
			}
			return res, newErr(CodeDuplicate, "An open issue with the same title already exists in "+w.Slug+" (someone else's).",
				"Use a more specific title, or ask the owner whether the existing issue already covers this.")
		}
		return res, fromMultica(err, "create the issue")
	}
	ident = issue.Identifier

	meta := [][2]any{{MetaSource, s.cfg.Source}}
	if hp != "" {
		meta = append(meta, [2]any{MetaHintProject, hp})
	}
	if ht != "" {
		meta = append(meta, [2]any{MetaHintType, ht})
	}
	if in.RequestID != "" {
		meta = append(meta, [2]any{MetaRequestID, in.RequestID})
	}
	for _, kv := range meta {
		if err := retry(ctx, func() error { return cl.SetMetadata(ctx, w.ID, issue.ID, kv[0].(string), kv[1]) }); err != nil {
			return res, newErr(CodePartial,
				fmt.Sprintf("Issue %s was created but could not be tagged (%s), so it will not show in list_my_issues.", ident, kv[0]),
				"Tell the owner the identifier; do not submit it again.")
		}
	}
	notes = append(notes, "Project and Type are only suggestions: "+agent.Name+" classifies the issue. Follow it with get_issue_status.")
	return SubmitResult{Issue: ident, URL: s.issueURL(w, ident), Workspace: w.Slug, AssignedTo: agent.Name,
		Status: issue.Status, Notes: notes}, nil
}

func retry(ctx context.Context, f func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = f(); err == nil {
			return nil
		}
		if multica.IsStatus(err, 400) || multica.IsStatus(err, 401) || multica.IsStatus(err, 403) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 300 * time.Millisecond):
		}
	}
	return err
}

func (s *Service) describe(req, who, hp string, hpKnown bool, ht string, htKnown bool) string {
	t := texts[s.cfg.Locale]
	var b strings.Builder
	b.WriteString(req)
	b.WriteString("\n\n## " + t.hintsHeading + "\n")
	line := func(label, v string, known bool) {
		switch {
		case v == "":
			b.WriteString("- " + label + ": " + t.none + "\n")
		case known:
			b.WriteString("- " + label + ": " + v + "\n")
		default:
			b.WriteString("- " + label + ": " + v + " " + t.unknown + "\n")
		}
	}
	line("Project", hp, hpKnown)
	line("Type", ht, htKnown)
	b.WriteString("\n---\n")
	b.WriteString(fmt.Sprintf(t.footer, who, s.cfg.Source))
	b.WriteString("\n")
	return b.String()
}

var texts = map[string]struct{ hintsHeading, none, unknown, footer string }{
	"en": {"Suggested by the sender", "none", "(not in this workspace)",
		"Sent by %s through multica-intake (source: %s). Project and Type above are only suggestions: the intake agent classifies this issue."},
	"vi": {"Gợi ý của người gửi", "không có", "(không có trong workspace này)",
		"Gửi bởi %s qua multica-intake (source: %s). Project và Type ở trên chỉ là gợi ý: agent nhận việc sẽ phân loại issue này."},
}

// ---------- status / reply / list ----------

// CommentView is one comment as the caller sees it.
type CommentView struct {
	Author string `json:"author"`
	Kind   string `json:"author_kind"` // you | agent | member
	At     string `json:"at"`
	Text   string `json:"text"`
}

// IssueStatus is what get_issue_status returns.
type IssueStatus struct {
	Issue      string        `json:"issue"`
	URL        string        `json:"url,omitempty"`
	Workspace  string        `json:"workspace"`
	Title      string        `json:"title"`
	State      string        `json:"state"` // open | done | rejected
	Status     string        `json:"status"`
	Project    string        `json:"project,omitempty"`
	Type       string        `json:"type,omitempty"`
	AssignedTo string        `json:"assigned_to,omitempty"`
	Rejection  string        `json:"rejection_reason,omitempty"`
	NeedsReply bool          `json:"needs_reply"`
	Comments   []CommentView `json:"comments"`
	Updated    string        `json:"updated_at"`
}

const maxComments = 20

// own finds one of the caller's issues by identifier (SENC-12) or UUID.
func (s *Service) own(ctx context.Context, c Caller, ref string) (multica.Workspace, multica.Issue, error) {
	ref = strings.TrimSpace(ref)
	notFound := newErr(CodeNotFound, fmt.Sprintf("%q is not one of the issues you submitted.", ref), "Call list_my_issues to see yours.")
	if ref == "" || len(ref) > 64 || strings.ContainsAny(ref, "/?#") {
		return multica.Workspace{}, multica.Issue{}, notFound
	}
	ws, err := s.allowed(ctx, c)
	if err != nil {
		return multica.Workspace{}, multica.Issue{}, err
	}
	cands := ws
	if p, _, ok := strings.Cut(ref, "-"); ok {
		var byPrefix []multica.Workspace
		for _, w := range ws {
			if strings.EqualFold(w.IssuePrefix, p) {
				byPrefix = append(byPrefix, w)
			}
		}
		if len(byPrefix) > 0 {
			cands = byPrefix
		}
	}
	cl := s.client(c.Token)
	for _, w := range cands {
		is, err := cl.Issue(ctx, w.ID, ref)
		if err != nil {
			if multica.IsStatus(err, 404) || multica.IsStatus(err, 400) || multica.IsStatus(err, 403) {
				continue
			}
			return w, is, fromMultica(err, "read the issue")
		}
		if is.WorkspaceID != w.ID || is.CreatorID != c.User.ID || is.CreatorType != "member" ||
			fmt.Sprint(is.Metadata[MetaSource]) != s.cfg.Source {
			return w, multica.Issue{}, notFound
		}
		return w, is, nil
	}
	return multica.Workspace{}, multica.Issue{}, notFound
}

func state(is multica.Issue) string {
	switch is.StatusCat {
	case "done":
		return "done"
	case "cancelled":
		return "rejected"
	}
	return "open"
}

// Status is the get_issue_status tool.
func (s *Service) Status(ctx context.Context, c Caller, ref string) (out IssueStatus, err error) {
	started := time.Now()
	wsSlug, ident := "", ref
	defer func() { s.log("get_issue_status", c, wsSlug, ident, started, &err) }()
	w, is, err := s.own(ctx, c, ref)
	if err != nil {
		return out, err
	}
	wsSlug, ident = w.Slug, is.Identifier
	cl := s.client(c.Token)
	names, err := s.names(ctx, cl, w.ID)
	if err != nil {
		return out, err
	}
	out = IssueStatus{Issue: is.Identifier, URL: s.issueURL(w, is.Identifier), Workspace: w.Slug, Title: is.Title,
		State: state(is), Status: statusLabel(is), Updated: is.UpdatedAt, Comments: []CommentView{}}
	if is.ProjectID != nil {
		if ps, err := cl.Projects(ctx, w.ID); err == nil {
			for _, p := range ps {
				if p.ID == *is.ProjectID {
					out.Project = p.Title
				}
			}
		}
	}
	if tp, err := s.typeProperty(ctx, cl, w.ID); err == nil && tp != nil {
		if raw, ok := is.Properties[tp.ID]; ok {
			var optID string
			if json.Unmarshal(raw, &optID) == nil {
				for _, o := range tp.Config.Options {
					if o.ID == optID {
						out.Type = o.Name
					}
				}
			}
		}
	}
	if is.AssigneeID != nil {
		out.AssignedTo = names[*is.AssigneeID]
	}
	cs, err := cl.Comments(ctx, w.ID, is.ID)
	if err != nil {
		return out, fromMultica(err, "read comments")
	}
	var kept []multica.Comment
	for _, cm := range cs {
		if cm.Type == "" || cm.Type == "comment" {
			kept = append(kept, cm)
		}
	}
	if len(kept) > maxComments {
		kept = kept[len(kept)-maxComments:]
	}
	for _, cm := range kept {
		kind, author := cm.AuthorType, names[cm.AuthorID]
		if cm.AuthorType == "member" && cm.AuthorID == c.User.ID {
			kind, author = "you", c.User.Name
		}
		out.Comments = append(out.Comments, CommentView{Author: author, Kind: kind, At: cm.CreatedAt, Text: clip(cm.Content, 4000)})
	}
	if n := len(out.Comments); n > 0 {
		last := out.Comments[n-1]
		out.NeedsReply = out.State == "open" && last.Kind != "you"
		if out.State == "rejected" && last.Kind != "you" {
			out.Rejection = last.Text
		}
	}
	return out, nil
}

func statusLabel(is multica.Issue) string {
	if is.StatusName != "" {
		return is.StatusName
	}
	return is.Status
}

// names maps agent and member ids to display names in a workspace.
func (s *Service) names(ctx context.Context, cl *multica.Client, ws string) (map[string]string, error) {
	m := map[string]string{}
	agents, err := cl.Agents(ctx, ws)
	if err != nil {
		return nil, fromMultica(err, "list agents")
	}
	for _, a := range agents {
		m[a.ID] = a.Name
	}
	members, err := cl.Members(ctx, ws)
	if err != nil {
		return nil, fromMultica(err, "list members")
	}
	for _, mb := range members {
		m[mb.UserID] = mb.Name
	}
	return m, nil
}

// ReplyResult is what reply_to_issue returns.
type ReplyResult struct {
	Issue   string `json:"issue"`
	Posted  bool   `json:"posted"`
	Notify  string `json:"notified,omitempty"`
	Comment string `json:"comment_id"`
}

// Reply is the reply_to_issue tool.
func (s *Service) Reply(ctx context.Context, c Caller, ref, text string) (out ReplyResult, err error) {
	started := time.Now()
	wsSlug, ident := "", ref
	defer func() { s.log("reply_to_issue", c, wsSlug, ident, started, &err) }()
	text = strings.TrimSpace(text)
	if text == "" {
		return out, newErr(CodeBadInput, "text is empty.", "Write the answer, then call reply_to_issue again.")
	}
	if utf8.RuneCountInString(text) > s.cfg.MaxComment {
		return out, newErr(CodeBadInput, fmt.Sprintf("text is longer than %d characters.", s.cfg.MaxComment), "Shorten the reply.")
	}
	w, is, err := s.own(ctx, c, ref)
	if err != nil {
		return out, err
	}
	wsSlug, ident = w.Slug, is.Identifier
	if st := state(is); st != "open" {
		return out, newErr(CodeClosed, fmt.Sprintf("%s is %s; replies go to open issues only.", is.Identifier, st),
			"If more work is needed, call submit_issue with a new request that mentions "+is.Identifier+".")
	}
	cl := s.client(c.Token)
	content := text
	if is.AssigneeType != nil && *is.AssigneeType == "agent" && is.AssigneeID != nil {
		names, err := s.names(ctx, cl, w.ID)
		if err != nil {
			return out, err
		}
		if n := names[*is.AssigneeID]; n != "" {
			// A mention wakes the assigned agent, as a person's reply would.
			content = fmt.Sprintf("[@%s](mention://agent/%s) %s", n, *is.AssigneeID, text)
			out.Notify = n
		}
	}
	cm, err := cl.AddComment(ctx, w.ID, is.ID, content)
	if err != nil {
		return out, fromMultica(err, "post the reply")
	}
	return ReplyResult{Issue: is.Identifier, Posted: true, Notify: out.Notify, Comment: cm.ID}, nil
}

// IssueSummary is one row of list_my_issues.
type IssueSummary struct {
	Issue     string `json:"issue"`
	Workspace string `json:"workspace"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Status    string `json:"status"`
	Updated   string `json:"updated_at"`
	URL       string `json:"url,omitempty"`
}

// ListInput is the list_my_issues arguments.
type ListInput struct {
	State     string `json:"state,omitempty" jsonschema:"open (default), closed (done or rejected), or all"`
	Workspace string `json:"workspace,omitempty" jsonschema:"only this workspace slug; empty for all"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most issues to return (default and maximum: the server's max_list)"`
}

// ListMine is the list_my_issues tool.
func (s *Service) ListMine(ctx context.Context, c Caller, in ListInput) (out []IssueSummary, err error) {
	started := time.Now()
	defer func() { s.log("list_my_issues", c, in.Workspace, "", started, &err) }()
	st := strings.ToLower(strings.TrimSpace(in.State))
	if st == "" {
		st = "open"
	}
	if st != "open" && st != "closed" && st != "all" {
		return nil, newErr(CodeBadInput, "state must be open, closed or all.", "Call list_my_issues with state: \"open\".")
	}
	limit := in.Limit
	if limit <= 0 || limit > s.cfg.MaxList {
		limit = s.cfg.MaxList
	}
	var ws []multica.Workspace
	if in.Workspace != "" {
		w, _, err := s.pickWorkspace(ctx, c, in.Workspace)
		if err != nil {
			return nil, err
		}
		ws = []multica.Workspace{w}
	} else if ws, err = s.allowed(ctx, c); err != nil {
		return nil, err
	}
	cl := s.client(c.Token)
	out = []IssueSummary{}
	for _, w := range ws {
		issues, err := cl.ListIssues(ctx, w.ID, multica.IssueFilter{CreatorID: c.User.ID, OpenOnly: st == "open",
			Metadata: map[string]any{MetaSource: s.cfg.Source}, Limit: 200})
		if err != nil {
			return nil, fromMultica(err, "list issues")
		}
		for _, is := range issues {
			if is.CreatorID != c.User.ID || fmt.Sprint(is.Metadata[MetaSource]) != s.cfg.Source {
				continue
			}
			sv := state(is)
			if (st == "open" && sv != "open") || (st == "closed" && sv == "open") {
				continue
			}
			out = append(out, IssueSummary{Issue: is.Identifier, Workspace: w.Slug, Title: is.Title, State: sv,
				Status: statusLabel(is), Updated: is.UpdatedAt, URL: s.issueURL(w, is.Identifier)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- helpers ----------

func (s *Service) issueURL(w multica.Workspace, ident string) string {
	if s.cfg.AppURL == "" || ident == "" {
		return ""
	}
	return s.cfg.AppURL + "/" + w.Slug + "/issues/" + ident
}

func (s *Service) log(tool string, c Caller, ws, issue string, started time.Time, err *error) {
	e := audit.Entry{Tool: tool, Account: c.User.ID, Name: c.User.Name, Workspace: ws, Issue: issue, OK: *err == nil}
	if *err != nil {
		var ie *Error
		if errors.As(*err, &ie) {
			e.Code = ie.Code
		} else {
			e.Code = "internal"
		}
	}
	s.audit.Log(e, started)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
