package rpc

import (
	"os"
	"testing"

	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/codefortest"
	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/utils"
	"github.com/ethereum/go-ethereum/common"
)

func init() {
	log.Root().SetHandler(log.LvlFilterHandler(log.LvlTrace, utils.MyStreamHandler(os.Stderr)))
}
func TestEventsGetInternal(t *testing.T) {
	registryAddress := common.HexToAddress("0x7B25494cF297D63eA2AF72d43Fc133408674c43a")
	tokenNetworkAddress := common.HexToAddress("0x06bE91b5DdA5a0459C0FF7Bc016A2D21E276C2e4")
	client, err := codefortest.GetEthClient()
	if err != nil {
		t.Error(err.Error())
	}
	logs, err := EventsGetInternal(
		GetQueryConext(),
		[]common.Address{registryAddress, tokenNetworkAddress},
		6000745,
		6000751,
		client)
	if err != nil {
		t.Error(err.Error())
	}
	for _, log := range logs {
		fmt.Println(log.BlockNumber)
		fmt.Println(log.String())
	}
	fmt.Printf("events num : %d\n", len(logs))
}
