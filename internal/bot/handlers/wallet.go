package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/bot/persian"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/services/reconcile"
)

var walletAdminCfg *config.AdminConfig

func RegisterWallet(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc, adminCfg *config.AdminConfig) {
	walletAdminCfg = adminCfg
	b.Handle("\fmenu_wallet", HandleWalletFlow, auth)
	b.Handle("\fbtn_topup", HandleTopupInstructions, auth)
	b.Handle("\fadmin_list_topups", HandleAdminPendingTopups, auth, admin)
	b.Handle("\fadmin_pending_topups", HandleAdminPendingTopups, auth, admin)
	b.Handle("\fadmin_approve_topup", HandleAdminApproveTopup, auth, admin)
	b.Handle("\fadmin_reject_topup", HandleAdminRejectTopup, auth, admin)
	b.Handle("\fadmin_approve_purchase", HandleAdminApprovePurchase, auth, admin)
	b.Handle("\fadmin_reject_purchase", HandleAdminRejectPurchase, auth, admin)
	b.Handle("\fadmin_claim_assign", HandleAdminClaimAssign, auth, admin)
	b.Handle(telebot.OnPhoto, HandleReceiptPhoto, auth)
}

func HandleWalletFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	menu := &telebot.ReplyMarkup{}
	row := []telebot.Row{menu.Row(menu.Data("📥 شارژ کیف پول", "btn_topup"))}
	if isConfiguredAdmin(c.Sender().ID) {
		row = append(row, menu.Row(menu.Data("⏳ تراکنش‌های در انتظار شارژ", "admin_list_topups")))
	}
	rows := append(row, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("👛 **موجودی کیف پول شما:** %s", persian.FormatMoney(user.WalletBalance)), menu)
}

