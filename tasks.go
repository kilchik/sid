package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

type task interface {
	Exec()
}

type payTask struct {
	title    string
	amount   int
	ts       time.Time
	owner    int
	members  []int64
	transIdx chan int64
}

func (pt *payTask) Exec() {
	log.Println(pt)
	logPrefix := fmt.Sprintf("exec pay task %q: ", pt.title)

	stmt, err := db.Prepare(`INSERT INTO transactions VALUES (NULL, ?, ?, ?);`)
	if err != nil {
		logE.Printf(logPrefix+"prepare insert new transaction query: %v", err)
		pt.transIdx <- -1
		return
	}

	var execRes sql.Result
	if execRes, err = stmt.Exec(pt.title, pt.ts, pt.owner); err != nil {
		logE.Printf(logPrefix+"exec insert new transaction query: ", err)
		pt.transIdx <- -1
		return
	}

	trid, err := execRes.LastInsertId()
	if err != nil {
		logE.Printf(logPrefix+"get last insert id: %v", err)
		pt.transIdx <- -1
		return
	}

	stmt, err = db.Prepare(`INSERT INTO operations VALUES (NULL, ?, ?, ?, ?);`)
	if err != nil {
		logE.Printf(logPrefix+"prepare insert operations query: %v", err)
		pt.transIdx <- -1
		// TODO: remove transaction
		return
	}

	for _, m := range pt.members {
		if execRes, err = stmt.Exec(pt.owner, m, pt.amount/len(pt.members), trid); err != nil {
			logE.Printf(logPrefix+"exec insert new transaction query: ", err)
			pt.transIdx <- -1
			// TODO: remove transaction
			return
		}
	}

	pt.transIdx <- trid
}

type giveTask struct {
	amount    int
	src       int
	dst       int
	succeeded chan bool
}

func (gt *giveTask) Exec() {
	log.Println(gt)
	logPrefix := "exec give task: "

	stmt, err := db.Prepare(`INSERT INTO operations VALUES (NULL, ?, ?, ?, NULL);`)
	if err != nil {
		logE.Printf(logPrefix+"prepare insert operations query: %v", err)
		gt.succeeded <- false
		return
	}

	if _, err := stmt.Exec(gt.src, gt.dst, gt.amount); err != nil {
		logE.Printf(logPrefix+"exec insert new transaction query: %v", err)
		gt.succeeded <- false
		return
	}

	gt.succeeded <- true
}

type undoTask struct {
	trid      int
	ownerId   int
	succeeded chan bool
}

func (ut *undoTask) Exec() {
	log.Println(ut)
	logPrefix := "exec undo task: "

	stmt, err := db.Prepare(`DELETE FROM	operations WHERE transaction_id=?;`)
	if err != nil {
		logE.Printf(logPrefix+"prepare delete operations query: %v", err)
		ut.succeeded <- false
		return
	}

	if _, err := stmt.Exec(ut.trid); err != nil {
		logE.Printf(logPrefix+"exec delete operations query: %v", err)
		ut.succeeded <- false
		return
	}

	stmt, err = db.Prepare(`DELETE FROM	transactions WHERE id=? AND owner_id=?;`)
	if err != nil {
		logE.Printf(logPrefix+"prepare delete transaction query: %v", err)
		ut.succeeded <- false
		return
	}

	if _, err := stmt.Exec(ut.trid, ut.ownerId); err != nil {
		logE.Printf(logPrefix+"exec delete transaction query: %v", err)
		ut.succeeded <- false
		return
	}

	ut.succeeded <- true
}
