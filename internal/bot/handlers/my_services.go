package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/bot/persian"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

const servicesPageSize = 6

func RegisterMyServices(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_my_services", HandleMyServicesFlow, auth)
	b.Handle("\fsvc_page", HandleMyServicesPage, auth)
	b.Handle("\fsvc_claim", HandleClaimSubscriptionPrompt, auth)
	b.Handle("\fview_sub", HandleViewSubscription, auth)
	b.Handle("\fsub_get_link", HandleGetLink, auth)
	b.Handle("\fsub_toggle", HandleToggleSubscription, auth)
	b.Handle("\fsub_rename", HandleSubscriptionRenamePrompt, auth)
	b.Handle("\fsub_delete_confirm", HandleDeleteSubscriptionConfirm, auth)
	b.Handle("\fsub_delete", HandleDeleteSubscription, auth)

	// IP limit upgrading handlers
	b.Handle("\fsub_limit", HandleSubscriptionLimitMenu, auth)
	b.Handle("\fsub_limit_set", HandleSubscriptionLimitConfirmPrompt, auth)
	b.Handle("\fsub_limit_confirm", HandleSubscriptionLimitSetWallet, auth)
	b.Handle("\fsub_limit_direct", HandleSubscriptionLimitSetDirect, auth)

	// Extension handlers
	b.Handle("\fsub_extend", HandleSubscriptionExtendMenu, auth)
	b.Handle("\fsub_extend_custom", HandleExtendCustomMonthsPrompt, auth)
	b.Handle("\fsub_extend_run", HandleExtendSubscriptionConfirmPrompt, auth)
	b.Handle("\fsub_extend_confirm", HandleExtendSubscriptionWallet, auth)
	b.Handle("\fsub_extend_direct", HandleExtendSubscriptionDirect, auth)
}

// ─── Subscription List (paginated) ───────────────────────────────────────────

func HandleMyServicesFlow(c telebot.Context) error {
	return showServicesPage(c, 0)
}

func HandleMyServicesPage(c telebot.Context) error {
	page, _ := strconv.Atoi(callbackPayload(c))
	return showServicesPage(c, page)
}

