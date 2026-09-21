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
	"xui-end-bot/internal/xui"
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "تومان"
	}
	menu := &telebot.ReplyMarkup{}
	row := []telebot.Row{menu.Row(menu.Data("📥 شارژ کیف پول", "btn_topup"))}
	if isConfiguredAdmin(c.Sender().ID) {
		row = append(row, menu.Row(menu.Data("⏳ تراکنش‌های در انتظار شارژ", "admin_list_topups")))
	}
	rows := append(row, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("👛 **موجودی کیف پول شما:** %d %s", user.WalletBalance, currency), menu)
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

	bot.FSM.SetState(user.TelegramID, "awaiting_receipt", nil)
	text := "لطفا پس از واریز مبلغ مورد نظر، تصویر رسید پرداخت (فیش واریزی) خود را در قالب عکس ارسال کنید."
	if card != "" {
		text += "\n\nشماره کارت جهت واریز:\n`" + card + "`"
	}
	if owner != "" {
		text += "\nنام صاحب کارت: **" + owner + "**"
	}
	if minAmount != "" && minAmount != "0" {
		text += "\nحداقل مبلغ شارژ مجاز: " + minAmount
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

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("هیچ فرآیند فعالی برای ارسال رسید وجود ندارد. لطفا ابتدا درخواست پرداخت خود را ثبت کنید.")
	}

	if c.Message() == nil || c.Message().Photo == nil {
		return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
	}
	fileID := c.Message().Photo.FileID

	if state.Step == "awaiting_receipt" {
		req := &db.TopupRequest{
			UserID:         user.ID,
			TelegramFileID: fileID,
			Status:         "pending",
		}
		if err := db.CreateTopupRequest(context.Background(), req); err != nil {
			return c.Send("خطا در ثبت درخواست افزایش موجودی.")
		}
		bot.FSM.ClearState(user.TelegramID)

		if walletAdminCfg != nil {
			for _, adminID := range walletAdminCfg.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(menu.Row(
					menu.Data("تایید", "admin_approve_topup", fmt.Sprintf("%d", req.ID)),
					menu.Data("رد", "admin_reject_topup", fmt.Sprintf("%d", req.ID)),
				))
				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: fmt.Sprintf("درخواست افزایش موجودی کیف پول #%d\nکاربر: @%s\nشناسه تلگرام: %d", req.ID, user.Username, user.TelegramID)}, menu)
			}
		}
		_ = c.Send("رسید شما دریافت شد. لطفا منتظر بررسی و تایید ادمین بمانید.")
		return showMainMenu(c, user)

	} else if state.Step == "awaiting_purchase_receipt" {
		pType := fmt.Sprintf("%v", state.Data["type"])
		priceStr := fmt.Sprintf("%v", state.Data["price"])
		price, _ := strconv.ParseFloat(priceStr, 64)

		var planIDPtr *int64
		if pidStr, ok := state.Data["plan_id"]; ok && pidStr != "" {
			pid, _ := parseInt64(fmt.Sprintf("%v", pidStr))
			planIDPtr = &pid
		}

		var subIDPtr *int64
		if sidStr, ok := state.Data["subscription_id"]; ok && sidStr != "" {
			sid, _ := parseInt64(fmt.Sprintf("%v", sidStr))
			subIDPtr = &sid
		}

		var months int
		if mStr, ok := state.Data["months"]; ok && mStr != "" {
			months, _ = strconv.Atoi(fmt.Sprintf("%v", mStr))
		}

		var ipLimit int
		if ipStr, ok := state.Data["ip_limit"]; ok && ipStr != "" {
			ipLimit, _ = strconv.Atoi(fmt.Sprintf("%v", ipStr))
		}

		var dataGB int
		if gbStr, ok := state.Data["data_gb"]; ok && gbStr != "" {
			dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", gbStr))
		}

		customName := ""
		if cn, ok := state.Data["custom_name"]; ok {
			customName = fmt.Sprintf("%v", cn)
		}

		email := ""
		if em, ok := state.Data["email"]; ok {
			email = fmt.Sprintf("%v", em)
		}
		operationKey := ""
		if op, ok := state.Data["operation_key"]; ok {
			operationKey = strings.TrimSpace(fmt.Sprintf("%v", op))
		}

		var quoteIDPtr *int64
		if qIDStr, ok := state.Data["quote_id"]; ok && qIDStr != "" {
			if qID, err := strconv.ParseInt(fmt.Sprintf("%v", qIDStr), 10, 64); err == nil && qID > 0 {
				quoteIDPtr = &qID
			}
		}
		var priceTomanPtr *int64
		if ptStr, ok := state.Data["price_toman"]; ok && ptStr != "" {
			if pt, err := strconv.ParseInt(fmt.Sprintf("%v", ptStr), 10, 64); err == nil && pt > 0 {
				priceTomanPtr = &pt
			}
		}
		if priceTomanPtr == nil && price > 0 {
			pt := int64(price)
			priceTomanPtr = &pt
		}

		req := &db.PurchaseRequest{
			UserID:         user.ID,
			Type:           pType,
			PlanID:         planIDPtr,
			SubscriptionID: subIDPtr,
			Price:          price,
			PriceToman:     priceTomanPtr,
			QuoteID:        quoteIDPtr,
			Months:         months,
			IPLimit:        ipLimit,
			DataGB:         dataGB,
			CustomName:     customName,
			ClientEmail:    email,
			TelegramFileID: fileID,
			Status:         "pending",
			OperationKey:   operationKey,
		}

		if err := db.CreatePurchaseRequest(context.Background(), req); err != nil {
			log.Printf("Failed to create purchase request: %v", err)
			return c.Send("خطا در ثبت درخواست خرید مستقیم.")
		}
		bot.FSM.ClearState(user.TelegramID)

		currency, _ := db.GetSetting(context.Background(), "currency_name")
		if currency == "" {
			currency = "تومان"
		}

		if walletAdminCfg != nil {
			for _, adminID := range walletAdminCfg.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(menu.Row(
					menu.Data("تایید خرید", "admin_approve_purchase", fmt.Sprintf("%d", req.ID)),
					menu.Data("رد خرید", "admin_reject_purchase", fmt.Sprintf("%d", req.ID)),
				))

				var details string
				switch pType {
				case "buy":
					details = fmt.Sprintf("خرید سرویس جدید\nطرح: %s\nایمیل: %s\nمدت: %d ماه\nکاربر همزمان: %s\nحجم: %d گیگابایت", customName, email, months, formatIPLimit(ipLimit), dataGB)
				case "extend":
					details = fmt.Sprintf("تمدید سرویس\nشناسه اشتراک: %d\nمدت تمدید: %d ماه", *subIDPtr, months)
				case "upgrade_ip":
					details = fmt.Sprintf("ارتقای تعداد کاربر همزمان\nشناسه اشتراک: %d\nتعداد کاربر جدید: %s", *subIDPtr, formatIPLimit(ipLimit))
				}

				caption := fmt.Sprintf("📥 درخواست خرید مستقیم #%d\nکاربر: @%s (%d)\nنوع: %s\nمبلغ: %s\n\nجزئیات:\n%s",
					req.ID, user.Username, user.TelegramID, pType, persian.FormatMoney(*priceTomanPtr), details)

				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: caption}, menu)
			}
		}

		_ = c.Send("رسید پرداخت شما دریافت شد. پس از بررسی ادمین، سرویس شما فعال شده و مشخصات آن برایتان ارسال خواهد شد.")
		return showMainMenu(c, user)
	}

	return c.Send("مرحله نامعتبر است. لطفا مجددا تلاش کنید.")
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
		return c.Send("درخواست خرید یافت نشد یا قبلا پردازش شده است.")
	}

	user, err := db.GetUserByID(context.Background(), req.UserID)
	if err != nil || user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	adminUser := userFromContext(c)
	req, err = db.ApprovePurchaseRequest(context.Background(), reqID, adminUser.TelegramID)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست خرید.")
	}

	var activationErr error
	var plan *db.PaidPlan
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "تومان"
	}

	switch req.Type {
	case "buy":
		var err error
		plan, err = db.GetPaidPlanByID(context.Background(), *req.PlanID)
		if err != nil || plan == nil {
			activationErr = fmt.Errorf("طرح خرید یافت نشد")
			break
		}
		activationErr = createSubscriptionFromApprovedRequest(user, plan, req)

	case "extend":
		sub, err := db.GetSubscriptionByID(context.Background(), int(*req.SubscriptionID))
		if err != nil || sub == nil {
			activationErr = fmt.Errorf("اشتراک یافت نشد")
			break
		}
		activationErr = extendSubscriptionFromApprovedRequest(user, sub, req)

	case "upgrade_ip":
		sub, err := db.GetSubscriptionByID(context.Background(), int(*req.SubscriptionID))
		if err != nil || sub == nil {
			activationErr = fmt.Errorf("اشتراک یافت نشد")
			break
		}
		activationErr = upgradeSubscriptionIPFromApprovedRequest(user, sub, req)
	}

	if activationErr != nil {
		log.Printf("[CRITICAL] Activation failed for purchase request #%d: %v", reqID, activationErr)
		provisioningStatus := db.PurchaseProvisioningFailed
		if xui.IsUnknownOutcome(activationErr) {
			provisioningStatus = db.PurchaseProvisioningRetryable
		}
		if statusErr := db.SetPurchaseProvisioningStatus(context.Background(), reqID, provisioningStatus); statusErr != nil {
			log.Printf("[CRITICAL] failed to persist provisioning status for purchase request #%d: %v", reqID, statusErr)
		}
		if provisioningStatus == db.PurchaseProvisioningRetryable {
			var expectedUUID, expectedSubID string
			var inboundIDs []int
			var unknownCreate *paidSubscriptionCreateUnknownError
			if errors.As(activationErr, &unknownCreate) && unknownCreate.Request.Client.ID != "" {
				expectedUUID = unknownCreate.Request.Client.ID
				expectedSubID = unknownCreate.Request.Client.SubID
				inboundIDs = unknownCreate.Request.InboundIDs
			}
			if len(inboundIDs) == 0 && plan != nil {
				inboundIDs = validInboundIDs(plan.InboundIDs)
			}
			var planID *int
			if req.PlanID != nil {
				id := int(*req.PlanID)
				planID = &id
			}
			payload := &reconcile.DirectPaymentProvisioningPayload{
				PurchaseRequestID: reqID,
				UserID:            user.ID,
				QuoteID:           req.QuoteID,
				OperationKey:      fmt.Sprintf("direct_payment:%d:provisioning", reqID),
				ClientEmail:       req.ClientEmail,
				ExpectedUUID:      expectedUUID,
				ExpectedSubID:     expectedSubID,
				PlanID:            planID,
				InboundIDs:        inboundIDs,
				Months:            req.Months,
				IPLimit:           req.IPLimit,
				DataGB:            req.DataGB,
				CustomName:        req.CustomName,
			}
			record := reconcile.NewDirectPaymentProvisioningRecord(payload)
			record.ObservedState = map[string]any{
				"outcome": "activation_failed",
				"error":   activationErr.Error(),
			}
			record.ErrorMessage = activationErr.Error()
			if recErr := db.CreateReconciliationRecord(context.Background(), record); recErr != nil {
				log.Printf("[CRITICAL] failed to persist provisioning reconciliation for purchase request #%d: %v", reqID, recErr)
			}
			return c.Send("پرداخت شما تایید شده است اما نتیجه فعال‌سازی سرویس در پنل نامشخص است؛ مبلغ و تایید پرداخت حفظ شد و وضعیت برای تلاش مجدد خودکار ثبت گردید.")
		}
		return c.Send("پرداخت شما تایید شده است اما فعال‌سازی سرویس انجام نشد؛ تایید پرداخت و تراکنش مالی حفظ شد و وضعیت خطا ثبت گردید.")
	}
	if statusErr := db.SetPurchaseProvisioningStatus(context.Background(), reqID, db.PurchaseProvisioningSucceeded); statusErr != nil {
		log.Printf("[CRITICAL] purchase request #%d activated but provisioning status update failed: %v", reqID, statusErr)
		return c.Send("پرداخت تایید و سرویس فعال شد، اما ثبت وضعیت فعال‌سازی در دیتابیس نیازمند تطبیق است.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID)})
	return c.Edit(fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID))
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
			actionLabel = "ارتقای تعداد کاربر همزمان"
		case "claim":
			actionLabel = "ثبت اشتراک قدیمی"
		}
		msg := fmt.Sprintf("❌ درخواست پرداخت مستقیم شما برای **%s** به مبلغ %.0f توسط ادمین رد شد. لطفا رسید واریزی خود را بررسی کنید یا با پشتیبانی در ارتباط باشید.", actionLabel, req.Price)
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID))
}

