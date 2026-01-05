package main

import (
	"encoding/json"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var BOT_TOKEN = "7982906158:AAHKNYvVUxn5kjUD1OCzJE3btJlVp80mMG8"

const MAIN_ADMIN_ID int64 = 7518992824

var admins = map[int64]bool{
	MAIN_ADMIN_ID: true, // Asosiy admin
}

// ==========================
// ====== Fayl nomlari
// ==========================
const (
	ANIME_STORAGE_FILE = "anime_data.json"
	ANIME_INFO_FILE    = "anime_info.json"
	ADMIN_CONFIG_FILE  = "admin_config.json"
)

// ==========================
// ====== Tiplar (Structs)
// ==========================
type ContentItem struct {
	Kind   string
	FileID string
	Text   string
}

type Channel struct {
	ChatID   int64
	Username string
}

type UploadTask struct {
	UserID int64
	Code   string
	Item   ContentItem
}

// Foydalanuvchi sahifasi
type UserPage struct {
	Name  string
	Items []ContentItem
	Page  int
}

// Admin konfiguratsiyasi
type AdminConfig struct {
	Admins   map[int64]bool
	Channels map[int64]string
	AllUsers map[int64]time.Time // user qo'shilgan vaqt bilan
}

// ==========================
// ====== Foydalanuvchilar bilan ishlash
// ==========================
var (
	usersMutex      sync.RWMutex
	userPages       = make(map[int64]*UserPage) // userID → UserPage
	userLastActive  = make(map[int64]time.Time) // userID → oxirgi faoliyat vaqti
	userJoinedAt    = make(map[int64]time.Time) // userID → botga qo'shilgan vaqt
	users           = map[int64]bool{}          // bot foydalanuvchilari
	startUsers      = make(map[int64]string)    // userID → boshlang‘ich xabar yoki state
	blockedUsers    = make(map[int64]bool)      // userID → bloklangan bo‘lsa true
	pendingRequests = make(map[int64]bool)      // userID → so‘rov yuborgan foydalanuvchilar
	allUsers        = make(map[int64]bool)      // bot foydalanuvchilari
	startCount      int
	searchStats     = make(map[string]int)
	statsMutex      sync.Mutex
	requestMutex    sync.RWMutex
	userJoined      = make(map[int64]time.Time) // foydalanuvchilar qo‘shilgan vaqt
	userActive      = make(map[int64]time.Time) // foydalanuvchilar oxirgi faoliyat

)

// ==========================
// ====== Anime bilan ishlash
// ==========================
var (
	storageMutex = sync.RWMutex{}
	infoMutex    = sync.RWMutex{}

	animeStorage = make(map[string][]ContentItem) // animeCode yoki animeName → []ContentItem
	animeInfo    = make(map[string]string)        // animeCode yoki animeName → ma'lumot
)

// ==========================
// ====== Adminlar bilan ishlash
// ==========================
var (
	adminIDs          = map[int64]bool{MAIN_ADMIN_ID: true}
	adminState        = make(map[int64]string)    // adminID → hozirgi holat
	adminTempID       = make(map[int64]int64)     // adminID → userID yoki boshqa ID
	animeNameTemp     = make(map[int64]string)    // adminID → animeName
	animeCodeTemp     = make(map[int64]string)    // adminID → animeCode
	adminTempChannels = make(map[int64][]Channel) // adminID → []Channel

	adminMutex sync.Mutex
)

// ==========================
// ====== Upload & Broadcast
// ==========================
var (
	uploadQueue    = make(chan UploadTask, 1000)
	broadcastCache = make(map[int64]*tgbotapi.Message)
)

// ==========================
// ====== VIP foydalanuvchilar
// ==========================
var (
	vipUsers = make(map[int64]bool)
	vipMutex sync.RWMutex
)

// ==========================
// ====== Kanallar
// ==========================
var channels = make(map[int64]string) // channelID → channelUsername yoki info

func main() {
	// ⚡ Bot ma'lumotlarini yuklash
	loadData()     // animeStorage, animeInfo, admins, channels, allUsers
	loadRequests() // pendingRequests

	// 🔑 Botni ishga tushurish
	bot, err := tgbotapi.NewBotAPI(BOT_TOKEN)
	if err != nil {
		log.Fatal(err)
	}

	initQueue(bot) // agar sizda task queue ishlatilsa

	log.Println("Bot ishga tushdi...")

	// Update config
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	u.AllowedUpdates = []string{"message", "callback_query", "chat_join_request", "chat_member"}

	updates := bot.GetUpdatesChan(u)

	for update := range updates {

		// 1️⃣ Foydalanuvchi kanalga qo'shilsa
		if update.ChatJoinRequest != nil {
			userID := update.ChatJoinRequest.From.ID
			requestMutex.Lock()
			pendingRequests[userID] = true
			requestMutex.Unlock()
			saveRequests()
			continue
		}

		// 2️⃣ Foydalanuvchi kanalni tark etsa
		if update.ChatMember != nil {
			userID := update.ChatMember.Chat.ID
			oldStatus := update.ChatMember.OldChatMember.Status
			newStatus := update.ChatMember.NewChatMember.Status

			if oldStatus != newStatus && (newStatus == "left" || newStatus == "kicked") {
				requestMutex.Lock()
				if _, exists := pendingRequests[userID]; exists {
					delete(pendingRequests, userID)
					saveRequests()
					log.Printf("🚫 Foydalanuvchi %d botni tark etdi, bazadan o'chirildi.", userID)
				}
				requestMutex.Unlock()
			}
			continue
		}

		// 3️⃣ Boshqa update'larni alohida goroutine'da qayta ishlash
		go handleUpdate(bot, update)

		// 4️⃣ Admin xolatini tekshirish
		var userID int64
		if update.Message != nil {
			userID = update.Message.From.ID
		} else if update.CallbackQuery != nil {
			userID = update.CallbackQuery.From.ID
		} else {
			continue
		}

		adminMutex.Lock()
		state := adminState[userID]
		adminMutex.Unlock()

		// 🔥 Agar admin reklama yuborish jarayonida bo'lsa, handleBroadcast chaqiriladi
		if state == "waiting_for_ad" || state == "confirm_ad" {
			handleBroadcast(bot, update, adminState, broadcastCache, pendingRequests, &adminMutex, &requestMutex)
			continue
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	var userID int64
	if update.Message != nil {
		userID = update.Message.From.ID
	} else if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
	} else {
		return
	}

	// 1️⃣ Agar admin "waiting_for_ad" holatida bo'lsa, xabarni cache ga saqlash
	if update.Message != nil {
		adminMutex.Lock()
		state := adminState[userID]
		if state == "waiting_for_ad" {
			broadcastCache[userID] = update.Message
			adminState[userID] = "confirm_ad"
		}
		adminMutex.Unlock()
	}

	// 2️⃣ Callback'larni qayta ishlash
	if update.CallbackQuery != nil {
		handleCallback(bot, update)
	}

	// 3️⃣ Oddiy foydalanuvchi xabarlarini qayta ishlash
	if update.Message != nil {
		handleMessage(bot, update)
	}

	// 4️⃣ Agar admin "waiting_for_ad" yoki "confirm_ad" holatida bo'lsa, broadcastni ishlatish
	var state string
	adminMutex.Lock()
	if s, ok := adminState[userID]; ok {
		state = s
	}
	adminMutex.Unlock()

	if state == "waiting_for_ad" || (update.CallbackQuery != nil && state == "confirm_ad") {
		handleBroadcast(bot, update, adminState, broadcastCache, allUsers, &adminMutex, &requestMutex)
	}
}

func playItem(bot *tgbotapi.BotAPI, chatID int64, idx int) {

	data := userPages[chatID]

	if data == nil {

		return

	}

	if idx < 0 || idx >= len(data.Items) {

		bot.Send(tgbotapi.NewMessage(chatID, "❌ Noto‘g‘ri qism raqami"))

		return

	}

	item := data.Items[idx]

	caption := fmt.Sprintf("%s\n\nQism: %d", data.Name, idx+1)

	// 🔥 Tugmalarni biriktirish mantiqi OLIB TASHLANDI.

	switch item.Kind {

	case "video":

		msg := tgbotapi.NewVideo(chatID, tgbotapi.FileID(item.FileID))

		msg.Caption = caption

		// msg.ReplyMarkup = markup <-- ENDI YO'Q

		bot.Send(msg)

	case "photo":

		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(item.FileID))

		msg.Caption = caption

		// msg.ReplyMarkup = markup <-- ENDI YO'Q

		bot.Send(msg)

	case "document":

		msg := tgbotapi.NewDocument(chatID, tgbotapi.FileID(item.FileID))

		msg.Caption = caption

		// msg.ReplyMarkup = markup <-- ENDI YO'Q

		bot.Send(msg)

	default:

		bot.Send(tgbotapi.NewMessage(chatID,

			fmt.Sprintf("%s\nQism: %d/%d\n", data.Name, idx+1, len(data.Items))))

	}

	// Qismlar ketma-ket yuborilganda Telegram API cheklovlari buzilmasligi uchun biroz kutish

	// Agar bu funksiya faqat callback orqali bitta qism yuborish uchun ishlatilsa, kutish shart emas.

}

func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	text := update.Message.Text
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID

	addUser(userID)
	updateUserActivity(userID)
	// 1. ADMIN PANEL (Adminlar uchun obuna shart emas)
	if text == "/admin" && admins[userID] {
		msg := tgbotapi.NewMessage(chatID, "🛠 Admin panel")
		msg.ReplyMarkup = adminMenu()
		bot.Send(msg)
		return
	}

	// 2. ADMIN STATE HANDLER (Adminlar uchun)
	if admins[userID] {
		handleAdminText(bot, update)
		return
	}

	// 3. BLOKLANISH TEKSHIRUVI
	if blockedUsers[userID] {
		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Siz adminlar tomonidan bloklangansiz."))
		return
	}

	// ---------------------------------------------------------
	// 🔥 MAJBURIY OBUNA TEKSHIRUVI (ASOSIY O'ZGARISH SHU YERDA)
	// ---------------------------------------------------------
	isMember, requiredChannelsMap := checkMembership(bot, userID)
	if !isMember {
		handleMembershipCheck(bot, chatID, requiredChannelsMap)
		return // Foydalanuvchi obuna bo'lmaguncha pastdagi kodlarga o'tmaydi
	}
	// ---------------------------------------------------------

	updateUserActivity(userID)

	// 4. /start BUYRUG'I
	if text == "/start" {
		usersMutex.Lock()
		if _, exists := userJoinedAt[userID]; !exists {
			userJoinedAt[userID] = time.Now()
			allUsers[userID] = true
			startCount++
			startUsers[userID] = update.Message.From.UserName
		}
		usersMutex.Unlock()

		saveData()
		saveStats()

		msg := tgbotapi.NewMessage(chatID, "👋 Assalomu alaykum!\nkino olish uchun kod kiriting:")
		bot.Send(msg)
		return
	}

	// /stats buyurilganda
	if text == "/stats" {
		displayStats(bot, chatID)
		return
	}

	// 5. MAXSUS BUYRUQLAR (Masalan: /clear_channels)
	switch text {
	case "/clear_channels":
		if !adminIDs[userID] {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Sizda bu buyruqni ishlatish huquqi yo‘q."))
			return
		}
		channels = make(map[int64]string)
		go saveData()
		bot.Send(tgbotapi.NewMessage(chatID, "✅ Barcha kanallar o‘chirildi!"))
		return
	}

	// 6. KOD KIRITSA (FOYDALANUVCHI)
	// Bu yerda text != "/start" tekshiruvi shart emas, chunki tepada return qilingan
	code := strings.ToLower(strings.TrimSpace(text))

	// Qidruv statistikasi
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

	// Pagination uchun ma'lumotni saqlash
	userPages[chatID] = &UserPage{
		Name:  name,
		Items: items,
		Page:  0,
	}

	if len(items) > 0 {
		firstItem := items[0]
		caption := fmt.Sprintf("%s\nQism: 1/%d", name, len(items))
		markup := sendPageMenuMarkup(chatID)

		switch firstItem.Kind {
		case "video":
			msg := tgbotapi.NewVideo(chatID, tgbotapi.FileID(firstItem.FileID))
			msg.Caption = caption
			msg.ReplyMarkup = markup
			bot.Send(msg)
		case "photo":
			msg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(firstItem.FileID))
			msg.Caption = caption
			msg.ReplyMarkup = markup
			bot.Send(msg)
		case "document":
			msg := tgbotapi.NewDocument(chatID, tgbotapi.FileID(firstItem.FileID))
			msg.Caption = caption
			msg.ReplyMarkup = markup
			bot.Send(msg)
		case "text":
			full := fmt.Sprintf(`%s\nQism: 1/%d\n\n%s`, name, len(items), firstItem.Text)
			msg := tgbotapi.NewMessage(chatID, full)
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = markup
			bot.Send(msg)
		default:
			msg := tgbotapi.NewMessage(chatID, caption+"\n")
			msg.ReplyMarkup = markup
			bot.Send(msg)
		}
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "Kontent topildi, lekin qismlar mavjud emas."))
	}

}

