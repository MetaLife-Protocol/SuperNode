package daotest

import (
	"testing"

	"github.com/MetaLife-Protocol/SuperNode/codefortest"
	"github.com/MetaLife-Protocol/SuperNode/utils"
)

func TestModelDB_XMPPIsAddrSubed(t *testing.T) {
	dao := codefortest.NewTestDB("")
	defer dao.CloseDB()
	addr := utils.NewRandomAddress()
	if dao.XMPPIsAddrSubed(addr) {
		t.Error("should not marked")
		return
	}
	dao.XMPPMarkAddrSubed(addr)
	if !dao.XMPPIsAddrSubed(addr) {
		t.Error("should marked")
		return
	}
	dao.XMPPUnMarkAddr(addr)
	if dao.XMPPIsAddrSubed(addr) {
		t.Error("should not marked")
	}
}