func HandleTopupInstructions(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")
	minAmount, _ := db.GetSetting(context.Background(), "min_topup_amount")

	// P0-1 & P0-2: Create durable topup payment intent before showing card details
	topupToken := fmt.Sprintf("topup_%d_%d", user.ID, time.Now().UnixNano())
	intent := &db.PaymentIntent{
		UserID:      user.ID,
		IntentToken: topupToken,
		ActionType:  "topup",
		Status:      db.IntentStatusAwaitingReceipt,
	}
	createdIntent, err := db.CreatePaymentIntent(context.Background(), intent)
	if err != nil {
		log.Printf("[INTENT] Failed to create topup payment intent for user %d: %v", user.ID, err)
		return paymentIntentCreateFailure(c, user, "عملیات با خطا مواجه شد. لطفاً مجدداً تلاش کنید یا با پشتیبانی در ارتباط باشید.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_receipt", map[string]interface{}{
		"intent_id":       createdIntent.ID,
		"operation_token": createdIntent.IntentToken,
	})

	text := "لطفا پس از واریز مبلغ مورد نظر به تومان، تصویر رسید پرداخت (فیش واریزی) خود را در قالب عکس ارسال کنید."
	if card != "" {
		text += "\n\nشماره کارت جهت واریز:\n`" + card + "`"
	}
	if owner != "" {
		text += "\nنام صاحب کارت: **" + owner + "**"
	}
	if minAmount != "" && minAmount != "0" {
		text += "\nحداقل مبلغ شارژ مجاز: " + formatTomanSetting(minAmount) + " تومان"
	}
	if desc != "" {
		text += "\n\n" + desc
	}
	return maybeEditOrSend(c, text)
}

func HandleReceiptPhoto(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("user_receipt:%d", user.ID))
	defer unlock()

	if c.Message() == nil || c.Message().Photo == nil {
		return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
	}
	fileID := c.Message().Photo.FileID

	state := bot.FSM.GetState(user.TelegramID)
	var activeIntent *db.PaymentIntent
	if state != nil && state.Data != nil {
		if intentIDVal, ok := state.Data["intent_id"]; ok && intentIDVal != nil {
			var iID int64
			switch v := intentIDVal.(type) {
			case int64:
				iID = v
			case int:
				iID = int64(v)
			case float64:
				iID = int64(v)
			case string:
				iID, _ = strconv.ParseInt(v, 10, 64)
			}
			if iID > 0 {
				if in, err := db.GetPaymentIntentByID(context.Background(), iID); err == nil && in != nil && in.UserID == user.ID && in.Status == db.IntentStatusAwaitingReceipt {
					activeIntent = in
				}
			}
		}
	}
	if activeIntent == nil {
		if recovered, err := db.GetLatestActivePaymentIntent(context.Background(), user.ID); err == nil && recovered != nil {
			activeIntent = recovered
		}
	}
	if activeIntent == nil {
		return c.Send("هیچ فرآیند فعالی برای ارسال رسید وجود ندارد. لطفا ابتدا درخواست پرداخت خود را ثبت کنید.")
	}

	// Transactional and idempotent submission (P0-3)
	if activeIntent.ActionType == "topup" {
		res, err := db.SubmitReceiptForActiveIntent(context.Background(), activeIntent.ID, user.ID, fileID, nil)
		if err != nil {
			log.Printf("[RECEIPT] Failed to submit topup receipt for intent %d: %v", activeIntent.ID, err)
			return c.Send("خطا در ثبت رسید پرداخت. لطفا مجددا تلاش کنید.")
		}
		bot.FSM.ClearState(user.TelegramID)
		if res.IsDuplicate {
			_ = c.Send("رسید شما قبلاً دریافت شده است و در انتظار بررسی ادمین می‌باشد.")
			return showMainMenu(c, user)
		}

		if walletAdminCfg != nil {
			for _, adminID := range walletAdminCfg.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(menu.Row(
					menu.Data("تایید", "admin_approve_topup", fmt.Sprintf("%d", res.TopupRequest.ID)),
					menu.Data("رد", "admin_reject_topup", fmt.Sprintf("%d", res.TopupRequest.ID)),
				))
				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: fmt.Sprintf("درخواست افزایش موجودی کیف پول #%d\nکاربر: @%s\nشناسه تلگرام: %d", res.TopupRequest.ID, user.Username, user.TelegramID)}, menu)
			}
		}
		_ = c.Send("رسید شما دریافت شد. لطفا منتظر بررسی و تایید ادمین بمانید.")
		return showMainMenu(c, user)
	}

	// Only the receipt attachment and authenticated user are incoming facts.
	// PaymentIntent is the durable source for all commercial and provisioning data.
	purchaseDetails := &db.PurchaseRequest{TelegramFileID: fileID}

	res, err := db.SubmitReceiptForActiveIntent(context.Background(), activeIntent.ID, user.ID, fileID, purchaseDetails)
	if err != nil {
		log.Printf("[RECEIPT] Failed to submit purchase receipt for intent %d: %v", activeIntent.ID, err)
		return c.Send("خطا در ثبت درخواست خرید مستقیم.")
	}
	bot.FSM.ClearState(user.TelegramID)

	if res.IsDuplicate {
		if res.NeedsManualReview {
			_ = c.Send("رسید شما قبلاً به‌صورت پایدار ذخیره شده است. فعال‌سازی همچنان متوقف و درخواست در صف بررسی دستی مدیریت قرار دارد.")
			return showMainMenu(c, user)
		}
		_ = c.Send("رسید پرداخت شما قبلاً دریافت شده است و در انتظار تایید ادمین می‌باشد.")
		return showMainMenu(c, user)
	}

	req := res.PurchaseRequest
	pType := req.Type
	priceToman := purchaseAmountToman(req)
	customName := snapshotString(req.ProvisioningSnapshot, "plan_name")
	if customName == "" {
		customName = req.CustomName
	}
	email := req.ClientEmail
	months, ipLimit, dataGB := req.Months, req.IPLimit, req.DataGB
	subIDPtr := req.SubscriptionID
	if walletAdminCfg != nil {
		for _, adminID := range walletAdminCfg.AdminIDs {
			menu := &telebot.ReplyMarkup{}
			if req.Status == db.PurchaseStatusNeedsManualReview || res.NeedsManualReview {
				menu.Inline(menu.Row(menu.Data("رد درخواست", "admin_reject_purchase", fmt.Sprintf("%d", req.ID))))
			} else {
				menu.Inline(menu.Row(
					menu.Data("تایید خرید", "admin_approve_purchase", fmt.Sprintf("%d", req.ID)),
					menu.Data("رد خرید", "admin_reject_purchase", fmt.Sprintf("%d", req.ID)),
				))
			}

			var details string
			switch pType {
			case "buy":
				details = fmt.Sprintf("خرید سرویس جدید\nطرح: %s\nایمیل: %s\nمدت: %d ماه\nIP همزمان: %s\nحجم: %d گیگابایت", customName, email, months, formatIPLimit(ipLimit), dataGB)
			case "extend":
				subDisplay := int64(0)
				if subIDPtr != nil {
					subDisplay = *subIDPtr
				}
				details = fmt.Sprintf("تمدید سرویس\nشناسه اشتراک: %d\nمدت تمدید: %d ماه", subDisplay, months)
			case "upgrade_ip":
				subDisplay := int64(0)
				if subIDPtr != nil {
					subDisplay = *subIDPtr
				}
				details = fmt.Sprintf("ارتقای تعداد IP همزمان\nشناسه اشتراک: %d\nتعداد سقف IP جدید: %s", subDisplay, formatIPLimit(ipLimit))
			}

			caption := fmt.Sprintf("📥 درخواست خرید مستقیم #%d\nکاربر: @%s (%d)\nنوع: %s\nمبلغ: %s\n\nجزئیات:\n%s",
				req.ID, user.Username, user.TelegramID, pType, persian.FormatMoney(priceToman), details)
			if req.Status == db.PurchaseStatusNeedsManualReview || res.NeedsManualReview {
				caption += "\n\n⚠️ رسید ثبت شد، اما شرایط تجاری تغییر کرده است؛ فعال‌سازی مسدود و بررسی دستی لازم است."
			}

			_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: caption}, menu)
		}
	}

	if req.Status == db.PurchaseStatusNeedsManualReview || res.NeedsManualReview {
		_ = c.Send("رسید پرداخت شما به‌صورت پایدار ذخیره شد. شرایط طرح یا سرویس تغییر کرده است و فعال‌سازی متوقف مانده؛ درخواست برای بررسی دستی به مدیریت ارسال شد.")
	} else {
		_ = c.Send("رسید پرداخت شما دریافت شد. پس از بررسی ادمین، سرویس شما فعال شده و مشخصات آن برایتان ارسال خواهد شد.")
	}
	return showMainMenu(c, user)
}