func adminMenu() tgbotapi.InlineKeyboardMarkup {

	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(

			tgbotapi.NewInlineKeyboardButtonData("📊 Statistika", "show_stats"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👮‍♂️ Adminlar", "admin_menu"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📢 Reklama yuborish", "broadcast"),
		),

		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👥 Foydalanuvchilar", "user_menu"),
		),
		tgbotapi.NewInlineKeyboardRow(

			tgbotapi.NewInlineKeyboardButtonData("✍️ Kino tahrirlash", "edit_anime"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🌟 VIP Boshqaruv", "admin_vip_main"),
		),

		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🖋 Kino joylash", "upload_anime"),

			tgbotapi.NewInlineKeyboardButtonData("🗑 Kino o‘chirish", "delete_anime"),
		),
		tgbotapi.NewInlineKeyboardRow(

			tgbotapi.NewInlineKeyboardButtonData("➕ Kanal qo‘shish", "add_channel"),

			tgbotapi.NewInlineKeyboardButtonData("🗑 Kanal o‘chirish", "remove_channel"),
		),
	)

}

func userManageKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🚫 Foydalanuvchini bloklash", "block_user"),
			tgbotapi.NewInlineKeyboardButtonData("♻️ Blokdan chiqarish", "unblock_user"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📵 Bloklanganlar ro‘yxati", "blocked_list"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Orqaga", "back_to_admin"),
		),
	)
}

func adminManageKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👤 Admin qo‘shish", "add_admin"),
			tgbotapi.NewInlineKeyboardButtonData("🗑 Adminni o‘chirish", "remove_admin"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 Adminlar ro‘yxati", "list_admins"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Orqaga", "back_to_admin"),
		),
	)
}

func vipAdminMenu() *tgbotapi.InlineKeyboardMarkup {
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ VIP Qo'shish", "vip_add"),
			tgbotapi.NewInlineKeyboardButtonData("🗑 VIP O'chirish", "vip_del"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📜 VIP Ro'yxati", "vip_list"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Orqaga", "back_to_admin"),
		),
	)
	return &markup // & belgisi pointer qaytarish uchun shart
}

func editMenu(code, name string) *tgbotapi.InlineKeyboardMarkup {

	markup := tgbotapi.NewInlineKeyboardMarkup(

		tgbotapi.NewInlineKeyboardRow(

			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("✏️ Nomini o‘zgartirish (%s)", name), "edit_name:"+code),

			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🆔 Kodini o‘zgartirish (%s)", code), "edit_code:"+code),
		),

		// Kontent tahrirlash

		tgbotapi.NewInlineKeyboardRow(

			// 🔥 Mantiqni o'zgartiramiz: edit_content:ANIME_CODE

			tgbotapi.NewInlineKeyboardButtonData("➕ Qo‘shish", "edit_content:"+code),

			tgbotapi.NewInlineKeyboardButtonData("🗑 Qismni o'chirish", "delete_part:"+code),
		),

		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ Kino to'plamini o'chirish", "delete_anime_confirm:"+code),
			tgbotapi.NewInlineKeyboardButtonData("🔢 Tartiblash", "reorder_request:"+code), // Yangi tugma
		),
	)

	return &markup

}

func checkMembership(bot *tgbotapi.BotAPI, userID int64) (bool, map[int64]string) {
	required := make(map[int64]string)

	// 1️⃣ VIP foydalanuvchi → obuna talab qilinmaydi
	vipMutex.RLock()
	if vipUsers[userID] {
		vipMutex.RUnlock()
		return true, nil
	}
	vipMutex.RUnlock()

	// 2️⃣ Kanallarni tekshirish
	for chatID, info := range channels {
		isPrivate := strings.HasPrefix(info, "https://t.me/+") // maxfiy kanal URL bilan aniqlanadi

		member, err := bot.GetChatMember(tgbotapi.GetChatMemberConfig{
			ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
				ChatID: chatID,
				UserID: userID,
			},
		})

		isMember := false
		if err == nil && (member.Status == "member" || member.Status == "administrator" || member.Status == "creator") {
			isMember = true
		}

		// Maxfiy kanal: faqat join request yuborgan foydalanuvchi ishlaydi
		requestMutex.RLock()
		hasRequested := pendingRequests[userID]
		requestMutex.RUnlock()

		if isPrivate {
			if !hasRequested { // join request yo‘q → required-ga qo‘shilmaydi
				continue
			}
			// join request yuborgan bo‘lsa → required-ga qo‘shilmaydi, bot ishlaydi
			continue
		}

		// Oddiy kanal: a’zo bo‘lmasa → required-ga qo‘shiladi
		if !isMember {
			required[chatID] = info
		}
	}

	// Agar required bo‘sh bo‘lsa → foydalanuvchi barcha shartlarni bajargan
	if len(required) == 0 {
		return true, nil
	}

	// Aks holda → foydalanuvchi hali obuna bo‘lmagan kanallar mavjud
	return false, required
}

func saveRequests() {
	requestMutex.RLock()
	defer requestMutex.RUnlock()
	file, _ := json.Marshal(pendingRequests)
	_ = os.WriteFile("requests.json", file, 0644)
}

func loadRequests() {
	file, err := os.ReadFile("requests.json")
	if err == nil {
		json.Unmarshal(file, &pendingRequests)
	}
}

func handleMembershipCheck(bot *tgbotapi.BotAPI, chatID int64, requiredChannels map[int64]string) {
	msgText := "⚠️ Davom etish uchun pastdagi kanallarga obuna bo‘ling yoki *Join Request* yuboring:"

	var rows [][]tgbotapi.InlineKeyboardButton

	for _, username := range requiredChannels {
		if username != "" && !strings.Contains(username, "t.me/") {
			// Oddiy kanal havolasi
			button := tgbotapi.NewInlineKeyboardButtonURL(
				fmt.Sprintf("➕ @%s", username),
				"https://t.me/"+username,
			)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
		} else {
			// Maxfiy kanal yoki +xxxx havola
			button := tgbotapi.NewInlineKeyboardButtonURL(
				"➕ Kanalga Kirish",
				username,
			)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
		}
	}

	// Obunani tekshirish tugmasi
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Obunani Tekshirish", "check_membership"),
	))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, msgText)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard

	bot.Send(msg)
}