func showServicesPage(c telebot.Context, page int) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	subs, err := db.GetManageableSubscriptionsByUserID(context.Background(), user.ID)
	if err != nil {
		return c.Send("خطا در بارگذاری اشتراک‌ها.")
	}
	var paidSubs []*db.Subscription
	for _, sub := range subs {
		if sub.PlanType == db.PlanTypePaid {
			paidSubs = append(paidSubs, sub)
		}
	}
	subs = paidSubs

	if len(subs) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("➕ ثبت اشتراک خریداری شده", "svc_claim")),
			menu.Row(menu.Data("« بازگشت", "menu_main")),
		)
		return maybeEditOrSend(c, "📋 شما در حال حاضر هیچ اشتراکی ندارید.\nجهت شروع می‌توانید از گزینه‌های 🧪 تست رایگان یا 💼 خرید سرویس استفاده کنید. یا اگر از قبل اشتراکی دارید، آن را ثبت کنید تا در ربات نمایش داده شود.", menu)
	}

	totalPages := (len(subs) + servicesPageSize - 1) / servicesPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * servicesPageSize
	end := start + servicesPageSize
	if end > len(subs) {
		end = len(subs)
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("📋 **سرویس‌های من** (صفحه %d از %d)\n\n", page+1, totalPages))
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, sub := range subs[start:end] {
		icon := "🔴"
		if sub.IsActive {
			icon = "🟢"
		}
		var expires string
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			expires = "شروع پس از اولین اتصال"
		} else {
			expires = "تاریخ انقضا " + sub.EndDate.Format("2006-01-02")
		}
		text.WriteString(fmt.Sprintf("%s %s — %s\n", icon, sub.ClientEmail, expires))
		rows = append(rows, menu.Row(menu.Data(icon+" "+sub.DisplayName, "view_sub", fmt.Sprintf("%d", sub.ID))))
	}

	navRow := []telebot.Btn{}
	if page > 0 {
		navRow = append(navRow, menu.Data("◀️ قبلی", "svc_page", fmt.Sprintf("%d", page-1)))
	}
	if page < totalPages-1 {
		navRow = append(navRow, menu.Data("بعدی ▶️", "svc_page", fmt.Sprintf("%d", page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows, menu.Row(menu.Data("➕ ثبت اشتراک خریداری شده", "svc_claim")))
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

// ─── Subscription Detail ──────────────────────────────────────────────────────

func HandleViewSubscription(c telebot.Context) error {
	sub, user, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	return showSubscriptionDetail(c, user, sub)
}

func showSubscriptionDetail(c telebot.Context, user *db.User, sub *db.Subscription) error {
	statusIcon := "🔴 غیرفعال"
	if sub.IsActive {
		statusIcon = "🟢 فعال"
	}

	var expiryStr = "انقضا: نامحدود"
	var trafficStr = ""

	if bot.XUIClient != nil {
		if traffic, err := bot.XUIClient.GetClientTraffic(sub.ClientEmail); err == nil && traffic != nil {
			downGB := float64(traffic.Down) / 1073741824
			upGB := float64(traffic.Up) / 1073741824
			usedGB := (float64(traffic.Down) + float64(traffic.Up)) / 1073741824

			if sub.TrafficLimitBytes > 0 {
				limitGB := float64(sub.TrafficLimitBytes) / 1073741824
				trafficStr = fmt.Sprintf("ترافیک: مصرف شده %.2f گیگابایت از %.2f گیگابایت (دانلود %.2f / آپلود %.2f)\n", usedGB, limitGB, downGB, upGB)
			} else {
				trafficStr = fmt.Sprintf("ترافیک: مصرف شده %.2f گیگابایت / نامحدود (دانلود %.2f / آپلود %.2f)\n", usedGB, downGB, upGB)
			}

			if traffic.ExpiryTime < 0 {
				durMs := -traffic.ExpiryTime
				days := durMs / (24 * 3600 * 1000)
				if days > 0 {
					expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d روز)", days)
				} else {
					hours := durMs / (3600 * 1000)
					expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
				}
			} else if traffic.ExpiryTime > 0 {
				expTime := time.UnixMilli(traffic.ExpiryTime)
				remaining := time.Until(expTime)
				days := int(remaining.Hours() / 24)
				if days >= 0 {
					expiryStr = fmt.Sprintf("انقضا: %s (%d روز باقی‌مانده)", expTime.Format("2006-01-02"), days)
				} else {
					expiryStr = fmt.Sprintf("انقضا: %s (منقضی شده)", expTime.Format("2006-01-02"))
				}
			}
		}
	}

	if expiryStr == "انقضا: نامحدود" && sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		durMs := -*sub.ExpireTime
		days := durMs / (24 * 3600 * 1000)
		if days > 0 {
			expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d روز)", days)
		} else {
			hours := durMs / (3600 * 1000)
			expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
		}
	} else if expiryStr == "انقضا: نامحدود" && !sub.EndDate.IsZero() {
		remaining := time.Until(sub.EndDate)
		days := int(remaining.Hours() / 24)
		if days >= 0 {
			expiryStr = fmt.Sprintf("انقضا: %s (%d روز باقی‌مانده)", sub.EndDate.Format("2006-01-02"), days)
		} else {
			expiryStr = fmt.Sprintf("انقضا: %s (منقضی شده)", sub.EndDate.Format("2006-01-02"))
		}
	}

	displayIPLimit := sub.IPLimit

	var text strings.Builder
	text.WriteString(fmt.Sprintf("📦 **%s**\n\n", sub.DisplayName))
	text.WriteString(fmt.Sprintf("📧 **ایمیل اشتراک:** `%s`\n", sub.ClientEmail))
	text.WriteString(fmt.Sprintf("⚡ **وضعیت سرویس:** %s\n", statusIcon))
	text.WriteString(fmt.Sprintf("👥 **IP همزمان:** %s\n", formatIPLimit(displayIPLimit)))
	text.WriteString("⏳ " + expiryStr + "\n")
	if trafficStr != "" {
		text.WriteString("📊 " + trafficStr)
	}

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(
			menu.Data("🔗 دریافت لینک اتصال", "sub_get_link", fmt.Sprintf("%d", sub.ID)),
		),
	}
	if sub.PlanType == db.PlanTypePaid {
		var hasIPUpgrade bool
		if plan, _ := paidPlanForSub(sub); plan != nil {
			if plan.MaxIPLimit > plan.BaseIPLimit && plan.BaseIPLimit > 0 {
				hasIPUpgrade = true
			}
		}
		if hasIPUpgrade {
			rows = append(rows, menu.Row(
				menu.Data("📶 افزایش IP همزمان", "sub_limit", fmt.Sprintf("%d", sub.ID)),
				menu.Data("⏳ تمدید سرویس", "sub_extend", fmt.Sprintf("%d", sub.ID)),
			))
		} else {
			rows = append(rows, menu.Row(
				menu.Data("⏳ تمدید سرویس", "sub_extend", fmt.Sprintf("%d", sub.ID)),
			))
		}
	}
	rows = append(rows,
		menu.Row(menu.Data("« بازگشت", "menu_my_services")),
	)
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

// ─── Get Link ─────────────────────────────────────────────────────────────────

func HandleGetLink(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	if bot.XUIClient == nil {
		return c.Send("خطا: پنل سرویس‌دهنده در دسترس نیست.")
	}
	links, err := bot.XUIClient.GetSubscriptionLinks(sub.SubID)
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
		subLink = bot.XUIClient.SubscriptionURLFor(sub.SubID)
	}
	var expiryStr string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		expiryStr = "شروع پس از اولین اتصال"
	} else {
		expiryStr = sub.EndDate.Format("2006-01-02 15:04 UTC")
	}
	detailsMsg := fmt.Sprintf("🔗 اشتراک: **%s**\n📅 تاریخ انقضا: %s", sub.DisplayName, expiryStr)
	return sendSubscriptionResult(c, subLink, detailsMsg)
}

// ─── Toggle ───────────────────────────────────────────────────────────────────

