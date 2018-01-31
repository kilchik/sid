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

	"strings"

	tgbotapi2 "github.com/go-telegram-bot-api/telegram-bot-api"
	_ "github.com/mattn/go-sqlite3"
)

// commands list:
//ipay - create new transaction
//iowe - find out how much you need to give back
//igive - give back a debt

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
	api.Debug = true
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
						logE.Print("no channel with user though started")
					}
					go ipayHandler(&update, api, clientChan, allowedUsers, tasksChan)
				case "igive":
					clientChan, ok := clients[update.Message.From.ID]
					if !ok {
						logE.Print("no channel with user though started")
					}
					go igiveHandler(&update, api, clientChan, allowedUsers, tasksChan)
				case "iowe":
					go ioweHandler(&update, api)
				case "abort":
					clients[update.Message.From.ID] <- reply{nil, update.Message}
				default:
					if strings.HasPrefix(update.Message.Text[1:], "undo") {
						clientChan, ok := clients[update.Message.From.ID]
						if !ok {
							logE.Print("no channel with user though started")
						}
						go undoHandler(&update, api, clientChan, tasksChan)
					} else {
						logI.Printf("unknown command: %q", update.Message.Text[1:])
						handleNotAllowed(update, api)
					}
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

func undoHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, tasksChan chan<- task) {
	logPrefix := "handle undo: "

	chatId := update.Message.Chat.ID
	caller := update.Message.From.ID
	undoCommand := "undo"
	var trid int
	var err error
	if trid, err = strconv.Atoi(update.Message.Text[1+len(undoCommand):]); err != nil {
		msg := tgbotapi2.NewMessage(chatId, "Invalid transaction index.")
		bot.Send(msg)
		return
	}

	undoSucceeded := make(chan bool)
	go func(undoRes chan bool, trid int, ownerId int) {
		succeeded := <-undoRes
		msgText := fmt.Sprintf("Failed to undo transaction %d", trid)
		if succeeded {
			msgText = fmt.Sprintf("Transaction %d removed.", trid)
		}
		msg := tgbotapi2.NewMessage(chatId, msgText)
		msg.ParseMode = "markdown"
		bot.Send(msg)

		var debt float64
		if err := calcDebt(ownerId, &debt); err != nil {
			logE.Printf(logPrefix+"calculate debt: %v", err)
			return
		}
		msgText = debtMessage(debt)
		msg = tgbotapi2.NewMessage(chatId, msgText)
		bot.Send(msg)
	}(undoSucceeded, trid, caller)

	tasksChan <- &undoTask{
		trid:      trid,
		ownerId:   caller,
		succeeded: undoSucceeded,
	}
}

func startHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI) {
	logD.Printf("handle start command from %s", update.Message.From.UserName)

	chatId := update.Message.Chat.ID
	msgId := update.Message.MessageID
	msg := tgbotapi2.NewMessage(chatId, "I am ready.")
	msg.ReplyToMessageID = msgId
	bot.Send(msg)
}

func retrieveAmount(chatId int64, replyTo int, action string, bot *tgbotapi2.BotAPI, replyChan <-chan reply) (amount float64, replyMsgId int) {
	// Ask for price
	msg := newAbortableMsg(chatId, fmt.Sprintf("How much € did you %s?", action))
	msg.ReplyToMessageID = replyTo

	bot.Send(msg)

	// Parse price
	r := <-replyChan
	if isAbort(r) {
		bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
		amount = -1
		return
	}

	amount, err := strconv.ParseFloat(r.msg.Text, 64)
	log.Printf("parsed: %.2f", amount)
	if err != nil {
		logE.Printf("parse amount from msg %q: %v", r.msg.Text, err)
		amount = -1
		return
	}

	//alertFinalAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, fmt.Sprintf("You %s €"+amountStr, action))
	//alertFinalAmount.ShowAlert = false
	//bot.AnswerCallbackQuery(alertFinalAmount)
	replyMsgId = r.msg.MessageID
	return
}

