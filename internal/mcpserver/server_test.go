package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vantt/multica-intake/internal/config"
	"github.com/vantt/multica-intake/internal/intake"
)

// fakeAPI answers /api/me for one token and lists no workspaces.
func fakeAPI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, `{"error":"bad token"}`, 401)
			return
		}
		switch r.URL.Path {
		case "/api/me":
			w.Write([]byte(`{"id":"u1","name":"Bot"}`))
		case "/api/workspaces":
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type bearer struct {
	tok string
	rt  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.rt.RoundTrip(r)
}

func TestHTTPRequiresAMulticaTokenAndServesTools(t *testing.T) {
	api := fakeAPI(t)
	c := config.Default()
	c.MulticaURL, c.IntakeAgent = api.URL, "PM"
	h := httptest.NewServer(Handler(intake.New(c, nil), 1<<20))
	defer h.Close()

	resp, err := http.Post(h.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("no token: %v %v", resp.StatusCode, err)
	}
	req, _ := http.NewRequest("POST", h.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	resp, _ = http.Get(h.URL + "/healthz")
	var hz map[string]any
	json.NewDecoder(resp.Body).Decode(&hz)
	if hz["ok"] != true {
		t.Fatalf("healthz %v", hz)
	}

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{"good", http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		got[tl.Name] = tl
	}
	for _, n := range []string{"list_workspaces", "submit_issue", "get_issue_status", "reply_to_issue", "list_my_issues"} {
		if got[n] == nil {
			t.Fatalf("missing tool %s", n)
		}
	}
	if len(got) != 5 {
		t.Fatalf("expected exactly 5 tools, got %d", len(got))
	}
	for _, n := range []string{"list_workspaces", "get_issue_status", "list_my_issues"} {
		if !got[n].Annotations.ReadOnlyHint {
			t.Errorf("%s must be read-only", n)
		}
	}
	for _, n := range []string{"submit_issue", "reply_to_issue"} {
		if got[n].Annotations.ReadOnlyHint {
			t.Errorf("%s must not be read-only", n)
		}
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_workspaces", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_workspaces: %v %+v", err, res)
	}
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "submit_issue", Arguments: map[string]any{"workspace": "x", "title": "t", "request": "r"}})
	if err != nil || !res.IsError {
		t.Fatalf("want a tool error: %v %+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var body struct {
		Error intake.Error `json:"error"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || body.Error.Code != intake.CodeNoWorkspace || body.Error.Fix == "" {
		t.Fatalf("error body %q", text)
	}
}