func HandleAdminPendingTopups(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqs, err := db.GetPendingTopupRequests(context.Background())
	if err != nil || len(reqs) == 0 {
		return c.Send("هیچ درخواست شارژ در انتظاری وجود ندارد.")
	}
	for _, req := range reqs {
		user, _ := db.GetUserByID(context.Background(), req.UserID)
		username := "unknown"
		if user != nil {
			username = user.Username
		}
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(
			menu.Data("تایید", "admin_approve_topup", fmt.Sprintf("%d", req.ID)),
			menu.Data("رد", "admin_reject_topup", fmt.Sprintf("%d", req.ID)),
		))
		_, _ = bot.Bot.Send(c.Sender(), &telebot.Photo{File: telebot.File{FileID: req.TelegramFileID}, Caption: fmt.Sprintf("درخواست افزایش موجودی کیف پول #%d\nکاربر: @%s", req.ID, username)}, menu)
	}
	return nil
}

func HandleAdminApproveTopup(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}
	req, err := db.GetTopupRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		return c.Send("درخواست شارژ یافت نشد یا قبلا بررسی شده است.")
	}
	admin := userFromContext(c)
	bot.FSM.SetState(admin.TelegramID, "awaiting_topup_amount", map[string]interface{}{"req_id": fmt.Sprintf("%d", reqID)})
	return maybeEditOrSend(c, fmt.Sprintf("لطفا مبلغ تایید شده برای درخواست شارژ #%d را ارسال کنید:", reqID))
}

