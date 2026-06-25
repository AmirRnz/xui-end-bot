package handlers

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

func RegisterTestSub(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_test_sub", HandleTestSubFlow, auth)
	b.Handle("\fselect_test_plan", HandleSelectTestPlan, auth)
	b.Handle("\fts_custom", HandleTestCustom, auth)
	b.Handle("\fts_random", HandleTestRandom, auth)
	b.Handle("\fts_multi", HandleMultipleTestsChoice, auth)
	b.Handle("\fts_multi_custom", HandleMultipleTestsCustom, auth)
	b.Handle("\fts_multi_random", HandleMultipleTestsRandom, auth)
	b.Handle("\fts_multi_run", HandleMultipleTestsRun, auth)

	// Backward-compatible callbacks
	b.Handle("\fmultiple_tests_custom", HandleMultipleTestsCustom, auth)
	b.Handle("\fmultiple_tests_random", HandleMultipleTestsRandom, auth)
}

func HandleTestSubFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	plans, err := db.GetTestPlansForUser(context.Background(), user.ID, false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌های تست.")
	}
	if len(plans) == 0 {
		return maybeEditOrSend(c, "در حال حاضر هیچ طرح تستی موجود نیست.")
	}

	globalDesc, _ := db.GetSetting(context.Background(), "test_global_description")
	var text strings.Builder
	text.WriteString("🧪 *اشتراک‌های تست رایگان*\n\n")
	if strings.TrimSpace(globalDesc) != "" {
		text.WriteString(globalDesc)
		text.WriteString("\n\n")
	}

	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(plans)+1)
	for _, plan := range plans {
		used, _ := db.GetTodayTestUsage(context.Background(), user.ID, plan.ID)
		limit := testLimitForUser(user, plan)
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		desc := plan.Description
		if desc == "" {
			desc = humanDuration(plan.ExpireSeconds)
		}
		text.WriteString(fmt.Sprintf("📦 *%s* — %s\nمصرف امروز: %d از %d (بازنشانی ساعت %s UTC)\n\n",
			plan.Name, desc, used, limit, nextUTCReset()))
		btnLabel := fmt.Sprintf("%s (%d از %d باقی‌مانده)", plan.Name, remaining, limit)
		rows = append(rows, menu.Row(menu.Data(btnLabel, "select_test_plan", fmt.Sprintf("%d", plan.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, strings.TrimSpace(text.String()), menu)
}

func HandleSelectTestPlan(c telebot.Context) error {
	planID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("طرح تست نامعتبر است.")
	}
	plan, err := db.GetTestPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح تست یافت نشد.")
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("✏️ نام دلخواه انگلیسی", "ts_custom", fmt.Sprintf("%d", plan.ID)),
			menu.Data("🎲 نام تصادفی انگلیسی", "ts_random", fmt.Sprintf("%d", plan.ID)),
		),
		menu.Row(menu.Data("📦 ایجاد چند تست همزمان", "ts_multi", fmt.Sprintf("%d", plan.ID))),
		menu.Row(menu.Data("« بازگشت", "menu_test_sub")),
	)
	return maybeEditOrSend(c, fmt.Sprintf("🧪 *%s*\n%s\n\nمدت زمان: %s\n\nنحوه نام‌گذاری اشتراک تست خود را انتخاب کنید:",
		plan.Name, plan.Description, humanDuration(plan.ExpireSeconds)), menu)
}

func HandleTestCustom(c telebot.Context) error {
	user := userFromContext(c)
	planID, err := parseInt64(callbackPayload(c))
	if user == nil || err != nil {
		return c.Send("طرح تست نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_test_custom_name", map[string]interface{}{"plan_id": fmt.Sprintf("%d", planID)})
	return maybeEditOrSend(c, "لطفا نام انگلیسی دلخواه خود را ارسال کنید:")
}

func ProcessTestCustomName(c telebot.Context, customName string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("هیچ طرح تستی انتخاب نشده است.")
	}
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	bot.FSM.ClearState(user.TelegramID)
	name := sanitizeName(customName)
	if name == "" {
		return c.Send("نام نامعتبر است. از حروف، اعداد یا خط تیره انگلیسی استفاده کنید.")
	}
	email := fmt.Sprintf("test_%s_%s", name, randomToken(4))
	return generateTestSubscription(c, user, planID, email)
}

func HandleTestRandom(c telebot.Context) error {
	user := userFromContext(c)
	planID, err := parseInt64(callbackPayload(c))
	if user == nil || err != nil {
		return c.Send("طرح تست نامعتبر است.")
	}
	email := fmt.Sprintf("test_%s_%s", randomName(), randomToken(4))
	return generateTestSubscription(c, user, planID, email)
}

