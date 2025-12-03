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
var BOT_TOKEN = "7982906158:AAHKNYvVUxn5kjUD1OCzJE3btJlVp80mMG8"
var ADMIN_ID int64 = 7518992824 // O'zingizni admin ID

// ---------------- STATISTIKA ----------------
var (
	startCount  int                    // /start bosganlar soni
	searchStats = make(map[string]int) // kod qidirish statistikasi
	statsMutex  sync.Mutex             // xavfsiz saqlash uchun
)

// ---------------- Foydalanuvchi va admin holatlari ----------------
var adminState = make(map[int64]string)    // admin dialog holatlari
var adminTempID = make(map[int64]int64)    // vaqtinchalik chatID saqlash
var animeNameTemp = make(map[int64]string) // admin: nomni vaqtincha saqlash
var animeCodeTemp = make(map[int64]string) // admin: kodni vaqtincha saqlash
var startUsers = make(map[int64]string)    // userID -> username
var blockedUsers = make(map[int64]bool)

// ContentItem turli kontent turlarini saqlash uchun
type ContentItem struct {
	Kind   string // "video", "photo", "document", "text"
	FileID string // file id agar mavjud bo'lsa
	Text   string // text uchun
}

// animeStorage: code -> slice of ContentItem
var animeStorage = make(map[string][]ContentItem)
var storageMutex sync.RWMutex

