package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const MaxDurationMinutes = 10
const DownloadFolder = "downloads"

var adminID int64
var allUsers = make(map[int64]bool)
var bannedUsers = make(map[int64]bool)
var awaitingBroadcast = make(map[int64]bool)
var awaitingBan = make(map[int64]bool)
var awaitingUnban = make(map[int64]bool)
var userLang = make(map[int64]string)
var titleCache = make(map[string]string)
var fileCache = make(map[string]string)
var durationCache = make(map[string]int)
var cacheMu sync.Mutex
var inlineProcessing = make(map[string]bool)

type SearchResult struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Duration int    `json:"duration"`
}

func main() {
	os.MkdirAll(DownloadFolder, 0755)

	// Get bot token from environment variable
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN environment variable is required")
	}

	// Get admin ID from environment variable
	adminIDStr := os.Getenv("ADMIN_ID")
	if adminIDStr != "" {
		adminID, _ = strconv.ParseInt(adminIDStr, 10, 64)
	}

	// Get cache channel ID from environment variable
	cacheChannelIDStr := os.Getenv("CACHE_CHANNEL_ID")
	var cacheChannelID int64
	if cacheChannelIDStr != "" {
		cacheChannelID, _ = strconv.ParseInt(cacheChannelIDStr, 10, 64)
	}

	// yt-dlp should be available in PATH after installation
	ytDlpPath := "yt-dlp"

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Bot started as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.InlineQuery != nil {
			handleInlineQuery(bot, update, ytDlpPath)
		} else if update.ChosenInlineResult != nil {
			handleChosenInlineResult(bot, update, ytDlpPath, cacheChannelID)
		} else if update.Message != nil {
			handleMessage(bot, update, ytDlpPath, cacheChannelID)
		} else if update.CallbackQuery != nil {
			handleCallbackQuery(bot, update, ytDlpPath, cacheChannelID)
		}
	}
}

func handleInlineQuery(bot *tgbotapi.BotAPI, update tgbotapi.Update, ytDlpPath string) {
	query := update.InlineQuery.Query
	if query == "" {
		return
	}

	results := searchYoutube(query, ytDlpPath)
	var articles []interface{}
	for i, r := range results {
		if r.Duration > MaxDurationMinutes*60 {
			continue
		}

		cacheMu.Lock()
		titleCache[r.ID] = r.Title
		durationCache[r.ID] = r.Duration
		cacheMu.Unlock()

		if fileID, exists := fileCache[r.ID]; exists {
			audioResult := tgbotapi.NewInlineQueryResultCachedAudio(r.ID, fileID)
			audioResult.Caption = "🎶 " + r.Title
			articles = append(articles, audioResult)
		} else {
			article := tgbotapi.NewInlineQueryResultArticle(r.ID, r.Title, r.Title)
			article.Description = "Нажмите для скачивания"

			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL("🎧 Скачать в боте", "https://t.me/"+bot.Self.UserName+"?start="+r.ID),
				),
			)

			article.ReplyMarkup = &keyboard
			article.InputMessageContent = tgbotapi.InputTextMessageContent{
				Text:      "🎵 *" + r.Title + "*\n\nНажмите кнопку ниже, чтобы скачать трек.",
				ParseMode: "Markdown",
			}
			articles = append(articles, article)
		}

		if i == 4 {
			break
		}
	}

	inlineConfig := tgbotapi.InlineConfig{
		InlineQueryID: update.InlineQuery.ID,
		IsPersonal:    true,
		CacheTime:     1,
		Results:       articles,
	}
	bot.Request(inlineConfig)
}