func HandleMultipleTestsChoice(c telebot.Context) error {
	planID, _ := parseInt64(callbackPayload(c))
	if planID == 0 {
		planID = 1
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("نام پایه دلخواه", "ts_multi_custom", fmt.Sprintf("%d", planID)), menu.Data("نام‌های تصادفی", "ts_multi_random", fmt.Sprintf("%d", planID))),
	)
	return maybeEditOrSend(c, "نحوه نام‌گذاری برای تست‌های چندگانه را انتخاب کنید:", menu)
}

func HandleMultipleTestsCustom(c telebot.Context) error {
	user := userFromContext(c)
	planID, _ := parseInt64(callbackPayload(c))
	if planID == 0 {
		planID = 1
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_multiple_tests_base_name", map[string]interface{}{"plan_id": fmt.Sprintf("%d", planID)})
	return maybeEditOrSend(c, "لطفا نام پایه انگلیسی را ارسال کنید:")
}

func HandleMultipleTestsRandom(c telebot.Context) error {
	user := userFromContext(c)
	planID, _ := parseInt64(callbackPayload(c))
	if planID == 0 {
		planID = 1
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_multiple_tests_count", map[string]interface{}{"naming": "random", "plan_id": fmt.Sprintf("%d", planID)})
	return maybeEditOrSend(c, "چه تعداد اشتراک تست می‌خواهید بسازید؟")
}

func ProcessMultipleTestsBaseName(c telebot.Context, baseName string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	planID := "1"
	if state != nil && state.Data["plan_id"] != nil {
		planID = fmt.Sprintf("%v", state.Data["plan_id"])
	}
	baseName = sanitizeName(baseName)
	if baseName == "" {
		return c.Send("نام پایه نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_multiple_tests_count", map[string]interface{}{"naming": "custom", "baseName": baseName, "plan_id": planID})
	return c.Send("چه تعداد اشتراک تست می‌خواهید بسازید؟")
}

func ProcessMultipleTestsCount(c telebot.Context, countStr string) error {
	count, err := strconv.Atoi(strings.TrimSpace(countStr))
	if err != nil || count <= 0 {
		return c.Send("تعداد وارد شده نامعتبر است. یک عدد مثبت وارد کنید.")
	}
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند تولید تست فعالی یافت نشد.")
	}
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	if planID == 0 {
		planID = 1
	}
	naming := fmt.Sprintf("%v", state.Data["naming"])
	base := sanitizeName(fmt.Sprintf("%v", state.Data["baseName"]))
	bot.FSM.ClearState(user.TelegramID)
	return generateMultipleTests(c, user, planID, count, naming, base)
}

func HandleMultipleTestsRun(c telebot.Context) error {
	user := userFromContext(c)
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست تست چندگانه نامعتبر است.")
	}
	count, err := strconv.Atoi(parts[0])
	if err != nil || count <= 0 {
		return c.Send("تعداد نامعتبر است.")
	}
	planID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("طرح تست نامعتبر است.")
	}
	return generateMultipleTests(c, user, planID, count, "random", "")
}

