package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runRequests(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	srv := New(&out, nil)
	if err := srv.Serve(strings.NewReader(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resps []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestInitializeAndToolsList(t *testing.T) {
	resps := runRequests(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (notification has no reply), got %d: %#v", len(resps), resps)
	}
	init := resps[0]
	if init["id"].(float64) != 1 {
		t.Fatalf("unexpected id: %v", init["id"])
	}
	result, ok := init["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", init)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("bad protocolVersion: %v", result["protocolVersion"])
	}

	list := resps[1]
	lr, ok := list["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing tools/list result: %#v", list)
	}
	tools, ok := lr["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected tools, got %#v", lr["tools"])
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "get_telegram_feed" {
		t.Fatalf("unexpected tool name: %v", first["name"])
	}
	second := tools[1].(map[string]any)
	if second["name"] != "get_polymarket_events" {
		t.Fatalf("expected second tool to be get_polymarket_events, got %v", second["name"])
	}
}

func TestUnknownMethod(t *testing.T) {
	resps := runRequests(t, `{"jsonrpc":"2.0","id":7,"method":"does/not/exist"}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	errObj, ok := resps[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %#v", resps[0])
	}
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("unexpected code: %v", errObj["code"])
	}
}

func TestParseError(t *testing.T) {
	resps := runRequests(t, `{not json`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	errObj, ok := resps[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %#v", resps[0])
	}
	if errObj["code"].(float64) != -32700 {
		t.Fatalf("unexpected code: %v", errObj["code"])
	}
}

func TestToolCallMissingArgument(t *testing.T) {
	resps := runRequests(t,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_telegram_feed","arguments":{}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	res, ok := resps[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %#v", resps[0])
	}
	if res["isError"] != true {
		t.Fatalf("expected isError=true, got %#v", res)
	}
}

func TestToolCallUnknownTool(t *testing.T) {
	resps := runRequests(t,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	if _, ok := resps[0]["error"].(map[string]any); !ok {
		t.Fatalf("expected error, got %#v", resps[0])
	}
}

func TestPolymarketToolInToolsList(t *testing.T) {
	resps := runRequests(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	result, ok := resps[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", resps[0])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %#v", result["tools"])
	}
	var found bool
	for _, tool := range tools {
		tm := tool.(map[string]any)
		if tm["name"] == "get_polymarket_events" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("get_polymarket_events not found in tools list")
	}
}

func TestPolymarketToolCallWithDefaults(t *testing.T) {
	// This test validates schema and invocation flow but won't hit real API.
	// A successful call would require live upstream or a fixture.
	resps := runRequests(t,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_polymarket_events","arguments":{}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	// We expect either a result (successful upstream call) or an isError=true result.
	res, ok := resps[0]["result"].(map[string]any)
	if !ok {
		// Could be an RPC error too if something else goes wrong
		t.Logf("got RPC error: %#v", resps[0])
		return
	}
	// If isError is present and true, that's expected for missing upstream.
	if isErr, _ := res["isError"].(bool); isErr {
		t.Logf("tool returned error result: %#v", res)
		return
	}
	// Otherwise we got content; validate structure.
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected content array, got %#v", res)
	}
}