func HandleToggleSubscription(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

// ─── Rename ───────────────────────────────────────────────────────────────────

func HandleSubscriptionRenamePrompt(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessSubscriptionRename(c telebot.Context, newName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

// ─── Delete (with confirmation) ───────────────────────────────────────────────

func HandleDeleteSubscriptionConfirm(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleDeleteSubscription(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

// ─── Increase IP limit ────────────────────────────────────────────────────────

func HandleSubscriptionLimitMenu(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تغییر سقف IP همزمان فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مرتبط یافت نشد.")
	}
	displayIPLimit := sub.IPLimit

	if displayIPLimit == 0 || plan.MaxIPLimit == 0 {
		return c.Send("تعداد IP همزمان برای اشتراک شما نامحدود است.")
	}

	if displayIPLimit >= plan.MaxIPLimit {
		return c.Send(fmt.Sprintf("اشتراک شما در حال حاضر در حداکثر سقف IP همزمان مجاز طرح خود (%d اتصال IP همزمان) قرار دارد.", plan.MaxIPLimit))
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := displayIPLimit + 1; ip <= plan.MaxIPLimit; ip++ {
		months := monthsRemainingFrom(sub.EndDate)
		if months < 1 {
			months = 1
		}
		cost := calculateIPUpgradePrice(plan, displayIPLimit, ip, months)
		rows = append(rows, menu.Row(menu.Data(
			fmt.Sprintf("%d IP همزمان — هزینه: %s", ip, persian.FormatMoney(cost)),
			"sub_limit_set", fmt.Sprintf("%d:%d", ip, sub.ID),
		)))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "view_sub", fmt.Sprintf("%d", sub.ID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("📶 ارتقای تعداد اتصال‌های IP همزمان برای **%s**\nسقف فعلی: %s اتصال IP همزمان", sub.DisplayName, formatIPLimit(displayIPLimit)), menu)
}

func HandleSubscriptionLimitConfirmPrompt(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست تغییر سقف IP همزمان نامعتبر است.")
	}
	newLimit, _ := strconv.Atoi(parts[0])
	_, _ = parseInt64(parts[1])

	sub, user, ok := loadOwnedSubscriptionFromPair(c)
	if !ok {
		return nil
	}

	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	displayIPLimit := sub.IPLimit

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := calculateIPUpgradePrice(plan, displayIPLimit, newLimit, months)
	operationToken := newOperationToken()
	operationKey := operationKeyFromToken("wallet_upgrade_ip", operationToken)
	bot.FSM.SetState(user.TelegramID, "awaiting_ip_upgrade_confirm", map[string]interface{}{
		"subscription_id": fmt.Sprintf("%d", sub.ID),
		"ip_limit":        fmt.Sprintf("%d", newLimit),
		"operation_key":   operationKey,
		"operation_token": operationToken,
	})

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "sub_limit_confirm", fmt.Sprintf("%d:%d:%s", newLimit, sub.ID, operationToken)),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "sub_limit_direct", fmt.Sprintf("%d:%d:%s", newLimit, sub.ID, operationToken)),
		),
		menu.Row(
			menu.Data("❌ انصراف", "view_sub", fmt.Sprintf("%d", sub.ID)),
		),
	)

	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **ارتقای IP همزمان سرویس %s**\n\nسقف جدید: %s اتصال IP همزمان\nسقف فعلی: %s اتصال IP همزمان\nهزینه ارتقا (تا پایان دوره): **%s**\n\nموجودی کیف پول شما: %s\n\nنحوه پرداخت ارتقا را انتخاب کنید:",
		sub.DisplayName, formatIPLimit(newLimit), formatIPLimit(displayIPLimit), persian.FormatMoney(cost), persian.FormatMoney(user.WalletBalance),
	), menu)
}

func HandleSubscriptionLimitSetWallet(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه ارتقا منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	newLimit, err := strconv.Atoi(parts[0])
	if err != nil || newLimit <= 0 {
		return c.Send("سقف جدید اتصال همزمان نامعتبر است.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID <= 0 {
		return c.Send("اشتراک نامعتبر است.")
	}
	token := strings.TrimSpace(parts[2])
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	if state := bot.FSM.GetState(user.TelegramID); state != nil {
		stateToken := fmt.Sprintf("%v", state.Data["operation_token"])
		if stateToken != "" && stateToken != token {
			return c.Send("این تایید مربوط به یک ارتقای قدیمی است. لطفا درخواست جدیدی ایجاد کنید.")
		}
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	if sub.ID != int(subID) || sub.PlanType != db.PlanTypePaid {
		return c.Send("درخواست ارتقا با وضعیت فعلی سرویس سازگار نیست.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد یا برای فروش غیرفعال است.")
	}
	if newLimit <= sub.IPLimit || newLimit > plan.MaxIPLimit {
		return c.Send("سقف جدید باید از سقف فعلی بیشتر و در محدوده مجاز طرح باشد. لطفا منو را دوباره باز کنید.")
	}
	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := calculateIPUpgradePrice(plan, sub.IPLimit, newLimit, months)
	if cost <= 0 {
		return c.Send("هزینه ارتقا نامعتبر است.")
	}
	desiredLimit := newLimit
	desiredActive := sub.IsActive
	err = db.DebitWalletForSubscriptionMutation(context.Background(), db.WalletSubscriptionMutationIntent{
		UserID: user.ID, SubscriptionID: sub.ID, Amount: cost,
		Description:     "IP limit increase for sub ID: " + strconv.Itoa(sub.ID),
		OperationKey:    operationKeyFromToken("wallet_upgrade_ip_v2", token),
		ExpectedIPLimit: sub.IPLimit, ExpectedExpireTime: sub.ExpireTime, ExpectedIsActive: sub.IsActive,
		ExpectedPlanUpdatedAt: plan.UpdatedAt,
		DesiredIPLimit:        &desiredLimit, DesiredIsActive: &desiredActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrInsufficientWalletBalance):
			return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه این ارتقا %s می‌باشد.", persian.FormatMoney(cost)))
		case errors.Is(err, db.ErrWalletOperationAlreadyApplied):
			return c.Send("این ارتقا قبلا ثبت شده یا در حال پردازش است.")
		case errors.Is(err, db.ErrWalletOperationConflict):
			log.Printf("[CRITICAL] wallet operation key collision while upgrading subscription %d for user %d: %v", sub.ID, user.ID, err)
			return c.Send("شناسه مالی با عملیات دیگری برخورد کرده است؛ برای جلوگیری از برداشت تکراری، این درخواست متوقف شد و نیازمند بررسی پشتیبانی است.")
		case errors.Is(err, db.ErrSubscriptionMutationStale), errors.Is(err, db.ErrSubscriptionMutationInProgress), errors.Is(err, db.ErrSubscriptionMutationInvalid):
			return c.Send("وضعیت سرویس یا طرح تغییر کرده است؛ موجودی کسر نشد. لطفا منو را دوباره باز کنید.")
		default:
			log.Printf("[ERROR] failed to queue wallet IP upgrade for subscription %d: %v", sub.ID, err)
			return c.Send("ثبت امن ارتقا ناموفق بود و مبلغی کسر نشد. لطفا کمی بعد دوباره تلاش کنید.")
		}
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "درخواست ارتقا ثبت شد."})
	return c.Send(fmt.Sprintf("درخواست ارتقای سقف همزمان با هزینه %s به‌طور پایدار ثبت شد. پس از همگام‌سازی با پنل اعمال می‌شود.", persian.FormatMoney(cost)))
}