func handleCallback(bot *tgbotapi.BotAPI, update tgbotapi.Update) {

	chatID := update.CallbackQuery.Message.Chat.ID

	userID := update.CallbackQuery.From.ID

	data := update.CallbackQuery.Data

	defer bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
	// ------------------- FOYDALANUVCHI MANTIQI ------------------
	if strings.HasPrefix(data, "play_") {

		idx, err := strconv.Atoi(strings.TrimPrefix(data, "play_"))

		if err != nil {

			log.Printf("Invalid play index: %v", err)

			return

		}

		playItem(bot, chatID, idx)

		return

	}

	if data == "next" || data == "prev" {

		pageData, ok := userPages[chatID]

		if !ok {

			log.Printf("Pagination data not found for chat %d", chatID)

			return

		}

		totalItems := len(pageData.Items)

		totalPages := (totalItems + 9) / 10

		if data == "next" {

			if pageData.Page+1 < totalPages {

				pageData.Page++

			}

		} else if data == "prev" {

			if pageData.Page > 0 {

				pageData.Page--

			}

		}

		if update.CallbackQuery.Message != nil {

			messageID := update.CallbackQuery.Message.MessageID

			newMarkup := sendPageMenuMarkup(chatID)

			msgText := fmt.Sprintf("*%s*\nJami qism: %d\nSahifa: %d/%d",

				pageData.Name, totalItems, pageData.Page+1, totalPages)

			var req tgbotapi.Chattable

			if update.CallbackQuery.Message.Caption != "" || update.CallbackQuery.Message.Photo != nil || update.CallbackQuery.Message.Video != nil || update.CallbackQuery.Message.Document != nil {

				editCaption := tgbotapi.NewEditMessageCaption(chatID, messageID, msgText)

				editCaption.ParseMode = "Markdown"

				editCaption.ReplyMarkup = newMarkup

				req = editCaption

			} else {

				editMsg := tgbotapi.NewEditMessageText(chatID, messageID, msgText)

				editMsg.ParseMode = "Markdown"

				editMsg.ReplyMarkup = newMarkup

				req = editMsg

			}

			_, err := bot.Send(req)

			if err != nil {

				log.Printf("Xabarni tahrirlashda yakuniy xato: %v", err)

			}

		}

		return

	}

	if data == "check_membership" {

		isMember, notMemberChannels := checkMembership(bot, userID)

		if isMember {
			// Foydalanuvchi VIP yoki barcha kanallarga obuna bo‘lgan
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, "✅ Obuna tekshirildi!"))
			bot.Send(tgbotapi.NewMessage(chatID, "✅ Obuna tasdiqlandi. Kod kiritishingiz mumkin."))

		} else {
			// Foydalanuvchi hali obuna bo‘lmagan kanallar
			handleMembershipCheck(bot, chatID, notMemberChannels)

			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, "⚠️ Avval barcha kanallarga obuna bo‘ling!"))
		}

		return
	}
	var state string
	adminMutex.Lock()
	if s, ok := adminState[userID]; ok {
		state = s
	}
	adminMutex.Unlock()

	if state == "waiting_for_ad" || state == "confirm_ad" {
		handleBroadcast(bot, update, adminState, broadcastCache, allUsers, &adminMutex, &requestMutex)
		return // defaultga tushmaydi
	}
	if admins[userID] {

		switch {

		case data == "admin_menu":
			markup := adminManageKeyboard() // InlineKeyboardMarkup qaytaradi
			msg := tgbotapi.NewEditMessageText(
				chatID,
				update.CallbackQuery.Message.MessageID,
				"👮‍♂️ *Adminlar boshqaruvi*",
			)
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = &markup // ✅ pointerga aylantirilgan
			bot.Send(msg)
			return

		case data == "list_admins":
			// 1️⃣ Adminlar sonini hisoblash
			counter := len(admins)

			// 2️⃣ Adminlar ro'yxatini yaratish
			adminList := ""
			for id := range admins {
				adminList += fmt.Sprintf("• `%d`\n", id)
			}

			// Agar hozircha admin bo‘lmasa
			if adminList == "" {
				adminList = "❌ Hozircha admin yo‘q"
			}

			// 3️⃣ Xabar tayyorlash
			message := fmt.Sprintf(
				"👥 *Jami adminlar soni:* %d\n\n"+
					"**Adminlar ro'yxati :**\n"+
					"%s\n"+
					"---",
				counter,
				adminList,
			)

			// 4️⃣ Xabarni yuborish
			msg := tgbotapi.NewMessage(chatID, message)
			msg.ParseMode = tgbotapi.ModeMarkdown // ID'larni aniq ko'rsatish uchun
			bot.Send(msg)

		case data == "show_stats":

			displayStats(bot, chatID)

		case data == "admin_vip_main":
			msg := tgbotapi.NewEditMessageText(chatID, update.CallbackQuery.Message.MessageID, "🌟 *VIP Foydalanuvchilarni Boshqarish Paneli*")
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = vipAdminMenu()
			bot.Send(msg)
			return // default-ga o'tib ketmasligi uchun

		case data == "vip_add":
			adminState[userID] = "wait_vip_add"
			bot.Send(tgbotapi.NewMessage(chatID, "🆔 VIP qilmoqchi bo'lgan foydalanuvchi ID sini yuboring:"))
			return

		case data == "vip_del":
			adminState[userID] = "wait_vip_del"
			bot.Send(tgbotapi.NewMessage(chatID, "🆔 VIP-dan chiqarmoqchi bo'lgan foydalanuvchi ID sini yuboring:"))
			return

		case data == "vip_list":
			vipMutex.RLock()
			text := "🌟 *VIP Foydalanuvchilar Ro'yxati:*\n\n"
			if len(vipUsers) == 0 {
				text += "_Hozircha VIP foydalanuvchilar yo'q._"
			} else {
				for id := range vipUsers {
					text += fmt.Sprintf("👤 `%d`\n", id)
				}
			}
			vipMutex.RUnlock()
			msg := tgbotapi.NewMessage(chatID, text)
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return

		case data == "user_menu":
			markup := userManageKeyboard() // Foydalanuvchi paneli
			msg := tgbotapi.NewEditMessageText(
				chatID,
				update.CallbackQuery.Message.MessageID,
				"👥 Foydalanuvchi boshqaruvi",
			)
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = &markup // pointerga aylantirish
			bot.Send(msg)
			return

			// handleBroadcast ichidagi yuborish qismi

		case data == "broadcast":
			adminMutex.Lock()
			adminState[userID] = "waiting_for_ad"
			adminMutex.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "📢 Reklama yuboring\n✍️ Matn\n🖼 Rasm\n🎥 Video\n↪️ Forward\nBitta xabar yuboring"))
			return

		case data == "broadcast_send":
			handleBroadcast(bot, update, adminState, broadcastCache, allUsers, &adminMutex, &requestMutex)
			return

		case data == "broadcast_cancel":
			adminMutex.Lock()
			delete(adminState, userID)
			delete(broadcastCache, userID)
			adminMutex.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bekor qilindi."))
			return

		case data == "back_to_admin":
			msg := tgbotapi.NewEditMessageText(chatID, update.CallbackQuery.Message.MessageID, "👨‍💻 *Admin Boshqaruv Paneli:*")
			msg.ParseMode = "Markdown"
			mainMarkup := adminMenu()
			msg.ReplyMarkup = &mainMarkup // Asosiy admin menyusi
			bot.Send(msg)
			return

		case data == "upload_anime":

			adminState[userID] = "anime_name"

			bot.Send(tgbotapi.NewMessage(chatID, "🎬 Kino nomini kiriting:"))

			// Agar bu logikani "delete_anime" tugmasi bosilganda ishlatmoqchi bo'lsangiz:

		case data == "delete_anime":

			// 1. Anime ro'yxatini shakllantirish
			var animeList string
			counter := 0

			// animeInfo xaritasi orqali aylanib chiqish (mutex bilan himoyalangan holda)
			infoMutex.RLock()                     // O'qish uchun bloklash
			for animeCode, _ := range animeInfo { // info o'zgaruvchisi kerak emas, shuning uchun uni '_' bilan almashtirdik
				counter++
				// ❌ info.Name olib tashlandi, faqat kod qoldi
				animeList += fmt.Sprintf("%d. Kodi: `%s`\n", counter, animeCode)
			}
			infoMutex.RUnlock() // Bloklashni yechish

			// 2. Umumiy ma'lumotni tuzish
			message := fmt.Sprintf(
				"📚 *Jami animelar soni:* %d\n\n"+
					"**Kino kodlari ro'yxati:**\n"+
					"%s\n"+
					"---",
				counter,
				animeList,
			)

			// 3. Xabarni yuborish (Ro'yxat)
			msgList := tgbotapi.NewMessage(chatID, message)
			msgList.ParseMode = tgbotapi.ModeMarkdown // Kodlarni aniq ko'rsatish uchun
			bot.Send(msgList)

			// 4. Holatni saqlash va keyingi savolni berish
			adminState[userID] = "delete_anime_code"
			bot.Send(tgbotapi.NewMessage(chatID, "🗑 Yuqoridagi ro'yxatdan o‘chirmoqchi bo‘lgan Kino kodini kiriting:"))

		case data == "edit_anime":

			adminState[userID] = "edit_anime_code"

			bot.Send(tgbotapi.NewMessage(chatID, "✍️ Tahrirlamoqchi bo‘lgan --- Kino kodini --- kiriting:"))

		case data == "add_channel":
			adminState[userID] = "add_channel_wait"
			bot.Send(tgbotapi.NewMessage(chatID,
				"🔗 Kanal ChatID yuboring\n\n"+
					"⚠️ Eslatma: Botni kanalga ADMIN qilib qo‘shishingiz shart!",
			))

		case update.Message != nil && adminState[userID] == "add_channel_chatid":

			text := update.Message.Text

			if strings.HasPrefix(text, "https://t.me/") {

				// Havoladan username yoki joinchat linkini ajratish

				parts := strings.Split(text, "/")

				groupIdentifier := parts[len(parts)-1]

				// Bu yerda JoinChat link bo'lsa, Telegram API orqali qo‘shilish sorovi yuborish

				// Go Telegram API-da to‘g‘ridan-to‘g‘ri qo‘shish yo‘q, lekin bot o‘sha guruhga "Invite Link" orqali qo‘shiladi

				bot.Send(tgbotapi.NewMessage(chatID, "✅ Guruhga qo‘shilish so‘rovi yuborildi: "+groupIdentifier))

			} else {

				// ChatID yuborilgan holat

				chatID, err := strconv.ParseInt(text, 10, 64)

				if err != nil {

					bot.Send(tgbotapi.NewMessage(chatID, "❌ ChatID noto‘g‘ri. Qayta kiriting:"))

					return

				}

				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ ChatID %d qo‘shildi.", chatID)))

			}

			adminState[userID] = ""

		case data == "remove_channel":

			// 1. Kanal ID va nomlari ro'yxatini shakllantirish
			var channelList string
			counter := 0

			// 'channels' xaritasi orqali aylanib chiqish (ChatID va Kanal Nomini o'z ichiga olgan xarita)
			for channelID, channelName := range channels {
				counter++
				// ID va nomni ro'yxatga qo'shish
				channelList += fmt.Sprintf("%d. 📢 *%s*\n   ID: `%d`\n", counter, channelName, channelID)
			}

			// 2. Umumiy ma'lumotni tuzish
			message := fmt.Sprintf(
				"🔗 *Jami ulangan kanallar soni:* %d\n\n"+
					"**Kanallar ro'yxati:**\n\n"+
					"%s\n\n"+
					"---",
				counter,
				channelList,
			)

			// 3. Xabarni yuborish
			msg := tgbotapi.NewMessage(chatID, message)
			msg.ParseMode = tgbotapi.ModeMarkdown // Nom va ID'larni aniq ko'rsatish uchun Markdown ishlatildi

			bot.Send(msg)

			// 4. Holatni saqlash va keyingi savolni berish
			adminState[userID] = "remove_channel"
			bot.Send(tgbotapi.NewMessage(chatID, "🗑 Yuqoridagi ro'yxatdan o‘chirmoqchi bo‘lgan kanal (ChatID'sini) kiriting:"))

		case data == "add_admin":

			adminState[userID] = "add_admin_id"

			bot.Send(tgbotapi.NewMessage(chatID, "➕ Yangi admin ID'sini kiriting:"))

		case data == "block_user":
			adminState[userID] = "block_user"
			bot.Send(tgbotapi.NewMessage(chatID, "🚫 Bloklanadigan foydalanuvchi ID'sini kiriting:"))

		case data == "remove_admin":

			// 1. Admin IDlar ro'yxatini shakllantirish
			var adminList string
			counter := 0

			// 'admins' xaritasi orqali aylanib chiqish
			for adminID, _ := range admins {
				counter++
				// IDni ro'yxatga qo'shish
				adminList += fmt.Sprintf("%d. ID: `%d`\n", counter, adminID)
				// Agar asosiy admin IDsi bo'lsa, buni ham belgilash mumkin
				// if adminID == MAIN_ADMIN_ID {
				//     adminList += " (Asosiy Admin)\n"
				// } else {
				//     adminList += "\n"
				// }
			}

			// 2. Umumiy ma'lumotni tuzish
			message := fmt.Sprintf(
				"👥 *Jami adminlar soni:* %d\n\n"+
					"**Adminlar ro'yxati :**\n"+
					"%s\n"+
					"---",
				counter,
				adminList,
			)

			// 3. Xabarni yuborish
			msg := tgbotapi.NewMessage(chatID, message)
			msg.ParseMode = tgbotapi.ModeMarkdown // ID'larni aniq ko'rsatish uchun Markdown ishlatildi

			bot.Send(msg)

			// 4. Holatni saqlash va keyingi savolni berish
			adminState[userID] = "remove_admin_id"
			bot.Send(tgbotapi.NewMessage(chatID, "🗑 Yuqoridagi ro'yxatdan o‘chirmoqchi bo‘lgan admin ID'sini kiriting:"))

		case data == "unblock_user":

			adminState[userID] = "unblock_user"

			bot.Send(tgbotapi.NewMessage(chatID, "♻️ Blokdan chiqariladigan foydalanuvchi ID'sini kiriting:"))

		case data == "blocked_list":

			displayBlockedUsers(bot, chatID)

		case data == "new_anime_upload": // 👈 YANGI ANIME QO'SHISH UCHUN (EditMenu ichidagi tugma)

			adminState[userID] = "anime_name"

			bot.Send(tgbotapi.NewMessage(chatID, "🎬 Yangi Kino nomini kiriting:"))

			// ------------------- TAHRIRLASH BUYRUQLARI (strings.HasPrefix) -------------------

			// Nomni o'zgartirishni boshlash

		case strings.HasPrefix(data, "edit_name:"):

			code := strings.TrimPrefix(data, "edit_name:")

			animeCodeTemp[userID] = code

			adminState[userID] = "edit_new_name"

			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✍️ '%s' uchun yangi nom kiriting:", strings.ToUpper(code))))

			// Kodni o'zgartirishni boshlash

		case strings.HasPrefix(data, "edit_code:"):

			code := strings.TrimPrefix(data, "edit_code:")

			animeCodeTemp[userID] = code

			adminState[userID] = "edit_new_code"

			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🆔 '%s' uchun yangi kod kiriting:", strings.ToUpper(code))))

			// handleCallback funksiyasi ichidagi switch { ... }

			// handleCallback funksiyasi ichidagi switch { ... }

		case strings.HasPrefix(data, "edit_content:"):

			code := strings.TrimPrefix(data, "edit_content:")

			// Anime nomi animeInfo mapidan olinadi

			infoMutex.RLock()

			name := animeInfo[code]

			infoMutex.RUnlock()

			// 🔥 Hozirgi qismlar sonini hisoblash

			storageMutex.RLock()

			currentCount := len(animeStorage[code])

			storageMutex.RUnlock()

			// Admin holatlarini saqlash

			animeCodeTemp[userID] = code

			animeNameTemp[userID] = name

			adminState[userID] = "anime_videos" // Kontent yuklash rejimiga o'tish

			// 🔥 YANGI XABAR MATNI

			msgText := fmt.Sprintf(

				"🎬 **Nom:** **%s**\n🆔 **Kod:** (%s)\n\n**Hozirgi qismlar:** %d ta.\n**Yangi kontent:** %d-qismdan boshlab qo‘shiladi.\n\nEndi qoʻshmoqchi boʻlgan **video, fayl yoki photeni** yuboring. Tugatgach **/ok** deb yozing.",

				name,

				strings.ToUpper(code), // Kodni katta harflarda ko'rsatamiz

				currentCount,

				currentCount+1, // Yangi qism tartib raqami

			)

			msg := tgbotapi.NewMessage(chatID, msgText)

			msg.ParseMode = "Markdown"

			bot.Send(msg)

			return

		case strings.HasPrefix(data, "delete_part:"):

			code := strings.TrimPrefix(data, "delete_part:")

			animeCodeTemp[userID] = code

			adminState[userID] = "delete_part_id"

			storageMutex.RLock()

			items := animeStorage[code]

			storageMutex.RUnlock()

			name := animeInfo[code]

			// 🔥 MUHIM: partList ni boshlang'ich qiymat bilan e'lon qilish

			partList := ""

			// Qismlarni ro'yxatlash

			if len(items) > 0 {

				for i, item := range items {

					partList += fmt.Sprintf("ID: %d | Turi: %s\n", i+1, strings.Title(item.Kind))

				}

			} else {

				partList = "Mavjud qismlar yo'q."

			}

			msgText := fmt.Sprintf("🗑 **%s** (%s) uchun o‘chirmoqchi bo‘lgan **qism ID raqamini kiriting:\n\n-- Qismlar Ro'yxati --\n%s", name, strings.ToUpper(code), partList)

			msg := tgbotapi.NewMessage(chatID, msgText)

			msg.ParseMode = "Markdown"

			bot.Send(msg)

			// Qismni ID bo'yicha o'chirishni boshlash

		case strings.HasPrefix(data, "delete_part:"):

			code := strings.TrimPrefix(data, "delete_part:")

			animeCodeTemp[userID] = code

			adminState[userID] = "delete_part_id"

			storageMutex.RLock()

			items := animeStorage[code]

			storageMutex.RUnlock()

			name := animeInfo[code]

			// Qismlarni ro'yxatlash

			partList := ""

			if len(items) > 0 {

				for i, item := range items {

					partList += fmt.Sprintf("ID: %d | Turi: %s\n", i+1, strings.Title(item.Kind))

				}

			} else {

				partList = "Mavjud qismlar yo'q."

			}

			msgText := fmt.Sprintf("🗑 **%s** (%s) uchun o‘chirmoqchi bo‘lgan **qism ID raqamini** (1, 2, 3...) kiriting:\n\n-- Qismlar Ro'yxati --\n%s", name, strings.ToUpper(code), partList)

			msg := tgbotapi.NewMessage(chatID, msgText)

			msg.ParseMode = "Markdown"

			bot.Send(msg)

			// Anime to'liq o'chirishni tasdiqlash

		case strings.HasPrefix(data, "delete_anime_confirm:"):

			code := strings.TrimPrefix(data, "delete_anime_confirm:")

			adminState[userID] = "delete_anime_confirm_final"

			animeCodeTemp[userID] = code

			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("❗ HAAQIQATDAN ham **%s** (%s) animeni butunlay o‘chirmoqchimisiz? Tasdiqlash uchun '/yes' deb yozing.", animeInfo[code], strings.ToUpper(code)))

			bot.Send(msg)
			// handleCallback funksiyasi ichidagi switch blokiga qo'shiladi
			// ... yuqoridagi boshqa case'lar (edit_name:, delete_part: va h.k.) ...
			// 🔥 Kontent qismlarini qayta tartiblashni boshlash
			// handleCallback funksiyasi ichidagi switch { ... }

		case strings.HasPrefix(data, "reorder_request:"):
			code := strings.TrimPrefix(data, "reorder_request:")

			storageMutex.Lock()
			items := animeStorage[code]
			storageMutex.Unlock()

			if len(items) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Tartiblash uchun qismlar yo'q."))
				return
			}

			listStr := ""
			// Elementlarni boricha (bazadagi tartibda) chiqaramiz
			for i := range items {
				// ID: i+1 foydalanuvchi tanlashi uchun indeks roli o'ynaydi
				listStr += fmt.Sprintf("ID: %d | Turi: Video/File\n", i+1)
			}

			text := fmt.Sprintf("🔢 *%s* Kino qismlarini tartiblash\n\n"+
				"-- Qismlar Ro'yxati --\n%s\n"+
				"Yangi tartibni kiriting.\nMisol uchun, oxirgi tashlangan 5-sini birinchi qo'ymoqchi bo'lsangiz: `5,4,3,2,1`",
				animeInfo[code], listStr)

			msg := tgbotapi.NewMessage(chatID, text)
			msg.ParseMode = "Markdown"
			bot.Send(msg)

			adminState[userID] = "wait_reorder_ids"
			animeCodeTemp[userID] = code

		default:

			// Handle unknown admin commands

			bot.Send(tgbotapi.NewMessage(chatID, "❓ Noma'lum buyruq. Iltimos, menu'dan tanlang."))

		}

		return

	}

	if strings.HasPrefix(data, "reorderAnime:") {
		code := strings.TrimPrefix(data, "reorderAnime:")

		storageMutex.RLock()
		list := animeStorage[code]
		storageMutex.RUnlock()

		if len(list) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Qismlar topilmadi."))
			return
		}

		var sb strings.Builder
		sb.WriteString("📋 *Qismlar ro'yxati:\n\n")

		for i, item := range list {
			sb.WriteString(fmt.Sprintf("ID: %d | Turi: %s\n", i+1, strings.Title(item.Kind)))
		}

		sb.WriteString("\n📝 Yangi tartibni yuboring (masalan: 1,2,3,5,4)")

		msg := tgbotapi.NewMessage(chatID, sb.String())
		msg.ParseMode = "Markdown"
		bot.Send(msg)

		adminState[userID] = "anime_reorder:" + code
		return
	}

}