func createSubscriptionFromApprovedRequest(user *db.User, plan *db.PaidPlan, req *db.PurchaseRequest) error {
	if bot.XUIClient == nil {
		return fmt.Errorf("x-ui client is not initialized")
	}
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return fmt.Errorf("این طرح هیچ کانکشن معتبری ندارد")
	}

	expireMilli := -int64(req.Months * 30 * 24 * 3600 * 1000)
	totalBytes := int64(req.DataGB) * 1073741824
	subID := makeSubID()
	clientUUID := makeClientUUID()
	client := prepareClientConfig(req.ClientEmail, serviceGroup(user), user.TelegramID, totalBytes, expireMilli, req.IPLimit, plan.Flow, subID, clientUUID, plan.Name, user)

	err := bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
	if err != nil && !xui.IsUnknownOutcome(err) {
		log.Printf("XUI AddClient failed: %v. Refreshing cache and retrying...", err)
		if bot.XUIClient.Cache != nil {
			bot.XUIClient.Cache.RefreshSync()
			newInboundIDs := validInboundIDs(plan.InboundIDs)
			if !intSlicesEqual(newInboundIDs, inboundIDs) {
				if len(newInboundIDs) == 0 {
					return fmt.Errorf("این طرح پس از بروزرسانی هیچ کانکشن معتبری ندارد")
				}
				err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: newInboundIDs})
			}
		}
	}
	if err != nil {
		if xui.IsUnknownOutcome(err) {
			return &paidSubscriptionCreateUnknownError{cause: err, Request: xui.AddClientRequest{Client: client, InboundIDs: inboundIDs}}
		}
		return err
	}

	planID := int(plan.ID)
	sub := &db.Subscription{
		UserID:            user.ID,
		PlanID:            &planID,
		QuoteID:           req.QuoteID,
		ClientEmail:       req.ClientEmail,
		ClientUUID:        clientUUID,
		SubID:             subID,
		Status:            "active",
		PlanType:          db.PlanTypePaid,
		DisplayName:       req.CustomName,
		IPLimit:           req.IPLimit,
		ExpireTime:        &expireMilli,
		IsActive:          true,
		StartDate:         nowUTC(),
		EndDate:           time.Time{},
		TrafficLimitBytes: totalBytes,
	}
	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		log.Printf("[CRITICAL] Database save failed for subscription %s: %v. Initiating safe compensation...", req.ClientEmail, err)
		deleteErr := bot.XUIClient.DeleteClient(req.ClientEmail)
		resolution, resErr := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
			return bot.XUIClient.GetClientByEmail(req.ClientEmail)
		})
		reqID64 := req.ID
		desired := map[string]any{
			"email":               req.ClientEmail,
			"client_id":           clientUUID,
			"client_uuid":         clientUUID,
			"sub_id":              subID,
			"inbound_ids":         inboundIDs,
			"expiry_time":         expireMilli,
			"ip_limit":            req.IPLimit,
			"total_gb":            client.TotalGB,
			"plan_id":             plan.ID,
			"user_id":             user.ID,
			"purchase_request_id": req.ID,
			"db_error":            err.Error(),
		}
		observed := map[string]any{"remote_created": true}
		if deleteErr != nil {
			observed["delete_error"] = deleteErr.Error()
		}
		if resErr != nil {
			observed["resolution_error"] = resErr.Error()
		}

		if resolution == deleteConfirmed {
			observed["remote_deleted"] = true
			if recErr := db.CreateReconciliationRecord(context.Background(), &db.ReconciliationRecord{
				OperationKey:      fmt.Sprintf("purchase_request_comp:%d", req.ID),
				Kind:              "purchase_request_db_failed_compensated",
				UserID:            &user.ID,
				PurchaseRequestID: &reqID64,
				DesiredState:      desired,
				ObservedState:     observed,
				Status:            "compensated",
				ErrorMessage:      fmt.Sprintf("DB save failed: %v; remote client deleted", err),
			}); recErr != nil {
				log.Printf("[CRITICAL] failed to persist purchase request compensation record for #%d: %v", req.ID, recErr)
			}
			return fmt.Errorf("failed to save subscription in database (remote client cancelled): %w", err)
		}

		observed["remote_deleted"] = false
		if resolution == deleteStillPresent {
			observed["client_present"] = true
		}
		if recErr := db.CreateReconciliationRecord(context.Background(), &db.ReconciliationRecord{
			OperationKey:      fmt.Sprintf("purchase_request_comp:%d", req.ID),
			Kind:              "purchase_request_db_failed_reconciliation",
			UserID:            &user.ID,
			PurchaseRequestID: &reqID64,
			DesiredState:      desired,
			ObservedState:     observed,
			Status:            "reconciliation_required",
			ErrorMessage:      fmt.Sprintf("DB save failed: %v; remote delete outcome: %s", err, resolution),
		}); recErr != nil {
			log.Printf("[CRITICAL] failed to persist purchase request reconciliation record for #%d: %v", req.ID, recErr)
		}
		return &xui.WriteError{Outcome: xui.WriteUnknown, Err: fmt.Errorf("DB save failed (%v) and remote client deletion is %s", err, resolution)}
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "تومان"
	}
	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", req.DataGB)
	}

	detailsMsg := fmt.Sprintf("✅ پرداخت شما تایید و اشتراک با موفقیت فعال شد!\n📦 طرح: %s\n⏱️ مدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\n📊 سقف ترافیک: %s\n💰 هزینه پرداخت شده: %.0f %s",
		plan.Name, req.Months, dataLabel, req.Price, currency)

	if plan.UsageDescription != "" {
		detailsMsg += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.UsageDescription)
	}

	_ = sendSubscriptionResultTo(user.TelegramID, subLink, detailsMsg)
	return nil
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "تومان"
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و اشتراک **%s** به مدت %d ماه تمدید شد.\nتاریخ انقضای جدید: %s\nمبلغ پرداخت شده: %.0f %s.",
		sub.DisplayName, req.Months, newExpiryLabel, req.Price, currency)
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "تومان"
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و سقف کاربر همزمان اشتراک **%s** به %s دستگاه ارتقا یافت.\nهزینه ارتقا پرداخت شده: %.0f %s.",
		sub.DisplayName, formatIPLimit(req.IPLimit), req.Price, currency)
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
	operationKey := fmt.Sprintf("%v", state.Data["operation_key"])
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
	_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, fmt.Sprintf("کیف پول شما به مبلغ %s تومان شارژ شد.", persian.FormatMoney(amount)))
	_ = c.Send(fmt.Sprintf("✅ کیف پول کاربر #%d به مبلغ %s تومان شارژ شد.", target.ID, persian.FormatMoney(amount)))
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

	adminUser := userFromContext(c)
	req, err = db.ApprovePurchaseRequest(context.Background(), req.ID, adminUser.TelegramID)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست ثبت اشتراک.")
	}

	// Actually link and sync the subscription
	err = createSubscriptionFromApprovedClaim(user, plan, req)
	if err != nil {
		log.Printf("[CRITICAL] Claim activation failed for request #%d: %v", req.ID, err)
		provisioningStatus := db.PurchaseProvisioningFailed
		if xui.IsUnknownOutcome(err) {
			provisioningStatus = db.PurchaseProvisioningRetryable
		}
		if statusErr := db.SetPurchaseProvisioningStatus(context.Background(), req.ID, provisioningStatus); statusErr != nil {
			log.Printf("[CRITICAL] failed to persist provisioning status for claim request #%d: %v", req.ID, statusErr)
		}
		if provisioningStatus == db.PurchaseProvisioningRetryable {
			return c.Send("پرداخت شما تایید شده است اما ثبت اشتراک در پنل نامشخص است؛ تایید پرداخت و تراکنش مالی حفظ شد و وضعیت برای تطبیق/تلاش مجدد ثبت گردید.")
		}
		return c.Send("پرداخت شما تایید شده است اما ثبت اشتراک انجام نشد؛ تایید پرداخت و تراکنش مالی حفظ شد و وضعیت خطا ثبت گردید.")
	}
	if statusErr := db.SetPurchaseProvisioningStatus(context.Background(), req.ID, db.PurchaseProvisioningSucceeded); statusErr != nil {
		log.Printf("[CRITICAL] claim request #%d activated but provisioning status update failed: %v", req.ID, statusErr)
		return c.Send("پرداخت تایید و اشتراک ثبت شد، اما ثبت وضعیت فعال‌سازی در دیتابیس نیازمند تطبیق است.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست ثبت اشتراک #%d تایید شد.", req.ID)})
	return c.Edit(fmt.Sprintf("✅ درخواست ثبت اشتراک #%d تایید شد و طرح %s اختصاص یافت.", req.ID, plan.Name))
}