func HandleSubscriptionLimitSetDirect(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه ارتقا منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	newLimit, err := strconv.Atoi(parts[0])
	if err != nil || newLimit <= 0 {
		return c.Send("سقف جدید اتصال همزمان نامعتبر است.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID <= 0 {
		return c.Send("اشتراک نامعتبر است.")
	}

	sub, user, ok := loadOwnedSubscriptionFromPair(c)
	if !ok {
		return nil
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد یا برای فروش غیرفعال است.")
	}
	if sub.ID != int(subID) || sub.Status != db.SubscriptionStatusActive || !sub.IsActive || newLimit <= sub.IPLimit || plan.MaxIPLimit <= 0 || newLimit > plan.MaxIPLimit {
		return c.Send("سقف درخواستی با وضعیت فعلی سرویس یا محدوده طرح سازگار نیست. لطفا منو را دوباره باز کنید.")
	}

	displayIPLimit := sub.IPLimit

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := calculateIPUpgradePrice(plan, displayIPLimit, newLimit, months)

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")

	callbackToken := strings.TrimSpace(parts[2])
	subID64 := int64(sub.ID)
	expectedExpiry := int64(0)
	if sub.ExpireTime != nil {
		expectedExpiry = *sub.ExpireTime
	}
	fsmData := map[string]interface{}{
		"type":                             "upgrade_ip",
		"subscription_id":                  fmt.Sprintf("%d", sub.ID),
		"ip_limit":                         fmt.Sprintf("%d", newLimit),
		"price_toman":                      fmt.Sprintf("%d", cost),
		"expected_ip_limit":                sub.IPLimit,
		"expected_expire_time_milli":       expectedExpiry,
		"expected_is_active":               sub.IsActive,
		"expected_subscription_updated_at": sub.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"expected_plan_updated_at":         plan.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"operation_key":                    operationKeyFromToken("direct_upgrade_ip", callbackToken),
		"operation_token":                  callbackToken,
	}
	intent := &db.PaymentIntent{
		UserID:               user.ID,
		IntentToken:          callbackToken,
		ActionType:           "upgrade_ip",
		SubscriptionID:       &subID64,
		AmountToman:          cost,
		Months:               months,
		IPLimit:              newLimit,
		ClientEmail:          sub.ClientEmail,
		ProvisioningSnapshot: fsmData,
		Status:               db.IntentStatusAwaitingReceipt,
	}
	createdIntent, err := db.CreatePaymentIntent(context.Background(), intent)
	if err != nil {
		log.Printf("[INTENT] Failed to create payment intent for user %d IP upgrade: %v", user.ID, err)
		return paymentIntentCreateFailure(c, user, "عملیات با خطا مواجه شد. لطفاً مجدداً تلاش کنید یا با پشتیبانی در ارتباط باشید.")
	}
	fsmData["intent_id"] = createdIntent.ID
	fsmData["operation_token"] = createdIntent.IntentToken
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", fsmData)

	var text strings.Builder
	text.WriteString("💳 **پرداخت مستقیم برای ارتقای تعداد اتصال‌های IP همزمان**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%s**\n\n", persian.FormatMoney(cost)))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز، **رسید پرداخت (تصویر فیش)** را در همینجا ارسال کنید تا ارتقا پس از تایید ادمین اعمال شود.")

	return maybeEditOrSend(c, text.String())
}

// ─── Extend Subscription ─────────────────────────────────────────────────────

