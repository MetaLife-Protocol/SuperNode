package stormdb

import (
	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/models"
)

//GetChainID :
func (model *StormDB) GetChainID() int64 {
	var chainID int64
	err := model.db.Get(models.BucketChainID, models.KeyChainID, &chainID)
	if err != nil {
		log.Error(fmt.Sprintf("models GetChainId err=%s", err))
	}
	return chainID
}

//SaveChainID :
func (model *StormDB) SaveChainID(chainID int64) {
	err := model.db.Set(models.BucketChainID, models.KeyChainID, chainID)
	if err != nil {
		log.Error(fmt.Sprintf("models SaveChainId err=%s", err))
	}
}