func handleChosenInlineResult(bot *tgbotapi.BotAPI, update tgbotapi.Update, ytDlpPath string, cacheChannelID int64) {
	resultID := update.ChosenInlineResult.ResultID
	if _, exists := fileCache[resultID]; !exists {
		go func(videoID string) {
			cacheMu.Lock()
			if inlineProcessing[videoID] {
				cacheMu.Unlock()
				return
			}
			inlineProcessing[videoID] = true
			cacheMu.Unlock()

			title := titleCache[videoID]
			downloadAndCacheAudio(bot, videoID, title, ytDlpPath, cacheChannelID)

			cacheMu.Lock()
			inlineProcessing[videoID] = false
			cacheMu.Unlock()
		}(resultID)
	}
}

func handleMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update, ytDlpPath string, cacheChannelID int64) {
	chatID := update.Message.Chat.ID
	allUsers[chatID] = true

	if bannedUsers[chatID] && chatID != adminID {
		bot.Send(tgbotapi.NewMessage(chatID, "🚫 Вы заблокированы."))
		return
	}

	if chatID == adminID {
		if awaitingBroadcast[chatID] {
			awaitingBroadcast[chatID] = false
			msg := update.Message.Text
			for uid := range allUsers {
				if uid != adminID {
					bot.Send(tgbotapi.NewMessage(uid, "📣 Сообщение от админа:\n\n"+msg))
				}
			}
			bot.Send(tgbotapi.NewMessage(chatID, "✅ Рассылка отправлена."))
			return
		}

		if awaitingBan[chatID] {
			awaitingBan[chatID] = false
			userID, err := strconv.ParseInt(update.Message.Text, 10, 64)
			if err == nil {
				bannedUsers[userID] = true
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Пользователь %d заблокирован.", userID)))
			} else {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный формат ID пользователя."))
			}
			return
		}

		if awaitingUnban[chatID] {
			awaitingUnban[chatID] = false
			userID, err := strconv.ParseInt(update.Message.Text, 10, 64)
			if err == nil {
				delete(bannedUsers, userID)
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Пользователь %d разблокирован.", userID)))
			} else {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный формат ID пользователя."))
			}
			return
		}
	}

	if update.Message.IsCommand() {
		cmd := update.Message.Command()
		args := update.Message.CommandArguments()

		switch cmd {
		case "start":
			if args != "" {
				videoID := args
				cacheMu.Lock()
				title, exists := titleCache[videoID]
				cacheMu.Unlock()

				if !exists {
					title = "Трек"
				}

				go handleDownload(bot, chatID, videoID, title, ytDlpPath, cacheChannelID)
				return
			}

			langButtons := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🇦🇲 Հայերեն", "lang_hy"),
					tgbotapi.NewInlineKeyboardButtonData("🇷🇺 Русский", "lang_ru"),
					tgbotapi.NewInlineKeyboardButtonData("🇺🇸 English", "lang_en"),
				),
			)
			msg := tgbotapi.NewMessage(chatID, "🌍 Select language / Выберите язык / Ընտրեք լեզուն")
			msg.ReplyMarkup = langButtons
			bot.Send(msg)
		case "help":
			sendHelpMessage(bot, chatID)
		case "admin":
			if chatID == adminID {
				sendAdminPanel(bot, chatID)
			}
		}
		return
	}

	if userLang[chatID] == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "🌐 Please select language using /start"))
		return
	}

	go handleSearch(bot, chatID, update.Message.Text, ytDlpPath, cacheChannelID)
}

