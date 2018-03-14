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

type createGroupTask struct {
	leaderId   int
	leaderName string
	groupName  string
	createTs   time.Time
	invite     string
	err        chan error
}

func (cgt *createGroupTask) Exec() {
	trans, err := db.Begin()
	if err != nil {
		cgt.err <- fmt.Errorf("create new sqlite-transaction: %v", err)
		return
	}

	stmt, err := db.Prepare(`INSERT INTO groups (id, name, create_ts, invite) VALUES (NULL, ?, ?, ?);`)
	if err != nil {
		cgt.err <- fmt.Errorf("prepare insert new group query: %v", err)
		return
	}

	var execRes sql.Result
	if execRes, err = stmt.Exec(cgt.groupName, cgt.createTs, cgt.invite); err != nil {
		cgt.err <- fmt.Errorf("exec insert new group query: %v", err)
		return
	}

	groupId, err := execRes.LastInsertId()
	if err != nil {
		cgt.err <- fmt.Errorf("get last insert id: %v", err)
		return
	}

	if err := upsertUserGroup(cgt.leaderId, cgt.leaderName, int(groupId)); err != nil {
		cgt.err <- fmt.Errorf("upsert user group: %v", err)
		return
	}

	if err := trans.Commit(); err != nil {
		cgt.err <- fmt.Errorf("commit sqlite-transaction: %v", err)
		return
	}

	cgt.err <- nil
}

// Updates user group if user exists or inserts new user otherwise
func upsertUserGroup(userId int, userName string, groupId int) error {
	stmt, err := db.Prepare(`UPDATE users SET name=?, group_id=? WHERE id=?;`)
	if err != nil {
		return fmt.Errorf("prepare update user group query: %v", err)
	}
	if _, err = stmt.Exec(userName, groupId, userId); err != nil {
		return fmt.Errorf("exec update user group query: %v", err)
	}
	stmt, err = db.Prepare(`INSERT OR IGNORE INTO users (id, name, group_id) VALUES (?, ?, ?);`)
	if err != nil {
		return fmt.Errorf("prepare insert user query: %v", err)
	}
	if _, err = stmt.Exec(userId, userName, groupId); err != nil {
		return fmt.Errorf("exec insert user query: %v", err)
	}
	return nil
}

type joinGroupTask struct {
	userId   int
	userName string
	invite   string
	err      chan error
}

func (jgt *joinGroupTask) Exec() {
	trans, err := db.Begin()
	if err != nil {
		jgt.err <- fmt.Errorf("create new sqlite-transaction: %v", err)
		return
	}
	rows, err := db.Query(`SELECT id FROM groups WHERE invite=?`, jgt.invite)
	if err != nil {
		jgt.err <- fmt.Errorf("select group with invite: %v", err)
		return
	}
	if !rows.Next() {
		jgt.err <- fmt.Errorf("no group with invite %q found", jgt.invite)
		return
	}
	var groupId int
	if err = rows.Scan(&groupId); err != nil {
		jgt.err <- fmt.Errorf("scan group id: %v", err)
		return
	}
	rows.Close()
	if err := upsertUserGroup(jgt.userId, jgt.userName, groupId); err != nil {
		jgt.err <- fmt.Errorf("upsert user group: %v", err)
		return
	}
	if err := trans.Commit(); err != nil {
		jgt.err <- fmt.Errorf("commit sqlite-transaction: %v", err)
		return
	}
	jgt.err <- nil
}

type leaveGroupTask struct {
	userId int
	err    chan error
}

func (lgt *leaveGroupTask) Exec() {
	trans, err := db.Begin()
	if err != nil {
		lgt.err <- fmt.Errorf("create new sqlite-transaction: %v", err)
		return
	}

	stmt, err := db.Prepare(`DELETE FROM users WHERE id=?;`)
	if err != nil {
		lgt.err <- fmt.Errorf("prepare delete user query: %v", err)
		return
	}
	log.Println("execing leavegroup")
	if _, err = stmt.Exec(lgt.userId); err != nil {
		lgt.err <- fmt.Errorf("exec delete user query: %v", err)
		return
	}

	if err := trans.Commit(); err != nil {
		lgt.err <- fmt.Errorf("commit sqlite-transaction: %v", err)
		return
	}
	lgt.err <- nil
}

