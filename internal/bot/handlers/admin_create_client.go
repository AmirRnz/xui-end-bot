package handlers

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

type CustomerDetail struct {
	Name       string
	TelegramID int64
	Username   string
}

func makePlaceholderTelegramID(username string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(username)))
	val := int64(h.Sum64() & 0x7FFFFFFF)
	if val == 0 {
		val = 1
	}
	return -val
}

func parseCustomerDetailsInput(input string) ([]CustomerDetail, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("ورودی خالی است.")
	}

	// Split input by newlines and commas
	rawItems := strings.FieldsFunc(input, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})

	var results []CustomerDetail
	for _, item := range rawItems {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		var name, identifier string
		var parts []string
		if strings.Contains(item, ":") {
			parts = strings.SplitN(item, ":", 2)
		} else if strings.Contains(item, "=") {
			parts = strings.SplitN(item, "=", 2)
		} else if strings.Contains(item, "|") {
			parts = strings.SplitN(item, "|", 2)
		}

		if len(parts) == 2 {
			name = strings.TrimSpace(parts[0])
			identifier = strings.TrimSpace(parts[1])
		} else {
			identifier = item
		}

		identifierClean := strings.TrimSpace(identifier)
		tgID, err := strconv.ParseInt(identifierClean, 10, 64)

		var detail CustomerDetail
		if err == nil && tgID > 0 {
			detail.TelegramID = tgID
			if name == "" {
				name = fmt.Sprintf("user_%d", tgID)
			}
		} else {
			cleanUsername := strings.TrimPrefix(identifierClean, "@")
			cleanUsername = strings.TrimSpace(cleanUsername)
			if cleanUsername == "" {
				return nil, fmt.Errorf("آی‌دی یا یوزرنیم تلگرام در آیتم '%s' نامعتبر است.", item)
			}
			detail.Username = cleanUsername
			if name == "" {
				name = cleanUsername
			}
		}

		sName := sanitizeName(name)
		if sName == "" {
			sName = name
		}
		detail.Name = sName

		results = append(results, detail)
	}

	if len(results) == 0 {
		return nil, errors.New("هیچ اطلاعات مشتری معتبری یافت نشد.")
	}

	return results, nil
}

func RegisterAdminCreateClient(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_create_client", HandleAdminCreateClientMenu, auth, admin)
	b.Handle("\fadmin_create_plan_paid", HandleAdminCreatePaidPlansList, auth, admin)
	b.Handle("\fadmin_create_select_paid_plan", HandleAdminCreateSelectPaidPlan, auth, admin)
	b.Handle("\fadmin_create_plan_custom", HandleAdminCreateCustomPlan, auth, admin)

	b.Handle("\fadmin_create_toggle_inbound", HandleAdminCreateToggleInbound, auth, admin)
	b.Handle("\fadmin_create_toggle_all", HandleAdminCreateToggleAll, auth, admin)
	b.Handle("\fadmin_create_inbounds_done", HandleAdminCreateInboundsDone, auth, admin)

	b.Handle("\fadmin_create_data_mode", HandleAdminCreateDataMode, auth, admin)
	b.Handle("\fadmin_create_datagb", HandleAdminCreateDataGB, auth, admin)
	b.Handle("\fadmin_create_iplimit", HandleAdminCreateIPLimit, auth, admin)
	b.Handle("\fadmin_create_months", HandleAdminCreateMonths, auth, admin)
	b.Handle("\fadmin_create_custom_prompt", HandleAdminCreateCustomPrompt, auth, admin)
}

func HandleAdminCreateClientMenu(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	bot.FSM.ClearState(user.TelegramID)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("💼 طرح آماده (Paid Plan)", "admin_create_plan_paid")),
		menu.Row(menu.Data("⚙️ طرح دلخواه (Custom Plan)", "admin_create_plan_custom")),
		menu.Row(menu.Data("« بازگشت به پنل", "admin_menu")),
	)

	return maybeEditOrSend(c, "➕ **ایجاد ساخت سرویس جدید (ادمین)**\n\nلطفا نوع طرح ساخت سرویس را انتخاب کنید:", menu)
}

