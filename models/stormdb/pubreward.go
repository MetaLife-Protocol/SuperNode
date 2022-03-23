package stormdb

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
)

// PubDB init
type PubRewardDB struct {
	db *sql.DB
}

func OpenPubDB(pubDataSource string) (DB *PubRewardDB, err error) {
	fmt.Println(pubDataSource)
	db, err := sql.Open("sqlite3", pubDataSource)
	if err != nil {
		return nil, err
	}

	sql_table := `
CREATE TABLE IF NOT EXISTS "historyreward" (
   "uid" INTEGER PRIMARY KEY AUTOINCREMENT,
   "clientid" TEXT NULL,
   "ethaddress" TEXT NULL default '',
   "rewardsum" int NULL default 0
);
   `
	_, err = db.Exec(sql_table)
	if err != nil {
		return nil, err
	}
	return &PubRewardDB{db: db}, nil
}

// InsertHistoryReward
func (pdb *PubRewardDB) InsertHistoryReward(clientid, ethaddr string, nowsum int) (lastid int64, err error) {
	stmt, err := pdb.db.Prepare("INSERT INTO historyreward(clientid,ethaddress,rewardsum) VALUES (?,?,?)")
	if err != nil {
		return 0, err
	}
	res, err := stmt.Exec(clientid, ethaddr, nowsum)
	if err != nil {
		return 0, err
	}
	lastid, err = res.LastInsertId()
	return
}

// UpdateHistoryReward
func (pdb *PubRewardDB) UpdateHistoryReward(clientid, ethaddr string, nowsum int) (affectid int64, err error) {
	hr, err := pdb.SelectHistoryReward(clientid, ethaddr)
	if err != nil {
		return 0, err
	}
	if hr == nil {
		_, err = pdb.InsertHistoryReward(clientid, ethaddr, nowsum)
		if err != nil {
			return 0, err
		}
		return 1, nil
	}
	var stmt *sql.Stmt
	stmt, err = pdb.db.Prepare("update historyreward set rewardsum=? WHERE clientid=? and ethaddress=?")
	if err != nil {
		return 0, err
	}
	res, err := stmt.Exec(nowsum, clientid, ethaddr)
	if err != nil {
		return 0, err
	}
	affectid, err = res.LastInsertId()
	return
}

// RewardInfo
type RewardInfo struct {
	ClientId         string
	EthAddress       string
	HistoryRewardSum int
}

// SelectHistoryReward
func (pdb *PubRewardDB) SelectHistoryReward(clientid, ethaddr string) (ri *RewardInfo, err error) {
	rows, err := pdb.db.Query("SELECT rewardsum FROM historyreward where clientid=? and ethaddress=?", clientid, ethaddr)
	if err != nil {
		return nil, err
	}
	xri := &RewardInfo{}
	defer rows.Close()
	for rows.Next() {
		//var cid string
		//var ethaddr string
		var rsum int
		err := rows.Scan(&rsum)
		if err != nil {
			return nil, err
		}
		xri = &RewardInfo{
			ClientId:         clientid,
			EthAddress:       ethaddr,
			HistoryRewardSum: rsum,
		}
		break
	}
	ri = xri
	return
}