func ProcessTopupApprovalAmount(c telebot.Context, amountText string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	state := bot.FSM.GetState(admin.TelegramID)
	if state == nil {
		return c.Send("فرآیند تایید شارژ فعالی وجود ندارد.")
	}
	reqID, _ := parseInt64(fmt.Sprintf("%v", state.Data["req_id"]))
	amount, err := parseInt64(amountText)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. یک عدد مثبت وارد کنید:")
	}
	minAmountStr, _ := db.GetSetting(context.Background(), "min_topup_amount")
	if minAmount, err := strconv.ParseInt(strings.TrimSpace(minAmountStr), 10, 64); err == nil && minAmount > 0 && amount < minAmount {
		return c.Send(fmt.Sprintf("مبلغ وارد شده کمتر از حداقل شارژ مجاز %s است.", persian.FormatMoney(minAmount)))
	}

	req, err := db.ApproveTopupRequest(context.Background(), reqID, admin.TelegramID, amount)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست شارژ.")
	}
	bot.FSM.ClearState(admin.TelegramID)

	target, _ := db.GetUserByID(context.Background(), req.UserID)
	if target != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, fmt.Sprintf("کیف پول شما با موفقیت به مبلغ %s شارژ شد.", persian.FormatMoney(amount)))
	}
	_ = c.Send(fmt.Sprintf("✅ درخواست شارژ شماره #%d با مبلغ %s تایید شد.", reqID, persian.FormatMoney(amount)))
	return HandleAdminMenu(c)
}

func HandleAdminRejectTopup(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}
	req, err := db.RejectTopupRequest(context.Background(), reqID, c.Sender().ID)
	if err != nil {
		return c.Send("خطا در رد درخواست شارژ.")
	}
	if req == nil {
		return c.Send("درخواست شارژ یافت نشد.")
	}
	target, _ := db.GetUserByID(context.Background(), req.UserID)
	if target != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, "درخواست افزایش موجودی کیف پول شما رد شد.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست شارژ #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست شارژ #%d رد شد.", reqID))
}

