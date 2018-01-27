package main

import "log"
import (
	"database/sql"
	"io/ioutil"
	"os"

	"flag"

	"strconv"

	"fmt"

	"time"

	tgbotapi2 "github.com/go-telegram-bot-api/telegram-bot-api"
	_ "github.com/mattn/go-sqlite3"
)

// commands list:
//ipay - create new transaction
//iowe - find out how much you need to give back

var (
	logD *log.Logger
	logI *log.Logger
	logW *log.Logger
	logE *log.Logger
)

var db *sql.DB

func initLoggers(debugMode bool) {
	debugHandle := ioutil.Discard
	if debugMode {
		debugHandle = os.Stdout
	}
	logD = log.New(debugHandle, "[D] ", log.Ldate|log.Ltime|log.Lshortfile)
	logI = log.New(os.Stdout, "[I] ", log.Ldate|log.Ltime|log.Lshortfile)
	logW = log.New(os.Stdout, "[W] ", log.Ldate|log.Ltime|log.Lshortfile)
	logE = log.New(os.Stderr, "[E] ", log.Ldate|log.Ltime)
}

type reply struct {
	cb  *tgbotapi2.CallbackQuery
	msg *tgbotapi2.Message
}

func processQueue(tasksChan <-chan task) {
	for {
		(<-tasksChan).Exec()
	}
}

func main() {
	debugMode := flag.Bool("debug", false, "debug logging")
	configPath := flag.String("conf", "/etc/sid/sid.conf", "config path")
	flag.Parse()

	initLoggers(*debugMode)

	// Parse config
	var conf configImpl
	if err := InitConfig(*configPath, &conf); err != nil {
		logE.Fatalf("init config: %v", err)
	}

	// Prepare db
	var err error
	_, err = os.Stat(conf.params.DBPath)
	dbExists := err == nil

	db, err = sql.Open("sqlite3", conf.params.DBPath)
	if err != nil {
		logE.Fatalf("create db connection: %v", err)
	}
	if db == nil {
		logE.Fatalf("failed to create db connection")
	}
	if !dbExists {
		logI.Println("db does not exist; creating tables")
		if err = createTables(); err != nil {
			log.Fatalf("create tables: %v", err)
		}
	} else {
		logI.Println("db already exists")
		if err = db.Ping(); err != nil {
			log.Fatalf("ping db: %v", err)
		}
	}

	// Set up bot
	api, err := tgbotapi2.NewBotAPI(conf.params.Token)
	if err != nil {
		logE.Fatalf("create bot: %v", err)
	}
	//api.Debug = true
	logI.Printf("authorized on account %s", api.Self.UserName)

	u := tgbotapi2.NewUpdate(0)
	u.Timeout = 60

	// Set up goroutine for tasks processing
	tasksChan := make(chan task)
	go processQueue(tasksChan)

	updatesChan, err := api.GetUpdatesChan(u)
	clients := make(map[int]chan reply)
	allowedReverse := make(map[int64]string)
	for k, v := range conf.params.AllowedUsers {
		allowedReverse[v] = k
	}

	// Main loop of processing updates
	for update := range updatesChan {
		processUpdate(update, clients, api, allowedReverse, tasksChan)
	}
}

func processUpdate(
	update tgbotapi2.Update, clients map[int]chan reply, api *tgbotapi2.BotAPI, allowedUsers map[int64]string, tasksChan chan<- task,
) {
	logPrefix := "process update: "
	if update.CallbackQuery != nil {
		// Got new callback
		logD.Printf(logPrefix+"callback from user %d", update.CallbackQuery.From.ID)
		clients[update.CallbackQuery.From.ID] <- reply{update.CallbackQuery, nil}
	} else if update.Message != nil {
		// Got new message
		logD.Printf(logPrefix+"message from user %d", update.Message.From.ID)
		if _, ok := allowedUsers[int64(update.Message.From.ID)]; !ok {
			handleNotAllowed(update, api)
			return
		}

		if len(update.Message.Text) > 0 {
			if update.Message.Text[0] == '/' {
				// Got new command
				switch update.Message.Text[1:] {
				case "start":
					if _, userChanExists := clients[update.Message.From.ID]; !userChanExists {
						logD.Printf("add channel with user %d", update.Message.From.ID)
						clientChan := make(chan reply)
						clients[update.Message.From.ID] = clientChan
					}
					go startHandler(&update, api)
				case "ipay":
					clientChan, ok := clients[update.Message.From.ID]
					if !ok {
						logE.Fatalf("no channel with user though started")
					}
					go ipayHandler(&update, api, clientChan, allowedUsers, tasksChan)
				case "iowe":
					logI.Println("handle /iowe")
				default:
					logI.Printf("unknown command: %q", update.Message.Text[1:])
					handleNotAllowed(update, api)
				}
			} else {
				// Got new text message
				logD.Printf("got new message from %d", update.Message.From.ID)
				clients[update.Message.From.ID] <- reply{nil, update.Message}
			}
		} else {
			logD.Println("no text in message; skipping")
		}
	} else {
		log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)
	}
}