func HandleAdminCreatePaidPlansList(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	plans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil || len(plans) == 0 {
		return maybeEditOrSend(c, "هیچ طرح خریدی برای انتخاب یافت نشد.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		typeLabel := "نامحدود"
		if plan.IsLimited {
			typeLabel = fmt.Sprintf("محدود (%dGB)", plan.MinDataGB)
		}
		label := fmt.Sprintf("📦 %s (%s)", plan.Name, typeLabel)
		rows = append(rows, menu.Row(menu.Data(label, "admin_create_select_paid_plan", fmt.Sprintf("%d", plan.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "admin_create_client")))
	menu.Inline(rows...)

	return maybeEditOrSend(c, "💼 **انتخاب طرح آماده**:\nلطفا یکی از طرح‌های فعال را برای ساخت سرویس انتخاب کنید:", menu)
}

func HandleAdminCreateSelectPaidPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	planID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("طرح انتخاب شده نامعتبر است.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}

	draft := map[string]interface{}{
		"mode":          "paid",
		"plan_id":       plan.ID,
		"plan_name":     plan.Name,
		"inbound_ids":   plan.InboundIDs,
		"flow":          plan.Flow,
		"is_limited":    plan.IsLimited,
		"data_gb":       plan.MinDataGB,
		"base_ip_limit": plan.BaseIPLimit,
		"max_ip_limit":  plan.MaxIPLimit,
		"ip_limit":      plan.BaseIPLimit,
		"months":        1,
	}

	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)
	return promptAdminCreateDuration(c, draft)
}

func HandleAdminCreateCustomPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	draft := map[string]interface{}{
		"mode":        "custom",
		"plan_name":   "Custom Plan",
		"inbound_ids": []int{},
		"is_limited":  false,
		"data_gb":     int64(0),
		"ip_limit":    1,
		"months":      1,
		"flow":        "",
	}

	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)
	return showAdminCreateInboundsMenu(c, draft)
}

func showAdminCreateInboundsMenu(c telebot.Context, draft map[string]interface{}) error {
	if bot.XUIClient == nil {
		return c.Send("ارتباط با پنل x-ui برقرار نیست.")
	}

	cached := bot.XUIClient.GetCachedInbounds()
	selectedIDs := draftGetIntSlice(draft, "inbound_ids")
	selectedMap := make(map[int]bool)
	for _, id := range selectedIDs {
		selectedMap[id] = true
	}

	var text strings.Builder
	text.WriteString("📡 **انتخاب کانکشن‌ها (Inbounds) برای طرح دلخواه**\n\n")
	text.WriteString("کانکشن‌های مورد نظر خود را علامت بزنید:\n\n")

	inboundNames := cachedInboundNames()
	if len(selectedIDs) == 0 {
		text.WriteString("  _(هیچ کانکشنی انتخاب نشده است)_\n")
	} else {
		for _, id := range selectedIDs {
			if name, ok := inboundNames[id]; ok && name != "" {
				text.WriteString(fmt.Sprintf("  ✅ ID %d: %s\n", id, name))
			} else {
				text.WriteString(fmt.Sprintf("  ✅ ID %d\n", id))
			}
		}
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	for _, inbound := range cached {
		isSelected := selectedMap[inbound.ID]
		mark := "❌"
		if isSelected {
			mark = "✅"
		}
		btnText := fmt.Sprintf("%s ID %d: %s (%s:%d)", mark, inbound.ID, inbound.Remark, inbound.Protocol, inbound.Port)
		rows = append(rows, menu.Row(menu.Data(btnText, "admin_create_toggle_inbound", fmt.Sprintf("%d", inbound.ID))))
	}

	allSelected := len(cached) > 0
	for _, inbound := range cached {
		if !selectedMap[inbound.ID] {
			allSelected = false
			break
		}
	}

	if allSelected && len(cached) > 0 {
		rows = append(rows, menu.Row(menu.Data("❌ لغو انتخاب همه", "admin_create_toggle_all", "deselect")))
	} else {
		rows = append(rows, menu.Row(menu.Data("✅ انتخاب همه", "admin_create_toggle_all", "select")))
	}
	rows = append(rows, menu.Row(menu.Data("💾 تایید و ادامه", "admin_create_inbounds_done")))
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "admin_create_client")))

	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminCreateToggleInbound(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	inboundID, err := strconv.Atoi(callbackPayload(c))
	if err != nil {
		return c.Send("شناسه کانکشن نامعتبر است.")
	}

	draft := state.Data
	selectedIDs := draftGetIntSlice(draft, "inbound_ids")

	var updated []int
	found := false
	for _, id := range selectedIDs {
		if id == inboundID {
			found = true
		} else {
			updated = append(updated, id)
		}
	}
	if !found {
		updated = append(updated, inboundID)
	}

	draft["inbound_ids"] = updated
	bot.FSM.SetState(user.TelegramID, state.Step, draft)

	return showAdminCreateInboundsMenu(c, draft)
}