func HandleAdminApprovePurchase(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("purchase_req:%d", reqID))
	defer unlock()

	req, err := db.GetPurchaseRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		if req != nil && req.Status == db.PurchaseStatusNeedsManualReview {
			return c.Send("این رسید به دلیل تغییر شرایط طرح یا سرویس نیازمند بررسی دستی است و قابل تایید خودکار نیست.")
		}
		return c.Send("درخواست خرید یافت نشد یا قبلا پردازش شده است.")
	}
	if req.Type != "buy" && req.Type != "extend" && req.Type != "upgrade_ip" {
		return c.Send(fmt.Sprintf("نوع درخواست خرید پشتیبانی نمی‌شود: %s. درخواست تایید نشد.", req.Type))
	}

	user, err := db.GetUserByID(context.Background(), req.UserID)
	if err != nil || user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	amount := int64(0)
	if req.PriceToman != nil {
		amount = *req.PriceToman
	}
	financialKey := req.OperationKey
	if financialKey == "" {
		financialKey = fmt.Sprintf("purchase_approval:%d", req.ID)
	}
	payload := &reconcile.DirectPaymentProvisioningPayload{
		PurchaseRequestID: req.ID, UserID: user.ID, ActionType: req.Type,
		QuoteID: req.QuoteID, AmountToman: amount, FinancialOperationKey: financialKey,
		OperationKey: fmt.Sprintf("direct_payment:%d:provisioning", req.ID),
		ClientEmail:  req.ClientEmail, SubscriptionID: req.SubscriptionID,
		Months: req.Months, IPLimit: req.IPLimit, DataGB: req.DataGB, CustomName: req.CustomName,
	}
	if payload.ActionType == "buy" {
		payload.PlanID = nil
		if req.PlanID != nil {
			planID := int(*req.PlanID)
			payload.PlanID = &planID
		}
		payload.ExpectedUUID = snapshotString(req.ProvisioningSnapshot, "client_uuid")
		payload.ExpectedSubID = snapshotString(req.ProvisioningSnapshot, "sub_id")
		payload.InboundIDs = snapshotIntSlice(req.ProvisioningSnapshot["inbound_ids"])
		payload.Flow = snapshotString(req.ProvisioningSnapshot, "flow")
		payload.Group = snapshotString(req.ProvisioningSnapshot, "group")
		payload.TelegramID = snapshotInt64(req.ProvisioningSnapshot, "telegram_id")
		payload.ExpiryTimeMilli = snapshotInt64(req.ProvisioningSnapshot, "expiry_time_milli")
		payload.TotalBytes = snapshotInt64(req.ProvisioningSnapshot, "total_bytes")
		if payload.ExpectedUUID == "" || payload.ExpectedSubID == "" || len(payload.InboundIDs) == 0 || payload.ExpiryTimeMilli == 0 || payload.TotalBytes < 0 {
			return c.Send("اطلاعات هویتی و وضعیت سرویس در درخواست پرداخت ذخیره نشده است؛ درخواست تایید نشد و نیازمند بررسی دستی است.")
		}
	} else {
		if req.SubscriptionID == nil {
			return c.Send("شناسه اشتراک در درخواست موجود نیست؛ درخواست تایید نشد.")
		}
		sub, subErr := db.GetSubscriptionByID(context.Background(), int(*req.SubscriptionID))
		if subErr != nil || sub == nil || sub.UserID != req.UserID || sub.ClientEmail != req.ClientEmail || sub.ClientUUID == "" || sub.SubID == "" {
			return c.Send("هویت اشتراک درخواستی قابل تایید نیست؛ درخواست تایید نشد.")
		}
		payload.ExpectedUUID = sub.ClientUUID
		payload.ExpectedSubID = sub.SubID
		if sub.ExpireTime != nil {
			payload.ExpiryTimeMilli = *sub.ExpireTime
		}
		if payload.ActionType == "extend" {
			payload.ExpiryTimeMilli, err = db.CalculateExtendedExpiry(sub.ExpireTime, req.Months, nowUTC())
			if err != nil {
				return c.Send("تاریخ انقضای فعلی برای تمدید معتبر نیست؛ درخواست تایید نشد.")
			}
		} else if payload.ActionType == "upgrade_ip" {
			desiredLimit := req.IPLimit
			payload.DesiredIPLimit = &desiredLimit
		}
	}
	workItem := reconcile.NewDirectPaymentProvisioningRecord(payload)
	approved, err := db.ApprovePurchaseRequest(context.Background(), reqID, c.Sender().ID, workItem)
	if err != nil || approved == nil {
		if errors.Is(err, db.ErrSubscriptionMutationStale) || errors.Is(err, db.ErrSubscriptionMutationInvalid) || errors.Is(err, db.ErrSubscriptionMutationInProgress) {
			return c.Send("شرایط طرح یا سرویس پس از ثبت رسید تغییر کرده است؛ درخواست به بررسی دستی منتقل شد و فعال‌سازی انجام نشد.")
		}
		log.Printf("[ERROR] failed to atomically approve purchase #%d and persist provisioning work: %v", reqID, err)
		return c.Send("خطا در ثبت تایید و کار فعال‌سازی درخواست خرید.")
	}

	if bot.XUIClient != nil {
		processor := reconcile.NewProcessor("direct_approval", bot.XUIClient)
		_, _ = processor.ProcessOnce(context.Background())
	}
	refreshed, _ := db.GetPurchaseRequestByID(context.Background(), reqID)
	if refreshed != nil && refreshed.ProvisioningStatus == db.PurchaseProvisioningSucceeded {
		_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID)})
		return c.Edit(fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID))
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("تایید درخواست خرید #%d ثبت شد؛ فعال‌سازی ادامه می‌یابد.", reqID)})
	return c.Edit(fmt.Sprintf("✅ درخواست خرید #%d تایید شد و برای فعال‌سازی امن ثبت گردید.", reqID))
}