func handleCallbackQuery(bot *tgbotapi.BotAPI, update tgbotapi.Update, ytDlpPath string, cacheChannelID int64) {
	chatID := update.CallbackQuery.Message.Chat.ID
	data := update.CallbackQuery.Data

	callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
	bot.Request(callback)

	if strings.HasPrefix(data, "dl_") {
		videoID := strings.TrimPrefix(data, "dl_")
		cacheMu.Lock()
		title := titleCache[videoID]
		cacheMu.Unlock()
		go handleDownload(bot, chatID, videoID, title, ytDlpPath, cacheChannelID)
		return
	}

	if chatID == adminID {
		if data == "admin_broadcast" {
			awaitingBroadcast[chatID] = true
			bot.Send(tgbotapi.NewMessage(chatID, "✉️ Enter broadcast message:"))
			return
		} else if data == "admin_stats" {
			stats := fmt.Sprintf("👥 Users: %d\n🚫 Banned: %d\n🎵 Cached songs: %d", len(allUsers), len(bannedUsers), len(fileCache))
			bot.Send(tgbotapi.NewMessage(chatID, stats))
			return
		} else if data == "admin_ban" {
			awaitingBan[chatID] = true
			bot.Send(tgbotapi.NewMessage(chatID, "👤 Enter user ID to ban:"))
			return
		} else if data == "admin_unban" {
			awaitingUnban[chatID] = true
			bot.Send(tgbotapi.NewMessage(chatID, "👤 Enter user ID to unban:"))
			return
		} else if data == "admin_clearcache" {
			cacheMu.Lock()
			fileCache = make(map[string]string)
			cacheMu.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "🧹 Cache cleared"))
			return
		}
	}

	if data == "lang_hy" {
		userLang[chatID] = "hy"
		bot.Send(tgbotapi.NewMessage(chatID, "👋 Բարի գալուստ!\n🎵 Ես կարող եմ գտնել երգեր YouTube-ում և ուղարկել MP3 ֆորմատով։"))
	} else if data == "lang_ru" {
		userLang[chatID] = "ru"
		bot.Send(tgbotapi.NewMessage(chatID, "👋 Добро пожаловать!\n🎵 Я могу найти песни на YouTube и отправить их в формате MP3."))
	} else if data == "lang_en" {
		userLang[chatID] = "en"
		bot.Send(tgbotapi.NewMessage(chatID, "👋 Welcome!\n🎵 I can find songs on YouTube and send them in MP3 format."))
	}
}

func sendHelpMessage(bot *tgbotapi.BotAPI, chatID int64) {
	lang := userLang[chatID]
	var helpText string

	switch lang {
	case "hy":
		helpText = "✨ ━━━━━━━━━━━━━━━━ ✨\n🔊 Ինչպես օգտվել Melody Bot-ից\n✨ ━━━━━━━━━━━━━━━━ ✨\n🎧 Երաժշտություն որոնելու համար:\n1️⃣ Ուղարկեք երգի/արտիստի անունը\n2️⃣ Ընտրեք առաջարկվող արդյունքներից\n3️⃣ Սպասեք MP3 ձևափոխման ավարտին\n4️⃣ Ստացեք երգը և վայելեք այն!\n✨ ━━━━━━━━━━━━━━━━ ✨"
	case "en":
		helpText = "✨ ━━━━━━━━━━━━━━━━ ✨\n🔊 How to use Melody Bot\n✨ ━━━━━━━━━━━━━━━━ ✨\n🎧 To find music:\n1️⃣ Send the song/artist name\n2️⃣ Choose from suggested results\n3️⃣ Wait for MP3 conversion\n4️⃣ Get your song and enjoy!\n✨ ━━━━━━━━━━━━━━━━ ✨"
	default:
		helpText = "✨ ━━━━━━━━━━━━━━━━ ✨\n🔊 Как пользоваться Melody Bot\n✨ ━━━━━━━━━━━━━━━━ ✨\n🎧 Для поиска музыки:\n1️⃣ Отправьте название песни/исполнителя\n2️⃣ Выберите из предложенных результатов\n3️⃣ Дождитесь конвертации в MP3\n4️⃣ Получите песню и наслаждайтесь!\n✨ ━━━━━━━━━━━━━━━━ ✨"
	}

	bot.Send(tgbotapi.NewMessage(chatID, helpText))
}