func generateMultipleTests(c telebot.Context, user *db.User, planID int64, count int, naming, base string) error {
	unlock := bot.Locker.Lock(fmt.Sprintf("user_test:%d", user.ID))
	defer unlock()

	plan, err := db.GetTestPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح تست یافت نشد.")
	}
	used, _ := db.GetTodayTestUsage(context.Background(), user.ID, plan.ID)
	limit := testLimitForUser(user, plan)
	if used+count > limit {
		return c.Send(fmt.Sprintf("تعداد تست‌های درخواستی فراتر از سقف روزانه شماست. شما امروز حداکثر مجاز به دریافت %d تست دیگر هستید.", maxInt(0, limit-used)))
	}

	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return c.Send("این طرح تست هیچ کانکشن معتبری ندارد.")
	}

	type GeneratedSub struct {
		Email       string
		UUID        string
		SubID       string
		ExpireMilli int64
		ExpireAt    time.Time
	}
	generated := make([]GeneratedSub, 0, count)
	items := make([]xui.BulkCreateItem, 0, count)

	expireMilli := -int64(plan.ExpireSeconds * 1000)
	var expireAt time.Time

	for i := 0; i < count; i++ {
		name := randomName()
		if naming == "custom" && base != "" {
			name = fmt.Sprintf("%s_%d", base, i+1)
		}
		email := fmt.Sprintf("test_%s_%s", name, randomToken(4))
		
		for {
			existing, _ := db.GetSubscriptionByEmail(context.Background(), email)
			if existing == nil {
				break
			}
			name = randomName()
			if naming == "custom" && base != "" {
				name = fmt.Sprintf("%s_%d", base, i+1)
			}
			email = fmt.Sprintf("test_%s_%s", name, randomToken(4))
		}

		subID := makeSubID()
		clientUUID := makeClientUUID()
		client := newClientConfig(email, serviceGroup(user), user.TelegramID, plan.MaxDataBytes, expireMilli, 1, plan.Flow, subID, clientUUID)

		items = append(items, xui.BulkCreateItem{
			Client:     client,
			InboundIDs: inboundIDs,
		})

		generated = append(generated, GeneratedSub{
			Email:       email,
			UUID:        clientUUID,
			SubID:       subID,
			ExpireMilli: expireMilli,
			ExpireAt:    expireAt,
		})
	}

	bulkResp, err := bot.XUIClient.BulkCreate(items)
	if err != nil {
		log.Printf("XUI BulkCreate failed: %v. Refreshing cache and retrying...", err)
		if bot.XUIClient.Cache != nil {
			bot.XUIClient.Cache.RefreshSync()
			newInboundIDs := validInboundIDs(plan.InboundIDs)
			if !intSlicesEqual(newInboundIDs, inboundIDs) {
				if len(newInboundIDs) == 0 {
					return c.Send("خطا: بعد از بازخوانی کانکشن‌ها، کانکشن معتبری پیدا نشد.")
				}
				inboundIDs = newInboundIDs
				for idx := range items {
					items[idx].InboundIDs = inboundIDs
				}
				bulkResp, err = bot.XUIClient.BulkCreate(items)
			}
		}
	}

	if err != nil {
		return c.Send("خطا در ایجاد گروهی کلاینت‌های تست در پنل: " + err.Error())
	}

	skippedEmails := make(map[string]bool)
	if bulkResp != nil {
		for _, s := range bulkResp.Skipped {
			skippedEmails[s.Email] = true
			log.Printf("BulkCreate skipped client %s: %s", s.Email, s.Reason)
		}
	}

	var text strings.Builder
	text.WriteString("✅ *اشتراک‌های تست با موفقیت ساخته شدند!*\n\n")

	actualCreated := 0
	for _, gen := range generated {
		if skippedEmails[gen.Email] {
			continue
		}

		planIDInt := int(plan.ID)
		sub := &db.Subscription{
			UserID:      user.ID,
			PlanID:      &planIDInt,
			ClientEmail: gen.Email,
			ClientUUID:  gen.UUID,
			SubID:       gen.SubID,
			Status:      "active",
			PlanType:    db.PlanTypeTest,
			DisplayName: gen.Email,
			IPLimit:     1,
			ExpireTime:  &gen.ExpireMilli,
			IsActive:    true,
			StartDate:   nowUTC(),
			EndDate:     gen.ExpireAt,
		}

		if err := db.CreateSubscription(context.Background(), sub); err != nil {
			log.Printf("[CRITICAL] Failed to save test sub to DB for %s: %v. Rolling back panel client.", gen.Email, err)
			emailToRollback := gen.Email
			go func() {
				var deleteErr error
				for i := 0; i < 5; i++ {
					if deleteErr = bot.XUIClient.DeleteClient(emailToRollback); deleteErr == nil {
						log.Printf("Rollback successful: Deleted test client %s from panel", emailToRollback)
						return
					}
					time.Sleep(time.Duration(1<<i) * time.Second)
				}
				log.Printf("[ALERT] CRITICAL: Failed to delete test client %s from panel after 5 retries: %v", emailToRollback, deleteErr)
			}()
			continue
		}

		actualCreated++

		subLink := bot.XUIClient.SubscriptionURLFor(gen.SubID)
		text.WriteString(fmt.Sprintf("📋 *نام:* `%s`\n", gen.Email))
		text.WriteString(fmt.Sprintf("🔗 *لینک اشتراک:* %s\n\n", subLink))
	}

	if actualCreated > 0 {
		_ = db.IncrementTestUsage(context.Background(), user.ID, plan.ID, actualCreated)
		_ = c.Send(text.String())
	} else {
		_ = c.Send("❌ خطا در ایجاد کلاینت‌های تست.")
	}

	return showMainMenu(c, user)
}