type payTask struct {
	title    string
	amount   float64
	ts       time.Time
	owner    int
	members  map[int64]bool
	transIdx chan int64
}

func (pt *payTask) Exec() {
	log.Println(pt)
	logPrefix := fmt.Sprintf("exec pay task %q: ", pt.title)

	trans, err := db.Begin()
	if err != nil {
		logE.Printf(logPrefix+"create new sqlite-transaction: %v", err)
		pt.transIdx <- -1
		return
	}

	stmt, err := db.Prepare(`INSERT INTO transactions (id, title, ts, owner_id) VALUES (NULL, ?, ?, ?);`)
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

	stmt, err = db.Prepare(`INSERT INTO operations (id, src, dst, amount, transaction_id) VALUES (NULL, ?, ?, ?, ?);`)
	if err != nil {
		logE.Printf(logPrefix+"prepare insert operations query: %v", err)
		pt.transIdx <- -1
		return
	}

	for m, _ := range pt.members {
		if execRes, err = stmt.Exec(pt.owner, m, pt.amount/float64(len(pt.members)), trid); err != nil {
			logE.Printf(logPrefix+"exec insert new transaction query: ", err)
			pt.transIdx <- -1
			return
		}
	}

	if err := trans.Commit(); err != nil {
		logE.Printf(logPrefix+"commit sqlite-transaction: %v", err)
		pt.transIdx <- -1
		return
	}

	pt.transIdx <- trid
}

type giveTask struct {
	amount    float64
	src       int
	dst       int
	succeeded chan bool
}

func (gt *giveTask) Exec() {
	log.Println(gt)
	logPrefix := "exec give task: "

	trans, err := db.Begin()
	if err != nil {
		logE.Printf(logPrefix+"create new sqlite-transaction: %v", err)
		gt.succeeded <- false
		return
	}

	stmt, err := db.Prepare(`INSERT INTO operations (id, src, dst, amount, transaction_id) VALUES (NULL, ?, ?, ?, NULL);`)
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

	if err := trans.Commit(); err != nil {
		logE.Printf(logPrefix+"commit sqlite-transaction: %v", err)
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

	trans, err := db.Begin()
	if err != nil {
		logE.Printf(logPrefix+"create new sqlite-transaction: %v", err)
		ut.succeeded <- false
		return
	}

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

	if err := trans.Commit(); err != nil {
		logE.Printf(logPrefix+"commit sqlite-transaction: %v", err)
		ut.succeeded <- false
		return
	}

	ut.succeeded <- true
}

type resetTask struct {
	succeeded chan bool
}

func (rt *resetTask) Exec() {
	log.Println("reset task")
	logPrefix := "exec calc debt task: "

	trans, err := db.Begin()
	if err != nil {
		logE.Printf(logPrefix+"create new sqlite-transaction: %v", err)
		rt.succeeded <- false
		return
	}

	stmt, err := db.Prepare(`DELETE FROM operations;`)
	if err != nil {
		logE.Printf(logPrefix+"prepare truncate query: %v", err)
		rt.succeeded <- false
		return
	}

	if _, err := stmt.Exec(); err != nil {
		logE.Printf(logPrefix+"exec truncate query: %v", err)
		rt.succeeded <- false
		return
	}

	stmt, err = db.Prepare(`DELETE FROM transactions;`)
	if err != nil {
		logE.Printf(logPrefix+"prepare truncate query: %v", err)
		rt.succeeded <- false
		return
	}

	if _, err := stmt.Exec(); err != nil {
		logE.Printf(logPrefix+"exec truncate query: %v", err)
		rt.succeeded <- false
		return
	}

	if err := trans.Commit(); err != nil {
		logE.Printf(logPrefix+"commit sqlite-transaction: %v", err)
		rt.succeeded <- false
		return
	}

	rt.succeeded <- true
}