func handleBroadcast(
	bot *tgbotapi.BotAPI,
	update tgbotapi.Update,
	adminState map[int64]string,
	broadcastCache map[int64]*tgbotapi.Message,
	users map[int64]bool, // foydalanuvchilar
	adminMutex *sync.Mutex, // admin holat va cache uchun
	usersMutex *sync.RWMutex, // users map uchun
) {
	var userID int64
	var chatID int64

	// ------------------ IDENTIFY ------------------
	if update.Message != nil {
		userID = update.Message.From.ID
		chatID = update.Message.Chat.ID
	} else if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
		chatID = update.CallbackQuery.Message.Chat.ID
	} else {
		return
	}

	// ------------------ CALLBACK ------------------
	if update.CallbackQuery != nil {
		data := update.CallbackQuery.Data

		switch data {
		case "broadcast_send":
			// Xabarni olish
			adminMutex.Lock()
			msg, ok := broadcastCache[userID]
			adminMutex.Unlock()

			if !ok || msg == nil {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Xabar topilmadi. Qaytadan yuboring."))
				return
			}

			// Admin holatini tozalash
			adminMutex.Lock()
			delete(adminState, userID)
			delete(broadcastCache, userID)
			adminMutex.Unlock()

			// Broadcastni goroutine orqali yuborish
			go func(msg *tgbotapi.Message, chatID int64) {
				usersMutex.RLock()
				userList := make([]int64, 0, len(users))
				for uid := range users {
					userList = append(userList, uid)
				}
				usersMutex.RUnlock()

				const batchSize = 30
				const delay = 1 * time.Second
				sent := 0

				for i := 0; i < len(userList); i += batchSize {
					end := i + batchSize
					if end > len(userList) {
						end = len(userList)
					}

					for _, uid := range userList[i:end] {
						copyMsg := tgbotapi.NewCopyMessage(uid, msg.Chat.ID, msg.MessageID)
						if _, err := bot.Send(copyMsg); err == nil {
							sent++
						}
						time.Sleep(30 * time.Millisecond)
					}

					time.Sleep(delay)
				}

				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Reklama %d foydalanuvchiga yetkazildi", sent)))
			}(msg, chatID)

			bot.Send(tgbotapi.NewMessage(chatID, "📢 Reklama yuborish boshlandi..."))
			return

		case "broadcast_cancel":
			adminMutex.Lock()
			delete(adminState, userID)
			delete(broadcastCache, userID)
			adminMutex.Unlock()

			bot.Send(tgbotapi.NewMessage(chatID, "❌ Reklama bekor qilindi."))
			return
		}
	}

	// ------------------ MESSAGE ------------------
	adminMutex.Lock()
	state := adminState[userID]
	adminMutex.Unlock()

	if update.Message != nil && state == "waiting_for_ad" {
		// Xabarni saqlash
		adminMutex.Lock()
		broadcastCache[userID] = update.Message
		adminState[userID] = "confirm_ad"
		adminMutex.Unlock()

		// Preview yuborish
		preview := tgbotapi.NewCopyMessage(chatID, update.Message.Chat.ID, update.Message.MessageID)
		bot.Send(preview)

		// Inline confirm tugmalar
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Yuborish", "broadcast_send"),
				tgbotapi.NewInlineKeyboardButtonData("❌ Bekor qilish", "broadcast_cancel"),
			),
		)

		confirm := tgbotapi.NewMessage(chatID, "⬆️ Reklama tayyor. Yuboraymi?")
		confirm.ReplyMarkup = keyboard
		bot.Send(confirm)
	}
}

func displayStats(bot *tgbotapi.BotAPI, chatID int64) {

	statsMutex.Lock()
	defer statsMutex.Unlock()

	var sb strings.Builder

	sb.WriteString("✨ <b>UMUMIY BOT STATISTIKASI</b> ✨\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// 1️⃣ BOT FAOLIYATI
	sb.WriteString("📊 <b>BOT FAOLIYATI</b>\n")
	sb.WriteString(fmt.Sprintf("👋 /start buyurganlar: <b>%d</b>\n", startCount))
	sb.WriteString(fmt.Sprintf("👥 Jami foydalanuvchilar: <b>%d</b>\n", len(users)))
	sb.WriteString(fmt.Sprintf("🚫 Bloklanganlar: <b>%d</b>\n\n", len(blockedUsers)))

	// 2️⃣ KOD QIDIRUVI
	sb.WriteString("🔍 <b>KOD QIDIRUVI</b>\n")
	if len(searchStats) == 0 {
		sb.WriteString("— Hali qidiruvlar yo‘q\n\n")
	} else {
		for code, count := range searchStats {
			sb.WriteString(fmt.Sprintf("• <code>%s</code> — <b>%d</b> marta\n",
				strings.ToUpper(code), count))
		}
		sb.WriteString("\n")
	}

	// 3️⃣ ENG MASHHUR 5 ANIME
	sb.WriteString("🏆 <b>ENG MASHHUR 5 Kino</b>\n")

	type kv struct {
		Code  string
		Count int
	}

	var arr []kv
	for code, count := range searchStats {
		arr = append(arr, kv{code, count})
	}

	sort.Slice(arr, func(i, j int) bool {
		return arr[i].Count > arr[j].Count
	})

	if len(arr) == 0 {
		sb.WriteString("— Statistika hali yetarli emas\n\n")
	} else {
		if len(arr) > 5 {
			arr = arr[:5]
		}
		for i, v := range arr {
			name := animeInfo[v.Code]
			if name == "" {
				name = "Nomaʼlum"
			}
			sb.WriteString(fmt.Sprintf("%d. <b>%s</b> (<code>%s</code>) — %d ta\n",
				i+1, name, strings.ToUpper(v.Code), v.Count))
		}
		sb.WriteString("\n")
	}

	// 4️⃣ KANALLAR
	sb.WriteString("🔗 <b>KANAL OBUNALARI</b>\n")
	if len(channels) == 0 {
		sb.WriteString("— Kanal ulanmagan\n\n")
	} else {
		for _, ch := range channels {
			sb.WriteString(fmt.Sprintf("✅ @%s\n", ch))
		}
		sb.WriteString("\n")
	}

	// 5️⃣ FOYDALANUVCHI STATISTIKASI
	active, inactive,
		todayNew, weekNew, monthNew,
		todayActive, weekActive, monthActive := calculateUserStats()

	sb.WriteString("📊 <b>FOYDALANUVCHI STATISTIKASI</b>\n")
	sb.WriteString(fmt.Sprintf("🟢 Faol: <b>%d</b>\n", active))
	sb.WriteString(fmt.Sprintf("🚫 Nofaol: <b>%d</b>\n\n", inactive))

	sb.WriteString("🆕 <b>OBUNACHILAR</b>\n")
	sb.WriteString(fmt.Sprintf("📅 Bugungi: <b>%d</b>\n", todayNew))
	sb.WriteString(fmt.Sprintf("🗓 7 kunlik: <b>%d</b>\n", weekNew))
	sb.WriteString(fmt.Sprintf("🗓 30 kunlik: <b>%d</b>\n\n", monthNew))

	sb.WriteString("🔥 <b>AKTIVLIK</b>\n")
	sb.WriteString(fmt.Sprintf("⚡ Bugungi: <b>%d</b>\n", todayActive))
	sb.WriteString(fmt.Sprintf("📈 7 kunlik: <b>%d</b>\n", weekActive))
	sb.WriteString(fmt.Sprintf("📊 30 kunlik: <b>%d</b>\n\n", monthActive))

	sb.WriteString("ℹ️ <i>Maʼlumotlar server vaqti bilan yangilandi</i>")

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ParseMode = tgbotapi.ModeHTML

	_, err := bot.Send(msg)
	if err != nil {
		log.Println("Statistika yuborishda xato:", err)
	}
}

func displayBlockedUsers(bot *tgbotapi.BotAPI, chatID int64) {
	// Implement blocked users list display logic here
	var blockedList []string
	for id := range blockedUsers {
		blockedList = append(blockedList, strconv.FormatInt(id, 10))
	}
	bot.Send(tgbotapi.NewMessage(chatID, "📵 Bloklangan foydalanuvchilar:\n"+strings.Join(blockedList, " \n ")))
}

func updateUserActivity(userID int64) {
	now := time.Now()

	statsMutex.Lock()
	defer statsMutex.Unlock()

	// 🔥 foydalanuvchini ro‘yxatga qo‘shish
	users[userID] = true

	// oxirgi aktivlik
	userLastActive[userID] = now

	// birinchi kirish vaqti
	if _, ok := userJoinedAt[userID]; !ok {
		userJoinedAt[userID] = now
	}
}

func sendPageMenuMarkup(chatID int64) *tgbotapi.InlineKeyboardMarkup {
	data := userPages[chatID]
	if data == nil {
		return nil
	}
	start := data.Page * 10
	end := start + 10
	if end > len(data.Items) {
		end = len(data.Items)
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton
	// Qism tugmalari 10 tadan ko'rsatiladi
	for i := start; i < end; i++ {
		label := fmt.Sprintf("%d", i+1)
		cb := fmt.Sprintf("play_%d", i)
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData(label, cb))
		// Har bi
		if len(currentRow) == 3 || i == end-1 {
			rows = append(rows, currentRow)
			currentRow = nil
		}
	}
	nav := []tgbotapi.InlineKeyboardButton{}
	totalPages := (len(data.Items) + 9) / 10 // Jami sahifalar soni
	// Navigatsiya tugmalari
	// ⬅️ Olingi (<) tugmasi
	if data.Page > 0 {
		// Matnni faqat "<" ga o'zgartirdik
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("<", "prev"))
	}
	// Sahifa raqamini ko'rsatuvchi tugmani Olib tashladik (Sizning talabingizga ko'ra)
	// nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d/%d", data.Page+1, totalPages), "page_count"))
	// ➡️ Keyingi (>) tugmasi
	if data.Page+1 < totalPages { // Agar joriy sahifa oxirgisidan oldin bo'lsa

		// Matnni faqat ">" ga o'zgartirdik

		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(">", "next"))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &markup
}

func calculateUserStats() (active int, inactive int, todayNew int, weekNew int, monthNew int, todayActive int, weekActive int, monthActive int) {
	now := time.Now()

	for userID := range users {

		// 🆕 Obuna statistikasi
		if joinTime, ok := userJoinedAt[userID]; ok {
			if joinTime.After(now.Add(-24 * time.Hour)) {
				todayNew++
			}
			if joinTime.After(now.Add(-7 * 24 * time.Hour)) {
				weekNew++
			}
			if joinTime.After(now.Add(-30 * 24 * time.Hour)) {
				monthNew++
			}
		}

		// 🔥 Aktivlik statistikasi
		if last, ok := userLastActive[userID]; ok {
			if last.After(now.Add(-24 * time.Hour)) {
				todayActive++
				weekActive++
				monthActive++
				active++
			} else if last.After(now.Add(-7 * 24 * time.Hour)) {
				weekActive++
				monthActive++
				active++
			} else if last.After(now.Add(-30 * 24 * time.Hour)) {
				monthActive++
				active++
			} else {
				inactive++
			}
		} else {
			inactive++
		}
	}

	return
}