func HandleAdminCreateToggleAll(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	action := callbackPayload(c)
	draft := state.Data

	if action == "select" && bot.XUIClient != nil {
		cached := bot.XUIClient.GetCachedInbounds()
		var allIDs []int
		for _, inbound := range cached {
			allIDs = append(allIDs, inbound.ID)
		}
		draft["inbound_ids"] = allIDs
	} else {
		draft["inbound_ids"] = []int{}
	}

	bot.FSM.SetState(user.TelegramID, state.Step, draft)
	return showAdminCreateInboundsMenu(c, draft)
}

func HandleAdminCreateInboundsDone(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	draft := state.Data
	selectedIDs := draftGetIntSlice(draft, "inbound_ids")
	if len(selectedIDs) == 0 {
		_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ لطفا حداقل یک کانکشن را انتخاب کنید."})
		return nil
	}

	return promptAdminCreateDataMode(c, draft)
}

func promptAdminCreateDataMode(c telebot.Context, draft map[string]interface{}) error {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("♾️ نامحدود", "admin_create_data_mode", "unlimited"),
			menu.Data("📊 محدود (حجمی)", "admin_create_data_mode", "limited"),
		),
		menu.Row(menu.Data("« انصراف", "admin_create_client")),
	)
	return maybeEditOrSend(c, "📊 **تعیین وضعیت ترافیک سرویس**:\nآیا ترافیک سرویس نامحدود است یا محدود؟", menu)
}

func HandleAdminCreateDataMode(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	mode := callbackPayload(c)
	draft := state.Data

	if mode == "unlimited" {
		draft["is_limited"] = false
		draft["data_gb"] = int64(0)
		bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)
		return promptAdminCreateIPLimit(c, draft)
	}

	draft["is_limited"] = true
	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)
	return promptAdminCreateDataGBOptions(c, draft)
}

func promptAdminCreateDataGBOptions(c telebot.Context, draft map[string]interface{}) error {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("10 گیگابایت", "admin_create_datagb", "10"),
			menu.Data("30 گیگابایت", "admin_create_datagb", "30"),
		),
		menu.Row(
			menu.Data("50 گیگابایت", "admin_create_datagb", "50"),
			menu.Data("100 گیگابایت", "admin_create_datagb", "100"),
		),
		menu.Row(
			menu.Data("✏️ حجم دلخواه", "admin_create_custom_prompt", "datagb"),
		),
		menu.Row(menu.Data("« انصراف", "admin_create_client")),
	)
	return maybeEditOrSend(c, "💾 **میزان ترافیک حجمی (به گیگابایت)** را انتخاب کنید یا مقدار دلخواه بزنید:", menu)
}

func HandleAdminCreateDataGB(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	gb, err := strconv.ParseInt(callbackPayload(c), 10, 64)
	if err != nil || gb <= 0 {
		return c.Send("مقدار ترافیک نامعتبر است.")
	}

	draft := state.Data
	draft["data_gb"] = gb
	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)

	return promptAdminCreateIPLimit(c, draft)
}

func promptAdminCreateIPLimit(c telebot.Context, draft map[string]interface{}) error {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("1 دستگاه", "admin_create_iplimit", "1"),
			menu.Data("2 دستگاه", "admin_create_iplimit", "2"),
		),
		menu.Row(
			menu.Data("3 دستگاه", "admin_create_iplimit", "3"),
			menu.Data("5 دستگاه", "admin_create_iplimit", "5"),
		),
		menu.Row(
			menu.Data("✏️ تعداد کاربر/دستگاه دلخواه", "admin_create_custom_prompt", "iplimit"),
		),
		menu.Row(menu.Data("« انصراف", "admin_create_client")),
	)
	return maybeEditOrSend(c, "🌐 **تعداد دستگاه‌های همزمان (IP Limit)** را انتخاب کنید:", menu)
}

func HandleAdminCreateIPLimit(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	ipLimit, err := strconv.Atoi(callbackPayload(c))
	if err != nil || ipLimit < 0 {
		return c.Send("سقف دستگاه نامعتبر است.")
	}

	draft := state.Data
	draft["ip_limit"] = ipLimit
	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)

	return promptAdminCreateDuration(c, draft)
}