func HandleSubscriptionExtendMenu(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تمدید فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	displayIPLimit := sub.IPLimit

	priceFor := func(months int) int64 {
		dataGB := int(sub.TrafficLimitBytes / 1073741824)
		return calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data(fmt.Sprintf("۱ ماهه — %s", persian.FormatMoney(priceFor(1))), "sub_extend_run", fmt.Sprintf("1:%d", sub.ID)),
			menu.Data(fmt.Sprintf("۳ ماهه — %s", persian.FormatMoney(priceFor(3))), "sub_extend_run", fmt.Sprintf("3:%d", sub.ID)),
		),
		menu.Row(
			menu.Data(fmt.Sprintf("۶ ماهه — %s", persian.FormatMoney(priceFor(6))), "sub_extend_run", fmt.Sprintf("6:%d", sub.ID)),
			menu.Data("✏️ مدت دلخواه", "sub_extend_custom", fmt.Sprintf("%d", sub.ID)),
		),
		menu.Row(menu.Data("« بازگشت", "view_sub", fmt.Sprintf("%d", sub.ID))),
	)

	var expiryLabel string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			durMs := -*sub.ExpireTime
			days := durMs / (24 * 3600 * 1000)
			if days > 0 {
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", days)
			} else {
				hours := durMs / (3600 * 1000)
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
			}
		} else {
			expiryLabel = "شروع پس از اولین اتصال"
		}
	} else {
		expiryLabel = sub.EndDate.Format("2006-01-02")
	}
	return maybeEditOrSend(c, fmt.Sprintf("⏳ تمدید سرویس **%s**\nتاریخ انقضای فعلی: %s\n\nمدت زمان تمدید را انتخاب کنید:", sub.DisplayName, expiryLabel), menu)
}

func HandleExtendCustomMonthsPrompt(c telebot.Context) error {
	user := userFromContext(c)
	subID, err := parseInt64(callbackPayload(c))
	if user == nil || err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_extend_months_text", map[string]interface{}{"sub_id": fmt.Sprintf("%d", subID)})
	return maybeEditOrSend(c, "لطفا تعداد ماه‌های مورد نظر برای تمدید را ارسال کنید (به عنوان مثال: 2):")
}

func ProcessExtendMonthsText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند تمدید فعالی یافت نشد.")
	}
	months, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || months <= 0 {
		return c.Send("تعداد ماه‌ها باید یک عدد مثبت باشد.")
	}
	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["sub_id"]))
	bot.FSM.ClearState(user.TelegramID)
	return showExtendConfirmation(c, user, int(subID), months)
}

func HandleExtendSubscriptionConfirmPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست تمدید نامعتبر است.")
	}
	months, err := strconv.Atoi(parts[0])
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت باشد.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}
	return showExtendConfirmation(c, user, int(subID), months)
}

func showExtendConfirmation(c telebot.Context, user *db.User, subID int, months int) error {
	sub, err := db.GetSubscriptionByID(context.Background(), subID)
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مرتبط یافت نشد.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := sub.IPLimit
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)

	operationToken := newOperationToken()
	bot.FSM.SetState(user.TelegramID, "awaiting_extend_confirm", map[string]interface{}{
		"subscription_id": fmt.Sprintf("%d", sub.ID),
		"months":          fmt.Sprintf("%d", months),
		"operation_key":   operationKeyFromToken("wallet_extend", operationToken),
		"operation_token": operationToken,
	})
	payload := fmt.Sprintf("%d:%d:%s", months, sub.ID, operationToken)
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "sub_extend_confirm", payload),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "sub_extend_direct", payload),
		),
		menu.Row(
			menu.Data("❌ انصراف", "view_sub", fmt.Sprintf("%d", sub.ID)),
		),
	)

	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **تمدید سرویس %s**\n\nمدت تمدید: %d ماه\nهزینه تمدید: **%s**\n\nموجودی کیف پول شما: %s\n\nنحوه پرداخت هزینه تمدید را انتخاب کنید:",
		sub.DisplayName, months, persian.FormatMoney(cost), persian.FormatMoney(user.WalletBalance),
	), menu)
}

