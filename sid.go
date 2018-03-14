package main

import "log"
import (
	"database/sql"
	"io/ioutil"
	"os"

	"flag"

	"fmt"

	"time"

	"encoding/csv"

	"os/exec"

	tgbotapi2 "github.com/go-telegram-bot-api/telegram-bot-api"
	_ "github.com/mattn/go-sqlite3"
)

// commands list:
//ipay - create new transaction
//iowe - find out how much you need to give back
//igive - give back a debt
//stat - display all balances

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

type group struct {
	id   int
	name string
}

type userExpense struct {
	title  string
	amount float64
	payer  string
	time   time.Time
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

	// Main loop of processing updates
	for update := range updatesChan {
		processUpdate(update, clients, api, conf.params.BotName, tasksChan)
	}
}

func username(u *tgbotapi2.User) string {
	if len(u.FirstName) != 0 {
		if len(u.LastName) != 0 {
			return u.FirstName + " " + u.LastName[:1] + "."
		} else {
			if len(u.UserName) != 0 {
				return u.FirstName + " (" + u.UserName + ")"
			} else {
				return u.FirstName
			}
		}
	} else if len(u.UserName) != 0 {
		return u.UserName
	}
	return ""
}

func newAbortableMsg(chatId int64, text string) tgbotapi2.MessageConfig {
	return tgbotapi2.NewMessage(chatId, text+" /abort")
}
func newAbortableEditMsg(chatId int64, replyTo int, text string) tgbotapi2.EditMessageTextConfig {
	return tgbotapi2.NewEditMessageText(chatId, replyTo, text+" /abort")
}

func isAbort(r reply) bool {
	return r.msg != nil && r.msg.Text == "/abort"
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

func createExpensesImage(user int64, users map[int64]string) (imgPath string, err error) {
	expenses, err := selectExpensesFromDB(user, users)
	if err != nil {
		err = fmt.Errorf("select all user expenses: %v", err)
		return
	}

	// Create csv file
	var total float64
	var records [][]string
	records = append(records, []string{"Title", "Amount", "Payer", "Date"})
	for _, e := range expenses {
		var record []string
		record = append(record, e.title)
		record = append(record, fmt.Sprintf("€%.2f", e.amount))
		record = append(record, e.payer)
		record = append(record, e.time.Format("02/01/2006 15:04:05"))

		total += e.amount
		records = append(records, record)
	}

	var summary []string
	summary = append(summary, "<b>Total</b>")
	summary = append(summary, fmt.Sprintf("€<b>%.2f</b>", total))
	summary = append(summary, "-")
	summary = append(summary, "-")

	records = append(records, summary)

	csvFilename := fmt.Sprintf("%s-%d.csv", users[user], int32(time.Now().Unix()))
	csvFile, err := os.Create(csvFilename)
	if err != nil {
		err = fmt.Errorf("open csv file: %v", err)
		return
	}

	csvWriter := csv.NewWriter(csvFile)
	for _, record := range records {
		if err = csvWriter.Write(record); err != nil {
			err = fmt.Errorf("error writing record to csv: %v", err)
			return
		}
	}
	csvWriter.Flush()
	csvFile.Close()
	if err = csvWriter.Error(); err != nil {
		return
	}

	// Call table renderer script to create the image
	var output []byte
	if output, err = exec.Command("./venv/bin/python", "table_renderer.py", csvFilename).CombinedOutput(); err != nil {
		err = fmt.Errorf("calling table renderer script: %v; output: %s", err, string(output))
		return
	}

	imgPath = csvFilename[:len(csvFilename)-3] + "png"
	if _, err = os.Stat(imgPath); os.IsNotExist(err) {
		err = fmt.Errorf("convert table into image: %v", err)
		return
	}

	return
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
