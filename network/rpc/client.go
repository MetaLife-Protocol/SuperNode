package rpc

import (
	"context"

	"github.com/MetaLife-Protocol/SuperNode/internal/rpanic"
	"github.com/MetaLife-Protocol/SuperNode/rerr"

	"math/big"

	"time"

	"fmt"

	"crypto/ecdsa"

	"sync"

	"encoding/json"

	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/models"
	"github.com/MetaLife-Protocol/SuperNode/network/helper"
	"github.com/MetaLife-Protocol/SuperNode/network/netshare"
	"github.com/MetaLife-Protocol/SuperNode/network/rpc/contracts"
	"github.com/MetaLife-Protocol/SuperNode/notify"
	"github.com/MetaLife-Protocol/SuperNode/params"
	"github.com/MetaLife-Protocol/SuperNode/utils"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

//GetCallContext context for tx
func GetCallContext() context.Context {
	ctx, cf := context.WithDeadline(context.Background(), time.Now().Add(params.DefaultTxTimeout))
	if cf != nil {
	}
	return ctx
}

//GetQueryConext context for query on chain
func GetQueryConext() context.Context {
	ctx, cf := context.WithDeadline(context.Background(), time.Now().Add(params.DefaultPollTimeout))
	if cf != nil {
	}
	return ctx
}

/*
BlockChainService provides quering on blockchain.
*/
type BlockChainService struct {
	//PrivKey of this node, todo remove this
	PrivKey *ecdsa.PrivateKey
	//NodeAddress is address of this node
	NodeAddress         common.Address
	tokenNetworkAddress common.Address
	SecretRegistryProxy *SecretRegistryProxy
	//Client if eth rpc client
	Client        *helper.SafeEthClient
	addressTokens map[common.Address]*TokenProxy
	RegistryProxy *RegistryProxy
	//Auth needs by call on blockchain todo remove this
	Auth  *bind.TransactOpts
	mlock sync.Mutex
	// things needs by contract call
	NotifyHandler     *notify.Handler
	TXInfoDao         models.TXInfoDao
	pendingTXInfoChan chan *models.TXInfo
	quitChan          chan error
}

//NewBlockChainService create BlockChainService
func NewBlockChainService(privateKey *ecdsa.PrivateKey, registryAddress common.Address, client *helper.SafeEthClient, notifyHandler *notify.Handler, txInfoDao models.TXInfoDao) (bcs *BlockChainService, err error) {
	bcs = &BlockChainService{
		PrivKey:             privateKey,
		NodeAddress:         crypto.PubkeyToAddress(privateKey.PublicKey),
		Client:              client,
		addressTokens:       make(map[common.Address]*TokenProxy),
		Auth:                bind.NewKeyedTransactor(privateKey),
		tokenNetworkAddress: registryAddress,
		NotifyHandler:       notifyHandler,
		TXInfoDao:           txInfoDao,
		pendingTXInfoChan:   make(chan *models.TXInfo, 10), // TODO ??????????????????????????????
		quitChan:            make(chan error),
	}
	// remove gas limit config and let it calculate automatically
	//bcs.Auth.GasLimit = uint64(params.GasLimit)
	bcs.Auth.GasPrice = big.NewInt(params.DefaultGasPrice)

	_, err = bcs.Registry(registryAddress, client.Status == netshare.Connected)
	return
}
func (bcs *BlockChainService) getQueryOpts() *bind.CallOpts {
	return &bind.CallOpts{
		Pending: false,
		From:    bcs.NodeAddress,
		Context: GetQueryConext(),
	}
}

// Token return a proxy to interact with a token.
func (bcs *BlockChainService) Token(tokenAddress common.Address) (t *TokenProxy, err error) {
	bcs.mlock.Lock()
	defer bcs.mlock.Unlock()
	_, ok := bcs.addressTokens[tokenAddress]
	if !ok {
		token, err := contracts.NewToken(tokenAddress, bcs.Client)
		if err != nil {
			log.Error(fmt.Sprintf("NewToken %s err %s", tokenAddress.String(), err))
			return nil, rerr.ContractCallError(err)
		}
		bcs.addressTokens[tokenAddress] = &TokenProxy{
			Address: tokenAddress, bcs: bcs, Token: token}
	}
	return bcs.addressTokens[tokenAddress], nil
}

//TokenNetwork return a proxy to interact with a NettingChannelContract.
func (bcs *BlockChainService) TokenNetwork(tokenAddress common.Address) (t *TokenNetworkProxy, err error) {
	return &TokenNetworkProxy{bcs.RegistryProxy, bcs, tokenAddress}, nil
}

// Registry Return a proxy to interact with Registry.
func (bcs *BlockChainService) Registry(address common.Address, hasConnectChain bool) (t *RegistryProxy, err error) {
	if bcs.RegistryProxy != nil && bcs.RegistryProxy.ch != nil {
		return bcs.RegistryProxy, nil
	}
	r := &RegistryProxy{
		Address: address,
	}
	if hasConnectChain {
		var reg *contracts.TokensNetwork
		reg, err = contracts.NewTokensNetwork(address, bcs.Client)
		if err != nil {
			log.Error(fmt.Sprintf("NewRegistry %s err %s ", address.String(), err))
			return
		}
		r.ch = reg
		var secAddr common.Address
		secAddr, err = r.ch.SecretRegistry(nil)
		if err != nil {
			log.Error(fmt.Sprintf("get Secret_registry_address %s", err))
			return
		}
		var s *contracts.SecretRegistry
		s, err = contracts.NewSecretRegistry(secAddr, bcs.Client)
		if err != nil {
			log.Error(fmt.Sprintf("NewSecretRegistry err %s", err))
			return
		}
		bcs.SecretRegistryProxy = &SecretRegistryProxy{
			Address:          secAddr,
			bcs:              bcs,
			registry:         s,
			RegisteredSecret: make(map[common.Hash]*sync.Mutex),
		}
		// 1. ??????pendingTXInfoListenLoop
		go bcs.pendingTXInfoListenLoop()
		// 2. ????????????pending?????????tx,?????????????????????
		var pendingTXs []*models.TXInfo
		pendingTXs, err = bcs.TXInfoDao.GetTXInfoList(utils.EmptyHash, 0, utils.EmptyAddress, "", models.TXInfoStatusPending)
		if err != nil {
			log.Error(fmt.Sprintf("GetTXInfoList err %s", err))
			return
		}
		for _, tx := range pendingTXs {
			bcs.RegisterPendingTXInfo(tx)
		}
	}
	bcs.RegistryProxy = r
	log.Info(fmt.Sprintf("RegistryProxy was updated,and RegistryProxy=%s", utils.StringInterface(bcs.RegistryProxy, 2)))
	return bcs.RegistryProxy, nil
}

// GetRegistryAddress :
func (bcs *BlockChainService) GetRegistryAddress() common.Address {
	if bcs.RegistryProxy != nil {
		return bcs.RegistryProxy.Address
	}
	return utils.EmptyAddress
}

// GetSecretRegistryAddress :
func (bcs *BlockChainService) GetSecretRegistryAddress() common.Address {
	if bcs.SecretRegistryProxy != nil {
		return bcs.SecretRegistryProxy.Address
	}
	return utils.EmptyAddress
}

// SyncProgress :
func (bcs *BlockChainService) SyncProgress() (sp *ethereum.SyncProgress, err error) {
	return bcs.Client.SyncProgress(context.Background())
}

// RegisterPendingTXInfo ??????Pending?????????tx,??????????????????????????????tx???receipt,??????????????????db
func (bcs *BlockChainService) RegisterPendingTXInfo(txInfo *models.TXInfo) {
	bcs.pendingTXInfoChan <- txInfo
}

/*
pending?????????tx????????????????????????,????????????,???????????????
*/
func (bcs *BlockChainService) pendingTXInfoListenLoop() {
	log.Info("goroutine of pendingTXInfoListenLoop start")
	for {
		select {
		case err := <-bcs.quitChan:
			if err != nil {
				log.Error("pendingTXInfoListenLoop quit because err = %s", err.Error())
			}
			return
		case txInfo := <-bcs.pendingTXInfoChan:
			// ?????????????????????tx,??????????????????????????????????????????
			go bcs.checkPendingTXDone(txInfo)
		}
	}
}

func (bcs *BlockChainService) checkPendingTXDone(pendingTXInfo *models.TXInfo) {
	defer rpanic.PanicRecover("checkPendingTXDone")
	if pendingTXInfo.Status != models.TXInfoStatusPending {
		log.Warn("checkPendingTXDone got tx with status=%s, maybe something wrong", pendingTXInfo.Status)
		return
	}
	// 1. ??????tx????????????
	receipt, err := waitMined(context.Background(), bcs.Client, pendingTXInfo.TXHash)
	if err != nil {
		err = rerr.ErrTxWaitMined.AppendError(err)
		log.Error(err.Error())
		return
	}
	// 2. ??????packBlockNumber
	var packBlockNumber int64
	if len(receipt.Logs) > 0 {
		packBlockNumber = int64(receipt.Logs[0].BlockNumber)
	}
	var savedTxInfo *models.TXInfo
	// 3. ??????
	if receipt.Status != types.ReceiptStatusSuccessful {
		// ????????????
		// a.????????????????????????
		savedTxInfo, err = bcs.TXInfoDao.UpdateTXInfoStatus(pendingTXInfo.TXHash, models.TXInfoStatusFailed, packBlockNumber, receipt.GasUsed)
		if err != nil {
			log.Error(err.Error())
		}
		// b. ????????????
		bcs.NotifyHandler.NotifyContractCallTXInfo(savedTxInfo)
		log.Warn(fmt.Sprintf("tx receipt failed :\n%s", utils.StringInterface(savedTxInfo, 3)))
		return
	}
	// ????????????
	log.Info(fmt.Sprintf("tx[txHash=%s,type=%s] receipt success", pendingTXInfo.TXHash.String(), pendingTXInfo.Type))
	// a.????????????????????????
	savedTxInfo, err = bcs.TXInfoDao.UpdateTXInfoStatus(pendingTXInfo.TXHash, models.TXInfoStatusSuccess, packBlockNumber, receipt.GasUsed)
	if err != nil {
		log.Error(err.Error())
	}
	// b. ????????????
	bcs.NotifyHandler.NotifyContractCallTXInfo(savedTxInfo)
	// b. ??????tx??????????????????????????????????????????
	switch pendingTXInfo.Type {
	case models.TXInfoTypeApproveDeposit: //approve??????????????????????????????deposit
		// ?????????????????????
		var depositParams models.DepositTXParams
		err = json.Unmarshal([]byte(pendingTXInfo.TXParams), &depositParams)
		if err != nil {
			log.Error(err.Error())
			break
		}
		// ??????deposit??????
		proxy, err := bcs.TokenNetwork(depositParams.TokenAddress)
		if err != nil {
			log.Error(err.Error())
			break
		}
		//log.Info(fmt.Sprintf("RegistryProxy proxy=%s", utils.StringInterface(proxy, 5)))
		tx, err := proxy.GetContract().Deposit(bcs.Auth, depositParams.TokenAddress, depositParams.ParticipantAddress, depositParams.PartnerAddress, depositParams.Amount, depositParams.SettleTimeout)
		if err != nil {
			log.Error(err.Error())
			break
		}
		// ??????TXInfo????????????bcs????????????????????????
		channelID := utils.CalcChannelID(depositParams.TokenAddress, bcs.RegistryProxy.Address, depositParams.ParticipantAddress, depositParams.PartnerAddress)
		txInfo, err := bcs.TXInfoDao.NewPendingTXInfo(tx, models.TXInfoTypeDeposit, channelID, 0, &depositParams)
		if err != nil {
			log.Error(err.Error())
			break
		}
		bcs.RegisterPendingTXInfo(txInfo)
	}
}

// ??????bind.WaitMined()??????,?????????????????????,???????????????
func waitMined(ctx context.Context, b bind.DeployBackend, txHash common.Hash) (*types.Receipt, error) {
	queryTicker := time.NewTicker(time.Second)
	defer queryTicker.Stop()

	logger := log.New("hash", txHash)
	for {
		receipt, err := b.TransactionReceipt(ctx, txHash)
		if receipt != nil {
			return receipt, nil
		}
		if err != nil {
			//logger.Trace("Receipt retrieval failed", "err", err)
		} else {
			logger.Trace("Transaction not yet mined")
		}
		// Wait for the next round.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-queryTicker.C:
		}
	}
}
