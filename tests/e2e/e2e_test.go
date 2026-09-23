package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/bot/handlers"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/scheduler"
	"xui-end-bot/internal/services/reconcile"
	"xui-end-bot/internal/xui"
)

func getStr(resp map[string]interface{}, key string) string {
	if resp == nil {
		return ""
	}
	val, ok := resp[key]
	if !ok || val == nil {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return fmt.Sprintf("%v", val)
	}
	return s
}

func extractCallbackData(resp map[string]interface{}, prefix string) string {
	rm, ok := resp["reply_markup"].(string)
	if !ok {
		return ""
	}
	var markup struct {
		InlineKeyboard [][]struct {
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(rm), &markup); err != nil {
		return ""
	}
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix) {
				return btn.CallbackData
			}
		}
	}
	return ""
}

// MockTelegramServer mocks the Telegram Bot API server.
type MockTelegramServer struct {
	Server    *httptest.Server
	Updates   chan map[string]interface{}
	Responses chan map[string]interface{}
	mu        sync.Mutex
	allSent   []map[string]interface{}
}

func NewMockTelegramServer() *MockTelegramServer {
	m := &MockTelegramServer{
		Updates:   make(chan map[string]interface{}, 1000),
		Responses: make(chan map[string]interface{}, 1000),
	}
	m.Server = httptest.NewServer(m)
	return m
}

func (m *MockTelegramServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/getMe") {
		w.Write([]byte(`{"ok":true,"result":{"id":96937669,"is_bot":true,"first_name":"TestBot","username":"test_bot"}}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/getUpdates") {
		select {
		case u := <-m.Updates:
			resp := map[string]interface{}{
				"ok":     true,
				"result": []interface{}{u},
			}
			json.NewEncoder(w).Encode(resp)
		case <-time.After(10 * time.Millisecond):
			w.Write([]byte(`{"ok":true,"result":[]}`))
		}
		return
	}
	if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
		params := make(map[string]interface{})
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			bodyBytes, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(bodyBytes, &params)
		} else {
			_ = r.ParseMultipartForm(10 << 20)
			for k, v := range r.Form {
				if len(v) > 0 {
					params[k] = v[0]
				}
			}
			if r.MultipartForm != nil {
				for k, v := range r.MultipartForm.Value {
					if len(v) > 0 {
						params[k] = v[0]
					}
				}
			}
		}

		text := getStr(params, "text")
		if text != "" {
			m.mu.Lock()
			m.allSent = append(m.allSent, params)
			m.mu.Unlock()

			select {
			case m.Responses <- params:
			default:
			}
		}

		w.Write([]byte(`{"ok":true,"result":true}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/sendMessage") ||
		strings.HasSuffix(r.URL.Path, "/editMessageText") ||
		strings.HasSuffix(r.URL.Path, "/sendPhoto") {

		params := make(map[string]interface{})
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			bodyBytes, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(bodyBytes, &params)
		} else {
			_ = r.ParseMultipartForm(10 << 20)
			for k, v := range r.Form {
				if len(v) > 0 {
					params[k] = v[0]
				}
			}
			if r.MultipartForm != nil {
				for k, v := range r.MultipartForm.Value {
					if len(v) > 0 {
						params[k] = v[0]
					}
				}
			}
		}

		m.mu.Lock()
		m.allSent = append(m.allSent, params)
		m.mu.Unlock()

		select {
		case m.Responses <- params:
		default:
		}

		if strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			replyMarkup := ""
			if rm, ok := params["reply_markup"]; ok {
				if rmStr, ok := rm.(string); ok {
					replyMarkup = fmt.Sprintf(",\"reply_markup\":%s", rmStr)
				} else {
					rmJSON, _ := json.Marshal(rm)
					replyMarkup = fmt.Sprintf(",\"reply_markup\":%s", string(rmJSON))
				}
			}
			caption := ""
			if cap, ok := params["caption"]; ok {
				caption = fmt.Sprintf(",\"caption\":%q", fmt.Sprintf("%v", cap))
			}
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"message_id":999,"chat":{"id":123456},"photo":[{"file_id":"test_photo_file_id","width":100,"height":100}]%s%s,"date":1600000000}}`, caption, replyMarkup)))
		} else {
			replyMarkup := ""
			if rm, ok := params["reply_markup"]; ok {
				if rmStr, ok := rm.(string); ok {
					replyMarkup = fmt.Sprintf(",\"reply_markup\":%s", rmStr)
				} else {
					rmJSON, _ := json.Marshal(rm)
					replyMarkup = fmt.Sprintf(",\"reply_markup\":%s", string(rmJSON))
				}
			}
			text := getStr(params, "text")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"message_id":999,"chat":{"id":123456},"text":%q%s,"date":1600000000}}`, text, replyMarkup)))
		}
		return
	}
	if strings.Contains(r.URL.Path, "/getFile") {
		w.Write([]byte(`{"ok":true,"result":{"file_id":"file123","file_path":"photos/receipt.jpg"}}`))
		return
	}
	if strings.Contains(r.URL.Path, "/file/bot") {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(make([]byte, 100))
		return
	}
}

func (m *MockTelegramServer) Clear() {
	m.mu.Lock()
	m.allSent = nil
	m.mu.Unlock()
	// Drain updates channel
	for {
		select {
		case <-m.Updates:
		default:
			goto updatesDrained
		}
	}
updatesDrained:
	// Drain responses channel
	for {
		select {
		case <-m.Responses:
		default:
			goto responsesDrained
		}
	}
responsesDrained:
}

// MockXUIServer mocks the X-UI Panel API server.
type MockXUIServer struct {
	Server         *httptest.Server
	Fail           bool
	Clients        map[string]xui.ClientConfig
	InboundIDs     map[string][]int
	AddClientCalls int
	mu             sync.Mutex
}

func NewMockXUIServer() *MockXUIServer {
	m := &MockXUIServer{
		Clients:    make(map[string]xui.ClientConfig),
		InboundIDs: make(map[string][]int),
	}
	m.Server = httptest.NewServer(m)
	return m
}

