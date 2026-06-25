package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
)

func RegisterAdminSettings(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_settings", HandleAdminSettings, auth, admin)
	b.Handle("\fadmin_set_card", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_card_number", "Send card number.")
	}, auth, admin)
	b.Handle("\fadmin_set_card_owner", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_card_owner", "Send card owner name.")
	}, auth, admin)
	b.Handle("\fadmin_set_currency", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_currency_name", "Send currency name, e.g. IRR.")
	}, auth, admin)
	b.Handle("\fadmin_set_min_topup", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_min_topup", "Send minimum top-up amount.")
	}, auth, admin)
	b.Handle("\fadmin_set_topup_desc", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_topup_description", "Send top-up instructions text.")
	}, auth, admin)
	b.Handle("\fadmin_set_test_global_desc", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_test_global_description", "Send the global description shown above all test plans.")
	}, auth, admin)
	b.Handle("\fadmin_set_expiry_notify_days", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_expiry_notify_days", "Send expiry notification days as comma-separated values, e.g. 3,1.")
	}, auth, admin)
	b.Handle("\fadmin_set_unapproved_limit", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_unapproved_test_limit", "Send unapproved users daily test limit per plan. Use 0 to block.")
	}, auth, admin)
}

func HandleAdminSettings(c telebot.Context) error {
	keys := []string{"card_number", "card_owner", "currency_name", "min_topup_amount", "unapproved_test_limit", "expiry_notify_days", "test_global_description"}
	values := map[string]string{}
	for _, key := range keys {
		values[key], _ = db.GetSetting(context.Background(), key)
	}

	text := fmt.Sprintf("Settings\nCard: %s\nOwner: %s\nCurrency: %s\nMinimum top-up: %s\nUnapproved test limit: %s\nExpiry notify days: %s\nTest global description: %s",
		values["card_number"], values["card_owner"], values["currency_name"], values["min_topup_amount"], values["unapproved_test_limit"], values["expiry_notify_days"], values["test_global_description"])

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("💳 Card", "admin_set_card"), menu.Data("👤 Owner", "admin_set_card_owner")),
		menu.Row(menu.Data("💱 Currency", "admin_set_currency"), menu.Data("💰 Min top-up", "admin_set_min_topup")),
		menu.Row(menu.Data("📝 Top-up text", "admin_set_topup_desc"), menu.Data("🔒 Unapproved limit", "admin_set_unapproved_limit")),
		menu.Row(menu.Data("📋 Test intro", "admin_set_test_global_desc"), menu.Data("🔔 Expiry days", "admin_set_expiry_notify_days")),
		menu.Row(menu.Data("« Back", "admin_menu")),
	)
	return maybeEditOrSend(c, text, menu)
}

func settingPrompt(c telebot.Context, step, prompt string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("Could not load admin account.")
	}
	bot.FSM.SetState(user.TelegramID, step, nil)
	return maybeEditOrSend(c, prompt)
}

func ProcessSettingText(c telebot.Context, key string, value string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("You do not have permission to use this command.")
	}
	value = strings.TrimSpace(value)
	switch key {
	case "min_topup_amount":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return c.Send("Minimum top-up must be a number.")
		}
	case "unapproved_test_limit":
		v, err := strconv.Atoi(value)
		if err != nil || v < 0 {
			return c.Send("Unapproved test limit must be zero or a positive integer.")
		}
	case "expiry_notify_days":
		for _, part := range strings.Split(value, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || v <= 0 {
				return c.Send("Expiry notification days must be positive integers separated by commas.")
			}
		}
	}
	if err := db.SetSetting(context.Background(), key, value); err != nil {
		return c.Send("Failed to save setting.")
	}
	bot.FSM.ClearState(user.TelegramID)
	_ = c.Send("✅ Setting saved.")
	return HandleAdminSettings(c)
}