func HandleExtendSubscriptionWallet(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه تمدید منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	months, err := strconv.Atoi(parts[0])
	if err != nil || months < 1 || months > 120 {
		return c.Send("مدت تمدید باید بین ۱ تا ۱۲۰ ماه باشد.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID <= 0 {
		return c.Send("اشتراک نامعتبر است.")
	}
	token := strings.TrimSpace(parts[2])
	if state := bot.FSM.GetState(user.TelegramID); state != nil {
		stateToken := fmt.Sprintf("%v", state.Data["operation_token"])
		if stateToken != "" && stateToken != token {
			return c.Send("این تایید مربوط به یک تمدید قدیمی است. لطفا درخواست جدیدی ایجاد کنید.")
		}
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تمدید فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد یا برای فروش غیرفعال است.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	cost := calculatePaidPrice(plan, months, sub.IPLimit, dataGB)
	if cost <= 0 {
		return c.Send("هزینه تمدید نامعتبر است.")
	}

	now := nowUTC()
	var newExpiryMilli int64
	var newExpiryLabel string
	if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		newDuration := -(*sub.ExpireTime) + int64(months)*30*24*3600*1000
		newExpiryMilli = -newDuration
		newExpiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", newDuration/(24*3600*1000))
	} else {
		endDate := sub.EndDate
		if endDate.Before(now) {
			endDate = now
		}
		endDate = endDate.Add(time.Duration(months) * 30 * 24 * time.Hour)
		newExpiryMilli = endDate.UnixMilli()
		newExpiryLabel = endDate.Format("2006-01-02")
	}
	desiredActive := true
	err = db.DebitWalletForSubscriptionMutation(context.Background(), db.WalletSubscriptionMutationIntent{
		UserID: user.ID, SubscriptionID: sub.ID, Amount: cost,
		Description:     "subscription extension: " + sub.ClientEmail,
		OperationKey:    operationKeyFromToken("wallet_extend_v2", token),
		ExpectedIPLimit: sub.IPLimit, ExpectedExpireTime: sub.ExpireTime, ExpectedIsActive: sub.IsActive,
		ExpectedPlanUpdatedAt: plan.UpdatedAt,
		DesiredExpireTime:     &newExpiryMilli, DesiredIsActive: &desiredActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrInsufficientWalletBalance):
			return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه تمدید %s می‌باشد.", persian.FormatMoney(cost)))
		case errors.Is(err, db.ErrWalletOperationAlreadyApplied):
			return c.Send("این تمدید قبلا ثبت شده یا در حال پردازش است.")
		case errors.Is(err, db.ErrWalletOperationConflict):
			log.Printf("[CRITICAL] wallet operation key collision while extending subscription %d for user %d: %v", sub.ID, user.ID, err)
			return c.Send("شناسه مالی با عملیات دیگری برخورد کرده است؛ برای جلوگیری از برداشت تکراری، این درخواست متوقف شد و نیازمند بررسی پشتیبانی است.")
		case errors.Is(err, db.ErrSubscriptionMutationStale), errors.Is(err, db.ErrSubscriptionMutationInProgress), errors.Is(err, db.ErrSubscriptionMutationInvalid):
			return c.Send("وضعیت سرویس یا طرح تغییر کرده است؛ موجودی کسر نشد. لطفا فرآیند تمدید را دوباره آغاز کنید.")
		default:
			log.Printf("[ERROR] failed to queue wallet extension for subscription %d: %v", sub.ID, err)
			return c.Send("ثبت امن تمدید ناموفق بود و مبلغی کسر نشد. لطفا کمی بعد دوباره تلاش کنید.")
		}
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "درخواست تمدید ثبت شد."})
	return c.Send(fmt.Sprintf("تمدید %d ماهه با هزینه %s به‌طور پایدار ثبت شد و پس از همگام‌سازی با پنل اعمال می‌شود. انقضای درخواستی: %s.", months, persian.FormatMoney(cost), newExpiryLabel))
}