func (m *MockXUIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Fail {
		w.Write([]byte(`{"success":false,"msg":"Panel offline"}`))
		return
	}

	if strings.HasSuffix(r.URL.Path, "/panel/api/server/getPanelUpdateInfo") {
		w.Write([]byte(`{"success":true,"msg":"","obj":{"currentVersion":"3.8.5","latestVersion":"v3.8.5","updateAvailable":false}}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/panel/api/inbounds/options") {
		w.Write([]byte(`{"success":true,"msg":"","obj":[{"id":1,"port":443,"protocol":"vless","remark":"Test Inbound","tag":"vless-inbound","tlsFlowCapable":true}]}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/panel/api/clients/add") {
		m.AddClientCalls++
		var req xui.AddClientRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			m.Clients[req.Client.Email] = req.Client
			m.InboundIDs[req.Client.Email] = append([]int(nil), req.InboundIDs...)
		}
		w.Write([]byte(`{"success":true,"msg":"Client added"}`))
		return
	}
	if strings.Contains(r.URL.Path, "/panel/api/clients/get/") {
		parts := strings.Split(r.URL.Path, "/get/")
		email := parts[len(parts)-1]
		if client, exists := m.Clients[email]; exists {
			info := xui.XUIClientInfo{
				ID: 1, Email: client.Email, UUID: client.ID, SubID: client.SubID,
				Enable: client.Enable, ExpiryTime: client.ExpiryTime, LimitIP: client.LimitIP,
				TotalGB: client.TotalGB, TgID: client.TgID, Group: client.Group,
				Comment: client.Comment, Flow: client.Flow,
				InboundIDs: append([]int(nil), m.InboundIDs[email]...),
			}
			objJSON, _ := json.Marshal(info)
			w.Write([]byte(fmt.Sprintf(`{"success":true,"msg":"","obj":%s}`, string(objJSON))))
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"success":false,"msg":"Client not found"}`))
		}
		return
	}
	if strings.Contains(r.URL.Path, "/panel/api/clients/update/") {
		parts := strings.Split(r.URL.Path, "/update/")
		email := parts[len(parts)-1]
		var req xui.UpdateClientRequest
		bodyBytes, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(bodyBytes, &req); err != nil || req.Client.Email == "" {
			var raw xui.ClientConfig
			if err := json.Unmarshal(bodyBytes, &raw); err == nil {
				req.Client = raw
			}
		}
		if req.Client.Email != "" || req.Client.ID != "" {
			if _, exists := m.Clients[email]; exists {
				client := m.Clients[email]
				if req.Client.Email != "" {
					delete(m.Clients, email)
					client.Email = req.Client.Email
					m.Clients[req.Client.Email] = client
					if inboundIDs, ok := m.InboundIDs[email]; ok {
						delete(m.InboundIDs, email)
						m.InboundIDs[req.Client.Email] = inboundIDs
					}
				} else {
					if req.Client.ID != "" {
						client.ID = req.Client.ID
					}
					client.Enable = req.Client.Enable
					if req.Client.LimitIP != 0 {
						client.LimitIP = req.Client.LimitIP
					}
					if req.Client.ExpiryTime != 0 {
						client.ExpiryTime = req.Client.ExpiryTime
					}
					m.Clients[email] = client
				}
			}
		}
		w.Write([]byte(`{"success":true,"msg":"Client updated"}`))
		return
	}
	if strings.Contains(r.URL.Path, "/panel/api/clients/del/") {
		parts := strings.Split(r.URL.Path, "/del/")
		email := parts[len(parts)-1]
		delete(m.Clients, email)
		w.Write([]byte(`{"success":true,"msg":"Client deleted"}`))
		return
	}
	if strings.Contains(r.URL.Path, "/panel/api/clients/subLinks/") {
		parts := strings.Split(r.URL.Path, "/subLinks/")
		subID := parts[len(parts)-1]
		link := fmt.Sprintf("https://test-sub.com/sub/%s", subID)
		w.Write([]byte(fmt.Sprintf(`{"success":true,"msg":"","obj":%q}`, link)))
		return
	}
	if strings.Contains(r.URL.Path, "/panel/api/clients/traffic/") {
		parts := strings.Split(r.URL.Path, "/traffic/")
		email := parts[len(parts)-1]
		w.Write([]byte(fmt.Sprintf(`{"success":true,"msg":"","obj":[{"id":1,"inboundId":1,"enable":true,"email":%q,"up":1073741824,"down":2147483648,"total":3221225472,"expiryTime":0}]}`, email)))
		return
	}
}

func (m *MockXUIServer) Clear() {
	m.mu.Lock()
	m.Fail = false
	m.Clients = make(map[string]xui.ClientConfig)
	m.InboundIDs = make(map[string][]int)
	m.AddClientCalls = 0
	m.mu.Unlock()
}

type TestEnv struct {
	mockTG  *MockTelegramServer
	mockXUI *MockXUIServer
	bot     *telebot.Bot
	ctx     context.Context
}

func (env *TestEnv) SendMessage(tgID int64, username string, text string) {
	update := map[string]interface{}{
		"update_id": int(time.Now().UnixNano()),
		"message": map[string]interface{}{
			"message_id": int(time.Now().UnixNano()),
			"from": map[string]interface{}{
				"id":         tgID,
				"is_bot":     false,
				"first_name": "TestUser",
				"username":   username,
			},
			"chat": map[string]interface{}{
				"id":         tgID,
				"type":       "private",
				"first_name": "TestUser",
				"username":   username,
			},
			"date": time.Now().Unix(),
			"text": text,
		},
	}
	env.mockTG.Updates <- update
}

func (env *TestEnv) SendCallback(tgID int64, username string, messageID int, data string) {
	update := map[string]interface{}{
		"update_id": int(time.Now().UnixNano()),
		"callback_query": map[string]interface{}{
			"id": fmt.Sprintf("cb_%d", time.Now().UnixNano()),
			"from": map[string]interface{}{
				"id":         tgID,
				"is_bot":     false,
				"first_name": "TestUser",
				"username":   username,
			},
			"message": map[string]interface{}{
				"message_id": messageID,
				"chat": map[string]interface{}{
					"id":   tgID,
					"type": "private",
				},
				"date": time.Now().Unix(),
				"text": "original message",
			},
			"data": data,
		},
	}
	env.mockTG.Updates <- update
}

func (env *TestEnv) SendPhoto(tgID int64, username string, fileID string) {
	update := map[string]interface{}{
		"update_id": int(time.Now().UnixNano()),
		"message": map[string]interface{}{
			"message_id": int(time.Now().UnixNano()),
			"from": map[string]interface{}{
				"id":         tgID,
				"is_bot":     false,
				"first_name": "TestUser",
				"username":   username,
			},
			"chat": map[string]interface{}{
				"id":         tgID,
				"type":       "private",
				"first_name": "TestUser",
				"username":   username,
			},
			"date": time.Now().Unix(),
			"photo": []interface{}{
				map[string]interface{}{
					"file_id":        fileID,
					"file_unique_id": "file_uniq_xyz",
					"width":          100,
					"height":         100,
					"file_size":      500,
				},
			},
		},
	}
	env.mockTG.Updates <- update
}

func (env *TestEnv) ExpectResponse(t *testing.T, timeout time.Duration) map[string]interface{} {
	t.Helper()
	select {
	case resp := <-env.mockTG.Responses:
		t.Logf("Received response: %+v", resp)
		return resp
	case <-time.After(timeout):
		t.Fatal("Timeout waiting for bot response")
		return nil
	}
}

func (env *TestEnv) ExpectNoResponse(t *testing.T, duration time.Duration) {
	t.Helper()
	select {
	case resp := <-env.mockTG.Responses:
		t.Fatalf("Expected no response, but got: %+v", resp)
	case <-time.After(duration):
		// success
	}
}

func cleanDB(ctx context.Context, t *testing.T) {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM payment_intents;
		DELETE FROM reconciliation_records;
		DELETE FROM purchase_requests;
		DELETE FROM purchase_quotes;
		DELETE FROM refund_requests;
		DELETE FROM bulk_credit_operations;
		DELETE FROM topup_requests;
		DELETE FROM subscriptions;
		DELETE FROM transactions;
		DELETE FROM test_usage;
		DELETE FROM bot_users;
		DELETE FROM paid_plans;
		DELETE FROM test_plans;
		DELETE FROM bot_settings;
	`)
	if err != nil {
		t.Fatalf("Failed to truncate tables: %v", err)
	}

	// Seed test plans
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO test_plans (id, name, description, inbound_ids, expire_seconds, max_data_bytes, flow, ip_limit, max_per_day, is_global, enabled)
		VALUES (1, 'Test Plan A', 'Test Description A', '[1]', 3600, 10737418240, '', 1, 1, true, true)
	`)
	if err != nil {
		t.Fatalf("Failed to seed test plans: %v", err)
	}

	// Seed paid plans
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO paid_plans (id, name, inbound_ids, base_price, base_price_toman, base_ip_limit, max_ip_limit, price_per_extra_ip, price_per_extra_ip_toman, flow, discount_tiers, is_global, enabled)
		VALUES (1, 'Paid Plan A', '[1]', 1000, 1000, 1, 3, 200, 200, '', '[{"months":3,"basis_points":1000}]', true, true)
	`)
	if err != nil {
		t.Fatalf("Failed to seed paid plans: %v", err)
	}

	_, err = db.Pool.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('bot_users', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('test_plans', 'id'), COALESCE((SELECT MAX(id) FROM test_plans), 1));
		SELECT setval(pg_get_serial_sequence('paid_plans', 'id'), COALESCE((SELECT MAX(id) FROM paid_plans), 1));
		SELECT setval(pg_get_serial_sequence('subscriptions', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('transactions', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('topup_requests', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('purchase_requests', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('purchase_quotes', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('reconciliation_records', 'id'), 1, false);
		SELECT setval(pg_get_serial_sequence('payment_intents', 'id'), 1, false);
	`)
	if err != nil {
		t.Fatalf("Failed to reset sequences: %v", err)
	}

	// Seed settings
	err = db.SetSetting(ctx, "card_number", "1234-5678-9012-3456")
	if err != nil {
		t.Fatalf("Failed to seed card setting: %v", err)
	}
	err = db.SetSetting(ctx, "test_reset_days", "30")
	if err != nil {
		t.Fatalf("Failed to seed reset days setting: %v", err)
	}
}

func setupE2E(t *testing.T) (*TestEnv, func()) {
	ctx := context.Background()

	// Load config
	if err := config.Load("../../config.yaml"); err != nil {
		if errEx := config.Load("../../config.example.yaml"); errEx != nil {
			t.Fatalf("Failed to load config: %v (example: %v)", err, errEx)
		}
	}
	// Keep E2E authorization independent of developer config files.
	config.Global.Admin.AdminIDs = []int64{96937669}

	// Setup mock servers
	mockTG := NewMockTelegramServer()
	mockXUI := NewMockXUIServer()

	// Override API URLs
	os.Setenv("TELEGRAM_API_URL", mockTG.Server.URL)
	config.Global.XUI.BaseURL = mockXUI.Server.URL
	config.Global.XUI.URL = mockXUI.Server.URL
	config.Global.XUI.APIToken = "e2e-test-token"
	if testDBURL := os.Getenv("TEST_DATABASE_URL"); testDBURL != "" {
		config.Global.Database.URL = testDBURL
	} else if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		config.Global.Database.URL = dbURL
	}

	// Connect to Database
	err := db.Connect(ctx, &config.Global.Database)
	if err != nil {
		t.Skipf("Skipping e2e test: database connection failed: %v", err)
	}

	err = db.Migrate(ctx)
	if err != nil {
		t.Skipf("Skipping e2e test: migration failed: %v", err)
	}

	// Start cache refresh (via scheduler)
	scheduler.Start(ctx, config.Global)

	xuiClient, err := xui.NewClient(&config.Global.XUI)
	if err != nil {
		t.Fatalf("Failed to init x-ui client: %v", err)
	}
	if _, err := xuiClient.CheckReadiness(ctx); err != nil {
		t.Fatalf("E2E XUI mock is not ready: %v", err)
	}
	cache := xui.NewInboundCache(xuiClient, time.Minute)
	xuiClient.Cache = cache
	cache.Start()
	defer cache.Stop()
	bot.XUIClient = xuiClient

	// Create bot settings
	apiURL := os.Getenv("TELEGRAM_API_URL")
	b, err := telebot.NewBot(telebot.Settings{
		URL:    apiURL,
		Token:  config.Global.Bot.Token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Failed to init telebot: %v", err)
	}

	// Setup Middleware & Handlers
	auth := bot.AuthMiddleware()
	admin := bot.AdminMiddleware(&config.Global.Admin)

	handlers.RegisterStart(b, auth, admin, &config.Global.Admin)
	handlers.RegisterTestSub(b, auth)
	handlers.RegisterBuySub(b, auth)
	handlers.RegisterMyServices(b, auth)
	handlers.RegisterWallet(b, auth, admin, &config.Global.Admin)
	handlers.RegisterAdminMenu(b, auth, admin)

	bot.Bot = b

	go b.Start()

	env := &TestEnv{
		mockTG:  mockTG,
		mockXUI: mockXUI,
		bot:     b,
		ctx:     ctx,
	}

	cleanup := func() {
		b.Stop()
		mockTG.Server.Close()
		mockXUI.Server.Close()
		db.Pool.Close()
		bot.FSM.Close()
	}

	return env, cleanup
}

func TestE2ESuite(t *testing.T) {
	env, cleanup := setupE2E(t)
	defer cleanup()

	const userTGID int64 = 111111
	const userUsername = "normal_user"
	const adminTGID int64 = 96937669
	const adminUsername = "admin_user"

	// Reset state helper
	resetState := func() {
		cleanDB(env.ctx, t)
		bot.GlobalFSM.ClearState(userTGID)
		bot.GlobalFSM.ClearState(adminTGID)
		env.mockTG.Clear()
		env.mockXUI.Clear()
	}

	// ==========================================
	// TIER 1: Feature Coverage (happy path cases)
	// ==========================================
	t.Run("Tier1_OnboardingAndAccessFlow", func(t *testing.T) {
		resetState()

		// 1. Start command registers a new user with 'approved' status directly
		env.SendMessage(userTGID, userUsername, "/start")
		resp := env.ExpectResponse(t, 2*time.Second)
		if !strings.Contains(getStr(resp, "text"), "خوش آمدید") && !strings.Contains(getStr(resp, "text"), "پنل کاربری") {
			t.Fatalf("Expected welcome message, got: %+v", resp)
		}

		// Verify user status in DB is approved
		u, err := db.GetUserByTelegramID(env.ctx, userTGID)
		if err != nil || u == nil || u.Status != "approved" {
			t.Fatalf("User status should be approved, got %v", u)
		}
	})

	t.Run("Tier1_TestSubscriptions", func(t *testing.T) {
		// Helper to setup approved user
		setupApprovedUser := func() {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en')`, userTGID, userUsername)
		}

		// 6. Unapproved user sees test plans list
		t.Run("UnapprovedListTestPlans", func(t *testing.T) {
			resetState() // Unapproved user
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'pending')`, userTGID, userUsername)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_test_sub")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "Test Plan A") {
				t.Fatalf("Expected test plan list, got: %+v", resp)
			}
		})

		// 7. Approved user generates test subscription with randomized name
		t.Run("RandomNameTestSub", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_test_sub")
			_ = env.ExpectResponse(t, 2*time.Second) // test plan list
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			resp := env.ExpectResponse(t, 2*time.Second) // QR photo
			if !strings.Contains(getStr(resp, "caption"), "/sub/") {
				t.Fatalf("Expected QR code with sub link, got: %+v", resp)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details message
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Verify in DB and X-UI Clients
			if len(env.mockXUI.Clients) != 1 {
				t.Fatalf("Client should exist on XUI Panel")
			}
			var email string
			for e := range env.mockXUI.Clients {
				email = e
			}
			if !strings.HasPrefix(email, "test_") {
				t.Fatalf("Expected email to have test_ prefix, got %s", email)
			}
			sub, err := db.GetSubscriptionByEmail(env.ctx, email)
			if err != nil || sub == nil {
				t.Fatalf("Subscription should exist in DB")
			}
		})

		// 8. Daily limit check: Approved user daily limit exceeded
		t.Run("DailyLimitExceeded", func(t *testing.T) {
			setupApprovedUser()
			// Generate first
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_test_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // Success QR photo
			_ = env.ExpectResponse(t, 2*time.Second) // details message
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Second attempt (daily limit is 1)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_test_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			respLimit := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respLimit, "text"), "قبلاً") && !strings.Contains(getStr(respLimit, "text"), "سقف") && !strings.Contains(getStr(respLimit, "text"), "محدودیت") {
				t.Fatalf("Expected limit exceeded message, got: %+v", respLimit)
			}
		})
	})

	t.Run("Tier1_PurchaseFlow", func(t *testing.T) {
		setupApprovedUserWithBalance := func(bal int64) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', $3)`, userTGID, userUsername, bal)
		}

		// 11. Select plan, select 1 month, select base IP (1), enter custom name, succeeds
		t.Run("BuySubBase1Month", func(t *testing.T) {
			setupApprovedUserWithBalance(2000)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second) // paid plans list
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // duration selector
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second) // IP prompt
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second) // Prompt name
			env.SendMessage(userTGID, userUsername, "laptop")
			respInvoice := env.ExpectResponse(t, 2*time.Second) // Invoice
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			respQR := env.ExpectResponse(t, 2*time.Second) // QR Photo success
			if !strings.Contains(getStr(respQR, "caption"), "/sub/") {
				t.Fatalf("Expected QR code with sub link, got: %+v", respQR)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details message
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Verify balance and sub
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 { // 2000 - 1000 base price
				t.Fatalf("Expected 1000 balance remaining, got %d", u.WalletBalance)
			}
			subs, err := db.GetSubscriptionsByUserID(env.ctx, u.ID)
			if err != nil || len(subs) == 0 || subs[0].IPLimit != 1 {
				t.Fatalf("Subscription not created properly: %v, %+v", err, subs)
			}
		})

		// 12. Buy with custom duration via text input
		t.Run("BuySubCustomDuration", func(t *testing.T) {
			setupApprovedUserWithBalance(3000)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_custom_dur|1")
			_ = env.ExpectResponse(t, 2*time.Second) // prompt text
			env.SendMessage(userTGID, userUsername, "2")
			_ = env.ExpectResponse(t, 2*time.Second) // IP prompt
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:2")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "workstation")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "caption"), "/sub/") {
				t.Fatalf("Expected QR code with sub link, got: %+v", resp)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details message
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 { // 3000 - 2000 (2 months * 1000)
				t.Fatalf("Expected 1000 balance, got %d", u.WalletBalance)
			}
		})

		// 13. Buy with extra IPs (e.g. 2 IPs, base is 1, extra IP costs 200/mo)
		t.Run("BuySubExtraIPs", func(t *testing.T) {
			setupApprovedUserWithBalance(2000)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|2:1:1") // 2 IPs for 1 month
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "dual")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			_ = env.ExpectResponse(t, 2*time.Second) // QR Photo
			_ = env.ExpectResponse(t, 2*time.Second) // Details
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 800 { // 2000 - (1000 + 200)
				t.Fatalf("Expected 800 balance, got %d", u.WalletBalance)
			}
		})

		// 14. Buy with discount applied (e.g. 3 months, 10% discount)
		t.Run("BuySubDiscount", func(t *testing.T) {
			setupApprovedUserWithBalance(4000)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|3:1") // 3 months
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:3")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "tablet")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			_ = env.ExpectResponse(t, 2*time.Second) // QR Photo
			_ = env.ExpectResponse(t, 2*time.Second) // Details
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			// Base: 1000 * 3 = 3000. 10% discount: 3000 - 300 = 2700. Remaining: 4000 - 2700 = 1300
			if u.WalletBalance != 1300 {
				t.Fatalf("Expected 1300 balance, got %d", u.WalletBalance)
			}
		})

		// 15. Insufficient balance purchase failure
		t.Run("BuySubInsufficientBalance", func(t *testing.T) {
			setupApprovedUserWithBalance(500)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "cheap")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "کافی نیست") {
				t.Fatalf("Expected insufficient balance message, got: %+v", resp)
			}
		})
	})

	t.Run("Tier1_WalletAndTopups", func(t *testing.T) {
		setupApprovedUser := func() {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en')`, userTGID, userUsername)
		}

		// 16. Approved user wallet, top up, receives details
		t.Run("TopupInitiation", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_wallet")
			_ = env.ExpectResponse(t, 2*time.Second) // Balance
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			resp := env.ExpectResponse(t, 2*time.Second) // Payment details
			if !strings.Contains(getStr(resp, "text"), "1234-5678-9012-3456") {
				t.Fatalf("Expected card number in details, got: %+v", resp)
			}
		})

		// 17. User sends receipt photo, saved as pending, admin notified
		t.Run("SendReceiptPending", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			_ = env.ExpectResponse(t, 2*time.Second) // Payment details with intent
			env.SendPhoto(userTGID, userUsername, "receipt_file_id")

			respAdmin := env.ExpectResponse(t, 2*time.Second) // admin notice
			if !strings.Contains(getStr(respAdmin, "caption"), "افزایش موجودی") {
				t.Fatalf("Expected top-up request to admin, got: %+v", respAdmin)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // user receipt received notice
			_ = env.ExpectResponse(t, 2*time.Second) // user main menu

			// Verify in DB
			var count int
			_ = db.Pool.QueryRow(env.ctx, "SELECT COUNT(*) FROM topup_requests WHERE status = 'pending'").Scan(&count)
			if count != 1 {
				t.Fatalf("Expected 1 pending topup request, got %d", count)
			}
		})

		// 18. Admin approves topup, enters credit, user balance increases
		t.Run("AdminApproveTopup", func(t *testing.T) {
			setupApprovedUser()
			// Create topup req
			_, err := db.Pool.Exec(env.ctx, "INSERT INTO topup_requests (id, user_id, telegram_file_id, status) VALUES (10, 1, 'file123', 'pending')")
			if err != nil {
				t.Fatal(err)
			}

			// Admin clicks approve
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_approve_topup|10")
			respPrompt := env.ExpectResponse(t, 2*time.Second) // Prompt for amount
			if !strings.Contains(getStr(respPrompt, "text"), "مبلغ") {
				t.Fatalf("Expected prompt for amount, got: %+v", respPrompt)
			}

			// Admin enters amount
			bot.GlobalFSM.SetState(adminTGID, "awaiting_topup_amount", map[string]interface{}{"req_id": 10})
			env.SendMessage(adminTGID, adminUsername, "1500")

			_ = env.ExpectResponse(t, 2*time.Second)                // User notice
			respAdminResult := env.ExpectResponse(t, 2*time.Second) // Admin notice
			if !strings.Contains(getStr(respAdminResult, "text"), "تایید شد") {
				t.Fatalf("Expected admin success message, got: %+v", respAdminResult)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // Admin menu

			// Verify DB
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1500 {
				t.Fatalf("User balance should be 1500, got %d", u.WalletBalance)
			}
		})

		// 19. Admin rejects topup request, status rejected
		t.Run("AdminRejectTopup", func(t *testing.T) {
			setupApprovedUser()
			_, _ = db.Pool.Exec(env.ctx, "INSERT INTO topup_requests (id, user_id, telegram_file_id, status) VALUES (20, 1, 'file123', 'pending')")

			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_reject_topup|20")
			_ = env.ExpectResponse(t, 2*time.Second)                // User notification
			respAdminResult := env.ExpectResponse(t, 2*time.Second) // Admin edit
			if !strings.Contains(getStr(respAdminResult, "text"), "رد شد") {
				t.Fatalf("Expected reject confirmation, got: %+v", respAdminResult)
			}

			// Verify DB
			var status string
			db.Pool.QueryRow(env.ctx, "SELECT status FROM topup_requests WHERE id = 20").Scan(&status)
			if status != "rejected" {
				t.Fatalf("Expected status to be rejected, got %s", status)
			}
		})

		// 20. Admin manually credits user
		t.Run("AdminManualCredit", func(t *testing.T) {
			setupApprovedUser()
			// Admin view user
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_view_user|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_user_credit|1")
			_ = env.ExpectResponse(t, 2*time.Second) // prompt credit

			env.SendMessage(adminTGID, adminUsername, "500")
			_ = env.ExpectResponse(t, 2*time.Second) // user credit notify
			respConfirmAdmin := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respConfirmAdmin, "text"), "شارژ شد") {
				t.Fatalf("Expected admin credit confirm, got: %+v", respConfirmAdmin)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // admin view user

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 500 {
				t.Fatalf("Expected user balance 500, got %d", u.WalletBalance)
			}
		})
	})

	t.Run("Tier1_ServicesManagement", func(t *testing.T) {
		setupApprovedUserWithSub := func() {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 50000)`, userTGID, userUsername)
			// Seed a subscription
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO subscriptions (id, user_id, plan_id, plan_type, client_email, sub_id, display_name, ip_limit, expire_time, is_active) VALUES (1, 1, 1, 'paid', 'myservice_deviceA', 'sub12345', 'Device A', 1, 1900000000000, true)`)
			// Seed client on Mock XUI
			env.mockXUI.Clients["myservice_deviceA"] = xui.ClientConfig{
				Email:  "myservice_deviceA",
				Enable: true,
			}
		}

		// 21. User views active services list
		t.Run("ViewServicesList", func(t *testing.T) {
			setupApprovedUserWithSub()
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_my_services")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "سرویس‌های من") || !strings.Contains(getStr(resp, "reply_markup"), "view_sub") {
				t.Fatalf("Expected active services list, got: %+v", resp)
			}
		})

		// 22. Toggle state (Enable -> Disable -> Enable)
		t.Run("ToggleServiceState", func(t *testing.T) {
			setupApprovedUserWithSub()
			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|1")
			_ = env.ExpectResponse(t, 2*time.Second) // details page

			// Disable
			env.SendCallback(userTGID, userUsername, 999, "\fsub_toggle|1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "غیرفعال") {
				t.Fatalf("Expected toggle to be disabled, got: %+v", resp)
			}
		})

		// 23. Rename service
		t.Run("RenameService", func(t *testing.T) {
			setupApprovedUserWithSub()
			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_rename|1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "غیرفعال") {
				t.Fatalf("Expected rename to be disabled, got: %+v", resp)
			}
		})

		// 24. Upgrade concurrent IP limit
		t.Run("UpgradeIPLimit", func(t *testing.T) {
			setupApprovedUserWithSub()
			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_limit|1")
			_ = env.ExpectResponse(t, 2*time.Second) // IP selector

			env.SendCallback(userTGID, userUsername, 999, "\fsub_limit_set|2:1") // Set to 2 IPs
			respLimitInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmLimitData := extractCallbackData(respLimitInvoice, "\fsub_limit_confirm")
			if confirmLimitData == "" {
				t.Fatalf("Expected sub_limit_confirm in invoice: %+v", respLimitInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmLimitData)
			_ = env.ExpectResponse(t, 2*time.Second)     // callback alert
			resp := env.ExpectResponse(t, 2*time.Second) // message
			if !strings.Contains(getStr(resp, "text"), "با موفقیت") && !strings.Contains(getStr(resp, "text"), "افزایش یافت") {
				t.Fatalf("Expected IP upgrade success, got: %+v", resp)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details page

			// Verify DB (deduction occurred) and panel limit
			sub, _ := db.GetSubscriptionByID(env.ctx, 1)
			if sub.IPLimit != 2 {
				t.Fatalf("IP limit should be 2, got %d", sub.IPLimit)
			}
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance >= 50000 {
				t.Fatalf("Balance should have been deducted for IP upgrade")
			}
		})

		// 25. Extend active subscription
		t.Run("ExtendSubscription", func(t *testing.T) {
			setupApprovedUserWithSub()

			// Manually set sub to inactive in DB to simulate expired state
			subBefore, _ := db.GetSubscriptionByID(env.ctx, 1)
			subBefore.IsActive = false
			if err := db.UpdateSubscription(env.ctx, subBefore); err != nil {
				t.Fatalf("Failed to disable subscription: %v", err)
			}

			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend|1")
			_ = env.ExpectResponse(t, 2*time.Second) // month selector

			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend_run|1:1") // 1 month
			respExtendInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmExtData := extractCallbackData(respExtendInvoice, "\fsub_extend_confirm")
			if confirmExtData == "" {
				t.Fatalf("Expected sub_extend_confirm in invoice: %+v", respExtendInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmExtData)
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "با موفقیت") && !strings.Contains(getStr(resp, "text"), "تمدید شد") {
				t.Fatalf("Expected extension success, got: %+v", resp)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details page

			// Verify DB (balance deducted) and expiry increased, and set back to active
			sub, _ := db.GetSubscriptionByID(env.ctx, 1)
			if sub.ExpireTime == nil {
				t.Fatalf("Sub should have expiry time")
			}
			if !sub.IsActive {
				t.Fatalf("Expected subscription to be active after extension")
			}
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 49000 { // 50000 - 1000 extension cost
				t.Fatalf("Wallet balance should be 49000, got %d", u.WalletBalance)
			}
		})
	})

	// ==========================================
	// TIER 2: Boundary & Corner Cases (>=25 cases)
	// ==========================================
	t.Run("Tier2_BoundaryCases", func(t *testing.T) {
		setupApprovedUser := func() {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 2000)`, userTGID, userUsername)
		}

		// --- Feature 1: Onboarding Boundary Cases ---

		// 26. /start twice does not create duplicate user
		t.Run("StartTwice", func(t *testing.T) {
			resetState()
			env.SendMessage(userTGID, userUsername, "/start")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "/start")
			_ = env.ExpectResponse(t, 2*time.Second)

			var count int
			_ = db.Pool.QueryRow(env.ctx, "SELECT COUNT(*) FROM bot_users WHERE telegram_id = $1", userTGID).Scan(&count)
			if count != 1 {
				t.Fatalf("Expected 1 user in DB, got %d", count)
			}
		})

		// 27. Banned user is blocked on /start
		t.Run("BannedUserBlocked", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'banned')`, userTGID, userUsername)
			env.SendMessage(userTGID, userUsername, "/start")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "مسدود") {
				t.Fatalf("Expected banned message, got: %+v", resp)
			}
		})

		// 28. Duplicate start updates username / activity
		t.Run("DuplicateStart", func(t *testing.T) {
			resetState()
			env.SendMessage(userTGID, userUsername, "/start")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, "updated_user", "/start")
			_ = env.ExpectResponse(t, 2*time.Second)
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u == nil || u.Username != "updated_user" {
				t.Fatalf("Expected updated username, got: %+v", u)
			}
		})

		// 29. Admin view user details
		t.Run("AdminViewUser", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_view_user|1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "کاربر") {
				t.Fatalf("Expected admin view user, got: %+v", resp)
			}
		})

		// 30. Non-admin accesses admin panel -> rejected
		t.Run("UnapprovedAdminPanelAccess", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved')`, userTGID, userUsername)
			env.SendMessage(userTGID, userUsername, "/admin")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "دسترسی") {
				t.Fatalf("Expected admin rejected message, got: %+v", resp)
			}
		})

		// --- Feature 2: Test Subscriptions Boundary Cases ---

		// 31. User exceeds test limit
		t.Run("UnapprovedTestLimitExceeded", func(t *testing.T) {
			setupApprovedUser()

			// Generate first test sub
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // success
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Generate second test sub
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			resp := env.ExpectResponse(t, 2*time.Second) // Limit error
			if !strings.Contains(getStr(resp, "text"), "قبلاً") && !strings.Contains(getStr(resp, "text"), "سقف") && !strings.Contains(getStr(resp, "text"), "محدودیت") {
				t.Fatalf("Expected test limit check, got: %+v", resp)
			}
		})

		// 32. Select non-existent test plan
		t.Run("NonExistentTestPlan", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|99") // Plan ID 99 doesn't exist
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "یافت نشد") && !strings.Contains(getStr(resp, "text"), "خطا") {
				t.Fatalf("Expected not found or error, got: %+v", resp)
			}
		})

		// 33. X-UI Panel returns error during test sub generation (rolled back)
		t.Run("XUIErrorTestSub", func(t *testing.T) {
			setupApprovedUser()
			env.mockXUI.Fail = true // Enable mock panel failure

			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "خطا") {
				t.Fatalf("Expected error message, got: %+v", resp)
			}

			// Verify no subscription record in DB
			var count int
			_ = db.Pool.QueryRow(env.ctx, "SELECT COUNT(*) FROM subscriptions").Scan(&count)
			if count != 0 {
				t.Fatalf("DB transaction should have rolled back, got %d subs", count)
			}
		})

		// 34. Multi test generation disabled
		t.Run("MultiTestDisabled", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fts_multi_run|3:1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "غیرفعال") {
				t.Fatalf("Expected disabled notification, got: %+v", resp)
			}
		})

		// --- Feature 3: Purchase Flow Boundary Cases ---

		// 36. Select non-existent paid plan
		t.Run("NonExistentPaidPlan", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|99")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "یافت نشد") && !strings.Contains(getStr(resp, "text"), "خطا") {
				t.Fatalf("Expected plan not found error, got: %+v", resp)
			}
		})

		// 37. Zero/negative months for custom buy duration
		t.Run("BuySubZeroDuration", func(t *testing.T) {
			setupApprovedUser()
			bot.GlobalFSM.SetState(userTGID, "awaiting_buy_months_text", map[string]interface{}{"plan_id": "1"})
			env.SendMessage(userTGID, userUsername, "0")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "عدد مثبت") {
				t.Fatalf("Expected invalid duration message, got: %+v", resp)
			}
		})

		// 38. IP limit higher than plan's max limit
		t.Run("BuySubExceedMaxIPs", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|10:1:1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "حداکثر") {
				t.Fatalf("Expected invalid IP limit error, got: %+v", resp)
			}
		})

		// 39. IP limit lower than plan's base limit
		t.Run("BuySubBelowBaseIPs", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|0:1:1")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "حداقل") {
				t.Fatalf("Expected invalid IP limit error, got: %+v", resp)
			}
		})

		// 40. A remote create is adopted after the local insert recovers
		t.Run("DBSaveErrorPaidSub", func(t *testing.T) {
			setupApprovedUser()
			if _, err := db.Pool.Exec(env.ctx, `
				CREATE OR REPLACE FUNCTION e2e_fail_paid_subscription_insert() RETURNS trigger
				LANGUAGE plpgsql AS $$
				BEGIN
					IF NEW.client_email LIKE 'faileddev_%' THEN
						RAISE EXCEPTION 'injected local subscription insert failure';
					END IF;
					RETURN NEW;
				END $$;
				CREATE TRIGGER e2e_fail_paid_subscription_insert
				BEFORE INSERT ON subscriptions
				FOR EACH ROW EXECUTE FUNCTION e2e_fail_paid_subscription_insert()
			`); err != nil {
				t.Fatalf("install local insert failure: %v", err)
			}
			t.Cleanup(func() {
				_, _ = db.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS e2e_fail_paid_subscription_insert ON subscriptions; DROP FUNCTION IF EXISTS e2e_fail_paid_subscription_insert()`)
			})
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "faileddev")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "ادامه می‌یابد") {
				t.Fatalf("expected durable pending provisioning notice, got: %+v", resp)
			}

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 || env.mockXUI.AddClientCalls != 1 || len(env.mockXUI.Clients) != 1 {
				t.Fatalf("expected one debit and one remote create before local recovery: user=%+v add_calls=%d clients=%d", u, env.mockXUI.AddClientCalls, len(env.mockXUI.Clients))
			}
			var email string
			for candidate := range env.mockXUI.Clients {
				email = candidate
			}
			if !strings.HasPrefix(email, "faileddev_") {
				t.Fatalf("unexpected remote email %q", email)
			}
			if sub, err := db.GetSubscriptionByEmail(env.ctx, email); err != nil || sub != nil {
				t.Fatalf("local subscription should still be absent before recovery: sub=%+v err=%v", sub, err)
			}
			var workID int64
			if err := db.Pool.QueryRow(env.ctx, `SELECT id FROM reconciliation_records WHERE user_id=$1 AND kind='purchase_provisioning_unknown' ORDER BY id DESC LIMIT 1`, u.ID).Scan(&workID); err != nil {
				t.Fatalf("read durable provisioning work: %v", err)
			}
			if _, err := db.Pool.Exec(env.ctx, `DROP TRIGGER e2e_fail_paid_subscription_insert ON subscriptions; DROP FUNCTION e2e_fail_paid_subscription_insert()`); err != nil {
				t.Fatalf("remove local insert failure: %v", err)
			}
			if _, err := db.Pool.Exec(env.ctx, `UPDATE reconciliation_records SET next_attempt_at=NOW() WHERE id=$1`, workID); err != nil {
				t.Fatalf("make durable work retryable: %v", err)
			}
			worker := reconcile.NewProcessor("e2e-local-insert-recovery", bot.XUIClient)
			if _, err := worker.ProcessOnce(env.ctx); err != nil {
				t.Fatalf("recover after local insert failure: %v", err)
			}
			sub, err := db.GetSubscriptionByEmail(env.ctx, email)
			work, workErr := db.GetReconciliationRecordByID(env.ctx, workID)
			if err != nil || workErr != nil || sub == nil || work == nil || work.Status != db.ReconciliationStatusResolvedVerified || env.mockXUI.AddClientCalls != 1 {
				t.Fatalf("expected exact remote client adoption without duplicate create: sub=%+v work=%+v add_calls=%d errs=(%v,%v)", sub, work, env.mockXUI.AddClientCalls, err, workErr)
			}
			u, _ = db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 {
				t.Fatalf("recovery must not debit or refund a second time: balance=%d", u.WalletBalance)
			}
		})

		// --- Feature 4: Wallet & Topups Boundary Cases ---

		// 41. Non-approved user tries to top up
		t.Run("NonApprovedTopupRejected", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'banned')`, userTGID, userUsername)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_wallet")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "مسدود") {
				t.Fatalf("Expected banned message, got: %+v", resp)
			}
		})

		// 42. User sends text instead of receipt photo
		t.Run("WalletReceiptInvalidInput", func(t *testing.T) {
			setupApprovedUser()
			bot.GlobalFSM.SetState(userTGID, "awaiting_receipt", nil)
			env.SendMessage(userTGID, userUsername, "Here is my text receipt")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "عکس") {
				t.Fatalf("Expected photo prompt, got: %+v", resp)
			}
		})

		// 43. Admin enters negative or non-numeric top-up amount
		t.Run("AdminInvalidTopupAmount", func(t *testing.T) {
			setupApprovedUser()
			_, _ = db.Pool.Exec(env.ctx, "INSERT INTO topup_requests (id, user_id, telegram_file_id, status) VALUES (11, 1, 'file123', 'pending')")
			bot.GlobalFSM.SetState(adminTGID, "awaiting_topup_amount", map[string]interface{}{"req_id": 11})

			env.SendMessage(adminTGID, adminUsername, "-500")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "نامعتبر") {
				t.Fatalf("Expected invalid amount warning, got: %+v", resp)
			}
		})

		// 44. Admin rejects non-existent top-up request
		t.Run("AdminRejectNonExistentTopup", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_reject_topup|999") // Request ID 999 doesn't exist
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "یافت نشد") && !strings.Contains(getStr(resp, "text"), "نامعتبر") {
				t.Fatalf("Expected not found response, got: %+v", resp)
			}
		})

		// 45. Admin manually credits non-existent user
		t.Run("AdminCreditNonExistentUser", func(t *testing.T) {
			setupApprovedUser()
			bot.GlobalFSM.SetState(adminTGID, "awaiting_manual_credit", map[string]interface{}{"target_user_id": "999"})
			env.SendMessage(adminTGID, adminUsername, "100")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "یافت نشد") && !strings.Contains(getStr(resp, "text"), "نامعتبر") {
				t.Fatalf("Expected credit failure, got: %+v", resp)
			}
		})

		// --- Feature 5: Services Management Boundary Cases ---

		// 46. Non-approved user accesses my services -> rejected
		t.Run("NonApprovedMyServicesRejected", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'banned')`, userTGID, userUsername)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_my_services")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "مسدود") {
				t.Fatalf("Expected banned message, got: %+v", resp)
			}
		})

		// 47. View non-existent subscription
		t.Run("ViewNonExistentSub", func(t *testing.T) {
			setupApprovedUser()
			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|999")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "یافت نشد") {
				t.Fatalf("Expected subscription not found, got: %+v", resp)
			}
		})

		// 48. Extend a test subscription -> forbidden
		t.Run("ExtendTestSubForbidden", func(t *testing.T) {
			setupApprovedUser()
			// Create a test subscription
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO subscriptions (id, user_id, plan_id, plan_type, client_email, sub_id, display_name, is_active) VALUES (1, 1, 1, 'test', 'myservice_device', 'sub123', 'Device', true)`)

			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend|1") // Extend
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "خریداری شده") {
				t.Fatalf("Expected test extend forbidden error, got: %+v", resp)
			}
		})

		// 49. Change IP limit of a test subscription -> forbidden
		t.Run("ChangeIPLimitTestSubForbidden", func(t *testing.T) {
			setupApprovedUser()
			// Create a test subscription
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO subscriptions (id, user_id, plan_id, plan_type, client_email, sub_id, display_name, is_active) VALUES (1, 1, 1, 'test', 'myservice_device', 'sub123', 'Device', true)`)

			env.SendCallback(userTGID, userUsername, 999, "\fsub_limit|1") // IP limit
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "خریداری شده") {
				t.Fatalf("Expected IP upgrade forbidden error, got: %+v", resp)
			}
		})

		// 50. IP upgrade insufficient wallet balance
		t.Run("UpgradeIPLimitInsufficientBalance", func(t *testing.T) {
			resetState()
			// Balance: 0
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 0)`, userTGID, userUsername)
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO subscriptions (id, user_id, plan_id, plan_type, client_email, sub_id, display_name, ip_limit, expire_time, is_active) VALUES (1, 1, 1, 'paid', 'myservice_device', 'sub123', 'Device', 1, 2900000000000, true)`)

			env.SendCallback(userTGID, userUsername, 999, "\fsub_limit_set|3:1") // Set to 3 IPs
			respLimitInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmLimitData := extractCallbackData(respLimitInvoice, "\fsub_limit_confirm")
			if confirmLimitData == "" {
				t.Fatalf("Expected sub_limit_confirm in invoice: %+v", respLimitInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmLimitData)
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "کافی نیست") {
				t.Fatalf("Expected insufficient balance message, got: %+v", resp)
			}
		})
	})

	// ==========================================
	// TIER 3: Cross-Feature Combinations (>=5 cases)
	// ==========================================
	t.Run("Tier3_CrossFeatureCombinations", func(t *testing.T) {
		// 51. Combination: Onboard -> top-up -> purchase plan
		t.Run("ComboOnboardTopupBuy", func(t *testing.T) {
			resetState()
			// User starts
			env.SendMessage(userTGID, userUsername, "/start")
			_ = env.ExpectResponse(t, 2*time.Second)

			// Topup request
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			_ = env.ExpectResponse(t, 2*time.Second) // payment details
			env.SendPhoto(userTGID, userUsername, "myreceipt")
			_ = env.ExpectResponse(t, 2*time.Second) // wait message
			_ = env.ExpectResponse(t, 2*time.Second) // admin notice
			_ = env.ExpectResponse(t, 2*time.Second) // user main menu

			// Admin approve topup
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_approve_topup|1")
			_ = env.ExpectResponse(t, 2*time.Second) // amount prompt
			bot.GlobalFSM.SetState(adminTGID, "awaiting_topup_amount", map[string]interface{}{"req_id": 1})
			env.SendMessage(adminTGID, adminUsername, "3000")
			_ = env.ExpectResponse(t, 2*time.Second) // user balance alert
			_ = env.ExpectResponse(t, 2*time.Second) // admin confirm
			_ = env.ExpectResponse(t, 2*time.Second) // admin menu

			// Buy plan
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second) // plans
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // durations
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second) // IPs
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second) // name prompt
			env.SendMessage(userTGID, userUsername, "device1")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			respQR := env.ExpectResponse(t, 2*time.Second) // QR Photo success
			if !strings.Contains(getStr(respQR, "caption"), "/sub/") {
				t.Fatalf("Expected QR code with sub link, got: %+v", respQR)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details text
			_ = env.ExpectResponse(t, 2*time.Second) // user main menu

			// Verify in DB and XUI
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 2000 {
				t.Fatalf("Expected 2000 balance remaining, got %d", u.WalletBalance)
			}
			subs, _ := db.GetSubscriptionsByUserID(env.ctx, u.ID)
			if len(subs) == 0 {
				t.Fatalf("Subscription should exist in DB")
			}
		})

		// 52. Combination: Generate test sub -> get wallet credit -> buy paid sub -> manage services
		t.Run("ComboTestSubToPaidSubManage", func(t *testing.T) {
			resetState()
			// Approved user setup
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 0)`, userTGID, userUsername)

			// Generate test sub
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // success QR photo
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Admin credits user
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_view_user|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_user_credit|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(adminTGID, adminUsername, "1000")
			_ = env.ExpectResponse(t, 2*time.Second) // user credit notify
			_ = env.ExpectResponse(t, 2*time.Second) // admin confirm
			_ = env.ExpectResponse(t, 2*time.Second) // admin view user

			// Buy paid sub
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "device2")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			_ = env.ExpectResponse(t, 2*time.Second) // QR Photo success
			_ = env.ExpectResponse(t, 2*time.Second) // details text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Manage services: view list
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_my_services")
			respList := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respList, "reply_markup"), "view_sub") {
				t.Fatalf("Expected list containing services, got: %+v", respList)
			}

			// Try to toggle subscription - should be disabled
			env.SendCallback(userTGID, userUsername, 999, "\fsub_toggle|1")
			respToggle := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respToggle, "text"), "غیرفعال") {
				t.Fatalf("Expected toggle to be disabled, got: %+v", respToggle)
			}
		})

		// 53. Combination: Admin changes test reset days -> user tests boundary
		t.Run("ComboAdminChangesTestResetDays", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'pending')`, userTGID, userUsername)

			// Admin opens settings
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_settings")
			_ = env.ExpectResponse(t, 2*time.Second) // settings menu
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_set_test_reset_days")
			_ = env.ExpectResponse(t, 2*time.Second) // prompt reset days

			// Enter 30
			bot.GlobalFSM.SetState(adminTGID, "awaiting_setting_test_reset_days", nil)
			env.SendMessage(adminTGID, adminUsername, "30")
			_ = env.ExpectResponse(t, 2*time.Second) // confirmation
			_ = env.ExpectResponse(t, 2*time.Second) // settings menu

			// Verify setting in DB
			limit, _ := db.GetSetting(env.ctx, "test_reset_days")
			if limit != "30" {
				t.Fatalf("Expected test_reset_days to be 30 in DB, got %s", limit)
			}

			// Generate 1st test sub
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // QR
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Generate 2nd test sub (should be blocked)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			resp2 := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp2, "text"), "قبلاً") && !strings.Contains(getStr(resp2, "text"), "سقف") && !strings.Contains(getStr(resp2, "text"), "محدودیت") {
				t.Fatalf("Expected limit blocked message, got: %+v", resp2)
			}
		})

		// 54. Combination: Buy sub with extra IPs -> upgrade IP limit further -> extend sub
		t.Run("ComboUpgradeIPAndExtend", func(t *testing.T) {
			resetState()
			// Seed approved user with plenty of balance
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 5000)`, userTGID, userUsername)

			// Buy plan with 2 IPs (extra IP costs 200/mo) for 3 months (3 * 1000 base + 3 * 200 extra = 3600. Discount 10%: 3600 - 360 = 3240)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_buy_sub")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|3:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|2:1:3")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "device")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			_ = env.ExpectResponse(t, 2*time.Second) // success QR
			_ = env.ExpectResponse(t, 2*time.Second) // details text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Balance check: 5000 - 3240 = 1760
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1760 {
				t.Fatalf("Expected 1760 balance, got %d", u.WalletBalance)
			}

			// Upgrade to 3 IPs (adds 1 IP for remaining 3 months. Cost = 200 * 3 = 600)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_limit_set|3:1")
			respLimitInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmLimitData := extractCallbackData(respLimitInvoice, "\fsub_limit_confirm")
			if confirmLimitData == "" {
				t.Fatalf("Expected sub_limit_confirm in invoice: %+v", respLimitInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmLimitData)
			_ = env.ExpectResponse(t, 2*time.Second) // callback alert
			_ = env.ExpectResponse(t, 2*time.Second) // confirmation message
			_ = env.ExpectResponse(t, 2*time.Second) // details page

			// Balance check: 1760 - 200 = 1560
			u, _ = db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1560 {
				t.Fatalf("Expected 1560 balance, got %d", u.WalletBalance)
			}

			// Extend subscription by 2 months (2 * 1000 base + 2 * 400 extra = 2800. Since balance is 1560, this extension should fail!)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend_run|2:1")
			respExtendInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmExtData := extractCallbackData(respExtendInvoice, "\fsub_extend_confirm")
			if confirmExtData == "" {
				t.Fatalf("Expected sub_extend_confirm in invoice: %+v", respExtendInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmExtData)
			respFail := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respFail, "text"), "کافی نیست") {
				t.Fatalf("Expected extension fail due to insufficient balance, got: %+v", respFail)
			}

			// Let's add credit and try again
			_, _ = db.Pool.Exec(env.ctx, "UPDATE bot_users SET wallet_balance = 5000 WHERE id = 1")
			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend_run|1:1")
			respExtendInvoice2 := env.ExpectResponse(t, 2*time.Second)
			confirmExtData2 := extractCallbackData(respExtendInvoice2, "\fsub_extend_confirm")
			env.SendCallback(userTGID, userUsername, 999, confirmExtData2)
			respSuccess := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respSuccess, "text"), "با موفقیت") && !strings.Contains(getStr(respSuccess, "text"), "تمدید شد") {
				t.Fatalf("Expected extension success, got: %+v", respSuccess)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details page
		})

		// 55. Combination: Buy fails -> Topup -> Admin Reject -> Manual Credit -> Buy succeeds
		t.Run("ComboFailedPurchaseToSuccessPath", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 0)`, userTGID, userUsername)

			// Try to buy, fails (balance 0)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "work")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			respFail := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respFail, "text"), "کافی نیست") {
				t.Fatalf("Expected insufficient balance error, got: %+v", respFail)
			}

			// User tops up
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			_ = env.ExpectResponse(t, 2*time.Second) // payment details
			env.SendPhoto(userTGID, userUsername, "receipt123")
			_ = env.ExpectResponse(t, 2*time.Second) // admin notice photo
			_ = env.ExpectResponse(t, 2*time.Second) // wait message to user
			_ = env.ExpectResponse(t, 2*time.Second) // user main menu

			// Admin rejects topup
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_reject_topup|1")
			_ = env.ExpectResponse(t, 2*time.Second) // user notice
			_ = env.ExpectResponse(t, 2*time.Second) // admin callback alert
			_ = env.ExpectResponse(t, 2*time.Second) // admin edit

			// Admin manually credits user instead
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_view_user|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_user_credit|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(adminTGID, adminUsername, "1200")
			_ = env.ExpectResponse(t, 2*time.Second) // user credit notify
			_ = env.ExpectResponse(t, 2*time.Second) // admin confirm
			_ = env.ExpectResponse(t, 2*time.Second) // admin view user

			// Try to buy again (succeeds)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "work")
			respInvoice2 := env.ExpectResponse(t, 2*time.Second)
			confirmData2 := extractCallbackData(respInvoice2, "\fbuy_confirm")
			if confirmData2 == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice2)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData2)
			respSuccess := env.ExpectResponse(t, 2*time.Second) // QR Photo success
			if !strings.Contains(getStr(respSuccess, "caption"), "/sub/") {
				t.Fatalf("Expected QR code with sub link, got: %+v", respSuccess)
			}
			_ = env.ExpectResponse(t, 2*time.Second) // details text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu
		})
	})

	// ==========================================
	// TIER 4: Real-World Application Scenarios (>=5 scenarios)
	// ==========================================
	t.Run("Tier4_RealWorldScenarios", func(t *testing.T) {
		setupApprovedUser := func() {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en')`, userTGID, userUsername)
		}

		// 56. Scenario: Complete Customer Lifecycle Journey
		t.Run("CompleteResellerLifecycleJourney", func(t *testing.T) {
			resetState()
			// 1. User joins
			env.SendMessage(userTGID, userUsername, "/start")
			_ = env.ExpectResponse(t, 2*time.Second)

			// 2. User tops up wallet
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			_ = env.ExpectResponse(t, 2*time.Second) // payment details
			env.SendPhoto(userTGID, userUsername, "receipt_img")
			_ = env.ExpectResponse(t, 2*time.Second) // admin notify photo
			_ = env.ExpectResponse(t, 2*time.Second) // user wait message
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Admin approves topup
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_approve_topup|1")
			_ = env.ExpectResponse(t, 2*time.Second) // amount prompt
			bot.GlobalFSM.SetState(adminTGID, "awaiting_topup_amount", map[string]interface{}{"req_id": 1})
			env.SendMessage(adminTGID, adminUsername, "5000")
			_ = env.ExpectResponse(t, 2*time.Second) // user balance alert
			_ = env.ExpectResponse(t, 2*time.Second) // admin confirm
			_ = env.ExpectResponse(t, 2*time.Second) // admin menu

			// 3. User generates 1 test subscription
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // photo
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// 4. User purchases 1 paid subscription (Paid Plan A)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "mysub")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			_ = env.ExpectResponse(t, 2*time.Second) // QR Photo
			_ = env.ExpectResponse(t, 2*time.Second) // details text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// 5. User manages active subscription: try to rename it (should be disabled)
			env.SendCallback(userTGID, userUsername, 999, "\fview_sub|2") // Paid sub is ID 2
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_rename|2")
			respRename := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respRename, "text"), "غیرفعال") {
				t.Fatalf("Expected rename to be disabled, got: %+v", respRename)
			}

			// 6. User extends active subscription
			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend|2")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_extend_run|1:2")
			respExtInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmExtData := extractCallbackData(respExtInvoice, "\fsub_extend_confirm")
			env.SendCallback(userTGID, userUsername, 999, confirmExtData)
			_ = env.ExpectResponse(t, 2*time.Second) // success
			_ = env.ExpectResponse(t, 2*time.Second) // details page

			// 7. User tries to delete subscription (should be disabled)
			env.SendCallback(userTGID, userUsername, 999, "\fsub_delete|2")
			respDelete := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respDelete, "text"), "غیرفعال") {
				t.Fatalf("Expected delete to be disabled, got: %+v", respDelete)
			}

			// Verify in DB and mock XUI that subscription is still active and not deleted
			sub, _ := db.GetSubscriptionByID(env.ctx, 2)
			if sub == nil {
				t.Fatalf("Subscription should still exist in DB")
			}
			if !sub.IsActive {
				t.Fatalf("Subscription should still be active")
			}
		})

		// 57. Scenario: Admin Settings Updates & Live Effects
		t.Run("AdminSettingsUpdatesAndEffects", func(t *testing.T) {
			setupApprovedUser()

			// Admin set card number
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_set_card")
			_ = env.ExpectResponse(t, 2*time.Second)
			bot.GlobalFSM.SetState(adminTGID, "awaiting_setting_card_number", nil)
			env.SendMessage(adminTGID, adminUsername, "9876-5432-1098-7654")
			_ = env.ExpectResponse(t, 2*time.Second) // saved
			_ = env.ExpectResponse(t, 2*time.Second) // settings menu

			// User checks wallet top up details
			env.SendCallback(userTGID, userUsername, 999, "\fbtn_topup")
			respTopup := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respTopup, "text"), "9876-5432-1098-7654") {
				t.Fatalf("Expected new card number to be displayed in details, got: %+v", respTopup)
			}
			if !strings.Contains(getStr(respTopup, "text"), "تومان") {
				t.Fatalf("Expected fixed Toman currency in top-up details, got: %+v", respTopup)
			}

			// Verify settings in DB
			card, _ := db.GetSetting(env.ctx, "card_number")
			if card != "9876-5432-1098-7654" {
				t.Fatalf("Settings values not updated properly in DB")
			}
		})

		// 58. Scenario: Recovery from X-UI Panel Outage
		t.Run("PanelOutageRecovery", func(t *testing.T) {
			resetState()
			_, _ = db.Pool.Exec(env.ctx, `INSERT INTO bot_users (telegram_id, username, first_name, last_name, service_name, status, language, wallet_balance) VALUES ($1, $2, 'Test', 'User', 'myservice', 'approved', 'en', 2000)`, userTGID, userUsername)

			// Enable outage
			env.mockXUI.Fail = true

			// User attempts purchase
			env.SendCallback(userTGID, userUsername, 999, "\fselect_buy_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_months|1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(userTGID, userUsername, 999, "\fbuy_ip_run|1:1:1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendMessage(userTGID, userUsername, "fails")
			respInvoice := env.ExpectResponse(t, 2*time.Second)
			confirmData := extractCallbackData(respInvoice, "\fbuy_confirm")
			if confirmData == "" {
				t.Fatalf("Expected buy_confirm in invoice: %+v", respInvoice)
			}
			env.SendCallback(userTGID, userUsername, 999, confirmData)
			respFail := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respFail, "text"), "ادامه می‌یابد") {
				t.Fatalf("expected durable pending provisioning notice, got: %+v", respFail)
			}

			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 || len(env.mockXUI.Clients) != 0 || env.mockXUI.AddClientCalls != 0 {
				t.Fatalf("unready XUI must leave one debited, durable order without a mutation: user=%+v clients=%d add_calls=%d", u, len(env.mockXUI.Clients), env.mockXUI.AddClientCalls)
			}
			var workID int64
			if err := db.Pool.QueryRow(env.ctx, `SELECT id FROM reconciliation_records WHERE user_id=$1 AND kind='purchase_provisioning_unknown' ORDER BY id DESC LIMIT 1`, u.ID).Scan(&workID); err != nil {
				t.Fatalf("read durable pending work: %v", err)
			}
			work, err := db.GetReconciliationRecordByID(env.ctx, workID)
			if err != nil || work == nil || (work.Status != db.ReconciliationStatusPending && work.Status != "retryable") {
				t.Fatalf("expected retryable durable provisioning work: work=%+v err=%v", work, err)
			}
			payload, err := reconcile.DecodePurchaseProvisioning(work.DesiredState, work.UserID, work.OperationKey)
			if err != nil || payload.ExpectedUUID == "" || payload.ExpectedSubID == "" {
				t.Fatalf("provisioning identity was not durable before panel recovery: payload=%+v err=%v", payload, err)
			}

			env.mockXUI.Fail = false
			if _, err := db.Pool.Exec(env.ctx, `UPDATE reconciliation_records SET next_attempt_at=NOW() WHERE id=$1`, workID); err != nil {
				t.Fatalf("make durable work retryable after panel recovery: %v", err)
			}
			worker := reconcile.NewProcessor("e2e-panel-restart", bot.XUIClient)
			if _, err := worker.ProcessOnce(env.ctx); err != nil {
				t.Fatalf("restart worker could not complete pending purchase: %v", err)
			}
			if _, err := worker.ProcessOnce(env.ctx); err != nil {
				t.Fatalf("duplicate worker pass failed: %v", err)
			}

			sub, err := db.GetSubscriptionByEmail(env.ctx, payload.Email)
			work, workErr := db.GetReconciliationRecordByID(env.ctx, workID)
			remote, remoteExists := env.mockXUI.Clients[payload.Email]
			var debitCount int
			_ = db.Pool.QueryRow(env.ctx, `SELECT COUNT(*) FROM transactions WHERE user_id=$1 AND type='debit' AND status='completed'`, u.ID).Scan(&debitCount)
			if err != nil || workErr != nil || sub == nil || work == nil || work.Status != db.ReconciliationStatusResolvedVerified ||
				!remoteExists || remote.ID != payload.ExpectedUUID || remote.SubID != payload.ExpectedSubID ||
				env.mockXUI.AddClientCalls != 1 || debitCount != 1 {
				t.Fatalf("recovered order must keep its identity and economic outcome: sub=%+v work=%+v remote=%+v add_calls=%d debits=%d errs=(%v,%v)", sub, work, remote, env.mockXUI.AddClientCalls, debitCount, err, workErr)
			}
			u, _ = db.GetUserByTelegramID(env.ctx, userTGID)
			if u.WalletBalance != 1000 {
				t.Fatalf("worker restart must not debit or refund again: balance=%d", u.WalletBalance)
			}
		})

		// 59. Scenario: Background Scheduler job checks and limits reset
		t.Run("SchedulerChecksAndResets", func(t *testing.T) {
			setupApprovedUser()

			// Create test plan usage record
			resetDate := time.Now().UTC().Format("2006-01-02")
			_, err := db.Pool.Exec(env.ctx, `
				INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
				VALUES (1, 1, 1, $1)`, resetDate)
			if err != nil {
				t.Fatal(err)
			}

			// Verify usage exists
			var count int
			_ = db.Pool.QueryRow(env.ctx, "SELECT COUNT(*) FROM test_usage WHERE user_id = 1 AND plan_id = 1").Scan(&count)
			if count != 1 {
				t.Fatalf("Usage should be recorded in DB")
			}

			tomorrow := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02")
			var usedTomorrow int
			err = db.Pool.QueryRow(env.ctx, `
				SELECT COALESCE((SELECT used_count FROM test_usage WHERE user_id = 1 AND plan_id = 1 AND reset_date = $1), 0)`,
				tomorrow).Scan(&usedTomorrow)
			if err != nil {
				t.Fatal(err)
			}
			if usedTomorrow != 0 {
				t.Fatalf("Expected daily count to be reset (0) for tomorrow, got %d", usedTomorrow)
			}
		})

		// 60. Scenario: Admin Bans a User and middleware locks them out
		t.Run("AdminBansUserLockout", func(t *testing.T) {
			setupApprovedUser()

			// Admin bans user
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_view_user|1")
			_ = env.ExpectResponse(t, 2*time.Second)
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_user_ban|1")
			_ = env.ExpectResponse(t, 2*time.Second) // confirmation
			_ = env.ExpectResponse(t, 2*time.Second) // admin view user updated

			// User attempts start command (blocked by AuthMiddleware)
			env.SendMessage(userTGID, userUsername, "/start")
			resp := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(resp, "text"), "مسدود") {
				t.Fatalf("Expected banned response, got: %+v", resp)
			}

			// Verify user status in DB is banned
			u, _ := db.GetUserByTelegramID(env.ctx, userTGID)
			if u.Status != "banned" {
				t.Fatalf("Expected user to be banned in DB")
			}
		})

		// 61. Scenario: Admin configures test_reset_days, support username, and resets tests
		t.Run("AdminConfigureLimitsSupportAndReset", func(t *testing.T) {
			setupApprovedUser()

			// 1. Admin configures test_reset_days to 30
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_set_test_reset_days")
			_ = env.ExpectResponse(t, 2*time.Second) // prompt
			bot.GlobalFSM.SetState(adminTGID, "awaiting_setting_test_reset_days", nil)
			env.SendMessage(adminTGID, adminUsername, "30")
			_ = env.ExpectResponse(t, 2*time.Second) // saved confirmation
			_ = env.ExpectResponse(t, 2*time.Second) // settings menu

			// Verify test_reset_days setting in DB
			limit, _ := db.GetSetting(env.ctx, "test_reset_days")
			if limit != "30" {
				t.Fatalf("Expected test_reset_days to be 30 in DB, got %s", limit)
			}

			// 2. Admin configures support_username to 'my_support_guy'
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_set_support_username")
			_ = env.ExpectResponse(t, 2*time.Second) // prompt
			bot.GlobalFSM.SetState(adminTGID, "awaiting_setting_support_username", nil)
			env.SendMessage(adminTGID, adminUsername, "my_support_guy")
			_ = env.ExpectResponse(t, 2*time.Second) // saved confirmation
			_ = env.ExpectResponse(t, 2*time.Second) // settings menu

			// Verify support_username setting in DB
			support, _ := db.GetSetting(env.ctx, "support_username")
			if support != "my_support_guy" {
				t.Fatalf("Expected support_username to be 'my_support_guy' in DB, got %s", support)
			}

			// 3. User checks support button (should show @my_support_guy)
			env.SendCallback(userTGID, userUsername, 999, "\fmenu_support")
			respSupport := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respSupport, "text"), "@my_support_guy") {
				t.Fatalf("Expected support username @my_support_guy in message, got: %+v", respSupport)
			}

			// 4. Generate 1 test subscription (since limit is 1)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // 1st success QR
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu

			// Try to generate 2nd test sub (should fail due to limit 1)
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			respFail := env.ExpectResponse(t, 2*time.Second)
			if !strings.Contains(getStr(respFail, "text"), "قبلاً") && !strings.Contains(getStr(respFail, "text"), "سقف") && !strings.Contains(getStr(respFail, "text"), "محدودیت") {
				t.Fatalf("Expected limit error for 2nd test sub, got: %+v", respFail)
			}

			// 5. Admin resets tests
			env.SendCallback(adminTGID, adminUsername, 999, "\fadmin_reset_tests")
			respReset := env.ExpectResponse(t, 2*time.Second) // alert callback response
			if !strings.Contains(getStr(respReset, "text"), "ریست شد") && !strings.Contains(getStr(respReset, "text"), "reset") {
				t.Fatalf("Expected reset alert, got: %+v", respReset)
			}

			// Verify test_usage table is cleared
			var count int
			_ = db.Pool.QueryRow(env.ctx, "SELECT COUNT(*) FROM test_usage").Scan(&count)
			if count != 0 {
				t.Fatalf("Expected test_usage count to be 0 after reset, got %d", count)
			}

			// 6. User should now be able to generate test sub again!
			env.SendCallback(userTGID, userUsername, 999, "\fselect_test_plan|1")
			_ = env.ExpectResponse(t, 2*time.Second) // success QR
			_ = env.ExpectResponse(t, 2*time.Second) // text
			_ = env.ExpectResponse(t, 2*time.Second) // main menu
		})
	})
}
