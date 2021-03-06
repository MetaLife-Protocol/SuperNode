package v1

import (
	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/dto"

	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/utils"
	"github.com/ant0ine/go-json-rest/rest"
	"github.com/ethereum/go-ethereum/common"
)

/*
Address is api of /api/1/address
*/
func Address(w rest.ResponseWriter, r *rest.Request) {
	writejson(w, dto.NewSuccessAPIResponse(API.Photon.NodeAddress.String()))
}

/*
GetBalanceByTokenAddress : get account's balance and locked account on token
*/
func GetBalanceByTokenAddress(w rest.ResponseWriter, r *rest.Request) {
	var resp *dto.APIResponse
	defer func() {
		log.Trace(fmt.Sprintf("Restful Api Call ----> GetBalanceByTokenAddress ,err=%s", resp.ToFormatString()))
		writejson(w, resp)
	}()
	tokenAddressStr := r.PathParam("tokenaddress")
	var tokenAddress common.Address
	if tokenAddressStr == "" {
		tokenAddress = utils.EmptyAddress
	} else {
		tokenAddress = common.HexToAddress(tokenAddressStr)
	}
	result, err := API.GetBalanceByTokenAddress(tokenAddress)
	resp = dto.NewAPIResponse(err, result)
}