func snapshotString(snapshot map[string]any, key string) string {
	value, _ := snapshot[key]
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func snapshotInt64(snapshot map[string]any, key string) int64 {
	value, _ := snapshot[key]
	parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", value)), 10, 64)
	return parsed
}

func snapshotIntSlice(value any) []int {
	switch values := value.(type) {
	case []int:
		return append([]int(nil), values...)
	case []int64:
		out := make([]int, 0, len(values))
		for _, v := range values {
			out = append(out, int(v))
		}
		return out
	case []any:
		out := make([]int, 0, len(values))
		for _, v := range values {
			parsed, err := strconv.Atoi(strings.TrimSpace(fmt.Sprintf("%v", v)))
			if err == nil {
				out = append(out, parsed)
			}
		}
		return out
	default:
		return nil
	}
}

func HandleAdminRejectPurchase(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	req, err := db.RejectPurchaseRequest(context.Background(), reqID, c.Sender().ID)
	if err != nil {
		return c.Send("خطا در رد درخواست.")
	}
	if req == nil {
		return c.Send("درخواست یافت نشد.")
	}

	user, _ := db.GetUserByID(context.Background(), req.UserID)
	if user != nil {
		var actionLabel string
		switch req.Type {
		case "buy":
			actionLabel = "خرید سرویس"
		case "extend":
			actionLabel = "تمدید سرویس"
		case "upgrade_ip":
			actionLabel = "ارتقای تعداد IP همزمان"
		case "claim":
			actionLabel = "ثبت اشتراک قدیمی"
		}
		msg := fmt.Sprintf("❌ درخواست پرداخت مستقیم شما برای **%s** به مبلغ %s توسط ادمین رد شد. لطفا رسید واریزی خود را بررسی کنید یا با پشتیبانی در ارتباط باشید.", actionLabel, persian.FormatMoney(purchaseAmountToman(req)))
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID))
}

func purchaseAmountToman(req *db.PurchaseRequest) int64 {
	if req == nil || req.PriceToman == nil {
		return 0
	}
	return *req.PriceToman
}

func extendSubscriptionFromApprovedRequest(user *db.User, sub *db.Subscription, req *db.PurchaseRequest) error {
	oldEnd := sub.EndDate
	var oldExpireTime *int64
	if sub.ExpireTime != nil {
		val := *sub.ExpireTime
		oldExpireTime = &val
	}
	oldIsActive := sub.IsActive

	var newExpiryMilli int64
	var newExpiryLabel string

	if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		newDuration := -(*sub.ExpireTime) + int64(req.Months)*30*24*3600*1000
		newExpiryMilli = -newDuration
		sub.ExpireTime = &newExpiryMilli
		sub.EndDate = time.Time{}
		newExpiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", newDuration/(24*3600*1000))
	} else {
		if sub.EndDate.Before(nowUTC()) {
			sub.EndDate = nowUTC()
		}
		sub.EndDate = sub.EndDate.Add(time.Duration(req.Months) * 30 * 24 * time.Hour)
		newExpiryMilli = sub.EndDate.UnixMilli()
		sub.ExpireTime = &newExpiryMilli
		newExpiryLabel = sub.EndDate.Format("2006-01-02")
	}

	sub.IsActive = true

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		return fmt.Errorf("خطا در بروزرسانی پنل: %w", err)
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		return fmt.Errorf("خطا در ذخیره‌سازی دیتابیس: %w", err)
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و اشتراک **%s** به مدت %d ماه تمدید شد.\nتاریخ انقضای جدید: %s\nمبلغ پرداخت شده: %s.",
		sub.DisplayName, req.Months, newExpiryLabel, persian.FormatMoney(purchaseAmountToman(req)))
	_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	return nil
}

