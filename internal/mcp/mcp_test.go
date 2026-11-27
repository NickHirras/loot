package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nickhirras/loot/internal/mcp"
)

type stubDoer struct {
	lastMethod string
	lastPath   string
	lastQuery  url.Values
	lastBody   any
	resp       json.RawMessage
	err        error
}

func (s *stubDoer) DoJSON(_ context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	s.lastMethod = method
	s.lastPath = path
	s.lastQuery = query
	s.lastBody = body
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func runRPC(t *testing.T, srv *mcp.Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode reply %q: %v", line, err)
		}
		replies = append(replies, m)
	}
	return replies
}

func TestMCPHandshakeAndReadOnlyTools(t *testing.T) {
	stub := &stubDoer{resp: json.RawMessage(`{"display_currency":"USD","stats":{"total_xp":500}}`)}
	srv := mcp.New(mcp.Options{Client: stub, Version: "1.2.3"})

	replies := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"loot_report","arguments":{"range":"90d","app":"Nistis"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	)

	// Notification produces no reply, so 4 replies total.
	if len(replies) != 4 {
		t.Fatalf("got %d replies, want 4: %+v", len(replies), replies)
	}

	initRes := replies[0]["result"].(map[string]any)
	if initRes["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v", initRes["protocolVersion"])
	}

	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	hasReport := false
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if name == "loot_report" {
			hasReport = true
		}
		if name == "loot_chest_open" {
			t.Fatal("read-only MCP server should not expose write tool loot_chest_open")
		}
	}
	if !hasReport {
		t.Fatal("tools/list missing loot_report")
	}

	if stub.lastMethod != "GET" || stub.lastPath != "/api/report" {
		t.Errorf("called %s %s, want GET /api/report", stub.lastMethod, stub.lastPath)
	}
	if stub.lastQuery.Get("range") != "90d" || stub.lastQuery.Get("app") != "Nistis" {
		t.Errorf("query = %v", stub.lastQuery)
	}

	callRes := replies[2]["result"].(map[string]any)
	content := callRes["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), `"total_xp": 500`) {
		t.Errorf("unexpected tool output: %v", content["text"])
	}
}

func TestMCPAllowWritesAndErrors(t *testing.T) {
	stub := &stubDoer{resp: json.RawMessage(`{"opened":"2026-11-27","count":2}`)}
	srv := mcp.New(mcp.Options{Client: stub, Version: "dev", AllowWrites: true})

	replies := runRPC(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"loot_chest_open","arguments":{"all":true}}}`,
	)
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if stub.lastMethod != "POST" || stub.lastPath != "/api/chest/open" {
		t.Errorf("called %s %s, want POST /api/chest/open", stub.lastMethod, stub.lastPath)
	}

	// Upstream error surfaces as MCP tool error (isError: true).
	stub.err = errors.New("401 Unauthorized")
	replies = runRPC(t, srv,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"loot_stats","arguments":{}}}`,
	)
	res := replies[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError=true, got %+v", res)
	}
}