func saveData() {
	// ========================
	// 1️⃣ Anime Storage (kontent)
	// ========================
	storageMutex.RLock()
	storageData, err := json.MarshalIndent(animeStorage, "", "  ")
	storageMutex.RUnlock()
	if err != nil {
		log.Printf("❌ Kino storage JSON xatosi: %v", err)
	} else {
		if err := os.WriteFile(ANIME_STORAGE_FILE, storageData, 0644); err != nil {
			log.Printf("❌ Kino storage faylga yozishda xato: %v", err)
		}
	}

	// ========================
	// 2️⃣ Anime Info (ma'lumot)
	// ========================
	infoMutex.RLock()
	infoData, err := json.MarshalIndent(animeInfo, "", "  ")
	infoMutex.RUnlock()
	if err != nil {
		log.Printf("❌ Kino info JSON xatosi: %v", err)
	} else {
		if err := os.WriteFile(ANIME_INFO_FILE, infoData, 0644); err != nil {
			log.Printf("❌ Kino info faylga yozishda xato: %v", err)
		}
	}

	// ========================
	// 3️⃣ Adminlar, Kanallar, Foydalanuvchilar
	// ========================
	config := AdminConfig{
		Admins:   adminIDs,
		Channels: channels,
		AllUsers: userJoinedAt,
	}

	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		log.Printf("❌ Config JSON xatosi: %v", err)
	} else {
		if err := os.WriteFile(ADMIN_CONFIG_FILE, configData, 0644); err != nil {
			log.Printf("❌ Config faylga yozishda xato: %v", err)
		}
	}

	// ========================
	// 4️⃣ Stats saqlash
	// ========================
	statsMutex.Lock()
	defer statsMutex.Unlock()

	data := struct {
		UserJoined  map[int64]time.Time `json:"userJoined"`
		UserActive  map[int64]time.Time `json:"userActive"`
		SearchStats map[string]int      `json:"searchStats"`
	}{
		UserJoined:  userJoined,
		UserActive:  userActive,
		SearchStats: searchStats,
	}

	file, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Printf("❌ stats.json JSON marshaling xatosi: %v", err)
		return
	}

	if err := os.WriteFile("stats.json", file, 0644); err != nil {
		log.Printf("❌ stats.json faylga yozishda xato: %v", err)
	}
}

func loadData() {
	// ========================
	// 1️⃣ Anime Storage
	// ========================
	if data, err := os.ReadFile(ANIME_STORAGE_FILE); err == nil {
		storageMutex.Lock()
		_ = json.Unmarshal(data, &animeStorage)
		storageMutex.Unlock()
	}

	// ========================
	// 2️⃣ Anime Info
	// ========================
	if data, err := os.ReadFile(ANIME_INFO_FILE); err == nil {
		infoMutex.Lock()
		_ = json.Unmarshal(data, &animeInfo)
		infoMutex.Unlock()
	}

	// ========================
	// 3️⃣ Adminlar, Kanallar, Foydalanuvchilar
	// ========================
	if data, err := os.ReadFile(ADMIN_CONFIG_FILE); err == nil {
		var config AdminConfig
		if err := json.Unmarshal(data, &config); err == nil {
			adminIDs = config.Admins
			channels = config.Channels
			userJoinedAt = config.AllUsers

			for id := range userJoinedAt {
				users[id] = true
			}
			log.Printf("✅ %d ta foydalanuvchi yuklandi.", len(userJoinedAt))
		}
	}

	// ========================
	// 4️⃣ Stats yuklash
	// ========================
	if data, err := os.ReadFile("stats.json"); err == nil {
		var statsData struct {
			UserJoined  map[int64]time.Time `json:"userJoined"`
			UserActive  map[int64]time.Time `json:"userActive"`
			SearchStats map[string]int      `json:"searchStats"`
		}
		if err := json.Unmarshal(data, &statsData); err == nil {
			statsMutex.Lock()
			userJoined = statsData.UserJoined
			userActive = statsData.UserActive
			searchStats = statsData.SearchStats
			statsMutex.Unlock()
		}
	}

	log.Printf("✅ %d ta Kino va %d ta kanal yuklandi.", len(animeInfo), len(channels))
}

func addUser(userID int64) {
	usersMutex.Lock()
	defer usersMutex.Unlock()

	if userJoinedAt == nil {
		userJoinedAt = make(map[int64]time.Time)
	}
	if allUsers == nil {
		allUsers = make(map[int64]bool)
	}

	if _, exists := userJoinedAt[userID]; !exists {
		userJoinedAt[userID] = time.Now()
		allUsers[userID] = true
		saveData() // darhol saqlash
		saveStats()
	}
}

func saveStats() {
	statsMutex.Lock()
	defer statsMutex.Unlock()

	data := struct {
		UserJoined  map[int64]time.Time `json:"userJoined"`
		UserActive  map[int64]time.Time `json:"userActive"`
		SearchStats map[string]int      `json:"searchStats"`
	}{
		UserJoined:  userJoined,
		UserActive:  userActive,
		SearchStats: searchStats,
	}

	file, _ := json.MarshalIndent(data, "", "  ")
	_ = os.WriteFile("stats.json", file, 0644)
}

