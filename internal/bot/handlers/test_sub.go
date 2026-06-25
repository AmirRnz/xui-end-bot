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

	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	email := fmt.Sprintf("test_%s_%s", randomName(), randomToken(4))
	return generateTestSubscription(c, user, plan.ID, email)
}

func HandleTestCustom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessTestCustomName(c telebot.Context, customName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func HandleTestRandom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsChoice(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsCustom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsRandom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessMultipleTestsBaseName(c telebot.Context, baseName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func ProcessMultipleTestsCount(c telebot.Context, countStr string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func HandleMultipleTestsRun(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func generateMultipleTests(c telebot.Context, user *db.User, planID int64, count int, naming, base string) error {
	return c.Send("این امکان غیرفعال شده است.")
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
		return c.Send(fmt.Sprintf("محدودیت تست‌های رایگان شما به پایان رسیده است. شما مجاز به دریافت حداکثر %d تست هستید.", limit))
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
	comment := fmt.Sprintf("created by xui-end-bot, %s, %s", plan.Name, userIdentifier(user))
	client := newClientConfig(email, serviceGroup(user), user.TelegramID, plan.MaxDataBytes, expireMilli, 1, plan.Flow, subID, clientUUID, comment)
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

	if plan.Description != "" {
		detailsMsg += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.Description)
	}

	if err := sendSubscriptionResult(c, subLink, detailsMsg); err != nil {
		_ = c.Send(detailsMsg + "\n`" + subLink + "`", telebot.ModeMarkdown)
	}
	return nil
}

func testLimitForUser(user *db.User, plan *db.TestPlan) int {
	limitKey := "test_limit"
	if user != nil && !user.IsApproved() {
		limitKey = "unapproved_test_limit"
	}
	limitStr, _ := db.GetSetting(context.Background(), limitKey)
	limit, err := strconv.Atoi(strings.TrimSpace(limitStr))
	if err != nil {
		return 1
	}
	if limit < 0 {
		return 0
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

