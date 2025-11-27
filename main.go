package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot token va Admin ID (o'zingiznikiga almashtiring)
var BOT_TOKEN = "7982906158:AAGWlif2nGgc5n0PgRoeCSg1RoaeHQ5bZL0"
var ADMIN_ID int64 = 7518992824 // O'zingizni admin ID

// ---------------- STATISTIKA ----------------
var (
	startCount  int                    // /start bosganlar soni
	searchStats = make(map[string]int) // kod qidirish statistikasi
	statsMutex  sync.Mutex             // xavfsiz saqlash uchun
)

// ---------------- Foydalanuvchi va admin holatlari ----------------
var adminState = make(map[int64]string)
var adminTempID = make(map[int64]int64)
var animeCode = make(map[int64]string)
var animeTemp = make(map[int64][]string)
var animeStorage = make(map[string][]string)
var storageMutex sync.RWMutex

// Kanal saqlash: [ChatID]Username
var channels = make(map[int64]string)

func main() {
	bot, err := tgbotapi.NewBotAPI(BOT_TOKEN)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Bot ishga tushdi...")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		handleUpdate(bot, update) // ✅ goroutine kerak emas
	}

}

// ---------------- UPDATE HANDLER ----------------
func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if update.Message != nil {
		handleMessage(bot, update)
	} else if update.CallbackQuery != nil {
		handleCallback(bot, update)
	}
}

// ---------------- MESSAGE HANDLER ----------------
func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	text := update.Message.Text
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID

	// ------------------ ADMIN PANEL ------------------
	if text == "/admin" && userID == ADMIN_ID {
		msg := tgbotapi.NewMessage(chatID, "🛠 Admin panel")
		msg.ReplyMarkup = adminMenu()
		bot.Send(msg)
		return
	}

	// ------------------ /start ------------------
	if text == "/start" && userID != ADMIN_ID {
		statsMutex.Lock()
		startCount++
		statsMutex.Unlock()

		msg := tgbotapi.NewMessage(chatID, "👋 Assalomu alaykum!\nkinolar olish uchun shunchaki kod kiriting:")
		bot.Send(msg)
		return
	}

	// ------------------ KOD KIRITSA ------------------
	if userID != ADMIN_ID && text != "/start" && text != "/admin" {
		// Majburiy obuna tekshiruvi
		isMember, requiredChannel := checkMembership(bot, userID)
		if !isMember {
			handleMembershipCheck(bot, chatID, requiredChannel)
			return
		}

		code := strings.ToLower(strings.TrimSpace(text))

		// Qidiruv statistikasi
		statsMutex.Lock()
		searchStats[code]++
		statsMutex.Unlock()

		storageMutex.RLock()
		videos, ok := animeStorage[code]
		storageMutex.RUnlock()

		if !ok || len(videos) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "🔍 Bunday kod bo‘yicha kinolar topilmadi."))
			return
		}

		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("🔍 %d ta video topildi. Yuborish boshlandi...", len(videos))))

		for i, vID := range videos {
			videoMsg := tgbotapi.NewVideo(chatID, tgbotapi.FileID(vID))
			videoMsg.Caption = fmt.Sprintf("Qism: %d/%d", i+1, len(videos))
			bot.Send(videoMsg)
			time.Sleep(1 * time.Second) // Video yuborish orasidagi vaqt
		}

		bot.Send(tgbotapi.NewMessage(chatID, "✅ Barcha qismlar yuborildi!"))
		return
	}

	// ------------------ ADMIN TEXT HANDLER ------------------
	if userID == ADMIN_ID {
		handleAdminText(bot, update)
	}
}

// ---------------- ADMIN PANEL TUGMALARI ----------------
func adminMenu() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Statistika", "show_stats"),
		), tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🖋 kinolar joylash", "upload_anime"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Kanal qo‘shish", "add_channel"),
			tgbotapi.NewInlineKeyboardButtonData("🗑 Kanal o‘chirish", "remove_channel"),
		),
	)
}

// ------------------ A'ZOLIKNI TEKSHIRISH ----------------
func checkMembership(bot *tgbotapi.BotAPI, userID int64) (bool, string) {
	if len(channels) == 0 {
		return true, ""
	}

	for chatID, username := range channels {
		member, err := bot.GetChatMember(tgbotapi.GetChatMemberConfig{
			ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
				ChatID: chatID,
				UserID: userID,
			},
		})

		if err != nil {
			log.Printf("A'zolikni tekshirishda xato yuz berdi %s: %v", username, err)
			return false, username
		}

		if member.Status != "member" && member.Status != "administrator" && member.Status != "creator" {
			return false, username
		}
	}
	return true, ""
}

// ------------------ OBUNA BO'LMAGANLARGA XABAR ----------------
func handleMembershipCheck(bot *tgbotapi.BotAPI, chatID int64, requiredChannel string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("➕ Obuna bo‘lish", "https://t.me/"+requiredChannel),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Tekshirish ", "check_membership"),
		),
	)

	msg := tgbotapi.NewMessage(chatID,
		"⚠️ Davom etish uchun avval bizning kanalimizga obuna bo‘ling")
	msg.ReplyMarkup = &keyboard
	bot.Send(msg)
}

