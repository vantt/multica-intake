package intake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vantt/multica-intake/internal/config"
)

// fakeMultica is an in-memory Multica API with the calls multica-intake uses.
type fakeMultica struct {
	mu        sync.Mutex
	users     map[string]map[string]any // token → user
	members   map[string][]string       // user id → workspace ids
	ws        []map[string]any
	agents    map[string][]map[string]any
	projects  map[string][]map[string]any
	props     map[string][]map[string]any
	issues    []map[string]any
	comments  map[string][]map[string]any
	created   []map[string]any // POST /api/issues bodies, for asserting what is sent
	n         int
	agentHdrs int // requests that carried X-Agent-ID (must stay 0)
}

func newFake(t *testing.T) (*fakeMultica, *httptest.Server) {
	f := &fakeMultica{
		users: map[string]map[string]any{
			"tok-maya": {"id": "u-maya", "name": "Maya (MausBot)"},
			"tok-ada":  {"id": "u-ada", "name": "Ada (MausBot)"},
			"tok-own":  {"id": "u-own", "name": "Owner"},
		},
		members: map[string][]string{"u-maya": {"w1", "w2"}, "u-ada": {"w1"}, "u-own": {"w1", "w2"}},
		ws: []map[string]any{
			{"id": "w1", "name": "Acme", "slug": "acme", "issue_prefix": "ACME", "description": "a test workspace"},
			{"id": "w2", "name": "Other", "slug": "other", "issue_prefix": "OTH"},
		},
		agents: map[string][]map[string]any{
			"w1": {{"id": "a-pm", "name": "Phương", "archived_at": nil}},
			"w2": {{"id": "a-x", "name": "Someone", "archived_at": nil}},
		},
		projects: map[string][]map[string]any{"w1": {{"id": "p1", "title": "Website", "description": "the site", "status": "planned"}}},
		props: map[string][]map[string]any{"w1": {{"id": "prop-type", "name": "Type", "type": "select", "archived": false,
			"config": map[string]any{"options": []map[string]any{{"id": "o1", "name": "Tech · Feature"}, {"id": "o2", "name": "Marketing · Copy"}}}}}},
		comments: map[string][]map[string]any{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMultica) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Agent-ID") != "" || r.Header.Get("X-Task-ID") != "" {
		f.agentHdrs++
	}
	if r.URL.Path == "/health" {
		w.WriteHeader(200)
		return
	}
	u, ok := f.users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, `{"error":"invalid token"}`, 401)
		return
	}
	uid := u["id"].(string)
	ws := r.Header.Get("X-Workspace-ID")
	member := func() bool {
		for _, m := range f.members[uid] {
			if m == ws {
				return true
			}
		}
		return false
	}
	j := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case p == "/api/me":
		j(200, u)
	case p == "/api/workspaces":
		var out []map[string]any
		for _, x := range f.ws {
			for _, m := range f.members[uid] {
				if m == x["id"] {
					out = append(out, x)
				}
			}
		}
		j(200, out)
	case !member():
		j(403, map[string]string{"error": "not a member"})
	case p == "/api/agents":
		j(200, f.agents[ws])
	case p == "/api/projects":
		j(200, map[string]any{"projects": f.projects[ws]})
	case p == "/api/properties":
		j(200, map[string]any{"properties": f.props[ws]})
	case strings.HasSuffix(p, "/members"):
		var out []map[string]any
		for tok, uu := range f.users {
			_ = tok
			out = append(out, map[string]any{"user_id": uu["id"], "name": uu["name"]})
		}
		j(200, out)
	case p == "/api/issues" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		for _, is := range f.issues {
			if is["workspace_id"] == ws && is["title"] == body["title"] && is["status_category"] != "done" && is["status_category"] != "cancelled" {
				j(409, map[string]any{"code": "active_duplicate_issue", "error": "duplicate of " + is["identifier"].(string), "issue": is})
				return
			}
		}
		f.n++
		prefix := map[string]string{"w1": "ACME", "w2": "OTH"}[ws]
		is := map[string]any{"id": fmt.Sprintf("i%d", f.n), "workspace_id": ws, "identifier": fmt.Sprintf("%s-%d", prefix, f.n),
			"title": body["title"], "description": body["description"], "status": body["status"], "status_category": body["status"],
			"assignee_type": body["assignee_type"], "assignee_id": body["assignee_id"], "creator_type": "member", "creator_id": uid,
			"project_id": nil, "metadata": map[string]any{}, "properties": map[string]any{}, "updated_at": fmt.Sprintf("2026-01-01T00:00:%02dZ", f.n)}
		f.issues = append(f.issues, is)
		j(201, is)
	case p == "/api/issues" && r.Method == http.MethodGet:
		q := r.URL.Query()
		var meta map[string]any
		if m := q.Get("metadata"); m != "" {
			_ = json.Unmarshal([]byte(m), &meta)
		}
		out := []map[string]any{}
		for _, is := range f.issues {
			if is["workspace_id"] != ws || (q.Get("creator_id") != "" && is["creator_id"] != q.Get("creator_id")) {
				continue
			}
			if q.Get("open_only") == "true" && (is["status_category"] == "done" || is["status_category"] == "cancelled") {
				continue
			}
			match := true
			for k, v := range meta {
				if is["metadata"].(map[string]any)[k] != v {
					match = false
				}
			}
			if match {
				out = append(out, is)
			}
		}
		j(200, map[string]any{"issues": out})
	case strings.HasPrefix(p, "/api/issues/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/issues/"), "/")
		var is map[string]any
		for _, x := range f.issues {
			if (x["id"] == parts[0] || x["identifier"] == parts[0]) && x["workspace_id"] == ws {
				is = x
			}
		}
		if is == nil {
			j(404, map[string]string{"error": "issue not found"})
			return
		}
		switch {
		case len(parts) == 1:
			j(200, is)
		case parts[1] == "metadata" && r.Method == http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			is["metadata"].(map[string]any)[parts[2]] = body["value"]
			j(200, map[string]any{"ok": true})
		case parts[1] == "comments" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			c := map[string]any{"id": fmt.Sprintf("c%d", len(f.comments[is["id"].(string)])+1), "author_type": "member", "author_id": uid,
				"content": body["content"], "type": "comment", "created_at": "2026-01-02T00:00:00Z"}
			f.comments[is["id"].(string)] = append(f.comments[is["id"].(string)], c)
			j(201, c)
		case parts[1] == "comments":
			j(200, append([]map[string]any{}, f.comments[is["id"].(string)]...))
		default:
			j(404, map[string]string{"error": "no route"})
		}
	default:
		j(404, map[string]string{"error": "no route " + p})
	}
}

func testService(t *testing.T, url string, mut func(*config.Config)) *Service {
	c := config.Default()
	c.MulticaURL = url
	c.IntakeAgent = "Phương"
	c.Source = "mausbot"
	c.AppURL = "http://app"
	if mut != nil {
		mut(&c)
	}
	if p := c.Validate(); len(p) > 0 {
		t.Fatalf("config: %v", p)
	}
	return New(c, nil)
}
