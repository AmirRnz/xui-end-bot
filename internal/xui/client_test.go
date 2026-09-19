package xui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"xui-end-bot/internal/config"
)

func TestAddClientTimeoutRepairsPartialInboundAttachmentsWithoutRetry(t *testing.T) {
	var mu sync.Mutex
	remote := XUIClientInfo{
		Email:      "partial@example.com",
		UUID:       "client-uuid",
		SubID:      "sub-partial",
		Enable:     true,
		ExpiryTime: -3600000,
		LimitIP:    2,
		TotalGB:    2147483648,
		InboundIDs: []int{11},
	}
	addCalls := 0
	attachCalls := 0
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/add":
			mu.Lock()
			addCalls++
			mu.Unlock()
			// The create has committed, but the response is lost.
			time.Sleep(100 * time.Millisecond)
		case "/panel/api/clients/get/partial@example.com":
			mu.Lock()
			getCalls++
			current := remote
			current.InboundIDs = append([]int(nil), remote.InboundIDs...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": current})
		case "/panel/api/clients/partial@example.com/attach":
			mu.Lock()
			attachCalls++
			remote.InboundIDs = append(remote.InboundIDs, 22)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.httpClient.Timeout = 10 * time.Millisecond
	result := client.AddClientResult(AddClientRequest{Client: ClientConfig{
		ID: "client-uuid", Email: "partial@example.com", SubID: "sub-partial", Enable: true,
		ExpiryTime: -3600000, LimitIP: 2, TotalGB: 2147483648,
	}, InboundIDs: []int{11, 22}})
	if result.Outcome != WriteSucceeded || result.Err != nil {
		t.Fatalf("expected repaired success, got %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if addCalls != 1 {
		t.Fatalf("expected exactly one create call, got %d", addCalls)
	}
	if attachCalls != 1 {
		t.Fatalf("expected exactly one missing-inbound repair, got %d", attachCalls)
	}
	if getCalls < 2 {
		t.Fatalf("expected readback before and after repair, got %d reads", getCalls)
	}
}

func TestAddClientTimeoutWithInconclusiveVerificationReturnsUnknownWithoutRetry(t *testing.T) {
	var mu sync.Mutex
	addCalls := 0
	remote := XUIClientInfo{Email: "unknown@example.com", UUID: "unknown-uuid", SubID: "sub-unknown", Enable: true, ExpiryTime: -3600000, LimitIP: 1, TotalGB: 0, InboundIDs: []int{11}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/add":
			mu.Lock()
			addCalls++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		case "/panel/api/clients/get/unknown@example.com":
			mu.Lock()
			current := remote
			current.InboundIDs = append([]int(nil), remote.InboundIDs...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": current})
		case "/panel/api/clients/unknown@example.com/attach":
			// The repair write is also ambiguous and does not become visible.
			time.Sleep(100 * time.Millisecond)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.httpClient.Timeout = 10 * time.Millisecond
	result := client.AddClientResult(AddClientRequest{Client: ClientConfig{
		ID: "unknown-uuid", Email: "unknown@example.com", SubID: "sub-unknown", Enable: true,
		ExpiryTime: -3600000, LimitIP: 1, TotalGB: 0,
	}, InboundIDs: []int{11, 22}})
	if result.Outcome != WriteUnknown || !IsUnknownOutcome(result.Err) {
		t.Fatalf("expected unknown outcome, got %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if addCalls != 1 {
		t.Fatalf("expected exactly one create call, got %d", addCalls)
	}
}

func TestGetSubscriptionLinksUsesPublicSubscriptionBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel/api/clients/subLinks/sub123" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     []string{"http://panel.internal:18104/sub/sub123"},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{
		BaseURL:             server.URL,
		SubscriptionBaseURL: "https://subs.example.com:9443",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub123")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0] != "https://subs.example.com:9443/sub/sub123" {
		t.Fatalf("unexpected subscription link: %s", links[0])
	}
}

func TestGetSubscriptionLinksBuildsFallbackWithSubscriptionPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     []string{},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{
		BaseURL:             server.URL,
		SubscriptionBaseURL: "subs.example.com",
		SubscriptionPath:    "/custom-sub/",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub456")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0] != "https://subs.example.com/custom-sub/sub456" {
		t.Fatalf("unexpected fallback link: %s", links[0])
	}
}

func TestGetSubscriptionLinksKeepsLinksWhenNoPublicBaseURLIsSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     "http://panel.internal/sub/sub123\nhttp://panel.internal/json/sub123",
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub123")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0] != "http://panel.internal/sub/sub123" || links[1] != "http://panel.internal/json/sub123" {
		t.Fatalf("unexpected links: %#v", links)
	}
}

func TestBulkAttachDetach(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/panel/api/clients/bulkAttach" && r.URL.Path != "/panel/api/clients/bulkDetach" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var req BulkAttachRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if len(req.Emails) != 2 || req.Emails[0] != "alice" || req.Emails[1] != "bob" {
			t.Fatalf("unexpected emails: %v", req.Emails)
		}
		if len(req.InboundIDs) != 2 || req.InboundIDs[0] != 7 || req.InboundIDs[1] != 9 {
			t.Fatalf("unexpected inbound IDs: %v", req.InboundIDs)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"msg":     "OK",
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := BulkAttachRequest{
		Emails:     []string{"alice", "bob"},
		InboundIDs: []int{7, 9},
	}

	if err := client.BulkAttach(req); err != nil {
		t.Fatalf("BulkAttach failed: %v", err)
	}

	if err := client.BulkDetach(req); err != nil {
		t.Fatalf("BulkDetach failed: %v", err)
	}
}

func TestBulkCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/panel/api/clients/bulkCreate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var req []BulkCreateItem
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if len(req) != 2 {
			t.Fatalf("expected 2 items, got %d", len(req))
		}
		if req[0].Client.Email != "alice" || req[1].Client.Email != "bob" {
			t.Fatalf("unexpected emails in bulk request")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": map[string]any{
				"created": 2,
				"skipped": []any{},
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	items := []BulkCreateItem{
		{Client: ClientConfig{Email: "alice"}, InboundIDs: []int{1}},
		{Client: ClientConfig{Email: "bob"}, InboundIDs: []int{2}},
	}

	resp, err := client.BulkCreate(items)
	if err != nil {
		t.Fatalf("BulkCreate failed: %v", err)
	}
	if resp.Created != 2 {
		t.Fatalf("expected 2 created, got %d", resp.Created)
	}
}