func sendAdminPanel(bot *tgbotapi.BotAPI, chatID int64) {
	menu := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Stats", "admin_stats"),
			tgbotapi.NewInlineKeyboardButtonData("📣 Broadcast", "admin_broadcast"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🚫 Ban", "admin_ban"),
			tgbotapi.NewInlineKeyboardButtonData("✅ Unban", "admin_unban"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🧹 Clear cache", "admin_clearcache"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "🛠 Admin Panel")
	msg.ReplyMarkup = menu
	bot.Send(msg)
}

func handleSearch(bot *tgbotapi.BotAPI, chatID int64, query string, ytDlpPath string, cacheChannelID int64) {
	lang := userLang[chatID]
	var searchText string

	switch lang {
	case "hy":
		searchText = fmt.Sprintf("🔍 Որոնում եմ: %s", query)
	case "en":
		searchText = fmt.Sprintf("🔍 Searching: %s", query)
	default:
		searchText = fmt.Sprintf("🔍 Ищу: %s", query)
	}

	msg := tgbotapi.NewMessage(chatID, searchText)
	sent, _ := bot.Send(msg)

	results := searchYoutube(query, ytDlpPath)
	if len(results) == 0 {
		var notFoundText string
		switch lang {
		case "hy":
			notFoundText = "❌ Ոչինչ չի գտնվել"
		case "en":
			notFoundText = "❌ Nothing found"
		default:
			notFoundText = "❌ Ничего не найдено"
		}
		bot.Send(tgbotapi.NewEditMessageText(chatID, sent.MessageID, notFoundText))
		return
	}

	var buttons [][]tgbotapi.InlineKeyboardButton
	for _, r := range results {
		if r.Duration > MaxDurationMinutes*60 {
			continue
		}

		cacheMu.Lock()
		titleCache[r.ID] = r.Title
		durationCache[r.ID] = r.Duration
		cacheMu.Unlock()

		durationMin := r.Duration / 60
		durationSec := r.Duration % 60
		title := fmt.Sprintf("%s (%d:%02d)", r.Title, durationMin, durationSec)

		btn := tgbotapi.NewInlineKeyboardButtonData(title, "dl_"+r.ID)
		buttons = append(buttons, tgbotapi.NewInlineKeyboardRow(btn))
	}

	if len(buttons) == 0 {
		var longText string
		switch lang {
		case "hy":
			longText = "❌ Բոլոր երգերը գերազանցում են առավելագույն տևողությունը (10 րոպե)"
		case "en":
			longText = "❌ All tracks exceed the maximum duration (10 minutes)"
		default:
			longText = "❌ Все треки превышают максимальную продолжительность (10 минут)"
		}
		bot.Send(tgbotapi.NewEditMessageText(chatID, sent.MessageID, longText))
		return
	}

	markup := tgbotapi.NewInlineKeyboardMarkup(buttons...)

	var selectText string
	switch lang {
	case "hy":
		selectText = "🎶 Ընտրեք երգը:"
	case "en":
		selectText = "🎶 Select a track:"
	default:
		selectText = "🎶 Выберите трек:"
	}

	edit := tgbotapi.NewEditMessageText(chatID, sent.MessageID, selectText)
	edit.ReplyMarkup = &markup
	bot.Send(edit)
}

func handleDownload(bot *tgbotapi.BotAPI, chatID int64, videoID, title string, ytDlpPath string, cacheChannelID int64) {
	cacheMu.Lock()
	duration, hasDuration := durationCache[videoID]
	cacheMu.Unlock()

	if hasDuration && duration > MaxDurationMinutes*60 {
		lang := userLang[chatID]
		var tooLongText string

		switch lang {
		case "hy":
			tooLongText = fmt.Sprintf("❌ Երգը շատ երկար է (%.2f րոպե). Առավելագույն տևողությունը 10 րոպե է:", float64(duration)/60)
		case "en":
			tooLongText = fmt.Sprintf("❌ Track is too long (%.2f min). Maximum duration is 10 minutes:", float64(duration)/60)
		default:
			tooLongText = fmt.Sprintf("❌ Трек слишком длинный (%.2f мин). Максимальная продолжительность - 10 минут:", float64(duration)/60)
		}

		bot.Send(tgbotapi.NewMessage(chatID, tooLongText))
		return
	}

	if fileID, ok := fileCache[videoID]; ok {
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(fileID))
		audio.Caption = "🎶 " + title
		audio.Title = title
		bot.Send(audio)
		return
	}

	lang := userLang[chatID]
	var downloadText string

	switch lang {
	case "hy":
		downloadText = "🎧 Ներբեռնում եմ: " + title
	case "en":
		downloadText = "🎧 Downloading: " + title
	default:
		downloadText = "🎧 Скачиваю: " + title
	}

	msg := tgbotapi.NewMessage(chatID, downloadText)
	statusMsg, _ := bot.Send(msg)

	resultFileID := downloadAndCacheAudio(bot, videoID, title, ytDlpPath, cacheChannelID)

	if resultFileID != "" {
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(resultFileID))
		audio.Caption = "🎶 " + title
		audio.Title = title
		bot.Send(audio)
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID)
		bot.Request(deleteMsg)
	} else {
		var errorText string

		switch lang {
		case "hy":
			errorText = "❌ Ներբեռնման սխալ"
		case "en":
			errorText = "❌ Download error"
		default:
			errorText = "❌ Ошибка скачивания"
		}

		edit := tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, errorText)
		bot.Send(edit)
	}
}