func createSubscriptionFromApprovedClaim(user *db.User, plan *db.PaidPlan, req *db.PurchaseRequest) error {
	if bot.XUIClient == nil {
		return fmt.Errorf("x-ui client is not initialized")
	}

	// Fetch client from 3x-ui by SubID (stored in req.CustomName)
	targetClient, err := bot.XUIClient.FindClientBySubID(req.CustomName)
	if err != nil || targetClient == nil {
		return fmt.Errorf("client with sub ID %s not found on panel: %w", req.CustomName, err)
	}

	// Check if this email is already registered in DB (avoid UNIQUE violation)
	existing, _ := db.GetSubscriptionByEmail(context.Background(), targetClient.Email)
	if existing != nil {
		return fmt.Errorf("subscription with email %s already registered in database", targetClient.Email)
	}

	// Map client values
	var expireMilli int64 = targetClient.ExpiryTime
	var trafficLimitBytes int64 = targetClient.TotalGB // TotalGB is bytes limit in 3x-ui
	var endDate time.Time
	if expireMilli > 0 {
		endDate = time.UnixMilli(expireMilli)
	}

	planID := int(plan.ID)
	devLimit, ok := parseDeviceLimitFromXUI(*targetClient)
	ipLimit := 1
	if ok && devLimit > 0 {
		ipLimit = devLimit
	} else if targetClient.LimitIP > 0 {
		ipLimit = targetClient.LimitIP
	}

	clientUUID := targetClient.UUID
	if clientUUID == "" {
		clientUUID = targetClient.Password
	}

	sub := &db.Subscription{
		UserID:            user.ID,
		PlanID:            &planID,
		ClientEmail:       targetClient.Email,
		ClientUUID:        clientUUID,
		SubID:             targetClient.SubID,
		Status:            "active",
		PlanType:          db.PlanTypePaid,
		DisplayName:       targetClient.Email,
		IPLimit:           ipLimit,
		ExpireTime:        &expireMilli,
		IsActive:          targetClient.Enable,
		StartDate:         time.Now().UTC(),
		EndDate:           endDate,
		TrafficLimitBytes: trafficLimitBytes,
	}

	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		return fmt.Errorf("failed to save subscription in database: %w", err)
	}

	// Update the client on the panel to set tgId and add the comment with the plan name!
	if err := updateXUIFromSubscription(sub); err != nil {
		log.Printf("[WARNING] Failed to update client %s on panel upon claim approval: %v", sub.ClientEmail, err)
		// We do not fail the whole approval process if just updating the panel comment/tgId fails,
		// as the DB record is already created and client is functional.
	}

	// Send success notification to the user
	targetUser := &telebot.User{ID: user.TelegramID}
	successMsg := fmt.Sprintf("✅ درخواست ثبت اشتراک شما تایید شد.\n\nسرویس **%s** به لیست سرویس‌های شما اضافه شد و اکنون می‌توانید آن را مدیریت کنید.", sub.ClientEmail)
	_, _ = bot.Bot.Send(targetUser, FormatMarkdown(successMsg), telebot.ModeMarkdown)

	// Send subscription connection details to user (QR and link)
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
	_ = sendSubscriptionResultTo(user.TelegramID, subLink, detailsMsg)

	return nil
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

		caption := fmt.Sprintf("📥 **درخواست ثبت اشتراک دستی #%d**\n\nکاربر: @%s (%d)\nایمیل اشتراک: `%s`\nشناسه اشتراک: `%s`\nکاربر همزمان: %s\nحجم: %d گیگابایت\n\nلطفا یکی از طرح‌های زیر را برای این اشتراک انتخاب کنید تا تایید شود:",
			req.ID, username, req.UserID, req.ClientEmail, req.CustomName, formatIPLimit(req.IPLimit), req.DataGB)

		_, _ = bot.Bot.Send(c.Sender(), FormatMarkdown(caption), menu, telebot.ModeMarkdown)
	}
	return nil
}
