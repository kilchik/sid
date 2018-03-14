package main

import (
	"database/sql"
	"fmt"
	"log"
)

func selectExpensesFromDB(uid int64, users map[int64]string) (expenses []userExpense, err error) {
	var rows *sql.Rows
	log.Printf("select expenses for uid=%d, users=%v", uid, users)
	rows, err = db.Query(`SELECT T.title, O.amount, O.src, T.ts
FROM operations O, transactions T
WHERE O.transaction_id=T.id AND O.dst=?
ORDER BY T.ts ASC;`, uid)
	if err != nil {
		err = fmt.Errorf("select user expenses: %v", err)
		return
	}

	defer rows.Close()
	for rows.Next() {
		var ue userExpense
		var src int64
		err = rows.Scan(&ue.title, &ue.amount, &src, &ue.time)
		if err != nil {
			return
		}

		var ok bool
		if ue.payer, ok = users[src]; !ok {
			err = fmt.Errorf("unknown user: %d", src)
			return
		}

		expenses = append(expenses, ue)
	}

	return
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

func selectGroupMembers(user int) (groupMembers map[int64]string, err error) {
	groupMembers = make(map[int64]string)
	var rows *sql.Rows
	rows, err = db.Query(`SELECT id, name FROM users
WHERE group_id=(SELECT group_id FROM users WHERE id=?);`, user)
	if err != nil {
		err = fmt.Errorf("select same group members: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var name string
		err = rows.Scan(&uid, &name)
		if err != nil {
			break
		}
		groupMembers[uid] = name
	}
	return
}

func getUserGroup(uid int) (g *group, err error) {
	var rows *sql.Rows
	rows, err = db.Query(`SELECT G.id, G.name FROM groups G, users U
WHERE U.group_id=G.id AND U.id=?`, uid)
	if err != nil {
		err = fmt.Errorf("get user group: %v", err)
		return
	}
	defer rows.Close()
	if rows.Next() {
		g = &group{}
		if err = rows.Scan(&g.id, &g.name); err != nil {
			err = fmt.Errorf("scan user group: %v", err)
			return
		}
	}
	return
}