func promptAdminCreateDuration(c telebot.Context, draft map[string]interface{}) error {
	mode := draftGetString(draft, "mode")
	isLimited := draftGetBool(draft, "is_limited")

	// If paid plan and is limited, check if data_gb set, otherwise prompt data_gb first
	if mode == "paid" && isLimited {
		if draftGetInt64(draft, "data_gb") <= 0 {
			minGB := draftGetInt64(draft, "min_data_gb")
			if minGB <= 0 {
				minGB = 10
			}
			draft["data_gb"] = minGB
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("۱ ماهه", "admin_create_months", "1"),
			menu.Data("۳ ماهه", "admin_create_months", "3"),
		),
		menu.Row(
			menu.Data("۶ ماهه", "admin_create_months", "6"),
			menu.Data("۱۲ ماهه", "admin_create_months", "12"),
		),
		menu.Row(
			menu.Data("✏️ مدت زمان دلخواه (ماه)", "admin_create_custom_prompt", "months"),
		),
		menu.Row(menu.Data("« انصراف", "admin_create_client")),
	)
	return maybeEditOrSend(c, "⏱️ **مدت زمان اعتبار سرویس (به ماه)** را انتخاب کنید:", menu)
}

func HandleAdminCreateMonths(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	months, err := strconv.Atoi(callbackPayload(c))
	if err != nil || months <= 0 {
		return c.Send("مدت زمان نامعتبر است.")
	}

	draft := state.Data
	draft["months"] = months

	// Move to Customer details prompt
	return promptAdminCreateCustomerDetails(c, draft)
}

func HandleAdminCreateCustomPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند فعال یافت نشد.")
	}

	field := callbackPayload(c)
	draft := state.Data

	switch field {
	case "datagb":
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_create_custom_datagb", draft)
		return maybeEditOrSend(c, "💾 لطفا مقدار ترافیک حجمی را به گیگابایت ارسال کنید (مثلاً: 50):\n\nجهت انصراف /cancel را بزنید.")
	case "iplimit":
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_create_custom_iplimit", draft)
		return maybeEditOrSend(c, "🌐 لطفا سقف تعداد دستگاه‌های همزمان (IP Limit) را ارسال کنید (مثلاً: 4):\n\nجهت انصراف /cancel را بزنید.")
	case "months":
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_create_custom_months", draft)
		return maybeEditOrSend(c, "⏱️ لطفا مدت زمان سرویس را به ماه ارسال کنید (مثلاً: 2):\n\nجهت انصراف /cancel را بزنید.")
	default:
		return c.Send("مرحله نامشخص است.")
	}
}

func ProcessAdminCreateCustomDataGBText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_create_custom_datagb" {
		return c.Send("هیچ فرآیندی یافت نشد.")
	}

	gb, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || gb <= 0 {
		return c.Send("مقدار ترافیک باید یک عدد مثبت به گیگابایت باشد. مجددا ارسال کنید:")
	}

	draft := state.Data
	draft["data_gb"] = gb
	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)

	return promptAdminCreateIPLimit(c, draft)
}

func ProcessAdminCreateCustomIPLimitText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_create_custom_iplimit" {
		return c.Send("هیچ فرآیندی یافت نشد.")
	}

	ipLimit, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || ipLimit < 0 {
		return c.Send("سقف دستگاه باید یک عدد صفر یا مثبت باشد. مجددا ارسال کنید:")
	}

	draft := state.Data
	draft["ip_limit"] = ipLimit
	bot.FSM.SetState(user.TelegramID, "admin_create_step", draft)

	return promptAdminCreateDuration(c, draft)
}

func ProcessAdminCreateCustomMonthsText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_create_custom_months" {
		return c.Send("هیچ فرآیندی یافت نشد.")
	}

	months, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت به ماه باشد. مجددا ارسال کنید:")
	}

	draft := state.Data
	draft["months"] = months

	return promptAdminCreateCustomerDetails(c, draft)
}

