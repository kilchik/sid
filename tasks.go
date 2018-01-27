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
