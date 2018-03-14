package main

import (
	"fmt"
	"log"
	"time"

	"sort"
	"strconv"
	"strings"

	tgbotapi2 "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/satori/go.uuid"
)

func startHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, botName string, replyChan <-chan reply, tasksChan chan<- task) {
	callerId := update.Message.From.ID
	logPrefix := fmt.Sprintf("handle start from %d: ", callerId)
	callerName := username(update.Message.From)
	if len(callerName) == 0 {
		logE.Printf(logPrefix+"cannot parse callerId name: %d", callerId)
		return
	}

	// Check if user already belongs to some group
	g, err := getUserGroup(callerId)
	if err != nil {
		logE.Printf(logPrefix+"get user group: %v", err)
		return
	}
	chatId := update.Message.Chat.ID
	originalMsg := update.Message.MessageID
	if g != nil {
		msg := tgbotapi2.NewMessage(chatId, fmt.Sprintf("You already belong to group %q", g.name))
		msg.ReplyToMessageID = originalMsg
		bot.Send(msg)
		return
	}

	// Ask user if he would like to join existing or create new one
	const (
		choiceCreateGroup = "Create new group"
		choiceJoinGroup   = "Join group"
	)
	kb := tgbotapi2.NewInlineKeyboardMarkup(
		[]tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(choiceCreateGroup, choiceCreateGroup)},
		[]tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(choiceJoinGroup, choiceJoinGroup)},
	)

S:
	msg := tgbotapi2.NewMessage(chatId, "Would you like to join existing group or create a new one?")
	msg.ReplyToMessageID = originalMsg
	msg.ReplyMarkup = kb
	sent, _ := bot.Send(msg)

	r := <-replyChan
	for ; r.cb == nil; r = <-replyChan {
	}

	switch r.cb.Data {
	case choiceJoinGroup:
		bot.Send(newAbortableEditMsg(chatId, sent.MessageID,
			"Ask your group leader to send you invitation message then forward it to me."))
		for r := range replyChan {
			if isAbort(r) {
				bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
				goto S
			}
			invite := parseInviteCode(r.msg.Text)
			if len(invite) != 0 {
				errChan := make(chan error)
				tasksChan <- &joinGroupTask{callerId, callerName, invite, errChan}
				err = <-errChan
				if err != nil {
					logE.Printf(logPrefix+"execute join-group task: %v", err)
					return
				}
				bot.Send(tgbotapi2.NewMessage(chatId, "You successfully joined group!"))
			} else {
				bot.Send(tgbotapi2.NewMessage(chatId, "Wrong message."))
			}
		}
	case choiceCreateGroup:
		bot.Send(newAbortableEditMsg(chatId, sent.MessageID, "Enter group name."))
		r = <-replyChan
		if isAbort(r) {
			bot.Send(tgbotapi2.NewMessage(chatId, "Aborted."))
			goto S
		}
		groupName := r.msg.Text
		errChan := make(chan error)
		invite, err := uuid.NewV4()
		if err != nil {
			logE.Printf(logPrefix+"generate uuid for invite: %v", err)
			return
		}
		tasksChan <- &createGroupTask{callerId, callerName, groupName, time.Now(), invite.String(), errChan}
		err = <-errChan
		if err != nil {
			logE.Printf("execute create-group task: %v", err)
			return
		}
		bot.Send(tgbotapi2.NewMessage(chatId, "Forward the message below to contacts you wish to invite to your group:"))
		invitationMsg := tgbotapi2.NewMessage(chatId,
			fmt.Sprintf("*This message is your invitation to %s's group %q (MBI-%s). Just forward it to* @%s.", callerName, groupName, invite.String(), botName))
		invitationMsg.ParseMode = "markdown"
		bot.Send(invitationMsg)
	}
}

func resetHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, tasksChan chan<- task) {
	// TODO: check that caller is group leader
	//if update.Message.From.ID != int(leaderUid) {
	//	return
	//}

	resetSucceeded := make(chan bool)
	go func(result chan bool) {
		succeeded := <-result
		msgText := "Failed to reset."
		if succeeded {
			msgText = "Done."
		}
		msg := tgbotapi2.NewMessage(update.Message.Chat.ID, msgText)
		msg.ParseMode = "markdown"
		bot.Send(msg)
	}(resetSucceeded)

	tasksChan <- &resetTask{
		succeeded: resetSucceeded,
	}
}

func ipayHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, tasksChan chan<- task) {
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
		// TODO: send smth
		return
	}

	// Select users with similar group id from db
	groupMembers, err := selectGroupMembers(ownerId)
	if err != nil {
		logE.Printf(logPrefix+"select group members: %v", err)
		// TODO: send smth
		return
	}

	// Ask for members
	// TODO: handle similar names
	composeUsersKb := func(except map[int64]bool) tgbotapi2.InlineKeyboardMarkup {
		var userButtons [][]tgbotapi2.InlineKeyboardButton
		for uid, name := range groupMembers {
			if except != nil && except[uid] {
				continue
			}
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(name, strconv.Itoa(int(uid)))})
		}
		if len(userButtons) > 0 {
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData("⏎", "⏎")})
		}
		return tgbotapi2.NewInlineKeyboardMarkup(userButtons...)
	}

	msgWho := newAbortableMsg(chatId, "Who did you pay for?")
	msgWho.ReplyToMessageID = rplMsgId
	msgWho.ReplyMarkup = composeUsersKb(nil)
	sent, _ := bot.Send(msgWho)

	selected := make(map[int64]bool)
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

		uid, _ := strconv.Atoi(r.cb.Data)
		selected[int64(uid)] = true
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
	for uid, _ := range selected {
		if len(membersStr) > 0 {
			if memberIdx == len(selected)-1 {
				membersStr += " and "
			} else {
				membersStr += ", "
			}
		}
		membersStr += groupMembers[uid]
	}

	title = fmt.Sprintf("€%.2f for %s (%s)", amount, title, membersStr)
	summary := "You paid " + title
	alertUpdateAmount := tgbotapi2.NewCallbackWithAlert(r.cb.ID, summary)
	alertUpdateAmount.ShowAlert = false
	bot.AnswerCallbackQuery(alertUpdateAmount)

	msgEditSummary := tgbotapi2.NewEditMessageText(chatId, r.cb.Message.MessageID, "Okay, I got it.")
	bot.Send(msgEditSummary)

	// Print transaction id on task executed
	transIdx := make(chan int64)
	go func(transIdx chan int64, title string, ownerId int) {
		trid := <-transIdx
		msgText := fmt.Sprintf("Failed to create transaction for %q", title)
		if trid != -1 {
			msgText = fmt.Sprintf("*tr #%d: %q* /undo%d", trid, title, trid)
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

	// Put new task into tasks channel
	tasksChan <- &payTask{
		title:    title,
		amount:   amount,
		ts:       transTime,
		owner:    ownerId,
		members:  selected,
		transIdx: transIdx,
	}
}

func igiveHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI, replyChan <-chan reply, tasksChan chan<- task) {
	logPrefix := "igive handler: "

	srcId := update.Message.From.ID

	// Retrieve the amount
	chatId := update.Message.Chat.ID
	amount, rplMsgId := retrieveAmount(chatId, update.Message.MessageID, "give back", bot, replyChan)
	if amount <= 0 {
		logI.Printf(logPrefix+"entered amount: %.2f", amount)
		return
	}

	// Select users with similar group id from db
	groupMembers, err := selectGroupMembers(srcId)
	if err != nil {
		logE.Printf(logPrefix+"select group members: %v", err)
		// TODO: send smth
		return
	}

	// Ask for dst
	composeUsersKb := func() tgbotapi2.InlineKeyboardMarkup {
		var userButtons [][]tgbotapi2.InlineKeyboardButton
		for uid, name := range groupMembers {
			if name == groupMembers[int64(srcId)] {
				continue
			}
			userButtons = append(userButtons, []tgbotapi2.InlineKeyboardButton{tgbotapi2.NewInlineKeyboardButtonData(name, strconv.Itoa(int(uid)))})
		}
		return tgbotapi2.NewInlineKeyboardMarkup(userButtons...)
	}

	msgWho := newAbortableMsg(chatId, "Who did you give money back?")
	msgWho.ReplyMarkup = composeUsersKb()
	msgWho.ReplyToMessageID = rplMsgId
	sent, _ := bot.Send(msgWho)

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

	selectedName, _ := groupMembers[int64(selected)]
	msgSummary := tgbotapi2.NewMessage(chatId, fmt.Sprintf("You gave back €%.2f to %s", amount, selectedName))
	bot.Send(msgSummary)

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

type debtor struct {
	name string
	debt float64
}

type maxDebtFirst []debtor

func (a maxDebtFirst) Len() int           { return len(a) }
func (a maxDebtFirst) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a maxDebtFirst) Less(i, j int) bool { return a[i].debt > a[j].debt }