func newAbortableMsg(chatId int64, text string) tgbotapi2.MessageConfig {
	return tgbotapi2.NewMessage(chatId, text+" /abort")
}

func isAbort(r reply) bool {
	return r.msg != nil && r.msg.Text == "/abort"
}

func ipayHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, allowedUsers map[int64]string, tasksChan chan<- task) {
	logPrefix := "ipay handler: "

	ownerId := update.Message.From.ID
	chatId := update.Message.Chat.ID
	var r reply

	// Ask for title
	msgTitleDemand := newAbortableMsg(chatId, "What did you pay for?")
	bot.Send(msgTitleDemand)

	//msg = tgbotapi2.NewMessage(chatId, "What did you pay for?")
	//msg.ReplyMarkup = tgbotapi.ReplyKeyboardRemove{true, false}
	//bot.Send(msg)

	// Parse title
	r = <-replyChan
	if isAbort(r) {
		bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
		return
	}
	title := r.msg.Text
	logD.Println("title: ", title)

	amount, rplMsgId := retrieveAmount(chatId, r.msg.MessageID, "pay", bot, replyChan)
	if amount <= 0 {
		logI.Printf(logPrefix+"entered amount: %.2f", amount)
		return
	}

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

	msgWho := newAbortableMsg(chatId, "Who did you pay for?")
	msgWho.ReplyToMessageID = rplMsgId
	msgWho.ReplyMarkup = composeUsersKb(map[string]bool{})
	sent, _ := bot.Send(msgWho)

	selected := make(map[string]bool)
	var transTime time.Time
	for r = range replyChan {
		if isAbort(r) {
			bot.Send(tgbotapi2.NewEditMessageText(chatId, sent.MessageID, "^C"))
			bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
			return
		}
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

		msgEditWho := tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Who else did you pay for?")
		bot.Send(msgEditWho)

		msgEditUsersKb := tgbotapi2.NewEditMessageReplyMarkup(chatId, r.cb.Message.MessageID, composeUsersKb(selected))
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

	title = fmt.Sprintf("€%.2f for %s with %s", amount, title, membersStr)
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
	go func(transIdx chan int64, title string, ownerId int) {
		trid := <-transIdx
		msgText := fmt.Sprintf("Failed to create transaction for %q", title)
		if trid != -1 {
			msgText = fmt.Sprintf("tr #%d: %q /undo%d", trid, title, trid)
		}
		msg := tgbotapi2.NewMessage(chatId, msgText)
		msg.ParseMode = "markdown"
		bot.Send(msg)

		var debt float64
		if err := calcDebt(ownerId, &debt); err != nil {
			logE.Printf(logPrefix+"calculate debt: %v", err)
			return
		}
		msgText = debtMessage(debt)
		msg = tgbotapi2.NewMessage(chatId, msgText)
		bot.Send(msg)
	}(transIdx, title, ownerId)

	tasksChan <- &payTask{
		title:    title,
		amount:   amount,
		ts:       transTime,
		owner:    ownerId,
		members:  getSelectedUsersIndices(),
		transIdx: transIdx,
	}
}

func igiveHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, allowedUsers map[int64]string, tasksChan chan<- task) {
	logPrefix := "igive handler: "

	srcId := update.Message.From.ID

	// Retrieve the amount
	chatId := update.Message.Chat.ID
	amount, rplMsgId := retrieveAmount(chatId, update.Message.MessageID, "give back", bot, replyChan)
	if amount <= 0 {
		logI.Printf(logPrefix+"entered amount: %.2f", amount)
		return
	}

	// Ask for dst
	composeUsersKb := func() tgbotapi2.InlineKeyboardMarkup {
		var userButtons [][]tgbotapi2.InlineKeyboardButton
		for id, name := range allowedUsers {
			if name == allowedUsers[int64(srcId)] {
				continue
			}
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(name, strconv.Itoa(int(id)))})
		}
		return tgbotapi2.NewInlineKeyboardMarkup(userButtons...)
	}

	msgWho := newAbortableMsg(chatId, "Who did you give money back?")
	msgWho.ReplyMarkup = composeUsersKb()
	msgWho.ReplyToMessageID = rplMsgId
	sent, _ := bot.Send(msgWho)

	//selected := make(map[string]bool)
	r := <-replyChan
	if isAbort(r) {
		bot.Send(tgbotapi2.NewEditMessageText(chatId, sent.MessageID, "^C"))
		bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
		return
	}

	selected, err := strconv.Atoi(r.cb.Data)
	if err != nil {
		logI.Printf(logPrefix+"selected: %d", selected)
		return
	}

	msgEditSummary := tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Okay, I got it.")
	bot.Send(msgEditSummary)

	selectedName, _ := allowedUsers[int64(selected)]
	msgSummary := tgbotapi2.NewMessage(chatId, fmt.Sprintf("You gave back €%.2f to %s", amount, selectedName))
	bot.Send(msgSummary)

	alertUpdateAmount := tgbotapi2.NewCallbackWithAlert(selectedName, selectedName)
	alertUpdateAmount.ShowAlert = false
	bot.AnswerCallbackQuery(alertUpdateAmount)

	succeeded := make(chan bool)
	go func(succeeded chan bool, ownerId int) {
		taskSucceeded := <-succeeded
		if !taskSucceeded {
			msgText := "Failed to register operation"
			msg := tgbotapi2.NewMessage(chatId, msgText)
			bot.Send(msg)
			return
		}

		var debt float64
		if err := calcDebt(ownerId, &debt); err != nil {
			logE.Printf(logPrefix+"calculate debt: %v", err)
			return
		}
		msgText := debtMessage(debt)
		msg := tgbotapi2.NewMessage(chatId, msgText)
		bot.Send(msg)
	}(succeeded, srcId)

	tasksChan <- &giveTask{
		amount:    amount,
		src:       srcId,
		dst:       selected,
		succeeded: succeeded,
	}
}