func promptAdminCreateCustomerDetails(c telebot.Context, draft map[string]interface{}) error {
	user := userFromContext(c)
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_create_client_details", draft)

	mode := draftGetString(draft, "mode")
	planName := draftGetString(draft, "plan_name")
	months := draftGetInt(draft, "months")
	ipLimit := draftGetInt(draft, "ip_limit")
	dataGB := draftGetInt64(draft, "data_gb")
	isLimited := draftGetBool(draft, "is_limited")

	trafficText := "نامحدود"
	if isLimited {
		trafficText = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	var summary strings.Builder
	summary.WriteString("📋 **خلاصه مشخصات ساخت سرویس**:\n")
	summary.WriteString(fmt.Sprintf("▫️ نوع طرح: %s (%s)\n", planName, mode))
	summary.WriteString(fmt.Sprintf("▫️ مدت زمان: %d ماه\n", months))
	summary.WriteString(fmt.Sprintf("▫️ میزان ترافیک: %s\n", trafficText))
	summary.WriteString(fmt.Sprintf("▫️ سقف دستگاه (IP Limit): %d\n\n", ipLimit))
	summary.WriteString("👤 **ارسال اطلاعات مشتری(ها)**:\n")
	summary.WriteString("لطفا مشخصات مشتری را با فرمت زیر ارسال کنید:\n")
	summary.WriteString("`name:telegram_id`\n\n")
	summary.WriteString("برای ساخت همزمان چند کاربر، آنها را با ویرگول یا خط جدید جدا کنید:\n")
	summary.WriteString("`user1:123456789, user2:987654321`\n\n")
	summary.WriteString("جهت لغو، عبارت /cancel را ارسال کنید.")

	return maybeEditOrSend(c, summary.String())
}

func ProcessAdminCreateClientDetails(c telebot.Context, text string) error {
	adminUser := userFromContext(c)
	if adminUser == nil || !isConfiguredAdmin(adminUser.TelegramID) {
		return c.Send("دسترسی غیرمجاز.")
	}

	state := bot.FSM.GetState(adminUser.TelegramID)
	if state == nil || state.Step != "awaiting_admin_create_client_details" {
		return c.Send("هیچ فرآیند در انتظاری یافت نشد.")
	}

	customers, err := parseCustomerDetailsInput(text)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ %s\nلطفا اطلاعات را با فرمت صحيح `name:telegram_id` مجددا ارسال کنید:", err.Error()))
	}

	if bot.XUIClient == nil {
		return c.Send("❌ خطای ارتباط: کلاینت x-ui مقداردهی نشده است.")
	}

	draft := state.Data
	inboundIDs := validInboundIDs(draftGetIntSlice(draft, "inbound_ids"))
	if len(inboundIDs) == 0 {
		return c.Send("❌ این طرح هیچ کانکشن معتبری ندارد. لطفا کانکشن‌های پنل را بررسی یا لغو کنید.")
	}

	months := draftGetInt(draft, "months")
	if months <= 0 {
		months = 1
	}
	ipLimit := draftGetInt(draft, "ip_limit")
	dataGB := draftGetInt64(draft, "data_gb")
	flow := draftGetString(draft, "flow")
	planName := draftGetString(draft, "plan_name")
	if planName == "" {
		planName = "Admin Custom Plan"
	}
	planID := draftGetInt64(draft, "plan_id")
	var planIDPtr *int
	if planID > 0 {
		pID := int(planID)
		planIDPtr = &pID
	}

	expireMilli := -int64(months * 30 * 24 * 3600 * 1000)
	totalBytes := dataGB * 1073741824

	bot.FSM.ClearState(adminUser.TelegramID)

	_ = c.Send(fmt.Sprintf("⏳ در حال ساخت %d سرویس در پنل... لطفاً شکیبا باشید.", len(customers)))

	ctx := context.Background()
	var report strings.Builder
	report.WriteString(fmt.Sprintf("✅ **گزارش ساخت %d سرویس**:\n\n", len(customers)))

	successCount := 0
	for idx, cust := range customers {
		var targetUser *db.User
		var err error

		if cust.TelegramID > 0 {
			targetUser, err = db.GetUserByTelegramID(ctx, cust.TelegramID)
			if err != nil {
				log.Printf("Error fetching user for telegram_id %d: %v", cust.TelegramID, err)
			}
			if targetUser == nil {
				targetUser = &db.User{
					TelegramID: cust.TelegramID,
					FirstName:  cust.Name,
					Status:     db.UserStatusApproved,
				}
				if err := db.CreateUser(ctx, targetUser); err != nil {
					report.WriteString(fmt.Sprintf("❌ **%s** (%d): خطا در ایجاد کاربر در دیتابیس (%v)\n\n", cust.Name, cust.TelegramID, err))
					continue
				}
			}
		} else if cust.Username != "" {
			targetUser, err = db.GetUserByUsername(ctx, cust.Username)
			if err != nil {
				log.Printf("Error fetching user for username %s: %v", cust.Username, err)
			}
			if targetUser == nil {
				placeholderID := makePlaceholderTelegramID(cust.Username)
				targetUser = &db.User{
					TelegramID: placeholderID,
					Username:   cust.Username,
					FirstName:  cust.Name,
					Status:     db.UserStatusApproved,
				}
				if err := db.CreateUser(ctx, targetUser); err != nil {
					report.WriteString(fmt.Sprintf("❌ **%s** (@%s): خطا در ایجاد کاربر در دیتابیس (%v)\n\n", cust.Name, cust.Username, err))
					continue
				}
			}
		} else {
			report.WriteString(fmt.Sprintf("❌ **%s**: مشخصات تلگرام (آی‌دی عددی یا یوزرنیم) نامعتبر است.\n\n", cust.Name))
			continue
		}

		cleanBase := sanitizeName(cust.Name)
		if cleanBase == "" {
			cleanBase = "client"
		}
		email := fmt.Sprintf("%s_%s", cleanBase, randomToken(4))
		subID := makeSubID()
		clientUUID := makeClientUUID()

		clientConfig := prepareClientConfig(email, serviceGroup(adminUser), targetUser.TelegramID, totalBytes, expireMilli, ipLimit, flow, subID, clientUUID, planName, adminUser)

		err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: clientConfig, InboundIDs: inboundIDs})
		if err != nil && !xui.IsUnknownOutcome(err) {
			log.Printf("XUI AddClient failed for %s: %v. Retrying with refreshed cache...", email, err)
			if bot.XUIClient.Cache != nil {
				bot.XUIClient.Cache.RefreshSync()
				newInboundIDs := validInboundIDs(draftGetIntSlice(draft, "inbound_ids"))
				if len(newInboundIDs) > 0 {
					err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: clientConfig, InboundIDs: newInboundIDs})
				}
			}
		}
		if err != nil {
			report.WriteString(fmt.Sprintf("❌ **%s**: خطا در اضافه کردن کلاینت به پنل 3x-ui (%v)\n\n", cust.Name, err))
			continue
		}

		sub := &db.Subscription{
			UserID:            targetUser.ID,
			PlanID:            planIDPtr,
			ClientEmail:       email,
			ClientUUID:        clientUUID,
			SubID:             subID,
			Status:            "active",
			PlanType:          db.PlanTypePaid,
			DisplayName:       cust.Name,
			IPLimit:           ipLimit,
			ExpireTime:        &expireMilli,
			IsActive:          true,
			StartDate:         nowUTC(),
			TrafficLimitBytes: totalBytes,
		}

		if err := db.CreateSubscription(ctx, sub); err != nil {
			log.Printf("CreateSubscription DB error for %s: %v. Rolling back XUI client...", email, err)
			_ = bot.XUIClient.DeleteClient(email)
			report.WriteString(fmt.Sprintf("❌ **%s**: خطا در ثبت سرویس در دیتابیس (%v)\n\n", cust.Name, err))
			continue
		}

		subLink := bot.XUIClient.SubscriptionURLFor(subID)
		successCount++

		tgDisplay := fmt.Sprintf("%d", targetUser.TelegramID)
		if targetUser.Username != "" {
			tgDisplay = fmt.Sprintf("@%s", targetUser.Username)
		}

		report.WriteString(fmt.Sprintf("👤 **مشتری %d: %s**\n", idx+1, cust.Name))
		report.WriteString(fmt.Sprintf("🆔 شناسه تلگرام: `%s`\n", tgDisplay))
		report.WriteString(fmt.Sprintf("📧 ایمیل: `%s`\n", email))
		report.WriteString(fmt.Sprintf("🔗 لینک اشتراک:\n`%s`\n\n", subLink))

		// Send individual QR code and copyable sub link
		detailsMsg := fmt.Sprintf("👤 سرویس ساخته شده برای **%s** (آی‌دی: `%s`)", cust.Name, tgDisplay)
		_ = sendSubscriptionResult(c, subLink, detailsMsg)
	}

	report.WriteString(fmt.Sprintf("📊 مجموع: %d از %d سرویس با موفقیت ساخته شد.", successCount, len(customers)))

	menu := &telebot.ReplyMarkup{}
	menu.Inline(menu.Row(menu.Data("« بازگشت به پنل ادمین", "admin_menu")))

	return c.Send(FormatMarkdown(report.String()), menu, telebot.ModeMarkdown)
}