// ------------------ CALLBACK HANDLER ----------------
func handleCallback(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	userID := update.CallbackQuery.From.ID
	chatID := update.CallbackQuery.Message.Chat.ID
	data := update.CallbackQuery.Data
	messageID := update.CallbackQuery.Message.MessageID

	bot.Send(tgbotapi.NewCallback(update.CallbackQuery.ID, "Tekshirilmoqda..."))

	switch data {

	case "show_stats":
		storageMutex.RLock()
		animeCount := len(animeStorage)
		storageMutex.RUnlock()

		statsMutex.Lock()
		starts := startCount
		topCode := ""
		topCount := 0
		for code, cnt := range searchStats {
			if cnt > topCount {
				topCode = code
				topCount = cnt
			}
		}
		statsMutex.Unlock()

		if topCode == "" {
			topCode = "Hali qidiruv bo‘lmagan"
		}

		text := fmt.Sprintf(
			"📊 *Statistika*\n\n"+
				"🔢 Saqlangan kinolar: *%d ta*\n"+
				"👤 /start bosganlar: *%d kishi*\n"+
				"🔍 Eng ko‘p qidirilgan kod: *%s* (%d marta)\n",
			animeCount, starts, topCode, topCount,
		)

		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		bot.Send(msg)

	case "add_channel":
		adminState[userID] = "add_channel_chatid"
		bot.Send(tgbotapi.NewMessage(chatID, "🆔 Kanal chatID kiriting (Masalan: -1001234567890):"))

	case "remove_channel":
		adminState[userID] = "remove_channel"
		bot.Send(tgbotapi.NewMessage(chatID, "O‘chirmoqchi bo‘lgan kanalning CHAT ID sini kiriting:"))

	case "upload_anime":
		adminState[userID] = "anime_code"
		bot.Send(tgbotapi.NewMessage(chatID, "🆔 kinolar kodini kiriting (masalan: 1 ):"))

	case "check_membership":
		isMember, requiredChannel := checkMembership(bot, userID)
		if isMember {
			editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "✅ A'zoligingiz tasdiqlandi. Endi kinolar kodini kiriting:")
			bot.Send(editMsg)
		} else {
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL("➕ Obuna bo‘lish", "https://t.me/"+requiredChannel),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("✅ Tekshirish ", "check_membership"),
				),
			)

			editMsg := tgbotapi.NewEditMessageText(chatID, messageID,
				fmt.Sprintf("⚠️ Obuna tasdiqlanmadi. Avval kanalga obuna bo‘ling:\n"))
			editMsg.ReplyMarkup = &keyboard
			bot.Send(editMsg)
		}
	}
}

// ------------------ ADMIN TEXT HANDLER ----------------
func handleAdminText(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	text := update.Message.Text

	switch adminState[userID] {

	case "add_channel_chatid":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Noto‘g‘ri chatID. ChatID raqam bo'lishi kerak (masalan: -100...)."))
			return
		}
		adminTempID[userID] = id
		adminState[userID] = "add_channel_username"
		bot.Send(tgbotapi.NewMessage(chatID, "🔗 Kanal username kiriting :"))

	case "add_channel_username":
		username := strings.TrimPrefix(text, "@")
		chatIDnum := adminTempID[userID]
		channels[chatIDnum] = username
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kanal qo‘shildi! \nChatID: %d \nUsername: @%s", chatIDnum, username)))
		delete(adminState, userID)
		delete(adminTempID, userID)

	case "remove_channel":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato chatID. ChatID raqam bo'lishi kerak."))
			return
		}
		if _, ok := channels[id]; ok {
			delete(channels, id)
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kanal (%d) o‘chirildi!", id)))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bunday chatID bilan kanal topilmadi!"))
		}
		delete(adminState, userID)

	case "anime_code":
		code := strings.ToLower(strings.TrimSpace(text))
		animeCode[userID] = code
		adminState[userID] = "anime_videos"
		animeTemp[userID] = nil
		bot.Send(tgbotapi.NewMessage(chatID, "🎬 Videolarni yuboring. Tugagach /TUGADI deb yozing."))

	case "anime_videos":
		if text == "/TUGADI" {
			code := animeCode[userID]
			if len(animeTemp[userID]) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Video yuborilmadi. Jarayon bekor qilindi."))
			} else {
				storageMutex.Lock()
				animeStorage[code] = animeTemp[userID]
				storageMutex.Unlock()
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ kinolar '%s' %d qism bilan saqlandi!", strings.ToUpper(code), len(animeTemp[userID]))))
			}
			delete(adminState, userID)
			delete(animeTemp, userID)
			delete(animeCode, userID)
			return
		}
		if update.Message.Video != nil {
			fileID := update.Message.Video.FileID
			animeTemp[userID] = append(animeTemp[userID], fileID)
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("📥 Video qabul qilindi! Jami: %d ta. Yana yuboring yoki /TUGADI deb yozing.", len(animeTemp[userID]))))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Faqat video yuborishingiz yoki /TUGADI deb yozishingiz mumkin."))
		}
	}
}