func handleNotAllowed(update tgbotapi2.Update, bot *tgbotapi2.BotAPI) {
	logD.Printf("handle not allowed from %s", update.Message.From.UserName)

	chatId := update.Message.Chat.ID
	msgId := update.Message.MessageID
	msg := tgbotapi2.NewMessage(chatId, "Fuck off.")
	msg.ReplyToMessageID = msgId
	bot.Send(msg)
}

func startHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI) {
	logD.Printf("handle start command from %s", update.Message.From.UserName)

	chatId := update.Message.Chat.ID
	msgId := update.Message.MessageID
	msg := tgbotapi2.NewMessage(chatId, "I am ready.")
	msg.ReplyToMessageID = msgId
	bot.Send(msg)
}

func ipayHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, allowedUsers map[int64]string, tasksChan chan<- task) {
	logPrefix := "ipay handler: "

	ownerId := update.Message.From.ID
	chatId := update.Message.Chat.ID
	var r reply

	// Ask for title
	msgTitleDemand := tgbotapi2.NewMessage(chatId, "What did you pay for?")
	bot.Send(msgTitleDemand)

	//msg = tgbotapi2.NewMessage(chatId, "What did you pay for?")
	//msg.ReplyMarkup = tgbotapi.ReplyKeyboardRemove{true, false}
	//bot.Send(msg)

	// Parse title
	r = <-replyChan
	title := r.msg.Text
	logD.Println("title: ", title)

	// Ask for price
	digitsKeyboard := tgbotapi2.NewInlineKeyboardMarkup(
		tgbotapi2.NewInlineKeyboardRow(
			tgbotapi2.NewInlineKeyboardButtonData("1", "1"),
			tgbotapi2.NewInlineKeyboardButtonData("2", "2"),
			tgbotapi2.NewInlineKeyboardButtonData("3", "3"),
		),
		tgbotapi2.NewInlineKeyboardRow(
			tgbotapi2.NewInlineKeyboardButtonData("4", "4"),
			tgbotapi2.NewInlineKeyboardButtonData("5", "5"),
			tgbotapi2.NewInlineKeyboardButtonData("6", "6"),
		),
		tgbotapi2.NewInlineKeyboardRow(
			tgbotapi2.NewInlineKeyboardButtonData("7", "7"),
			tgbotapi2.NewInlineKeyboardButtonData("8", "8"),
			tgbotapi2.NewInlineKeyboardButtonData("9", "9"),
		),
		tgbotapi2.NewInlineKeyboardRow(
			tgbotapi2.NewInlineKeyboardButtonData("⌫", "⌫"),
			tgbotapi2.NewInlineKeyboardButtonData("0", "0"),
			tgbotapi2.NewInlineKeyboardButtonData("⏎", "⏎"),
		),
	)

	msg := tgbotapi2.NewMessage(chatId, "How much € did you pay?")
	msg.ReplyMarkup = digitsKeyboard
	msg.ReplyToMessageID = r.msg.MessageID

	bot.Send(msg)

	// Parse price
	amountStr := ""

CB:
	for r = range replyChan {
		switch r.cb.Data {
		case "⏎":
			break CB
		case "⌫":
			amountStr = amountStr[:len(amountStr)-1]
		default:
			amountStr = amountStr + r.cb.Data
		}
		logD.Println("amount: ", amountStr)
		alertUpdateAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, amountStr)
		alertUpdateAmount.ShowAlert = false
		bot.AnswerCallbackQuery(alertUpdateAmount)
	}
	amount, err := strconv.Atoi(amountStr)
	if err != nil {
		log.Fatalf(logPrefix+"convert amount string to integer: %v", err)
	}
	alertFinalAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, "You paid €"+amountStr)
	alertFinalAmount.ShowAlert = false
	bot.AnswerCallbackQuery(alertFinalAmount)

	// Ask for members
	composeUsersKb := func(except map[string]bool) tgbotapi2.InlineKeyboardMarkup {
		var userButtons [][]tgbotapi2.InlineKeyboardButton
		for _, name := range allowedUsers {
			if except[name] {
				continue
			}
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(name, name)})
		}
		if len(userButtons) > 0 {
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData("⏎", "⏎")})
		}
		return tgbotapi2.NewInlineKeyboardMarkup(userButtons...)
	}

	msgEditWho := tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Who did you pay for?")
	bot.Send(msgEditWho)

	msgEditUsersKb := tgbotapi2.NewEditMessageReplyMarkup(chatId, r.cb.Message.MessageID, composeUsersKb(map[string]bool{}))
	bot.Send(msgEditUsersKb)

	selected := make(map[string]bool)
	var transTime time.Time
	for r = range replyChan {
		if r.cb.Data == "⏎" {
			log.Println(r.cb.Message.Date)
			transTime = time.Unix(int64(r.cb.Message.Date), 0)
			log.Println(transTime)
			break
		}

		selected[r.cb.Data] = true
		alertUpdateAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, r.cb.Data)
		alertUpdateAmount.ShowAlert = false
		bot.AnswerCallbackQuery(alertUpdateAmount)

		newKb := composeUsersKb(selected)
		if len(newKb.InlineKeyboard) == 0 {
			log.Println(r.cb.Message.Date)
			transTime = time.Unix(int64(r.cb.Message.Date), 0)
			log.Println(transTime)
			break
		}

		msgEditWho = tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Who else did you pay for?")
		bot.Send(msgEditWho)

		msgEditUsersKb = tgbotapi2.NewEditMessageReplyMarkup(chatId, r.cb.Message.MessageID, composeUsersKb(selected))
		bot.Send(msgEditUsersKb)
	}

	// Send summary
	membersStr := ""
	memberIdx := 0
	for name, _ := range selected {
		if len(membersStr) > 0 {
			if memberIdx == len(selected)-1 {
				membersStr += " and "
			} else {
				membersStr += ", "
			}
		}
		membersStr += name
	}

	title = fmt.Sprintf("€%d for %s with %s", amount, title, membersStr)
	summary := "You paid " + title
	alertUpdateAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, summary)
	alertUpdateAmount.ShowAlert = false
	bot.AnswerCallbackQuery(alertUpdateAmount)

	msgEditSummary := tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Okay, I got it.")
	bot.Send(msgEditSummary)

	// Put new task into tasks channel
	getSelectedUsersIndices := func() (selectedUids []int64) {
		for uid, uname := range allowedUsers {
			if _, ok := selected[uname]; ok {
				selectedUids = append(selectedUids, uid)
			}
		}
		return
	}

	// Print transaction id on task executed
	transIdx := make(chan int64)
	go func(transIdx chan int64, title string) {
		trid := <-transIdx
		msgText := fmt.Sprintf("Failed to create transaction for %q", title)
		if trid != -1 {
			msgText = fmt.Sprintf("tr #%d: %q", trid, title)
		}
		msg := tgbotapi2.NewMessage(chatId, msgText)
		bot.Send(msg)
	}(transIdx, title)

	tasksChan <- &payTask{
		title:    title,
		amount:   amount,
		ts:       transTime,
		owner:    ownerId,
		members:  getSelectedUsersIndices(),
		transIdx: transIdx,
	}
}