func upgradeSubscriptionIPFromApprovedRequest(user *db.User, sub *db.Subscription, req *db.PurchaseRequest) error {
	oldLimit := sub.IPLimit
	sub.IPLimit = req.IPLimit

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.IPLimit = oldLimit
		return fmt.Errorf("خطا در بروزرسانی پنل: %w", err)
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.IPLimit = oldLimit
		return fmt.Errorf("خطا در ذخیره‌سازی دیتابیس: %w", err)
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و سقف IP همزمان اشتراک **%s** به %s IP ارتقا یافت.\nهزینه ارتقا پرداخت شده: %s.",
		sub.DisplayName, formatIPLimit(req.IPLimit), persian.FormatMoney(purchaseAmountToman(req)))
	_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	return nil
}

func ProcessManualCreditAmount(c telebot.Context, amountText string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	state := bot.FSM.GetState(admin.TelegramID)
	if state == nil {
		return c.Send("فرآیند افزایش موجودی دستی فعالی وجود ندارد.")
	}
	targetID, _ := parseInt64(fmt.Sprintf("%v", state.Data["target_user_id"]))
	amount, err := strconv.ParseInt(strings.TrimSpace(amountText), 10, 64)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. یک عدد صحیح مثبت (به تومان) وارد کنید:")
	}
	target, err := db.GetUserByID(context.Background(), targetID)
	if err != nil || target == nil {
		return c.Send("کاربر یافت نشد.")
	}
	operationKey := ""
	if op, ok := state.Data["operation_key"]; ok && op != nil && op != "" && op != "<nil>" {
		operationKey = fmt.Sprintf("%v", op)
	}
	if operationKey == "" {
		operationKey = fmt.Sprintf("manual_admin_credit:%d:%d:%d", admin.TelegramID, target.ID, time.Now().UnixNano())
	}
	if err := db.CreditWalletBalanceWithKey(context.Background(), target.ID, amount, "manual admin credit", operationKey); err != nil {
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این عملیات شارژ قبلاً اعمال شده است.")
		}
		return c.Send("خطا در افزایش موجودی کاربر.")
	}
	bot.FSM.ClearState(admin.TelegramID)
	_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, fmt.Sprintf("کیف پول شما به مبلغ %s شارژ شد.", persian.FormatMoney(amount)))
	_ = c.Send(fmt.Sprintf("✅ کیف پول کاربر #%d به مبلغ %s شارژ شد.", target.ID, persian.FormatMoney(amount)))
	target, _ = db.GetUserByID(context.Background(), targetID)
	if target != nil {
		return showAdminViewUser(c, target)
	}
	return nil
}

