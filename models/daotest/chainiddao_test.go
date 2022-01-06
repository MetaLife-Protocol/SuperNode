package daotest

import (
	"testing"

	"github.com/MetaLife-Protocol/SuperNode/codefortest"
)

func TestChainIDDao(t *testing.T) {
	dao := codefortest.NewTestDB("")
	defer dao.CloseDB()
	bn := int64(500)
	dao.SaveChainID(bn)
	bn1 := dao.GetChainID()
	if bn1 != bn {
		t.Error("not equal")
		return
	}
}