// code -> name (masalan: "naruto1" -> "Naruto")
var animeInfo = make(map[string]string)
var infoMutex sync.RWMutex

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
		// Har bir update ni alohida goroutine ichida parallel qayta ishlaymiz
		go handleUpdate(bot, update) // 🚀 Bu o'zgarish bot tezligini oshiradi
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
		startUsers[userID] = update.Message.From.UserName
		statsMutex.Unlock()

		msg := tgbotapi.NewMessage(chatID, "👋 Assalomu alaykum!\nAnime olish uchun kod kiriting:")
		bot.Send(msg)
		return
	}
	if blockedUsers[userID] {
		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Siz botdan bloklangansiz."))
		return
	}

	// ------------------ KOD KIRITSA (FOYDALANUVCHI) ------------------
	if userID != ADMIN_ID && text != "/start" && text != "/admin" {
		// Majburiy obuna tekshiruvi (agar kanallar bo'lsa)
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

		// Saqlangan kontentni o'qish
		storageMutex.RLock()
		items, ok := animeStorage[code]
		storageMutex.RUnlock()

		if !ok || len(items) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "🔍 Bunday kod bo‘yicha kontent topilmadi."))
			return
		}

		// Nomni olish
		infoMutex.RLock()
		name, hasName := animeInfo[code]
		infoMutex.RUnlock()
		if !hasName {
			name = "No-name"
		}

		// Birinchi xabar: topildi degan xabar
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("🔍 %d ta qism topildi. Yuborish boshlandi...", len(items))))

		// Har bir itemni turiga qarab yuborish
		for i, it := range items {
			caption := fmt.Sprintf("%s\nQism: %d/%d", name, i+1, len(items))

			switch it.Kind {
			case "video":
				videoMsg := tgbotapi.NewVideo(chatID, tgbotapi.FileID(it.FileID))
				videoMsg.Caption = caption
				bot.Send(videoMsg)
			case "photo":
				photoMsg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(it.FileID))
				photoMsg.Caption = caption
				bot.Send(photoMsg)
			case "document":
				docMsg := tgbotapi.NewDocument(chatID, tgbotapi.FileID(it.FileID))
				docMsg.Caption = caption
				bot.Send(docMsg)
			case "text":
				// Matn bo'lsa, bitta message: nom + qism + matn
				full := fmt.Sprintf(`%s\nQism: %d/%d

%s`, name, i+1, len(items), it.Text)
				bot.Send(tgbotapi.NewMessage(chatID, full))
			default:
				// Noma'lum tur bo'lsa text sifatida yuborish
				full := fmt.Sprintf("%s\nasosiy kanal - @Manga_uzbekcha26 \n Qism: %d/%d\n\n(noma'lum kontent)", name, i+1, len(items))
				bot.Send(tgbotapi.NewMessage(chatID, full))
			}

			// Sekinroq yuborish uchun kichik tanaffus
			time.Sleep(800 * time.Millisecond)
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
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🖋 kinolar joylash", "upload_anime"),
			tgbotapi.NewInlineKeyboardButtonData("🗑 kinolar o‘chirish", "delete_anime"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Kanal qo‘shish", "add_channel"),
			tgbotapi.NewInlineKeyboardButtonData("🗑 Kanal o‘chirish", "remove_channel"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🚫 Foydalanuvchini bloklash", "block_user"),
			tgbotapi.NewInlineKeyboardButtonData("♻️ Blokdan chiqarish", "unblock_user"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📵 Bloklanganlar ro‘yxati", "blocked_list"),
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
	case "delete_anime":
		adminState[userID] = "delete_anime_code"
		bot.Send(tgbotapi.NewMessage(chatID, "🆔 O‘chirmoqchi bo‘lgan kinolar kodini kiriting:"))

	case "block_user":
		adminState[userID] = "block_user"
		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Bloklamoqchi bo‘lgan foydalanuvchi ID sini kiriting:"))

	case "unblock_user":
		adminState[userID] = "unblock_user"
		bot.Send(tgbotapi.NewMessage(chatID, "♻️ Blokdan chiqariladigan user ID ni kiriting:"))

	case "blocked_list":
		if len(blockedUsers) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "📵 Bloklanganlar yo‘q."))
			return
		}

		txt := "📵 *Bloklangan foydalanuvchilar:*\n\n"
		for id := range blockedUsers {
			txt += fmt.Sprintf("🚫 %d\n", id)
		}

		msg := tgbotapi.NewMessage(chatID, txt)
		msg.ParseMode = "Markdown"
		bot.Send(msg)

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
		// Boshlash: avval nom so'raladi
		adminState[userID] = "anime_name"
		bot.Send(tgbotapi.NewMessage(chatID, "📝 kinolar nomini kiriting :"))

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
	case "delete_anime_code":
		code := strings.ToLower(strings.TrimSpace(text))

		infoMutex.RLock()
		_, exists := animeInfo[code]
		infoMutex.RUnlock()

		if !exists {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu kod bo‘yicha kinolar topilmadi."))
			delete(adminState, userID)
			return
		}

		// 🔥 O‘chiramiz
		infoMutex.Lock()
		delete(animeInfo, code)
		infoMutex.Unlock()

		storageMutex.Lock()
		delete(animeStorage, code)
		storageMutex.Unlock()

		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 '%s' kodi bo‘yicha kinolar o‘chirildi!", strings.ToUpper(code))))
		delete(adminState, userID)
		return

	case "block_user":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
			return
		}
		blockedUsers[id] = true
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🚫 %d bloklandi!", id)))
		delete(adminState, userID)

	case "unblock_user":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
			return
		}
		delete(blockedUsers, id)
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("♻️ %d blokdan chiqarildi!", id)))
		delete(adminState, userID)

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

	// ------------------ ADMIN: anime nomi so'rov ------------------
	case "anime_name":
		name := strings.TrimSpace(text)
		if name == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Nomi bo'sh bo'lishi mumkin emas. Iltimos nom kiriting:"))
			return
		}
		animeNameTemp[userID] = name
		adminState[userID] = "anime_code"
		bot.Send(tgbotapi.NewMessage(chatID, "🆔 Endi kinolar kodi kiriting :"))

	// ------------------ ADMIN: anime kodi so'rov ------------------
	case "anime_code":
		code := strings.ToLower(strings.TrimSpace(text))

		// ❗ 1) Bo'sh kod tekshiruvi
		if code == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Kod bo'sh bo'lishi mumkin emas. Iltimos kod kiriting:"))
			return
		}

		// ❗ 2) Kod allaqachon mavjudligini tekshiramiz
		infoMutex.RLock()
		_, exists := animeInfo[code]
		infoMutex.RUnlock()

		if exists {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Bu kod allaqachon mavjud! Boshqa kod kiriting:"))
			return
		}

		// ❗ 3) Kodni saqlaymiz (code -> anime name)
		infoMutex.Lock()
		animeInfo[code] = animeNameTemp[userID]
		infoMutex.Unlock()

		// ❗ Bo'sh storage slot yaratamiz
		storageMutex.Lock()
		animeStorage[code] = nil
		storageMutex.Unlock()

		// ❗ Admin kiritgan kodni vaqtincha saqlaymiz
		animeCodeTemp[userID] = code

		// ❗ Admin endi kontent yuborishi kerak
		adminState[userID] = "anime_videos"

		bot.Send(tgbotapi.NewMessage(
			chatID,
			fmt.Sprintf("🎞 Endi '%s' uchun videolar/rasmlar/fayllar yoki matnlarni yuboring. Tugagach /ok deb yozing.",
				animeNameTemp[userID]),
		))
		return

	// ------------------ ADMIN: kontent qabul qilish ------------------
	case "anime_videos":
		code := animeCodeTemp[userID]

		// Agar admin /TUGADI deb yuborsa yakunlaymiz
		if strings.ToLower(text) == "/ok" {
			storageMutex.RLock()
			count := len(animeStorage[code])
			storageMutex.RUnlock()

			bot.Send(tgbotapi.NewMessage(chatID,
				fmt.Sprintf("✅ '%s' uchun barcha kontent saqlandi! Jami: %d ta", animeNameTemp[userID], count)))

			// Tozalash
			delete(adminState, userID)
			delete(animeCodeTemp, userID)
			delete(animeNameTemp, userID)
			return
		}

		storageMutex.Lock() // ⬅️ Hamma append va count shu yerda mutex ichida
		defer storageMutex.Unlock()

		if update.Message.Video != nil {
			animeStorage[code] = append(animeStorage[code], ContentItem{
				Kind:   "video",
				FileID: update.Message.Video.FileID,
			})
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🎬 Video qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
			return
		}

		if update.Message.Photo != nil {
			photo := update.Message.Photo[len(update.Message.Photo)-1].FileID
			animeStorage[code] = append(animeStorage[code], ContentItem{
				Kind:   "photo",
				FileID: photo,
			})
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🖼 Rasm qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
			return
		}

		if update.Message.Document != nil {
			animeStorage[code] = append(animeStorage[code], ContentItem{
				Kind:   "document",
				FileID: update.Message.Document.FileID,
			})
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("📁 Fayl qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
			return
		}

		if update.Message.Text != "" {
			animeStorage[code] = append(animeStorage[code], ContentItem{
				Kind: "text",
				Text: update.Message.Text,
			})
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✏️ Matn qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
			return
		}

		bot.Send(tgbotapi.NewMessage(chatID, "❌ Noma’lum format! Video, rasm, fayl yoki matn yuboring."))
	}
}