func HandleExtendSubscriptionDirect(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه تمدید منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	months, err := strconv.Atoi(parts[0])
	if err != nil || months < 1 || months > 120 {
		return c.Send("مدت تمدید باید بین ۱ تا ۱۲۰ ماه باشد.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID <= 0 {
		return c.Send("اشتراک نامعتبر است.")
	}
	operationToken := strings.TrimSpace(parts[2])

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID || sub.ID != int(subID) {
		return c.Send("اشتراک یافت نشد.")
	}
	if sub.Status != db.SubscriptionStatusActive || !sub.IsActive || sub.PlanType != db.PlanTypePaid {
		return c.Send("وضعیت سرویس برای تمدید مناسب نیست؛ لطفا منو را دوباره باز کنید.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد یا برای فروش غیرفعال است.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := sub.IPLimit
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")

	subID64 := int64(sub.ID)
	var planID64Ptr *int64
	if plan != nil {
		p64 := int64(plan.ID)
		planID64Ptr = &p64
	}
	expectedExpiry := int64(0)
	if sub.ExpireTime != nil {
		expectedExpiry = *sub.ExpireTime
	}
	extendData := map[string]interface{}{
		"type":                             "extend",
		"subscription_id":                  fmt.Sprintf("%d", sub.ID),
		"months":                           fmt.Sprintf("%d", months),
		"price_toman":                      fmt.Sprintf("%d", cost),
		"expected_ip_limit":                sub.IPLimit,
		"expected_expire_time_milli":       expectedExpiry,
		"expected_is_active":               sub.IsActive,
		"expected_subscription_updated_at": sub.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"expected_plan_updated_at":         plan.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"operation_key":                    operationKeyFromToken("direct_extend", operationToken),
		"operation_token":                  operationToken,
	}
	extendIntent := &db.PaymentIntent{
		UserID:               user.ID,
		IntentToken:          operationToken,
		ActionType:           "extend",
		SubscriptionID:       &subID64,
		PlanID:               planID64Ptr,
		AmountToman:          cost,
		Months:               months,
		IPLimit:              displayIPLimit,
		DataGB:               dataGB,
		ClientEmail:          sub.ClientEmail,
		ProvisioningSnapshot: extendData,
		Status:               db.IntentStatusAwaitingReceipt,
	}
	createdIntent, err := db.CreatePaymentIntent(context.Background(), extendIntent)
	if err != nil {
		log.Printf("[INTENT] Failed to create payment intent for user %d extend: %v", user.ID, err)
		return paymentIntentCreateFailure(c, user, "عملیات با خطا مواجه شد. لطفاً مجدداً تلاش کنید یا با پشتیبانی در ارتباط باشید.")
	}
	extendData["intent_id"] = createdIntent.ID
	extendData["operation_token"] = createdIntent.IntentToken
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", extendData)

	var text strings.Builder
	text.WriteString("💳 **پرداخت مستقیم برای تمدید سرویس**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%s**\n\n", persian.FormatMoney(cost)))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز، **رسید پرداخت (تصویر فیش)** را در همینجا ارسال کنید تا سرویس پس از تایید ادمین تمدید شود.")

	return maybeEditOrSend(c, text.String())
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func loadOwnedSubscription(c telebot.Context) (*db.Subscription, *db.User, bool) {
	subID, err := parseInt64(callbackPayload(c))
	if err != nil || subID == 0 {
		_ = c.Send("اشتراک نامعتبر است.")
		return nil, nil, false
	}
	user := userFromContext(c)
	if user == nil {
		_ = c.Send("خطا در بارگذاری حساب کاربری.")
		return nil, nil, false
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		_ = c.Send("اشتراک یافت نشد.")
		return nil, nil, false
	}
	return sub, user, true
}

func loadOwnedSubscriptionFromPair(c telebot.Context) (*db.Subscription, *db.User, bool) {
	subID, err := parseSubscriptionPairPayload(callbackPayload(c))
	if err != nil {
		_ = c.Send("درخواست اشتراک نامعتبر است.")
		return nil, nil, false
	}
	user := userFromContext(c)
	if user == nil {
		_ = c.Send("خطا در بارگذاری حساب کاربری.")
		return nil, nil, false
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		_ = c.Send("اشتراک یافت نشد.")
		return nil, nil, false
	}
	return sub, user, true
}

// parseSubscriptionPairPayload accepts both the original action:subscription
// callback and the tokenized action:subscription:operation callback. The
// operation token must not change which subscription is authorized.
func parseSubscriptionPairPayload(payload string) (int64, error) {
	parts := strings.Split(payload, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid subscription callback payload")
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID == 0 {
		return 0, fmt.Errorf("invalid subscription id")
	}
	return subID, nil
}

func paidPlanForSub(sub *db.Subscription) (*db.PaidPlan, error) {
	if sub == nil || sub.PlanID == nil {
		return nil, nil
	}
	return db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID))
}

func updateXUIFromSubscription(sub *db.Subscription) error {
	if bot.XUIClient == nil {
		return ErrXUIClientUnavailable
	}
	expireMilli := int64(0)
	if sub.ExpireTime != nil {
		expireMilli = *sub.ExpireTime
	} else if !sub.EndDate.IsZero() {
		expireMilli = sub.EndDate.UnixMilli()
	}
	limitIP := sub.IPLimit
	enable := sub.IsActive
	patch := xui.ClientPatch{
		Enable:     &enable,
		ExpiryTime: &expireMilli,
		LimitIP:    &limitIP,
	}
	if sub.TrafficLimitBytes > 0 {
		totalGB := sub.TrafficLimitBytes
		patch.TotalGB = &totalGB
	}
	return bot.XUIClient.UpdateClientPatch(sub.ClientEmail, patch)
}

func updateXUIRename(oldEmail string, sub *db.Subscription) error {
	if bot.XUIClient == nil {
		return ErrXUIClientUnavailable
	}
	client := clientConfigFromSubscription(sub, sub.ClientEmail)
	client.Enable = sub.IsActive
	return bot.XUIClient.UpdateClient(oldEmail, client)
}

func clientConfigFromSubscription(sub *db.Subscription, email string) xui.ClientConfig {
	expireMilli := int64(0)
	if sub.ExpireTime != nil {
		expireMilli = *sub.ExpireTime
	} else if !sub.EndDate.IsZero() {
		expireMilli = sub.EndDate.UnixMilli()
	}
	user, _ := db.GetUserByID(context.Background(), sub.UserID)
	group := ""
	tgID := int64(0)
	if user != nil {
		group = serviceGroup(user)
		tgID = user.TelegramID
	}
	flow := ""
	total := sub.TrafficLimitBytes
	if sub.PlanID != nil {
		if sub.PlanType == db.PlanTypeTest {
			if plan, _ := db.GetTestPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
				flow = plan.Flow
				if total == 0 {
					total = plan.MaxDataBytes
				}
			}
		} else if plan, _ := db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
			flow = plan.Flow
		}
	}
	if total == 0 && bot.XUIClient != nil {
		if traffic, err := bot.XUIClient.GetClientTraffic(sub.ClientEmail); err == nil && traffic != nil && traffic.Total > 0 {
			total = traffic.Total
		}
	}
	planName := "unlimited"
	if sub.PlanType == db.PlanTypeTest {
		planName = "test"
		if sub.PlanID != nil {
			if plan, _ := db.GetTestPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
				planName = plan.Name
			}
		}
	} else if sub.PlanID != nil {
		if plan, _ := db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
			planName = plan.Name
		}
	}
	client := prepareClientConfig(email, group, tgID, total, expireMilli, sub.IPLimit, flow, sub.SubID, sub.ClientUUID, planName, user)
	return client
}

func monthsRemainingFrom(expire time.Time) int {
	now := nowUTC()
	if expire.Before(now) {
		return 0
	}
	days := int(expire.Sub(now).Hours() / 24)
	months := days / 30
	if months < 1 {
		months = 1
	}
	return months
}

func HandleClaimSubscriptionPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_claim_subscription_link")

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("❌ انصراف", "menu_my_services")),
	)

	return maybeEditOrSend(c, "🔗 لطفا لینک اشتراک خریداری شده خود را ارسال کنید:\n\nمثال:\n`https://sub.domain.com/sub/xxxxxx`", menu)
}

