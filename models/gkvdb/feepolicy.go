package gkvdb

import (
	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/models"
)

// SaveFeePolicy :
func (dao *GkvDB) SaveFeePolicy(fp *models.FeePolicy) (err error) {
	fp.Key = models.KeyFeePolicy
	err = dao.saveKeyValueToBucket(models.BucketFeePolicy, fp.Key, fp)
	err = models.GeneratDBError(err)
	return
}

// GetFeePolicy :
func (dao *GkvDB) GetFeePolicy() (fp *models.FeePolicy) {
	if fp == nil {
		fp = &models.FeePolicy{}
	}
	err := dao.getKeyValueToBucket(models.BucketFeePolicy, models.KeyFeePolicy, &fp)
	if err == ErrorNotFound {
		return models.NewDefaultFeePolicy()
	}
	if err != nil {
		log.Error(fmt.Sprintf("GetFeePolicy err %s, use default fee policy", err))
		return models.NewDefaultFeePolicy()
	}
	return
}