func handleAdminText(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	text := update.Message.Text
	currentState := adminState[userID]

	// Admin holatini tekshiramiz

	currentState, ok := adminState[userID]

	if !ok {
		return
	}

	if strings.HasPrefix(currentState, "anime_reorder:") {
		handleAnimeReorder(bot, update, userID, chatID, currentState)
		return
	}

	switch currentState {

	case "admin_vip_main":
		msg := tgbotapi.NewEditMessageText(chatID, update.CallbackQuery.Message.MessageID, "🌟 **VIP Foydalanuvchilar Paneli:**")
		msg.ReplyMarkup = vipAdminMenu()
		bot.Send(msg)

	case "wait_vip_add":
		// 1. Matnni raqamga (ID) aylantiramiz
		targetID, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato! Iltimos, faqat raqamli ID yuboring."))
			return
		}

		// 2. VIP ro'yxatiga qo'shamiz
		vipMutex.Lock()
		vipUsers[targetID] = true
		vipMutex.Unlock()

		// 3. Ma'lumotni saqlaymiz (faylga)
		go saveData()

		// 4. Admunga javob qaytaramiz va holatni o'chiramiz
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ ID: %d muvaffaqiyatli VIP qilindi!", targetID)))
		delete(adminState, userID)
		return

	case "wait_vip_del":
		targetID, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato! Iltimos, faqat raqamli ID yuboring."))
			return
		}

		vipMutex.Lock()
		if _, exists := vipUsers[targetID]; exists {
			delete(vipUsers, targetID)
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 ID: %d VIP ro'yxatidan o'chirildi!", targetID)))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu ID VIP ro'yxatida topilmadi."))
		}
		vipMutex.Unlock()

		go saveData()
		delete(adminState, userID)
		return

	case "wait_reorder_ids":
		code := animeCodeTemp[userID]
		input := strings.TrimSpace(text)

		// 1. Foydalanuvchi yuborgan ID-larni massivga olamiz (masalan: [5, 1, 4, 2, 3])
		newOrder := parseIDsToDelete(input)

		storageMutex.Lock()
		oldItems := animeStorage[code]

		if len(newOrder) != len(oldItems) {
			storageMutex.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Xato! Barcha %d ta IDni kiritishingiz kerak.", len(oldItems))))
			return
		}

		// 2. Yangi tartib bo'yicha vaqtinchalik massiv yaratamiz
		var reorderedItems []ContentItem

		for _, id := range newOrder {
			index := id - 1 // foydalanuvchi yozgan 1 -> index 0
			if index >= 0 && index < len(oldItems) {
				// Tanlangan elementni yangi massivga qo'shamiz
				reorderedItems = append(reorderedItems, oldItems[index])
			} else {
				storageMutex.Unlock()
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Noto'g'ri ID: %d", id)))
				return
			}
		}

		// 3. Bazani yangilaymiz
		animeStorage[code] = reorderedItems
		storageMutex.Unlock()

		go saveData()

		bot.Send(tgbotapi.NewMessage(chatID, "✅ Tartib saqlandi! Endi qismlar siz yuborgan ketma-ketlikda chiqadi."))

		delete(adminState, userID)
		delete(animeCodeTemp, userID)

	case "delete_anime_code":
		code := strings.ToLower(strings.TrimSpace(text))

		infoMutex.RLock()
		_, exists := animeInfo[code]
		infoMutex.RUnlock()

		if !exists {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu kod bo‘yicha Kino topilmadi."))
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

		// 📢 MUHIM: Ma'lumotlar o'chirilgandan so'ng saqlash
		go saveData() // 💾 Qo'shildi

		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 '%s' kodi bo‘yicha Kino o‘chirildi!", strings.ToUpper(code))))
		delete(adminState, userID)
		return

	case "add_channel_wait":
		text := update.Message.Text
		var chatID64 int64
		var err error

		// 1. ChatID yoki Username orqali kanalni topish
		if strings.HasPrefix(text, "-100") {
			chatID64, err = strconv.ParseInt(text, 10, 64)
		} else {
			username := text
			if !strings.HasPrefix(username, "@") {
				username = "@" + username
			}
			// Username orqali kanal ma'lumotini olish
			chat, err := bot.GetChat(tgbotapi.ChatInfoConfig{
				ChatConfig: tgbotapi.ChatConfig{SuperGroupUsername: username},
			})
			if err == nil {
				chatID64 = chat.ID
			}
		}

		if err != nil || chatID64 == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Kanal topilmadi. ID yoki Usernameni tekshiring."))
			return
		}

		// 2. Kanal turini tekshirish
		chat, _ := bot.GetChat(tgbotapi.ChatInfoConfig{
			ChatConfig: tgbotapi.ChatConfig{ChatID: chatID64},
		})

		if chat.Type == "channel" || chat.Type == "supergroup" {
			// Agar kanal MAXFIY bo'lsa (username yo'q bo'lsa)
			if chat.UserName == "" {
				adminState[userID] = "add_private_link"
				adminTempID[userID] = chat.ID // Kanal ID sini vaqtincha saqlaymiz
				bot.Send(tgbotapi.NewMessage(chatID, "🔒 Bu maxfiy kanal ekan. Iltimos, kanalga ulanish havolasini (Invite Link) yuboring:"))
			} else {
				// Agar kanal OCHIQ bo'lsa
				channels[chat.ID] = chat.UserName
				go saveData()
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Ochiq kanal qo'shildi: @%s", chat.UserName)))
				adminState[userID] = ""
			}
		}

	case "add_private_link":
		link := update.Message.Text
		if !strings.HasPrefix(link, "https://t.me/") {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Noto'g'ri havola. Havola https://t.me/ bilan boshlanishi kerak."))
			return
		}

		targetChatID := adminTempID[userID]
		channels[targetChatID] = link // Maxfiy kanal uchun siz yuborgan havolani saqlaydi
		go saveData()

		bot.Send(tgbotapi.NewMessage(chatID, "✅ Maxfiy kanal va havola muvaffaqiyatli saqlandi!"))
		adminState[userID] = ""
		delete(adminTempID, userID) // Vaqtincha IDni o'chiramiz

	case "add_admin_id":
		newID, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ ID noto‘g‘ri. Faqat raqam kiriting."))
			return
		}
		admins[newID] = true
		go saveData() // 💾 Qo'shildi
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ %d adminlarga qo‘shildi!", newID)))
		delete(adminState, userID)
		return

	case "edit_new_code":
		new_code := strings.ToLower(strings.TrimSpace(text))
		old_code := animeCodeTemp[userID]
		if new_code == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Kod bo‘sh bo‘lishi mumkin emas. Qayta kiriting:"))
			return
		}
		if new_code == old_code {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yangi kod eski kod bilan bir xil bo‘lishi mumkin emas."))
			return
		}
		// Yangi kod allaqachon mavjudligini tekshirish
		infoMutex.RLock()
		_, exists := animeInfo[new_code]
		infoMutex.RUnlock()

		if exists {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu kod (ID) allaqachon boshqa animega tegishli!"))
			return
		}

		// 🔥 Ma'lumotlarni yangi kodga ko'chirish
		infoMutex.Lock()
		storageMutex.Lock()

		// 1. Anime nomini yangi kod bilan saqlash
		animeInfo[new_code] = animeInfo[old_code]

		// 2. Anime kontentini yangi kod bilan saqlash
		animeStorage[new_code] = animeStorage[old_code]

		// 3. Eskilarini o'chirish
		delete(animeInfo, old_code)
		delete(animeStorage, old_code)

		storageMutex.Unlock()
		infoMutex.Unlock()

		// 💾 MUHIM: Ma'lumotlar muvaffaqiyatli ko'chirilgandan so'ng saqlash!
		go saveData() // 💾 Qo'shildi

		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kino kodi  ( %s ) -  dan - ( %s ) ga muvaffaqiyatli o'zgartirildi!", strings.ToUpper(old_code), strings.ToUpper(new_code))))
		delete(adminState, userID)
		delete(animeCodeTemp, userID)
		return // ------------------ ADMIN: Admin o'chirish ID so'rov ------------------

	case "edit_new_name":
		new_name := strings.TrimSpace(text)
		old_code, ok := animeCodeTemp[userID]
		if !ok {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato: Kino kodi topilmadi. Qayta boshlash uchun /admin buyrug'ini kiriting."))
			delete(adminState, userID)
			return
		}

		if new_name == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Nom bo‘sh bo‘lishi mumkin emas. Qayta kiriting:"))
			return
		}

		// Nomni yangilash
		infoMutex.Lock()
		animeInfo[old_code] = new_name
		infoMutex.Unlock()

		go saveData() // 💾 Qo'shildi

		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kino nomi ( %s ) ga muvaffaqiyatli o'zgartirildi!", new_name)))

		// Holatni tozalash
		delete(adminState, userID)
		delete(animeCodeTemp, userID)
		return

		// ... yuqoridagi boshqa case'lar (delete_anime_code, add_admin_id va h.k.) ...

		// ⚠️ Kodning boshqa joyida e'lon qilingan bo'lishi kerak:
		// const MAIN_ADMIN_ID int64 = 123456789

	case "remove_admin_id":
		remID, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ ID noto‘g‘ri. Faqat raqam kiriting."))
			return
		}

		if remID == userID {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ O‘zingizni o‘chirishingiz mumkin emas!"))
			return
		}

		// 👇👇👇 ASOSIY ADMINNI TEKSHIRISH QO'SHILDI 👇👇👇
		if remID == MAIN_ADMIN_ID {
			bot.Send(tgbotapi.NewMessage(chatID, "🛑 Asosiy adminni o‘chirish mumkin emas!"))
			return
		}
		// 👆👆👆 ASOSIY ADMINNI TEKSHIRISH QO'SHILDI 👆👆👆

		delete(admins, remID)
		go saveData() // 💾 Qo'shildi
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 %d admin o‘chirildi!", remID)))
		delete(adminState, userID)
		return // return qo'shildi

	case "block_user":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
			return
		}
		blockedUsers[id] = true
		go saveData() // 💾 Qo'shildi
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🚫 %d bloklandi!", id)))
		delete(adminState, userID)
		return // return qo'shildi

	case "unblock_user":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
			return
		}
		delete(blockedUsers, id)
		go saveData() // 💾 Qo'shildi
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("♻️ %d blokdan chiqarildi!", id)))
		delete(adminState, userID)
		return // return qo'shildi

	case "add_channel_chatid":
		text = strings.TrimSpace(text)

		// 1️⃣ HAVOLA bo‘lsa → bu ID EMAS
		if strings.Contains(text, "t.me") {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu chatID emas. Iltimos, kanalning raqamli ChatID'sini yuboring (masalan: -1001234567890)."))
			return
		}

		// 2️⃣ faqat raqam bo‘lishi kerak va -100 bilan boshlanishi sharti
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || !strings.HasPrefix(text, "-100") {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Noto‘g‘ri chatID. ChatID -100 bilan boshlanishi kerak."))
			return
		}

		// 3️⃣ ChatID vaqtinchalik saqlanadi
		adminTempID[userID] = id
		adminState[userID] = "add_channel_username"

		bot.Send(tgbotapi.NewMessage(chatID, "🔗 Endi kanal username yoki havolasini yuboring:"))
		return

	case "add_channel_username":
		username := strings.TrimSpace(text)

		if username == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Iltimos, kanal username yoki havolasini kiriting!"))
			return
		}

		chatIDnum := adminTempID[userID]

		// Maxfiy havola bo'lsa
		if strings.HasPrefix(username, "https://t.me/+") {
			// Bot kanalga kirmaydi, faqat ro'yxatga qo'shamiz
			adminTempChannels[userID] = append(adminTempChannels[userID], Channel{
				ChatID:   0, // hali raqam yo'q
				Username: username,
			})

			bot.Send(tgbotapi.NewMessage(chatID, "🔗 Guruh havolasi olindi!\n📨 Admin tasdiqlashi kerak."))
			bot.Send(tgbotapi.NewMessage(chatID, "✅ Kanal qo‘shildi!"))
		} else {
			// Oddiy username yoki raqamli ChatID
			channels[chatIDnum] = username
			saveData()
			bot.Send(tgbotapi.NewMessage(chatID,
				fmt.Sprintf("✅ Kanal qo‘shildi!\nChatID: %d\nUsername: %s", chatIDnum, username),
			))
		}

		delete(adminState, userID)
		delete(adminTempID, userID)
		return

	case "remove_channel":
		if strings.HasPrefix(text, "https://t.me/+") {
			// Maxfiy havola bo'lsa
			// ... (Sizning kodingizdagi kabi, adminTempChannels ro'yxatidan o'chirish)
			found := false
			for i, ch := range adminTempChannels[userID] {
				if ch.Username == text {
					adminTempChannels[userID] = append(adminTempChannels[userID][:i], adminTempChannels[userID][i+1:]...)
					found = true
					bot.Send(tgbotapi.NewMessage(chatID, "✅ Maxfiy kanal havolasi o‘chirildi!")) // Xabarni aniqlashtirdik
					break
				}
			}
			if !found {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Bunday maxfiy havola topilmadi."))
			}
			return
		}

		// Raqamli ChatID bo'lsa
		id, err := strconv.ParseInt(text, 10, 64)
		if err == nil { // Matn ChatID raqami sifatida parse qilina olsa

			// 1. Oddiy kanallar xaritasidan o'chirish (agar mavjud bo'lsa)
			if _, ok := channels[id]; ok {
				delete(channels, id)
				saveData()
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Oddiy kanal o‘chirildi! \n(ChatID: %d)", id)))
				return
			}

			// 2. adminTempChannels ro'yxatidan o'chirish (agar maxfiy kanal bo'lsa)
			found := false
			for i, ch := range adminTempChannels[userID] {
				// Agar ro'yxatdagi kanalning ChatID'si so'ralgan ID ga teng bo'lsa
				if ch.ChatID == id {
					adminTempChannels[userID] = append(adminTempChannels[userID][:i], adminTempChannels[userID][i+1:]...)
					found = true
					bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Ro'yxatdagi kanal o‘chirildi! \n (ChatID: %d) ", id)))
					break
				}
			}
			if found {
				return
			}

		}

		// Agar raqam bo'lmasa yoki yuqorida topilmagan bo'lsa, username bo'lishi mumkin
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Bunday kanal topilmadi. Raqamli ChatID yoki to'liq maxfiy havolani kiriting."))
		return

	case "anime_name":
		name := strings.TrimSpace(text)
		if name == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Nomi bo'sh bo'lishi mumkin emas. Iltimos nom kiriting:"))
			return
		}
		animeNameTemp[userID] = name
		adminState[userID] = "anime_code"
		bot.Send(tgbotapi.NewMessage(chatID, "🆔 Endi Kino kodi kiriting :"))
		return // return qo'shildi

	case "delete_channel":
		text = strings.TrimSpace(text)
		var found bool
		var chatID int64

		// 1. Agar raqam bo'lsa
		id, err := strconv.ParseInt(text, 10, 64)
		if err == nil {
			if _, ok := channels[id]; ok {
				delete(channels, id)
				found = true
				chatID = id
			}
		} else {
			// 2. Agar havola (@username) bo'lsa
			for idTemp, username := range channels {
				if username == text || "@"+username == text {
					delete(channels, idTemp)
					found = true
					chatID = idTemp
					break
				}
			}
		}

		// Javob yuborish
		if found {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kanal o‘chirildi! ChatID: %d", chatID)))
		} else {
			// Agar topilmasa, javobni adminga yoki default chatga yuborish kerak
			bot.Send(tgbotapi.NewMessage(chatID /* yoki defaultChatID */, "❌ Bunday ChatID yoki havola topilmadi."))
		}
		return

	case "anime_code":
		code := strings.ToLower(strings.TrimSpace(text))

		// ❗️ 1) Bo'sh kod tekshiruvi
		if code == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Kod bo'sh bo'lishi mumkin emas. Iltimos kod kiriting:"))
			return
		}

		// ❗️ 2) Kod allaqachon mavjudligini tekshiramiz
		infoMutex.RLock()
		_, exists := animeInfo[code]
		infoMutex.RUnlock()

		if exists {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Bu kod allaqachon mavjud! Boshqa kod kiriting:"))
			return
		}

		// ❗️ 3) Kodni saqlaymiz (code -> anime name)
		infoMutex.Lock()
		animeInfo[code] = animeNameTemp[userID]
		infoMutex.Unlock()

		// ❗️ Bo'sh storage slot yaratamiz
		storageMutex.Lock()
		animeStorage[code] = nil
		storageMutex.Unlock()

		// 💾 Nom va kod saqlanganidan so'ng saqlash!
		go saveData() // 💾 Qo'shildi

		// ❗️ Admin kiritgan kodni vaqtincha saqlaymiz
		animeCodeTemp[userID] = code

		// ❗️ Admin endi kontent yuborishi kerak
		adminState[userID] = "anime_videos"

		bot.Send(tgbotapi.NewMessage(
			chatID,
			fmt.Sprintf("🎞 Endi '%s' uchun videolar/rasmlar/fayllar yoki matnlarni yuboring. Tugagach /ok deb yozing.",
				animeNameTemp[userID]),
		))
		return // return qo'shildi

	case "delete_part_id":
		code := animeCodeTemp[userID]
		input := strings.TrimSpace(text) // 'text' o'rniga 'input' ishlatiladi

		// 1. Kiritilgan ma'lumotni tahlil qilish (parse)
		idsToDelete := parseIDsToDelete(input) // Yuqoridagi javobdagi funksiya kerak!

		if len(idsToDelete) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ ID noto‘g‘ri kiritildi. Iltimos, bitta raqam, vergul bilan ajratilgan raqamlar (3,5,7) yoki oralig'ini (4-8) kiriting:"))
			return
		}

		// 2. IDlarni tartiblash va teskari tartibda o'chirishga tayyorlanish
		sort.Ints(idsToDelete) // O'sish tartibida tartiblaymiz

		storageMutex.Lock()
		items := animeStorage[code]
		deletedCount := 0

		// Eng katta ID'dan eng kichigiga qarab aylanamiz (siljish muammosini hal qilish uchun)
		for i := len(idsToDelete) - 1; i >= 0; i-- {
			partID := idsToDelete[i]
			indexToRemove := partID - 1 // 0-asosli indeks

			// Indexning to'g'riligini tekshirish
			if indexToRemove >= 0 && indexToRemove < len(items) {
				// Elementni o'chirish (Go slicing texnikasi)
				items = append(items[:indexToRemove], items[indexToRemove+1:]...)
				deletedCount++
			}
		}

		// 3. Storage'ni yangilash va Lockni bo'shatish
		animeStorage[code] = items
		storageMutex.Unlock()

		if deletedCount > 0 {
			go saveData() // 💾 O'zgartirish saqlanadi

			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
				"✅ %s (%s) dan %d ta qism muvaffaqiyatli o‘chirildi!",
				animeInfo[code], strings.ToUpper(code), deletedCount)))

		} else {
			// Agar o'chirilgan ID yo'q bo'lsa (masalan, 100-ID kiritilgan bo'lsa)
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
				"❌ Kiritilgan ID'larning hech biri (%s) uchun topilmadi. Mavjud qismlar soni: %d",
				strings.ToUpper(code), len(items))))
		}

		// Holatni yakunlash
		delete(adminState, userID)
		delete(animeCodeTemp, userID)
		return // ------------------ ADMIN: Tahrirlash kodi so'rov ------------------

	case "edit_anime_code":
		code := strings.ToLower(strings.TrimSpace(text))

		infoMutex.RLock()
		name, exists := animeInfo[code]
		infoMutex.RUnlock()

		if !exists {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu kod bo‘yicha Kino topilmadi."))
			delete(adminState, userID)
			return
		}

		// 🔥 Qismlar sonini hisoblash
		storageMutex.RLock()
		partCount := len(animeStorage[code])
		storageMutex.RUnlock()

		// ✅ Menyuni yuborish
		msg := tgbotapi.NewMessage(
			chatID,
			fmt.Sprintf("Kino: %s (%s)\nMavjud qismlar soni: %d ta.\n\nQuyidagilardan birini tanlang:",
				name, strings.ToUpper(code), partCount), // <-- Qismlar soni qo'shildi
		)
		msg.ParseMode = "Markdown"

		// editMenu ni chaqirish
		msg.ReplyMarkup = editMenu(code, name)

		delete(adminState, userID)
		bot.Send(msg)
		return // ------------------ ADMIN: kontent qabul qilish (TO'G'RILANGAN) ------------------
		// handleAdminText funksiyasi ichidagi switch blokiga qo'shiladi

		// handleAdminText funksiyasi ichidagi switch blokida "anime_videos" holatining yangilangan qismi:

	case "anime_videos":
		// 1. Dastlabki tekshiruvlar va o'zgaruvchilarni olish
		code := animeCodeTemp[userID]
		chatID := update.Message.Chat.ID
		text := update.Message.Text

		if code == "" {
			// Xato: Agar foydalanuvchi holati "anime_videos" bo'lsa-yu, code bo'lmasa, bu xato.
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xatolik: Kino kodi topilmadi. Jarayonni boshidan boshlang."))
			delete(adminState, userID) // Holatni tozalash
			return
		}

		// --- 1. /ok Buyrug'i Yakunlash ---
		if strings.ToLower(text) == "/ok" {
			storageMutex.RLock()
			total := len(animeStorage[code])
			storageMutex.RUnlock()

			// 💾 Ma'lumotlarni asinxron saqlash
			go saveData()

			bot.Send(tgbotapi.NewMessage(chatID,
				fmt.Sprintf("✅ '%s' uchun yuklash tugadi! Jami: %d ta qism saqlandi.",
					animeNameTemp[userID], total)))

			// Tozalash
			delete(adminState, userID)
			delete(animeCodeTemp, userID)
			delete(animeNameTemp, userID)
			return
		}

		// --- 2. Kontentni Qabul Qilish va Itemni Tuzish ---
		var item ContentItem
		var itemKind string
		var isContent = true

		if update.Message.Video != nil {
			item = ContentItem{Kind: "video", FileID: update.Message.Video.FileID}
			itemKind = "video"
		} else if update.Message.Photo != nil {
			// Eng katta rasm sifatini olish
			p := update.Message.Photo[len(update.Message.Photo)-1]
			item = ContentItem{Kind: "photo", FileID: p.FileID}
			itemKind = "rasm"
		} else if update.Message.Document != nil {
			item = ContentItem{Kind: "document", FileID: update.Message.Document.FileID}
			itemKind = "fayl"
		} else if update.Message.Text != "" {
			item = ContentItem{Kind: "text", Text: update.Message.Text}
			itemKind = "matn"
		} else {
			isContent = false
			// Xatolik xabarini yuborish, ammo davom etmaslik
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Noma’lum format! Rasm, video, fayl yoki matn yuboring."))
			return
		}

		if isContent {
			storageMutex.Lock()

			// Itemni aniqlangan 'code' ga bog'langan ro'yxatga qo'shamiz
			animeStorage[code] = append(animeStorage[code], item)

			// Mutex ichida bo'lgani uchun ketma-ketlik kafolatlanadi
			episode := len(animeStorage[code])

			// Mutexni darhol ochish
			storageMutex.Unlock()

			// Adminni xabardor qilish
			msgText := fmt.Sprintf("💾 %d-qism qabul qilindi. Turi: %s", episode, strings.Title(itemKind))
			bot.Send(tgbotapi.NewMessage(chatID, msgText))
		}

		return // Jarayonni yakunlash

	case "broadcast_text":
		broadcastCache[userID] = update.Message // <-- *tgbotapi.Message sifatida saqlaymiz

		bot.Send(tgbotapi.NewMessage(chatID, "⬆️ Reklama tayyor. Hammasi to'g'rimi? (Ha / Yo‘q)"))
		adminState[userID] = "broadcast_confirm"
		return
	case "broadcast_confirm":
		if strings.ToLower(strings.TrimSpace(text)) == "ha" {
			go func(msg *tgbotapi.Message) {
				for id := range allUsers {
					if msg.Text != "" {
						bot.Send(tgbotapi.NewMessage(id, msg.Text))
					} else if msg.Photo != nil {
						photo := msg.Photo[len(msg.Photo)-1]
						bot.Send(tgbotapi.NewPhoto(id, tgbotapi.FileID(photo.FileID)))
					} else if msg.Video != nil {
						bot.Send(tgbotapi.NewVideo(id, tgbotapi.FileID(msg.Video.FileID)))
					} else if msg.Document != nil {
						bot.Send(tgbotapi.NewDocument(id, tgbotapi.FileID(msg.Document.FileID)))
					}
				}
			}(broadcastCache[userID])

			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("📢 Reklama barcha foydalanuvchilarga yetkazildi! (%d ta)", len(allUsers))))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Reklama bekor qilindi."))
		}

		delete(broadcastCache, userID)
		delete(adminState, userID)
		return

	default:
		// Agar 'adminState' o'rnatilgan bo'lsa, lekin 'case' mos kelmasa
		bot.Send(tgbotapi.NewMessage(chatID, "❓ Noma'lum holat! Iltimos, qayta urining yoki /admin deb yozing."))
		delete(adminState, userID) // Noto'g'ri holatni tozalash
		return
	}
}

