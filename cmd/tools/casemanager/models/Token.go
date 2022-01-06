package models

import (
	"github.com/MetaLife-Protocol/SuperNode/network/rpc/contracts"
	"github.com/ethereum/go-ethereum/common"
)

// Token name and address
type Token struct {
	Name         string
	Token        *contracts.Token
	TokenAddress common.Address
}