func HandleAdminClaimAssign(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر.")
	}
	reqID, err := parseInt64(parts[0])
	if err != nil || reqID == 0 {
		return c.Send("شناسه درخواست نامعتبر.")
	}
	planID, err := parseInt64(parts[1])
	if err != nil || planID == 0 {
		return c.Send("شناسه طرح نامعتبر.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("purchase_req:%d", reqID))
	defer unlock()

	req, err := db.GetPurchaseRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		return c.Send("درخواست یافت نشد یا قبلا پردازش شده است.")
	}

	user, err := db.GetUserByID(context.Background(), req.UserID)
	if err != nil || user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}

	// Update plan_id in purchase request before approving
	_, err = db.Pool.Exec(context.Background(), "UPDATE purchase_requests SET plan_id = $1 WHERE id = $2", plan.ID, req.ID)
	if err != nil {
		return c.Send("خطا در بروزرسانی طرح درخواست در دیتابیس.")
	}

	claimPayload := &reconcile.SubscriptionClaimPayload{
		PurchaseRequestID: req.ID, UserID: user.ID, PlanID: int64(plan.ID),
		ClientEmail: req.ClientEmail, SubID: req.CustomName,
	}
	workItem := reconcile.NewSubscriptionClaimRecord(claimPayload, req.ID)
	req, err = db.ApprovePurchaseRequest(context.Background(), req.ID, c.Sender().ID, workItem)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست ثبت اشتراک.")
	}
	if bot.XUIClient != nil {
		processor := reconcile.NewProcessor("claim_approval", bot.XUIClient)
		_, _ = processor.ProcessOnce(context.Background())
	}
	refreshed, _ := db.GetPurchaseRequestByID(context.Background(), req.ID)
	if refreshed == nil || refreshed.ProvisioningStatus != db.PurchaseProvisioningSucceeded {
		return c.Send("درخواست ثبت اشتراک تایید و برای تکمیل ایمن ذخیره شد؛ بررسی پنل پس‌زمینه ادامه می‌یابد.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست ثبت اشتراک #%d تایید شد.", req.ID)})
	return c.Edit(fmt.Sprintf("✅ درخواست ثبت اشتراک #%d تایید شد و طرح %s اختصاص یافت.", req.ID, plan.Name))
}

func HandleAdminPendingClaims(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqs, err := db.GetPendingPurchaseRequests(context.Background())
	if err != nil {
		return c.Send("خطا در دریافت درخواست‌ها.")
	}

	var claimReqs []*db.PurchaseRequest
	for _, r := range reqs {
		if r.Type == "claim" {
			claimReqs = append(claimReqs, r)
		}
	}

	if len(claimReqs) == 0 {
		return c.Send("هیچ درخواست ثبت اشتراک دستی در انتظاری وجود ندارد.")
	}

	paidPlans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil {
		log.Printf("Failed to fetch paid plans: %v", err)
	}

	for _, req := range claimReqs {
		user, _ := db.GetUserByID(context.Background(), req.UserID)
		username := "unknown"
		if user != nil {
			username = user.Username
		}

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
			req.ID, username, req.UserID, req.ClientEmail, req.CustomName, formatIPLimit(req.IPLimit), req.DataGB)

		_, _ = bot.Bot.Send(c.Sender(), FormatMarkdown(caption), menu, telebot.ModeMarkdown)
	}
	return nil
}

func HandleAdminManualPaymentReviews(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqs, err := db.GetPurchaseRequestsNeedingManualReview(context.Background())
	if err != nil {
		return c.Send("خطا در دریافت رسیدهای نیازمند بررسی دستی.")
	}
	if len(reqs) == 0 {
		return c.Send("رسیدی برای بررسی دستی وجود ندارد.")
	}
	for _, req := range reqs {
		user, _ := db.GetUserByID(context.Background(), req.UserID)
		username := "unknown"
		if user != nil {
			username = user.Username
		}
		caption := fmt.Sprintf("⚠️ رسید پرداخت نیازمند بررسی دستی #%d\nکاربر: @%s (%d)\nنوع: %s\nمبلغ: %s\nایمیل: %s\nشناسه intent: %v\nدلیل: شرایط تجاری پس از صدور پرداخت تغییر کرده است؛ فعال‌سازی خودکار مسدود است.", req.ID, username, req.UserID, req.Type, persian.FormatMoney(purchaseAmountToman(req)), req.ClientEmail, req.PaymentIntentID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("رد درخواست", "admin_reject_purchase", fmt.Sprintf("%d", req.ID))))
		_, _ = bot.Bot.Send(c.Sender(), &telebot.Photo{File: telebot.File{FileID: req.TelegramFileID}, Caption: caption}, menu)
	}
	return nil
}