func initQueue(bot *tgbotapi.BotAPI) {
	go processQueue(bot)
}

func handleAnimeReorder(bot *tgbotapi.BotAPI, update tgbotapi.Update, userID int64, chatID int64, currentState string) {
	code := strings.TrimPrefix(currentState, "anime_reorder:")
	newOrder := update.Message.Text

	nums := strings.Split(newOrder, ",")

	storageMutex.Lock()
	oldList := animeStorage[code]

	if len(nums) != len(oldList) {
		storageMutex.Unlock()
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Idlar soni mos emas!"))
		return
	}

	reordered := make([]ContentItem, len(oldList))
	for newIndex, idStr := range nums {
		idStr = strings.TrimSpace(idStr)
		id, err := strconv.Atoi(idStr)
		if err != nil || id < 1 || id > len(oldList) {
			storageMutex.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID: "+idStr))
			return
		}
		reordered[newIndex] = oldList[id-1]
	}

	animeStorage[code] = reordered
	storageMutex.Unlock()

	bot.Send(tgbotapi.NewMessage(chatID, "✅ Qismlar muvaffaqiyatli tartiblandi!"))
	delete(adminState, userID)
}

func processQueue(bot *tgbotapi.BotAPI) {
	for task := range uploadQueue {
		storageMutex.Lock()
		animeStorage[task.Code] = append(animeStorage[task.Code], task.Item)
		episode := len(animeStorage[task.Code])
		storageMutex.Unlock()

		bot.Send(tgbotapi.NewMessage(task.UserID,
			fmt.Sprintf("💾 %d-qism saqlandi (navbat bilan).", episode)))
	}
}

func parseIDsToDelete(input string) []int {
	var ids []int
	seen := make(map[int]bool) // Takrorlanishlarni oldini olish uchun

	// 1. Kiritilgan matnni vergul (,) bo'yicha ajratamiz
	parts := strings.Split(input, ",")

	for _, part := range parts {
		trimmedPart := strings.TrimSpace(part)
		if trimmedPart == "" {
			continue
		}

		// 2. Agar tire (-) bo'lsa, uni oralig' (range) sifatida tahlil qilamiz
		if strings.Contains(trimmedPart, "-") {
			rangeParts := strings.Split(trimmedPart, "-")
			if len(rangeParts) != 2 {
				continue
			}

			start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))

			// Tekshiruv: Raqamlar musbat bo'lishi va start <= end bo'lishi kerak
			if err1 == nil && err2 == nil && start > 0 && end >= start {
				for i := start; i <= end; i++ {
					if !seen[i] {
						ids = append(ids, i)
						seen[i] = true
					}
				}
			}
		} else {
			// 3. Shunchaki bitta ID raqam bo'lsa
			id, err := strconv.Atoi(trimmedPart)
			if err == nil && id > 0 {
				if !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
		}
	}
	return ids
}