func statHandler(update *tgbotapi2.Update, bot *tgbotapi2.BotAPI) {
	logPrefix := "stat handler: "
	callerId := update.Message.From.ID
	chatId := update.Message.Chat.ID

	// Select users with similar group id from db
	groupMembers, err := selectGroupMembers(callerId)
	if err != nil {
		logE.Printf(logPrefix+"select group members: %v", err)
		// TODO: send smth
		return
	}
	log.Println("groupMembers: %v", groupMembers)

	var debtors []debtor
	for uid, name := range groupMembers {
		var debt float64
		if err := calcDebt(int(uid), &debt); err != nil {
			logE.Printf(logPrefix+"calculate debt of %d: %v", uid, err)
			return
		}
		debtors = append(debtors, debtor{name, debt})
	}

	sort.Sort(maxDebtFirst(debtors))

	var debtsSummary string
	for _, debtor := range debtors {
		if len(debtsSummary) != 0 {
			debtsSummary += "\n"
		}
		debtsSummary += fmt.Sprintf("`%-8s \t%-7.2f`", debtor.name, debtor.debt)
	}

	msg := tgbotapi2.NewMessage(chatId, debtsSummary)
	msg.ParseMode = "markdown"
	bot.Send(msg)

	expensesImage, err := createExpensesImage(int64(update.Message.From.ID), groupMembers)
	if err != nil {
		logE.Printf(logPrefix+"create expenses image: %v", err)
		return
	}
	msgImg := tgbotapi2.NewPhotoUpload(chatId, expensesImage)
	bot.Send(msgImg)
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

func processUpdate(
	update tgbotapi2.Update, clients map[int]chan reply, api *tgbotapi2.BotAPI, botName string, tasksChan chan<- task,
) {
	logPrefix := "process update: "
	if update.CallbackQuery != nil {
		// Got new callback
		logD.Printf(logPrefix+"callback from user %d", update.CallbackQuery.From.ID)
		clients[update.CallbackQuery.From.ID] <- reply{update.CallbackQuery, nil}
	} else if update.Message != nil {
		// Got new message
		if len(update.Message.Text) > 0 {
			if update.Message.Text[0] == '/' {
				// Got new command
				switch update.Message.Text[1:] {
				case "start":
					logD.Printf("add channel with user %d", update.Message.From.ID)
					clientChan := make(chan reply, 10)
					clients[update.Message.From.ID] = clientChan

					go startHandler(&update, api, botName, clientChan, tasksChan)
				case "ipay":
					logD.Printf("add channel with user %d", update.Message.From.ID)
					clientChan := make(chan reply, 10)
					clients[update.Message.From.ID] = clientChan

					go ipayHandler(&update, api, clientChan, tasksChan)
				case "igive":
					logD.Printf("add channel with user %d", update.Message.From.ID)
					clientChan := make(chan reply, 10)
					clients[update.Message.From.ID] = clientChan

					go igiveHandler(&update, api, clientChan, tasksChan)
				case "iowe":
					go ioweHandler(&update, api)
				case "abort":
					clients[update.Message.From.ID] <- reply{nil, update.Message}
				case "reset":
					go resetHandler(&update, api, tasksChan)
				case "stat":
					go statHandler(&update, api)
				default:
					if strings.HasPrefix(update.Message.Text[1:], "undo") {
						logD.Printf("add channel with user %d", update.Message.From.ID)
						clientChan := make(chan reply, 10)
						clients[update.Message.From.ID] = clientChan

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

func createTables() error {
	return nil
}