func ProcessClaimSubscriptionLink(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}

	text = strings.TrimSpace(text)
	u, err := url.Parse(text)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.Send("لینک وارد شده نامعتبر است. لطفا یک لینک معتبر با قالب http/https ارسال کنید.")
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return c.Send("لینک وارد شده نامعتبر است. شناسه اشتراک یافت نشد.")
	}
	subID := parts[len(parts)-1]

	unlock := bot.Locker.Lock(fmt.Sprintf("claim_sub:%s", subID))
	defer unlock()

	// 1. Check if subID already exists in local DB
	existingSub, err := db.GetSubscriptionBySubID(context.Background(), subID)
	if err != nil {
		log.Printf("Error checking DB for subID %s: %v", subID, err)
	}
	if existingSub != nil {
		bot.FSM.ClearState(user.TelegramID)
		if existingSub.UserID == user.ID {
			return c.Send("این اشتراک در حال حاضر در لیست سرویس‌های شما قرار دارد.")
		}
		return c.Send("این اشتراک قبلا توسط کاربر دیگری ثبت شده است. در صورت نیاز با پشتیبانی در ارتباط باشید.")
	}

	// Check if there is already a pending claim for this subID
	pendingExists, err := db.HasPendingClaimRequest(context.Background(), subID)
	if err != nil {
		log.Printf("Error checking DB for pending claim subID %s: %v", subID, err)
	}
	if pendingExists {
		bot.FSM.ClearState(user.TelegramID)
		return c.Send("درخواست ثبت برای این اشتراک قبلا ثبت شده است و در انتظار بررسی ادمین می‌باشد.")
	}

	// 2. Fetch client from 3x-ui to verify it exists
	if bot.XUIClient == nil {
		return c.Send("خطا: پنل سرویس‌دهنده در دسترس نیست.")
	}

	targetClient, err := bot.XUIClient.FindClientBySubID(subID)
	if err != nil || targetClient == nil {
		if errors.Is(err, xui.ErrNotFound) || targetClient == nil {
			return c.Send("اشتراک معتبری با این مشخصات در پنل یافت نشد. لطفا مطمئن شوید لینک ارسال شده صحیح است.")
		}
		log.Printf("XUI FindClientBySubID failed: %v", err)
		return c.Send("خطا در بررسی اشتراک در پنل. لطفا مجددا تلاش کنید.")
	}

	// 3. Check if client email already exists in local DB (avoid UNIQUE violation on client_email)
	existingEmailSub, err := db.GetSubscriptionByEmail(context.Background(), targetClient.Email)
	if err != nil {
		log.Printf("Error checking DB for email %s: %v", targetClient.Email, err)
	}
	if existingEmailSub != nil {
		bot.FSM.ClearState(user.TelegramID)
		if existingEmailSub.UserID == user.ID {
			return c.Send("این اشتراک در حال حاضر در لیست سرویس‌های شما قرار دارد.")
		}
		return c.Send("این اشتراک قبلا توسط کاربر دیگری ثبت شده است. در صورت نیاز با پشتیبانی در ارتباط باشید.")
	}

	// 4. Create pending claim PurchaseRequest
	req := &db.PurchaseRequest{
		UserID:         user.ID,
		Type:           "claim",
		PlanID:         nil,
		SubscriptionID: nil,
		Price:          0,
		Months:         0,
		IPLimit:        targetClient.LimitIP,
		DataGB:         int(targetClient.TotalGB / 1073741824),
		CustomName:     subID,
		ClientEmail:    targetClient.Email,
		TelegramFileID: "claim",
		Status:         "pending",
	}

	if err := db.CreatePurchaseRequest(context.Background(), req); err != nil {
		log.Printf("Failed to create claim purchase request: %v", err)
		return c.Send("خطا در ثبت درخواست ثبت اشتراک دستی.")
	}

	bot.FSM.ClearState(user.TelegramID)

	// Notify User
	_ = c.Send("📥 درخواست ثبت اشتراک شما ثبت شد و در انتظار تایید ادمین می‌باشد.\nپس از تایید ادمین، سرویس به بخش «سرویس‌های من» اضافه خواهد شد.")

	// Notify Admins with Plan selection buttons
	paidPlans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil {
		log.Printf("Failed to fetch paid plans for claim approval menu: %v", err)
	}

	if config.Global != nil {
		for _, adminID := range config.Global.Admin.AdminIDs {
			menu := &telebot.ReplyMarkup{}
			var rows []telebot.Row
			for _, plan := range paidPlans {
				rows = append(rows, menu.Row(
					menu.Data(fmt.Sprintf("طرح: %s", plan.Name), "admin_claim_assign", fmt.Sprintf("%d:%d", req.ID, plan.ID)),
				))
			}
			rows = append(rows, menu.Row(
				menu.Data("❌ رد درخواست", "admin_reject_purchase", fmt.Sprintf("%d", req.ID)),
			))
			menu.Inline(rows...)

			caption := fmt.Sprintf("📥 **درخواست ثبت اشتراک دستی #%d**\n\nکاربر: @%s (%d)\nایمیل اشتراک: `%s`\nشناسه اشتراک: `%s`\nIP همزمان: %s\nحجم: %d گیگابایت\n\nلطفا یکی از طرح‌های زیر را برای این اشتراک انتخاب کنید تا تایید شود:",
				req.ID, user.Username, user.TelegramID, req.ClientEmail, req.CustomName, formatIPLimit(req.IPLimit), req.DataGB)

			_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, FormatMarkdown(caption), menu, telebot.ModeMarkdown)
		}
	}

	return showMainMenu(c, user)
}
