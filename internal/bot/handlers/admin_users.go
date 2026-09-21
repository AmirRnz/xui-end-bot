package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/bot/persian"
	"xui-end-bot/internal/db"
)

const usersPageSize = 15

func RegisterAdminUsers(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_users", HandleAdminUsers, auth, admin)
	b.Handle("\fadmin_users_page", HandleAdminUsersPage, auth, admin)
	b.Handle("\fadmin_view_user", HandleAdminViewUser, auth, admin)
	b.Handle("\fadmin_user_ban", HandleAdminUserBan, auth, admin)
	b.Handle("\fadmin_user_unban", HandleAdminUserUnban, auth, admin)
	b.Handle("\fadmin_user_credit", HandleAdminUserCreditPrompt, auth, admin)
	b.Handle("\fbulk_credit", HandleBulkCreditPrompt, auth, admin)
}

func HandleAdminUsers(c telebot.Context) error {
	return showAdminUsersPage(c, 0)
}

func HandleAdminUsersPage(c telebot.Context) error {
	page, _ := strconv.Atoi(callbackPayload(c))
	return showAdminUsersPage(c, page)
}

func showAdminUsersPage(c telebot.Context, page int) error {
	total, _ := db.GetAllUsersCount(context.Background())
	totalPages := (total + usersPageSize - 1) / usersPageSize
	if page < 0 {
		page = 0
	}
	if totalPages > 0 && page >= totalPages {
		page = totalPages - 1
	}
	offset := page * usersPageSize

	users, err := db.ListUsers(context.Background(), usersPageSize, offset)
	if err != nil {
		return c.Send("خطا در بارگذاری لیست کاربران.")
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👥 **کاربران** (%d کاربر کل، صفحه %d از %d)\n\n", total, page+1, max(totalPages, 1)))
	for _, user := range users {
		statusIcon := "⏳"
		statusText := "در انتظار"
		switch user.Status {
		case "approved", "active":
			statusIcon = "✅"
			statusText = "تایید شده"
		case "banned":
			statusIcon = "🚫"
			statusText = "مسدود"
		}
		text.WriteString(fmt.Sprintf("%s #%d @%s (%s) — %s\n", statusIcon, user.ID, user.Username, statusText, persian.FormatMoney(int64(user.WalletBalance))))
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, user := range users {
		name := user.ServiceNameValue()
		if name == "" {
			name = user.Username
		}
		if name == "" {
			name = fmt.Sprintf("user_%d", user.ID)
		}
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("#%d %s", user.ID, name), "admin_view_user", fmt.Sprintf("%d", user.ID))))
	}

	navRow := []telebot.Btn{}
	if page > 0 {
		navRow = append(navRow, menu.Data("◀️ صفحه قبل", "admin_users_page", fmt.Sprintf("%d", page-1)))
	}
	if totalPages > 0 && page < totalPages-1 {
		navRow = append(navRow, menu.Data("صفحه بعد ▶️", "admin_users_page", fmt.Sprintf("%d", page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows,
		menu.Row(menu.Data("📢 شارژ همگانی تاییدشدگان", "bulk_credit")),
		menu.Row(menu.Data("« بازگشت", "admin_menu")),
	)
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminViewUser(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	return showAdminViewUser(c, user)
}

func showAdminViewUser(c telebot.Context, user *db.User) error {
	subs, _ := db.GetSubscriptionsByUserID(context.Background(), user.ID)

	statusText := user.Status
	switch user.Status {
	case "approved":
		statusText = "تایید شده"
	case "pending":
		statusText = "در انتظار تایید"
	case "banned":
		statusText = "مسدود شده"
	case "approved_name_pending":
		statusText = "تایید شده (در انتظار ثبت نام)"
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👤 **مشخصات کاربر #%d**\n\n", user.ID))
	text.WriteString(fmt.Sprintf("🆔 **شناسه عددی تلگرام**: `%d`\n", user.TelegramID))
	text.WriteString(fmt.Sprintf("🌐 **نام کاربری**: @%s\n", user.Username))
	text.WriteString(fmt.Sprintf("📝 **نام**: %s %s\n", user.FirstName, user.LastName))
	text.WriteString(fmt.Sprintf("⚡ **وضعیت حساب**: %s\n", statusText))
	text.WriteString(fmt.Sprintf("💼 **نام سرویس**: %s\n", user.ServiceNameValue()))
	text.WriteString(fmt.Sprintf("👛 **موجودی کیف پول**: %s تومان\n", persian.FormatMoney(int64(user.WalletBalance))))
	text.WriteString(fmt.Sprintf("📦 **تعداد اشتراک‌ها**: %d\n", len(subs)))

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(menu.Data("💳 افزایش موجودی دستی", "admin_user_credit", fmt.Sprintf("%d", user.ID))),
	}
	if user.Status == db.UserStatusBanned {
		rows = append(rows, menu.Row(menu.Data("✅ رفع مسدودیت (آنبن)", "admin_user_unban", fmt.Sprintf("%d", user.ID))))
	} else {
		rows = append(rows, menu.Row(menu.Data("🚫 مسدودسازی (بن)", "admin_user_ban", fmt.Sprintf("%d", user.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت به لیست", "admin_users")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminUserBan(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, db.UserStatusBanned); err != nil {
		return c.Send("خطا در مسدود کردن کاربر.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("🚫 کاربر #%d مسدود شد.", user.ID)})
	user.Status = db.UserStatusBanned
	return showAdminViewUser(c, user)
}

func HandleAdminUserUnban(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	status := db.UserStatusApproved
	if user.ServiceNameValue() == "" {
		status = db.UserStatusApprovedNamePending
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, status); err != nil {
		return c.Send("خطا در رفع مسدودیت کاربر.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ کاربر #%d از مسدودیت خارج شد.", user.ID)})
	user.Status = status
	return showAdminViewUser(c, user)
}

func HandleAdminUserCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	userID, err := parseInt64(callbackPayload(c))
	if admin == nil || err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_manual_credit", map[string]interface{}{"target_user_id": fmt.Sprintf("%d", userID)})
	return maybeEditOrSend(c, "لطفاً مبلغ مورد نظر جهت شارژ حساب این کاربر را به تومان وارد کنید:")
}

func HandleBulkCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	if admin == nil {
		return c.Send("امکان بارگذاری حساب ادمین وجود ندارد.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_bulk_credit_amount", nil)
	return maybeEditOrSend(c, "لطفاً مبلغ مورد نظر جهت شارژ همگانی کلیه کاربران تایید شده را به تومان وارد کنید:")
}

func ProcessBulkCredit(c telebot.Context, amountStr string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی به این دستور را ندارید.")
	}
	amount, err := strconv.ParseInt(strings.TrimSpace(amountStr), 10, 64)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. لطفاً یک عدد صحیح مثبت وارد کنید.")
	}
	count, err := db.CreditAllApprovedUsers(context.Background(), amount, "admin bulk credit")
	if err != nil {
		return c.Send("خطا در افزایش موجودی همگانی کاربران.")
	}
	bot.FSM.ClearState(admin.TelegramID)
	_ = c.Send(fmt.Sprintf("✅ مبلغ %s تومان با موفقیت به حساب %d کاربر تایید شده افزوده شد.", persian.FormatMoney(amount), count))
	return HandleAdminUsers(c)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