func generateTestSubscription(c telebot.Context, user *db.User, planID int64, email string) error {
	unlock := bot.Locker.Lock(fmt.Sprintf("user_test:%d", user.ID))
	defer unlock()

	plan, err := db.GetTestPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح تست یافت نشد.")
	}
	used, _ := db.GetTodayTestUsage(context.Background(), user.ID, plan.ID)
	limit := testLimitForUser(user, plan)
	if used >= limit {
		return c.Send(fmt.Sprintf("محدودیت روزانه تست به پایان رسیده است. شما برای این طرح امروز مجاز به دریافت حداکثر %d تست هستید.", limit))
	}
	if err := createAndSendTest(c, user, plan, email); err != nil {
		return err
	}
	_ = db.IncrementTestUsage(context.Background(), user.ID, plan.ID, 1)
	return showMainMenu(c, user)
}

func createAndSendTest(c telebot.Context, user *db.User, plan *db.TestPlan, email string) error {
	if bot.XUIClient == nil {
		return c.Send("خطا: کلاینت x-ui متصل نیست.")
	}
	if existing, _ := db.GetSubscriptionByEmail(context.Background(), email); existing != nil {
		return c.Send("نام تولید شده قبلا انتخاب شده است. لطفا مجددا تلاش کنید.")
	}

	expireMilli := -int64(plan.ExpireSeconds * 1000)
	var expireAt time.Time
	subID := makeSubID()
	clientUUID := makeClientUUID()
	client := newClientConfig(email, serviceGroup(user), user.TelegramID, plan.MaxDataBytes, expireMilli, 1, plan.Flow, subID, clientUUID)
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return c.Send("این طرح تست هیچ کانکشن معتبری ندارد.")
	}

	err := bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
	if err != nil {
		log.Printf("XUI AddClient failed: %v. Refreshing cache and retrying...", err)
		if bot.XUIClient.Cache != nil {
			bot.XUIClient.Cache.RefreshSync()
			newInboundIDs := validInboundIDs(plan.InboundIDs)
			if !intSlicesEqual(newInboundIDs, inboundIDs) {
				if len(newInboundIDs) == 0 {
					return c.Send("خطا: بعد از بازخوانی کانکشن‌ها، کانکشن معتبری پیدا نشد.")
				}
				inboundIDs = newInboundIDs
				err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
			}
		}
	}
	if err != nil {
		return c.Send("خطا در ایجاد اشتراک تست در پنل: " + err.Error())
	}

	planID := int(plan.ID)
	sub := &db.Subscription{
		UserID:      user.ID,
		PlanID:      &planID,
		ClientEmail: email,
		ClientUUID:  clientUUID,
		SubID:       subID,
		Status:      "active",
		PlanType:    db.PlanTypeTest,
		DisplayName: email,
		IPLimit:     1,
		ExpireTime:  &expireMilli,
		IsActive:    true,
		StartDate:   nowUTC(),
		EndDate:     expireAt,
	}
	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		_ = bot.XUIClient.DeleteClient(email)
		return c.Send("خطا در ذخیره‌سازی اشتراک تست. عملیات در پنل خنثی شد.")
	}

	links, err := bot.XUIClient.GetSubscriptionLinks(subID)
	var subLink string
	if err == nil {
		for _, l := range links {
			if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
				subLink = l
				break
			}
		}
	}
	if subLink == "" {
		subLink = bot.XUIClient.SubscriptionURLFor(subID)
	}
	var dataLimitStr string
	if plan.MaxDataBytes == 0 {
		dataLimitStr = "نامحدود"
	} else {
		dataLimitStr = fmt.Sprintf("%.2f گیگابایت", float64(plan.MaxDataBytes)/1073741824)
	}
	durationStr := humanDuration(plan.ExpireSeconds)
	detailsMsg := fmt.Sprintf("✅ اشتراک تست رایگان شما آماده شد!\n📦 طرح: %s\n⏱️ مدت اعتبار: %s (پس از اولین اتصال شروع می‌شود)\n📊 حجم مجاز: %s", plan.Name, durationStr, dataLimitStr)

	if err := sendSubscriptionResult(c, subLink, detailsMsg); err != nil {
		_ = c.Send(detailsMsg + "\n`" + subLink + "`", telebot.ModeMarkdown)
	}
	return nil
}

func testLimitForUser(user *db.User, plan *db.TestPlan) int {
	if user != nil && user.IsApproved() {
		return plan.MaxPerDay
	}
	limitStr, _ := db.GetSetting(context.Background(), "unapproved_test_limit")
	limit, err := strconv.Atoi(strings.TrimSpace(limitStr))
	if err != nil {
		return 1
	}
	if limit < 0 {
		return 0
	}
	if plan.MaxPerDay > 0 && limit > plan.MaxPerDay {
		return plan.MaxPerDay
	}
	return limit
}

func nextUTCReset() string {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return next.Format("15:04")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