func createTables() error {
	return nil
}

func calcDebt() {

}

//func askForCurrency() {
//	// Ask for currency
//	currencyKeyboard := tgbotapi2.NewInlineKeyboardMarkup(
//		tgbotapi2.NewInlineKeyboardRow(
//			tgbotapi2.NewInlineKeyboardButtonData("₽", "₽"),
//			tgbotapi2.NewInlineKeyboardButtonData("€", "€"),
//		),
//	)
//
//	chatId := update.Message.Chat.ID
//	msgId := update.Message.MessageID
//	msg := tgbotapi2.NewMessage(chatId, "Select currency")
//	msg.ReplyMarkup = currencyKeyboard
//	msg.ReplyToMessageID = msgId
//
//	bot.Send(msg)
//
//	// Parse currency
//	r := <-replyChan
//	currencySymbol := r.cb.Data
//
//}

//
//func GetUserInfo(bot *tgbotapi2.BotAPI, uid int) (userInfo tgbotapi2.User, err error) {
//	v := url.Values{}
//	v.Add("id", strconv.Itoa(uid))
//
//	resp, err := bot.MakeRequest("getFullUser", v)
//	if err != nil {
//		err = fmt.Errorf("request failed: %v", err)
//		return
//	}
//
//	json.Unmarshal(resp.Result, &userInfo)
//
//	return
//}