//
//import (
//	"fmt"
//	"log"
//	"strconv"
//	"strings"
//	"sync"
//	"time"
//
//	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
//)
//
//// Bot token va Admin ID (o'zingiznikiga almashtiring)
//var BOT_TOKEN = "7982906158:AAHKNYvVUxn5kjUD1OCzJE3btJlVp80mMG8"
//var ADMIN_ID int64 = 7518992824 // O'zingizni admin ID
//
//// ---------------- STATISTIKA ----------------
//var (
//	startCount  int                    // /start bosganlar soni
//	searchStats = make(map[string]int) // kod qidirish statistikasi
//	statsMutex  sync.Mutex             // xavfsiz saqlash uchun
//)
//
//// ---------------- Foydalanuvchi va admin holatlari ----------------
//var adminState = make(map[int64]string)    // admin dialog holatlari
//var adminTempID = make(map[int64]int64)    // vaqtinchalik chatID saqlash
//var animeNameTemp = make(map[int64]string) // admin: nomni vaqtincha saqlash
//var animeCodeTemp = make(map[int64]string) // admin: kodni vaqtincha saqlash
//var startUsers = make(map[int64]string)    // userID -> username
//var blockedUsers = make(map[int64]bool)
//
//// ContentItem turli kontent turlarini saqlash uchun
//type ContentItem struct {
//	Kind   string // "video", "photo", "document", "text"
//	FileID string // file id agar mavjud bo'lsa
//	Text   string // text uchun
//}
//
//// animeStorage: code -> slice of ContentItem
//var animeStorage = make(map[string][]ContentItem)
//var storageMutex sync.RWMutex
//
//// code -> name (masalan: "naruto1" -> "Naruto")
//var animeInfo = make(map[string]string)
//var infoMutex sync.RWMutex
//
//// Kanal saqlash: [ChatID]Username
//var channels = make(map[int64]string)
//
//func main() {
//	bot, err := tgbotapi.NewBotAPI(BOT_TOKEN)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	log.Println("Bot ishga tushdi...")
//
//	u := tgbotapi.NewUpdate(0)
//	u.Timeout = 60
//	updates := bot.GetUpdatesChan(u)
//
//	for update := range updates {
//		// Har bir update ni alohida goroutine ichida parallel qayta ishlaymiz
//		go handleUpdate(bot, update) // 🚀 Bu o'zgarish bot tezligini oshiradi
//	}
//}
//
//// ---------------- UPDATE HANDLER ----------------
//func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
//	if update.Message != nil {
//		handleMessage(bot, update)
//	} else if update.CallbackQuery != nil {
//		handleCallback(bot, update)
//	}
//}
//
//// ---------------- MESSAGE HANDLER ----------------
//func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
//	text := update.Message.Text
//	userID := update.Message.From.ID
//	chatID := update.Message.Chat.ID
//
//	// ------------------ ADMIN PANEL ------------------
//	if text == "/admin" && userID == ADMIN_ID {
//		msg := tgbotapi.NewMessage(chatID, "🛠 Admin panel")
//		msg.ReplyMarkup = adminMenu()
//		bot.Send(msg)
//		return
//	}
//
//	// ------------------ /start ------------------
//	if text == "/start" && userID != ADMIN_ID {
//		statsMutex.Lock()
//		startCount++
//		startUsers[userID] = update.Message.From.UserName
//		statsMutex.Unlock()
//
//		msg := tgbotapi.NewMessage(chatID, "👋 Assalomu alaykum!\nAnime olish uchun kod kiriting:")
//		bot.Send(msg)
//		return
//	}
//	if blockedUsers[userID] {
//		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Siz botdan bloklangansiz."))
//		return
//	}
//
//	// ------------------ KOD KIRITSA (FOYDALANUVCHI) ------------------
//	if userID != ADMIN_ID && text != "/start" && text != "/admin" {
//		// Majburiy obuna tekshiruvi (agar kanallar bo'lsa)
//		isMember, requiredChannel := checkMembership(bot, userID)
//		if !isMember {
//			handleMembershipCheck(bot, chatID, requiredChannel)
//			return
//		}
//
//		code := strings.ToLower(strings.TrimSpace(text))
//
//		// Qidiruv statistikasi
//		statsMutex.Lock()
//		searchStats[code]++
//		statsMutex.Unlock()
//
//		// Saqlangan kontentni o'qish
//		storageMutex.RLock()
//		items, ok := animeStorage[code]
//		storageMutex.RUnlock()
//
//		if !ok || len(items) == 0 {
//			bot.Send(tgbotapi.NewMessage(chatID, "🔍 Bunday kod bo‘yicha kontent topilmadi."))
//			return
//		}
//
//		// Nomni olish
//		infoMutex.RLock()
//		name, hasName := animeInfo[code]
//		infoMutex.RUnlock()
//		if !hasName {
//			name = "No-name"
//		}
//
//		// Birinchi xabar: topildi degan xabar
//		bot.Send(tgbotapi.NewMessage(chatID,
//			fmt.Sprintf("🔍 %d ta qism topildi. Yuborish boshlandi...", len(items))))
//
//		// Har bir itemni turiga qarab yuborish
//		for i, it := range items {
//			caption := fmt.Sprintf("%s\nQism: %d/%d", name, i+1, len(items))
//
//			switch it.Kind {
//			case "video":
//				videoMsg := tgbotapi.NewVideo(chatID, tgbotapi.FileID(it.FileID))
//				videoMsg.Caption = caption
//				bot.Send(videoMsg)
//			case "photo":
//				photoMsg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(it.FileID))
//				photoMsg.Caption = caption
//				bot.Send(photoMsg)
//			case "document":
//				docMsg := tgbotapi.NewDocument(chatID, tgbotapi.FileID(it.FileID))
//				docMsg.Caption = caption
//				bot.Send(docMsg)
//			case "text":
//				// Matn bo'lsa, bitta message: nom + qism + matn
//				full := fmt.Sprintf(`%s\nQism: %d/%d
//
//%s`, name, i+1, len(items), it.Text)
//				bot.Send(tgbotapi.NewMessage(chatID, full))
//			default:
//				// Noma'lum tur bo'lsa text sifatida yuborish
//				full := fmt.Sprintf("%s\nasosiy kanal - @Manga_uzbekcha26 \n Qism: %d/%d\n\n(noma'lum kontent)", name, i+1, len(items))
//				bot.Send(tgbotapi.NewMessage(chatID, full))
//			}
//
//			// Sekinroq yuborish uchun kichik tanaffus
//			time.Sleep(800 * time.Millisecond)
//		}
//
//		bot.Send(tgbotapi.NewMessage(chatID, "✅ Barcha qismlar yuborildi!"))
//		return
//	}
//
//	// ------------------ ADMIN TEXT HANDLER ------------------
//	if userID == ADMIN_ID {
//		handleAdminText(bot, update)
//	}
//}
//
//// ---------------- ADMIN PANEL TUGMALARI ----------------
//func adminMenu() tgbotapi.InlineKeyboardMarkup {
//	return tgbotapi.NewInlineKeyboardMarkup(
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("📊 Statistika", "show_stats"),
//		),
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("🖋 kinolar joylash", "upload_anime"),
//			tgbotapi.NewInlineKeyboardButtonData("🗑 kinolar o‘chirish", "delete_anime"),
//		),
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("➕ Kanal qo‘shish", "add_channel"),
//			tgbotapi.NewInlineKeyboardButtonData("🗑 Kanal o‘chirish", "remove_channel"),
//		),
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("🚫 Foydalanuvchini bloklash", "block_user"),
//			tgbotapi.NewInlineKeyboardButtonData("♻️ Blokdan chiqarish", "unblock_user"),
//		),
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("📵 Bloklanganlar ro‘yxati", "blocked_list"),
//		),
//	)
//}
//
//// ------------------ A'ZOLIKNI TEKSHIRISH ----------------
//func checkMembership(bot *tgbotapi.BotAPI, userID int64) (bool, string) {
//	if len(channels) == 0 {
//		return true, ""
//	}
//
//	for chatID, username := range channels {
//		member, err := bot.GetChatMember(tgbotapi.GetChatMemberConfig{
//			ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
//				ChatID: chatID,
//				UserID: userID,
//			},
//		})
//
//		if err != nil {
//			log.Printf("A'zolikni tekshirishda xato yuz berdi %s: %v", username, err)
//			return false, username
//		}
//
//		if member.Status != "member" && member.Status != "administrator" && member.Status != "creator" {
//			return false, username
//		}
//	}
//	return true, ""
//}
//
//// ------------------ OBUNA BO'LMAGANLARGA XABAR ----------------
//func handleMembershipCheck(bot *tgbotapi.BotAPI, chatID int64, requiredChannel string) {
//	keyboard := tgbotapi.NewInlineKeyboardMarkup(
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonURL("➕ Obuna bo‘lish", "https://t.me/"+requiredChannel),
//		),
//		tgbotapi.NewInlineKeyboardRow(
//			tgbotapi.NewInlineKeyboardButtonData("✅ Tekshirish ", "check_membership"),
//		),
//	)
//
//	msg := tgbotapi.NewMessage(chatID,
//		"⚠️ Davom etish uchun avval bizning kanalimizga obuna bo‘ling")
//	msg.ReplyMarkup = &keyboard
//	bot.Send(msg)
//}
//
//// ------------------ CALLBACK HANDLER ----------------
//func handleCallback(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
//	userID := update.CallbackQuery.From.ID
//	chatID := update.CallbackQuery.Message.Chat.ID
//	data := update.CallbackQuery.Data
//	messageID := update.CallbackQuery.Message.MessageID
//
//	bot.Send(tgbotapi.NewCallback(update.CallbackQuery.ID, "Tekshirilmoqda..."))
//
//	switch data {
//	case "delete_anime":
//		adminState[userID] = "delete_anime_code"
//		bot.Send(tgbotapi.NewMessage(chatID, "🆔 O‘chirmoqchi bo‘lgan kinolar kodini kiriting:"))
//
//	case "block_user":
//		adminState[userID] = "block_user"
//		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Bloklamoqchi bo‘lgan foydalanuvchi ID sini kiriting:"))
//
//	case "unblock_user":
//		adminState[userID] = "unblock_user"
//		bot.Send(tgbotapi.NewMessage(chatID, "♻️ Blokdan chiqariladigan user ID ni kiriting:"))
//
//	case "blocked_list":
//		if len(blockedUsers) == 0 {
//			bot.Send(tgbotapi.NewMessage(chatID, "📵 Bloklanganlar yo‘q."))
//			return
//		}
//
//		txt := "📵 *Bloklangan foydalanuvchilar:*\n\n"
//		for id := range blockedUsers {
//			txt += fmt.Sprintf("🚫 %d\n", id)
//		}
//
//		msg := tgbotapi.NewMessage(chatID, txt)
//		msg.ParseMode = "Markdown"
//		bot.Send(msg)
//
//	case "show_stats":
//		storageMutex.RLock()
//		animeCount := len(animeStorage)
//		storageMutex.RUnlock()
//
//		statsMutex.Lock()
//		starts := startCount
//		topCode := ""
//		topCount := 0
//		for code, cnt := range searchStats {
//			if cnt > topCount {
//				topCode = code
//				topCount = cnt
//			}
//		}
//		statsMutex.Unlock()
//
//		if topCode == "" {
//			topCode = "Hali qidiruv bo‘lmagan"
//		}
//
//		text := fmt.Sprintf(
//			"📊 *Statistika*\n\n"+
//				"🔢 Saqlangan kinolar: *%d ta*\n"+
//				"👤 /start bosganlar: *%d kishi*\n"+
//				"🔍 Eng ko‘p qidirilgan kod: *%s* (%d marta)\n",
//			animeCount, starts, topCode, topCount,
//		)
//
//		msg := tgbotapi.NewMessage(chatID, text)
//		msg.ParseMode = "Markdown"
//		bot.Send(msg)
//
//	case "add_channel":
//		adminState[userID] = "add_channel_chatid"
//		bot.Send(tgbotapi.NewMessage(chatID, "🆔 Kanal chatID kiriting (Masalan: -1001234567890):"))
//
//	case "remove_channel":
//		adminState[userID] = "remove_channel"
//		bot.Send(tgbotapi.NewMessage(chatID, "O‘chirmoqchi bo‘lgan kanalning CHAT ID sini kiriting:"))
//
//	case "upload_anime":
//		// Boshlash: avval nom so'raladi
//		adminState[userID] = "anime_name"
//		bot.Send(tgbotapi.NewMessage(chatID, "📝 kinolar nomini kiriting :"))
//
//	case "check_membership":
//		isMember, requiredChannel := checkMembership(bot, userID)
//		if isMember {
//			editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "✅ A'zoligingiz tasdiqlandi. Endi kinolar kodini kiriting:")
//			bot.Send(editMsg)
//		} else {
//			keyboard := tgbotapi.NewInlineKeyboardMarkup(
//				tgbotapi.NewInlineKeyboardRow(
//					tgbotapi.NewInlineKeyboardButtonURL("➕ Obuna bo‘lish", "https://t.me/"+requiredChannel),
//				),
//				tgbotapi.NewInlineKeyboardRow(
//					tgbotapi.NewInlineKeyboardButtonData("✅ Tekshirish ", "check_membership"),
//				),
//			)
//
//			editMsg := tgbotapi.NewEditMessageText(chatID, messageID,
//				fmt.Sprintf("⚠️ Obuna tasdiqlanmadi. Avval kanalga obuna bo‘ling:\n"))
//			editMsg.ReplyMarkup = &keyboard
//			bot.Send(editMsg)
//		}
//	}
//}
//
//// ------------------ ADMIN TEXT HANDLER ----------------
//func handleAdminText(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
//	userID := update.Message.From.ID
//	chatID := update.Message.Chat.ID
//	text := update.Message.Text
//
//	switch adminState[userID] {
//	case "delete_anime_code":
//		code := strings.ToLower(strings.TrimSpace(text))
//
//		infoMutex.RLock()
//		_, exists := animeInfo[code]
//		infoMutex.RUnlock()
//
//		if !exists {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bu kod bo‘yicha kinolar topilmadi."))
//			delete(adminState, userID)
//			return
//		}
//
//		// 🔥 O‘chiramiz
//		infoMutex.Lock()
//		delete(animeInfo, code)
//		infoMutex.Unlock()
//
//		storageMutex.Lock()
//		delete(animeStorage, code)
//		storageMutex.Unlock()
//
//		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑 '%s' kodi bo‘yicha kinolar o‘chirildi!", strings.ToUpper(code))))
//		delete(adminState, userID)
//		return
//
//	case "block_user":
//		id, err := strconv.ParseInt(text, 10, 64)
//		if err != nil {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
//			return
//		}
//		blockedUsers[id] = true
//		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🚫 %d bloklandi!", id)))
//		delete(adminState, userID)
//
//	case "unblock_user":
//		id, err := strconv.ParseInt(text, 10, 64)
//		if err != nil {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato ID. Raqam kiriting."))
//			return
//		}
//		delete(blockedUsers, id)
//		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("♻️ %d blokdan chiqarildi!", id)))
//		delete(adminState, userID)
//
//	case "add_channel_chatid":
//		id, err := strconv.ParseInt(text, 10, 64)
//		if err != nil {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Noto‘g‘ri chatID. ChatID raqam bo'lishi kerak (masalan: -100...)."))
//			return
//		}
//		adminTempID[userID] = id
//		adminState[userID] = "add_channel_username"
//		bot.Send(tgbotapi.NewMessage(chatID, "🔗 Kanal username kiriting :"))
//
//	case "add_channel_username":
//		username := strings.TrimPrefix(text, "@")
//		chatIDnum := adminTempID[userID]
//		channels[chatIDnum] = username
//		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kanal qo‘shildi! \nChatID: %d \nUsername: @%s", chatIDnum, username)))
//		delete(adminState, userID)
//		delete(adminTempID, userID)
//
//	case "remove_channel":
//		id, err := strconv.ParseInt(text, 10, 64)
//		if err != nil {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Xato chatID. ChatID raqam bo'lishi kerak."))
//			return
//		}
//		if _, ok := channels[id]; ok {
//			delete(channels, id)
//			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Kanal (%d) o‘chirildi!", id)))
//		} else {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Bunday chatID bilan kanal topilmadi!"))
//		}
//		delete(adminState, userID)
//
//	// ------------------ ADMIN: anime nomi so'rov ------------------
//	case "anime_name":
//		name := strings.TrimSpace(text)
//		if name == "" {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Nomi bo'sh bo'lishi mumkin emas. Iltimos nom kiriting:"))
//			return
//		}
//		animeNameTemp[userID] = name
//		adminState[userID] = "anime_code"
//		bot.Send(tgbotapi.NewMessage(chatID, "🆔 Endi kinolar kodi kiriting :"))
//
//	// ------------------ ADMIN: anime kodi so'rov ------------------
//	case "anime_code":
//		code := strings.ToLower(strings.TrimSpace(text))
//
//		// ❗ 1) Bo'sh kod tekshiruvi
//		if code == "" {
//			bot.Send(tgbotapi.NewMessage(chatID, "❌ Kod bo'sh bo'lishi mumkin emas. Iltimos kod kiriting:"))
//			return
//		}
//
//		// ❗ 2) Kod allaqachon mavjudligini tekshiramiz
//		infoMutex.RLock()
//		_, exists := animeInfo[code]
//		infoMutex.RUnlock()
//
//		if exists {
//			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Bu kod allaqachon mavjud! Boshqa kod kiriting:"))
//			return
//		}
//
//		// ❗ 3) Kodni saqlaymiz (code -> anime name)
//		infoMutex.Lock()
//		animeInfo[code] = animeNameTemp[userID]
//		infoMutex.Unlock()
//
//		// ❗ Bo'sh storage slot yaratamiz
//		storageMutex.Lock()
//		animeStorage[code] = nil
//		storageMutex.Unlock()
//
//		// ❗ Admin kiritgan kodni vaqtincha saqlaymiz
//		animeCodeTemp[userID] = code
//
//		// ❗ Admin endi kontent yuborishi kerak
//		adminState[userID] = "anime_videos"
//
//		bot.Send(tgbotapi.NewMessage(
//			chatID,
//			fmt.Sprintf("🎞 Endi '%s' uchun videolar/rasmlar/fayllar yoki matnlarni yuboring. Tugagach /ok deb yozing.",
//				animeNameTemp[userID]),
//		))
//		return
//
//	// ------------------ ADMIN: kontent qabul qilish ------------------
//	case "anime_videos":
//		code := animeCodeTemp[userID]
//
//		// Agar admin /TUGADI deb yuborsa yakunlaymiz
//		if strings.ToLower(text) == "/ok" {
//			storageMutex.RLock()
//			count := len(animeStorage[code])
//			storageMutex.RUnlock()
//
//			bot.Send(tgbotapi.NewMessage(chatID,
//				fmt.Sprintf("✅ '%s' uchun barcha kontent saqlandi! Jami: %d ta", animeNameTemp[userID], count)))
//
//			// Tozalash
//			delete(adminState, userID)
//			delete(animeCodeTemp, userID)
//			delete(animeNameTemp, userID)
//			return
//		}
//
//		storageMutex.Lock() // ⬅️ Hamma append va count shu yerda mutex ichida
//		defer storageMutex.Unlock()
//
//		if update.Message.Video != nil {
//			animeStorage[code] = append(animeStorage[code], ContentItem{
//				Kind:   "video",
//				FileID: update.Message.Video.FileID,
//			})
//			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🎬 Video qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
//			return
//		}
//
//		if update.Message.Photo != nil {
//			photo := update.Message.Photo[len(update.Message.Photo)-1].FileID
//			animeStorage[code] = append(animeStorage[code], ContentItem{
//				Kind:   "photo",
//				FileID: photo,
//			})
//			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🖼 Rasm qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
//			return
//		}
//
//		if update.Message.Document != nil {
//			animeStorage[code] = append(animeStorage[code], ContentItem{
//				Kind:   "document",
//				FileID: update.Message.Document.FileID,
//			})
//			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("📁 Fayl qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
//			return
//		}
//
//		if update.Message.Text != "" {
//			animeStorage[code] = append(animeStorage[code], ContentItem{
//				Kind: "text",
//				Text: update.Message.Text,
//			})
//			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✏️ Matn qabul qilindi. Jami: %d ta", len(animeStorage[code]))))
//			return
//		}
//
//		bot.Send(tgbotapi.NewMessage(chatID, "❌ Noma’lum format! Video, rasm, fayl yoki matn yuboring."))
//	}
//}