func debtMessage(debt float64) string {
	if debt == 0 {
		return "You owe nothing"
	} else if debt > 0 {
		return fmt.Sprintf("You owe €%.2f", debt)
	} else {
		return fmt.Sprintf("You are owed €%.2f", -debt)
	}
}

func ioweHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI) {
	logPrefix := "iowe handler: "

	requestorId := update.Message.From.ID
	chatId := update.Message.Chat.ID

	var debt float64
	if err := calcDebt(requestorId, &debt); err != nil {
		logE.Printf(logPrefix+"calculate debt: %v", err)
		return
	}
	msg := tgbotapi2.NewMessage(chatId, debtMessage(debt))
	bot.Send(msg)
}

func createTables() error {
	return nil
}

func calcDebt(uid int, debt *float64) error {
	logPrefix := "calculate debt: "
	var rows *sql.Rows
	rows, err := db.Query(`SELECT SUM(O.amount) FROM operations O
WHERE O.src=? AND O.dst!=?`, uid, uid)
	if err != nil {
		return fmt.Errorf(logPrefix+"select sum of payments: %v", err)
	}
	var plus float64
	for rows.Next() {
		err = rows.Scan(&plus)
		if err != nil {
			break
		}
	}
	rows.Close()
	logD.Printf(logPrefix+"+%.2f", plus)

	rows, err = db.Query(`SELECT SUM(O.amount) FROM operations O
WHERE O.dst=? AND O.src!=?`, uid, uid)
	if err != nil {
		return fmt.Errorf(logPrefix+"select sum of debts: %v", err)
	}
	var minus float64
	for rows.Next() {
		err = rows.Scan(&minus)
		if err != nil {
			break
		}
	}
	rows.Close()
	logD.Printf(logPrefix+"-%.2f", minus)

	*debt = minus - plus
	return nil
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