func downloadAndCacheAudio(bot *tgbotapi.BotAPI, videoID, title string, ytDlpPath string, cacheChannelID int64) string {
	cacheMu.Lock()
	if fileID, ok := fileCache[videoID]; ok {
		cacheMu.Unlock()
		return fileID
	}
	cacheMu.Unlock()

	safeTitle := sanitizeFileName(title)
	output := fmt.Sprintf("%s/%s.%%(ext)s", DownloadFolder, safeTitle)

	infoCmd := exec.Command(ytDlpPath, "--print", "duration", "https://www.youtube.com/watch?v="+videoID)
	durationBytes, err := infoCmd.Output()
	if err == nil {
		durationStr := strings.TrimSpace(string(durationBytes))
		duration, err := strconv.Atoi(durationStr)
		if err == nil {
			cacheMu.Lock()
			durationCache[videoID] = duration
			cacheMu.Unlock()

			if duration > MaxDurationMinutes*60 {
				return ""
			}
		}
	}

	cmd := exec.Command(ytDlpPath, "-x", "--audio-format", "mp3", "-o", output, "https://www.youtube.com/watch?v="+videoID)
	err = cmd.Run()
	if err != nil {
		return ""
	}

	mp3 := fmt.Sprintf("%s/%s.mp3", DownloadFolder, safeTitle)
	if _, err := os.Stat(mp3); err == nil {
		audioToCache := tgbotapi.NewAudio(cacheChannelID, tgbotapi.FilePath(mp3))
		audioToCache.Caption = "🎶 " + title
		audioToCache.Title = title
		result, err := bot.Send(audioToCache)
		if err == nil {
			cacheMu.Lock()
			fileCache[videoID] = result.Audio.FileID
			cacheMu.Unlock()
			os.Remove(mp3)
			return result.Audio.FileID
		}
		os.Remove(mp3)
	}
	return ""
}

func searchYoutube(query string, ytDlpPath string) []SearchResult {
	cmd := exec.Command(ytDlpPath, "-j", "--flat-playlist", "ytsearch5:"+query)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var results []SearchResult
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		var r SearchResult
		json.Unmarshal([]byte(line), &r)
		results = append(results, r)
	}
	return results
}

func sanitizeFileName(name string) string {
	forbidden := []string{"/", "\\", ":", "*", "?", "'", "<", ">", "|", "\"", "."}
	for _, ch := range forbidden {
		name = strings.ReplaceAll(name, ch, "_")
	}
	if len(name) > 50 {
		name = name[:50]
	}
	return strings.TrimSpace(name)
}
