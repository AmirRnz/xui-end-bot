package handlers

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
)

func RegisterStart(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc, adminCfg *config.AdminConfig) {
	b.Handle("/start", HandleStart, auth)
	b.Handle(telebot.OnText, HandleText, auth)
	b.Handle("\fmenu_main", func(c telebot.Context) error {
		user := userFromContext(c)
		if user == nil {
			return HandleStart(c)
		}
		return showMainMenu(c, user)
	}, auth)
	b.Handle("\fmenu_support", func(c telebot.Context) error {
		support, _ := db.GetSetting(context.Background(), "support_username")
		support = strings.TrimSpace(support)
		if support != "" {
			if !strings.HasPrefix(support, "@") {
				support = "@" + support
			}
			return c.Send(fmt.Sprintf("برای پشتیبانی لطفا با آی‌دی زیر در ارتباط باشید:\n%s", support))
		}
		return c.Send("برای پشتیبانی لطفا با ادمین در ارتباط باشید.")
	}, auth)
}

func HandleStart(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری. لطفا مجددا تلاش کنید.")
	}
	bot.FSM.ClearState(user.TelegramID) // Reset state on start

	if user.Status == db.UserStatusBanned {
		return c.Send("حساب کاربری شما مسدود شده است.")
	}

	return showMainMenu(c, user)
}

func HandleText(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}

	text := strings.TrimSpace(c.Text())
	if text == "" {
		return c.Send("لطفا متن مربوط به مرحله فعلی را ارسال کنید.")
	}

	if strings.HasPrefix(text, "/") {
		if text == "/cancel" || text == "/cancel@bot" {
			bot.FSM.ClearState(user.TelegramID)
			return showMainMenu(c, user)
		}
		bot.FSM.ClearState(user.TelegramID) // Clear state to execute command
		// let execution fallthrough to standard command routers
	} else if state := bot.FSM.GetState(user.TelegramID); state != nil {
		switch state.Step {
		case "awaiting_buy_months_text":
			return ProcessBuyMonthsText(c, text)
		case "awaiting_buy_data_gb_text":
			return ProcessBuyDataGBText(c, text)
		case "awaiting_buy_custom_name":
			return ProcessBuyCustomName(c, text)
		case "awaiting_test_custom_name":
			return ProcessTestCustomName(c, text)
		case "awaiting_multiple_tests_base_name":
			return ProcessMultipleTestsBaseName(c, text)
		case "awaiting_multiple_tests_count":
			return ProcessMultipleTestsCount(c, text)
		case "awaiting_receipt":
			return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
		case "awaiting_purchase_receipt":
			return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
		case "awaiting_topup_amount":
			return ProcessTopupApprovalAmount(c, text)
		case "awaiting_manual_credit":
			return ProcessManualCreditAmount(c, text)
		case "awaiting_bulk_credit_amount":
			return ProcessBulkCredit(c, text)
		case "awaiting_setting_card_number":
			return ProcessSettingText(c, "card_number", text)
		case "awaiting_setting_card_owner":
			return ProcessSettingText(c, "card_owner", text)
		case "awaiting_setting_currency_name":
			return ProcessSettingText(c, "currency_name", text)
		case "awaiting_setting_min_topup":
			return ProcessSettingText(c, "min_topup_amount", text)
		case "awaiting_setting_topup_description":
			return ProcessSettingText(c, "topup_description", text)
		case "awaiting_setting_test_global_description":
			return ProcessSettingText(c, "test_global_description", text)
		case "awaiting_setting_expiry_notify_days":
			return ProcessSettingText(c, "expiry_notify_days", text)
		case "awaiting_setting_test_reset_days":
			return ProcessSettingText(c, "test_reset_days", text)
		case "awaiting_setting_group_name":
			return ProcessSettingText(c, "group_name", text)
		case "awaiting_setting_support_username":
			return ProcessSettingText(c, "support_username", text)
		case "awaiting_sub_rename":
			return ProcessSubscriptionRename(c, text)
		case "awaiting_extend_months_text":
			return ProcessExtendMonthsText(c, text)
		case "awaiting_admin_draft_input":
			return ProcessAdminDraftInput(c, text)
		case "awaiting_admin_plan_access":
			return ProcessAdminPlanAccess(c, text)
		case "awaiting_claim_subscription_link":
			return ProcessClaimSubscriptionLink(c, text)
		}
	}

	switch strings.ToLower(text) {
	case "free test", "/test", "تست رایگان":
		return HandleTestSubFlow(c)
	case "buy", "buy subscription", "/buy", "خرید سرویس":
		return HandleBuySubFlow(c)
	case "my services", "/services", "سرویس‌های من":
		return HandleMyServicesFlow(c)
	case "wallet", "/wallet", "کیف پول":
		return HandleWalletFlow(c)
	}

	return showMainMenu(c, user)
}

func isConfiguredAdmin(id int64) bool {
	if config.Global == nil {
		return false
	}
	for _, adminID := range config.Global.Admin.AdminIDs {
		if id == adminID {
			return true
		}
	}
	return false
}

