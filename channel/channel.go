package channel

import (
	"fmt"
	"math/big"

	"github.com/MetaLife-Protocol/SuperNode/channel/channeltype"
	"github.com/MetaLife-Protocol/SuperNode/encoding"
	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/network/rpc/contracts"
	"github.com/MetaLife-Protocol/SuperNode/network/rpc/fee"
	"github.com/MetaLife-Protocol/SuperNode/rerr"
	"github.com/MetaLife-Protocol/SuperNode/transfer"
	"github.com/MetaLife-Protocol/SuperNode/transfer/mtree"
	"github.com/MetaLife-Protocol/SuperNode/utils"
	"github.com/ethereum/go-ethereum/common"
)

/*
Channel is the living representation of  channel on blockchain.
it contains all the transfers between two participants.
*/
type Channel struct {
	OurState          *EndState
	PartnerState      *EndState
	ExternState       *ExternalState
	ChannelIdentifier contracts.ChannelUniqueID //this channel
	TokenAddress      common.Address
	RevealTimeout     int
	SettleTimeout     int
	feeCharger        fee.Charger //calc fee for each transfer?
	State             channeltype.State
}

/*
NewChannel returns the living channel.
channelIdentifier must be a valid contract adress
settleTimeout must be valid, it cannot too small.
*/
func NewChannel(ourState, partnerState *EndState, externState *ExternalState, tokenAddr common.Address, channelIdentifier *contracts.ChannelUniqueID,
	revealTimeout, settleTimeout int) (c *Channel, err error) {
	if settleTimeout <= revealTimeout {
		err = rerr.ErrChannelInvalidSttleTimeout.Errorf("reveal_timeout can not be larger-or-equal to settle_timeout, reveal_timeout=%d,settle_timeout=%d", revealTimeout, settleTimeout)
		return
	}
	if revealTimeout < 3 {
		err = rerr.ErrChannelRevealTimeout.Append("reveal_timeout must be at least 3")
		return
	}
	c = &Channel{
		OurState:          ourState,
		PartnerState:      partnerState,
		ExternState:       externState,
		ChannelIdentifier: *channelIdentifier,
		TokenAddress:      tokenAddr,
		RevealTimeout:     revealTimeout,
		SettleTimeout:     settleTimeout,
		State:             channeltype.StateOpened, //???????????????????????????,state??????????????????,???????????????????????????????????????open
	}
	return
}

/*
Distributable return the available amount of the token that our end of the channel can transfer to the partner.
*/
func (c *Channel) Distributable() *big.Int {
	return c.OurState.Distributable(c.PartnerState)
}

/*
CanTransfer  a closed channel and has no Balance channel cannot
transfer tokens to partner.
*/
func (c *Channel) CanTransfer() bool {
	return channeltype.CanTransferMap[c.State]
}

/*
IsClosed returns true when this channel closed
*/
func (c *Channel) IsClosed() bool {
	return c.State == channeltype.StateClosed
}

//CanContinueTransfer unfinished transfer can continue?
func (c *Channel) CanContinueTransfer() bool {
	return !channeltype.TransferCannotBeContinuedMap[c.State]
}

/*
ContractBalance Return the total amount of token we deposited in the channel
*/
func (c *Channel) ContractBalance() *big.Int {
	return c.OurState.ContractBalance
}

/*
TransferAmount Return how much we transferred to partner.
*/
func (c *Channel) TransferAmount() *big.Int {
	return c.OurState.TransferAmount()
}

/*
Balance Return our current Balance.

OurBalance is equal to `initial_deposit + received_amount - sent_amount`,
were both `receive_amount` and `sent_amount` are unlocked.
*/
func (c *Channel) Balance() *big.Int {
	x := new(big.Int)
	x.Sub(c.OurState.ContractBalance, c.OurState.TransferAmount())
	x.Add(x, c.PartnerState.TransferAmount())
	return x
}

/*
PartnerBalance return partner current Balance.
OurBalance is equal to `initial_deposit + received_amount - sent_amount`,
were both `receive_amount` and `sent_amount` are unlocked.
*/
func (c *Channel) PartnerBalance() *big.Int {
	x := new(big.Int)
	x.Sub(c.PartnerState.ContractBalance, c.PartnerState.TransferAmount())
	x.Add(x, c.OurState.TransferAmount())
	return x
}

/*
Locked return the current amount of our token that is locked waiting for a
        secret.

The locked value is equal to locked transfers that have been
initialized but their secret has not being revealed.
*/
func (c *Channel) Locked() *big.Int {
	return c.OurState.amountLocked()
}

/*
Outstanding is the tokens on road...
*/
func (c *Channel) Outstanding() *big.Int {
	return c.PartnerState.amountLocked()
}

/*
GetSettleExpiration returns how many blocks have to wait before settle.
*/
func (c *Channel) GetSettleExpiration(blocknumer int64) int64 {
	ClosedBlock := c.ExternState.ClosedBlock
	if ClosedBlock != 0 {
		return ClosedBlock + int64(c.SettleTimeout)
	}
	return blocknumer + int64(c.SettleTimeout)
}

/*
HandleBalanceProofUpdated ????????????????????????????????????,????????????????????????????????? settle ??????
*/
/*
 *	HandleBalanceProofUpdated : It handles events that channel partners submitting BalanceProof that is not the most recent,
 * 		which leads to inability to settle channel.
 */
func (c *Channel) HandleBalanceProofUpdated(updatedParticipant common.Address, transferAmount *big.Int, locksRoot common.Hash) {
	endStateContractUpdated := c.OurState
	if updatedParticipant == c.PartnerState.Address {
		endStateContractUpdated = c.PartnerState
	}
	endStateContractUpdated.SetContractTransferAmount(transferAmount)
	endStateContractUpdated.SetContractLocksroot(locksRoot)
	//???updateBalanceProof??????,?????????unlock
	if updatedParticipant == c.OurState.Address {
		unlockProofs := c.PartnerState.GetCanUnlockOnChainLocks()
		if len(unlockProofs) > 0 {
			result := c.ExternState.Unlock(unlockProofs, c.PartnerState.contractTransferAmount())
			go func() {
				err := <-result.Result
				if err != nil {
					// todo need to report error to Photon user
					log.Error(fmt.Sprintf("Unlock failed because of %s", err))
				}
			}()
		}
	}
}

/*
HandleChannelPunished ????????? Punish ??????,???????????????????????????????????????????????????.
*/
/*
 *	HandleChannelPunished : Punish event occurs,
 * 		which means that information on contract of beneficiary has been changed.
 */
func (c *Channel) HandleChannelPunished(beneficiaries common.Address) {
	log.Trace(fmt.Sprintf("receive punish for %s,channel id=%s", beneficiaries.String(), c.ChannelIdentifier.ChannelIdentifier.String()))
	var beneficiaryState, cheaterState *EndState
	if beneficiaries == c.OurState.Address {
		beneficiaryState = c.OurState
		cheaterState = c.PartnerState
	} else if beneficiaries == c.PartnerState.Address {
		beneficiaryState = c.PartnerState
		cheaterState = c.OurState
	} else {
		panic(fmt.Sprintf("channel=%s,but participant =%s",
			c.ChannelIdentifier.String(),
			beneficiaries.String(),
		))
	}
	beneficiaryState.SetContractTransferAmount(utils.BigInt0)
	beneficiaryState.SetContractLocksroot(utils.EmptyHash)
	beneficiaryState.SetContractNonce(0xffffffffffffffff)
	beneficiaryState.ContractBalance = beneficiaryState.ContractBalance.Add(
		beneficiaryState.ContractBalance, cheaterState.ContractBalance,
	)
	cheaterState.ContractBalance = new(big.Int).Set(utils.BigInt0)
	log.Trace(fmt.Sprintf("c=%s", utils.StringInterface(c, 5)))
}

/*
HandleClosed handles this channel was closed on blockchain
1. ??????NonClosing ????????? ContractTransferAmount ??? LocksRoot,
2. ?????????????????????BalanceProof, ??????????????????????????? TransferAmount ??? LocksRoot??????
3. ????????????????????????,??????????????????????????? BalanceProof
4. ??????????????????????????????,????????????.
*/
/*
 *	HandleClosed : It handles events of closing channel.
 *
 *		1. Update ContractTransferAmount & LocksRoot of the non-closing participant.
 *		2. That participant may submit used BalanceProof, in which TransferAmount & LocksRoot are not consistent with mine.
 *		3. If I am not the non-closing participant, then update the BalanceProof of my channel partner.
 *		4. All locks I am holding that have known secrets must be unlocked.
 */
func (c *Channel) HandleClosed(closingAddress common.Address, transferredAmount *big.Int, locksRoot common.Hash) {
	endStateUpdatedOnContract := c.PartnerState
	balanceProof := c.PartnerState.BalanceProofState
	//???????????????????????? ContractTransferAmount ?????? LocksRoot ?????????????????????
	//the channel was closed, update our half of the state if we need to
	if closingAddress != c.OurState.Address {
		c.ExternState.UpdateTransfer(balanceProof)
		endStateUpdatedOnContract = c.OurState
	}
	endStateUpdatedOnContract.SetContractTransferAmount(transferredAmount)
	endStateUpdatedOnContract.SetContractLocksroot(locksRoot)
	/*
		????????????,???????????????????????????????????????????????????,????????????????????????,?????????????????????????????????????????????.
	*/
	// Verify data, if no more update message, which might be attack, or which might be local storage error.
	if endStateUpdatedOnContract.TransferAmount().Cmp(endStateUpdatedOnContract.contractTransferAmount()) != 0 {
		log.Error(fmt.Sprintf("Channel %s closed,but contract transfer amount is %s, and local stored %s's transfer amount is %s",
			utils.HPex(c.ChannelIdentifier.ChannelIdentifier), endStateUpdatedOnContract.contractTransferAmount(),
			utils.APex2(endStateUpdatedOnContract.Address), endStateUpdatedOnContract.TransferAmount(),
		))
	}
	if endStateUpdatedOnContract.locksRoot() != endStateUpdatedOnContract.contractLocksRoot() {
		log.Error(fmt.Sprintf("channel %s closed,but contract locksroot is %s, and local stored %s's locksroot is %s",
			utils.HPex(c.ChannelIdentifier.ChannelIdentifier), utils.HPex(endStateUpdatedOnContract.contractLocksRoot()),
			utils.APex2(endStateUpdatedOnContract.Address), utils.HPex(endStateUpdatedOnContract.locksRoot()),
		))
	}
	//?????????????????????,?????????????????????unlock,??????????????????,?????????updateBalanceProof????????????unlock
	if closingAddress == c.OurState.Address {
		unlockProofs := c.PartnerState.GetCanUnlockOnChainLocks()
		if len(unlockProofs) > 0 {
			result := c.ExternState.Unlock(unlockProofs, c.PartnerState.contractTransferAmount())
			go func() {
				err := <-result.Result
				if err != nil {
					// todo need to report error to Photon user
					log.Error(fmt.Sprintf("Unlock failed because of %s", err))
				}
			}()
		}
	}
}

/*
HandleSettled handles this channel was settled on blockchain
there is nothing tod rightnow
*/
func (c *Channel) HandleSettled(blockNumber int64) {
	c.State = channeltype.StateSettled
}

//HandleWithdrawed ????????????????????????????????????????????????
/*
 *	HandleWithdrawed : function to handle withdraw message.
 *		This function will re-allocate the messages that initialize the whole payment channel.
 */
func (c *Channel) HandleWithdrawed(newOpenBlockNumber int64, participant1, participant2 common.Address, participant1Balance, participant2Balance *big.Int) {
	var p1, p2 *EndState
	if c.OurState.Address == participant1 && c.PartnerState.Address == participant2 {
		p1 = c.OurState
		p2 = c.PartnerState
	} else if c.OurState.Address == participant2 && c.PartnerState.Address == participant1 {
		p1 = c.PartnerState
		p2 = c.OurState
	} else {
		panic(fmt.Sprintf("channel event error, ourAddress=%s,partnerAddress=%s,p1=%s,p2=%s",
			c.OurState.Address.String(), c.PartnerState.Address.String(),
			participant1.String(), participant2.String(),
		))
	}
	if len(p1.Lock2UnclaimedLocks) > 0 || len(p2.Lock2UnclaimedLocks) > 0 {
		log.Warn(fmt.Sprintf("channel %s receive contract withdraw event, but has unclaimed locks."+
			"p1lock=%s,p2lock=%s", c.ChannelIdentifier.String(), utils.StringInterface(p1.Lock2UnclaimedLocks, 3),
			utils.StringInterface(p2.Lock2UnclaimedLocks, 3)))
	}
	/*
		???????????????????????????????????????,??????????????? settle ???????????????,
	*/
	// all history record in channel should be abandoned, and do not store them in channel settle history.
	c.ChannelIdentifier.OpenBlockNumber = newOpenBlockNumber
	c.State = channeltype.StateOpened
	c.ExternState.ChannelIdentifier.OpenBlockNumber = newOpenBlockNumber
	c.ExternState.ClosedBlock = 0
	c.ExternState.SettledBlock = 0
	p1.ContractBalance = participant1Balance
	p1.BalanceProofState = transfer.NewEmptyBalanceProofState()
	p1.Lock2PendingLocks = make(map[common.Hash]channeltype.PendingLock)
	p1.Lock2UnclaimedLocks = make(map[common.Hash]channeltype.UnlockPartialProof)
	p1.Tree = mtree.NewMerkleTree(nil)
	p2.ContractBalance = participant2Balance
	p2.BalanceProofState = transfer.NewEmptyBalanceProofState()
	p2.Lock2PendingLocks = make(map[common.Hash]channeltype.PendingLock)
	p2.Lock2UnclaimedLocks = make(map[common.Hash]channeltype.UnlockPartialProof)
	p2.Tree = mtree.NewMerkleTree(nil)

}

/*
GetStateFor returns the latest status of one participant
*/
func (c *Channel) GetStateFor(nodeAddress common.Address) (*EndState, error) {
	if c.OurState.Address == nodeAddress {
		return c.OurState, nil
	}
	if c.PartnerState.Address == nodeAddress {
		return c.PartnerState, nil
	}
	return nil, rerr.ErrChannelNotParticipant.Errorf("GetStateFor Unknown address %s", nodeAddress)
}

/*
RegisterSecret Register a secret to this channel

        This wont claim the lock (update the transferred_amount), it will only
        save the secret in case that a proof needs to be created. This method
        can be used for any of the ends of the channel.

        Note:
            When a secret is revealed a message could be in-transit containing
            the older lockroot, for this reason the recipient cannot update
            their locksroot at the moment a secret was revealed.

            The protocol is to register the secret so that it can compute a
            proof of Balance, if necessary, forward the secret to the sender
            and wait for the update from it. It's the sender's duty to order the
            current in-transit (and possible the transfers in queue) transfers
            and the secret/locksroot update.

            The channel and its queue must be changed in sync, a transfer must
            not be created while we update the balance_proof.

        Args:
            secret: The secret that releases a locked transfer.
*/
func (c *Channel) RegisterSecret(secret common.Hash) error {
	hashlock := utils.ShaSecret(secret[:])
	ourKnown := c.OurState.IsKnown(hashlock)
	partenerKnown := c.PartnerState.IsKnown(hashlock)
	if !ourKnown && !partenerKnown {
		return rerr.ErrChannelLockSecretHashNotFound.Errorf("secret doesn't correspond to a registered hashlock. hashlock %s token %s",
			utils.Pex(hashlock[:]), utils.HPex(c.ChannelIdentifier.ChannelIdentifier))
	}
	if ourKnown {
		lock := c.OurState.getLockByHashlock(hashlock)
		log.Debug(fmt.Sprintf("secret registered node=%s,from=%s,to=%s,token=%s,hashlock=%s, secret=%s, amount=%s",
			utils.Pex(c.OurState.Address[:]), utils.Pex(c.OurState.Address[:]),
			utils.Pex(c.PartnerState.Address[:]), utils.APex(c.TokenAddress),
			utils.Pex(hashlock[:]), utils.Pex(secret[:]), lock.Amount))
		err := c.OurState.RegisterSecret(secret)
		return err
	}
	if partenerKnown {
		lock := c.PartnerState.getLockByHashlock(hashlock)
		log.Debug(fmt.Sprintf("secret registered node=%s,from=%s,to=%s,token=%s,hashlock=%s, secret=%s, amount=%s",
			utils.Pex(c.OurState.Address[:]), utils.Pex(c.PartnerState.Address[:]),
			utils.Pex(c.OurState.Address[:]), utils.APex(c.TokenAddress),
			utils.Pex(hashlock[:]), utils.Pex(secret[:]), lock.Amount))
		err := c.PartnerState.RegisterSecret(secret)
		if err != nil {
			return err
		}
	}
	return nil
}

//RegisterRevealedSecretHash ??????????????????????????????
// RegisterRevealedSecretHash : secret has been registered on chain.
func (c *Channel) RegisterRevealedSecretHash(lockSecretHash, secret common.Hash, blockNumber int64) error {
	ourKnown := c.OurState.IsKnown(lockSecretHash)
	partenerKnown := c.PartnerState.IsKnown(lockSecretHash)
	if !ourKnown && !partenerKnown {
		return rerr.ErrChannelLockSecretHashNotFound.Errorf("LockSecretHash doesn't correspond to a registered lockSecretHash. lockSecretHash %s token %s",
			utils.Pex(lockSecretHash[:]), utils.HPex(c.ChannelIdentifier.ChannelIdentifier))
	}
	if ourKnown {
		lock := c.OurState.getLockByHashlock(lockSecretHash)
		log.Debug(fmt.Sprintf("lockSecretHash registered node=%s,from=%s,to=%s,token=%s,lockSecretHash=%s,amount=%s",
			utils.Pex(c.OurState.Address[:]), utils.Pex(c.OurState.Address[:]),
			utils.Pex(c.PartnerState.Address[:]), utils.APex(c.TokenAddress),
			utils.Pex(lockSecretHash[:]), lock.Amount))
		err := c.OurState.RegisterRevealedSecretHash(lockSecretHash, secret, blockNumber)
		if err == nil {
			//??????????????????,????????????????????????,?????????statemanager???????????????
		}
		return err
	}
	if partenerKnown {
		lock := c.PartnerState.getLockByHashlock(lockSecretHash)
		log.Debug(fmt.Sprintf("lockSecretHash registered node=%s,from=%s,to=%s,token=%s,lockSecretHash=%s,amount=%s",
			utils.Pex(c.OurState.Address[:]), utils.Pex(c.PartnerState.Address[:]),
			utils.Pex(c.OurState.Address[:]), utils.APex(c.TokenAddress),
			utils.Pex(lockSecretHash[:]), lock.Amount))
		return c.PartnerState.RegisterRevealedSecretHash(lockSecretHash, secret, blockNumber)
	}
	return nil
}

//RegisterTransfer register a signed transfer, updating the channel's state accordingly.
//????????????????????? channel ???balance Proof
/*
 *	RegisterTransfer : register a signed transfer, updating the channel's state accordingly.
 *		This transfer will change BalanceProof of this channel.
 */
func (c *Channel) RegisterTransfer(blocknumber int64, tr encoding.EnvelopMessager) error {
	var err error
	switch msg := tr.(type) {
	case *encoding.MediatedTransfer:
		err = c.registerMediatedTranser(msg, blocknumber)
	case *encoding.DirectTransfer:
		err = c.registerDirectTransfer(msg, blocknumber)
	case *encoding.UnLock:
		err = c.registerUnlock(msg, blocknumber)
	case *encoding.AnnounceDisposedResponse:
		err = c.RegisterAnnounceDisposedResponse(msg, blocknumber)
	case *encoding.RemoveExpiredHashlockTransfer:
		err = c.RegisterRemoveExpiredHashlockTransfer(msg, blocknumber)
	default:
		panic(fmt.Sprintf("receive unkonw transfer %s", tr))
	}
	return err
}

/*
PreCheckRecievedTransfer pre check received message(directtransfer,mediatedtransfer,refundtransfer) is valid or not
*/
func (c *Channel) PreCheckRecievedTransfer(tr encoding.EnvelopMessager) (fromState *EndState, toState *EndState, err error) {
	evMsg := tr.GetEnvelopMessage()
	if !c.isChannelIdentifierValid(evMsg) {
		err = rerr.ErrChannelIdentifierMismatch.Errorf("ch address mismatch,expect=%s,got=%s", c.ChannelIdentifier.String(), evMsg)
		return
	}
	if tr.GetSender() == c.OurState.Address {
		fromState = c.OurState
		toState = c.PartnerState
	} else if tr.GetSender() == c.PartnerState.Address {
		fromState = c.PartnerState
		toState = c.OurState
	} else {
		err = rerr.ErrChannelNotParticipant.Errorf("received transfer from unknown address =%s", utils.APex(tr.GetSender()))
		return
	}
	/*
			  nonce is changed only when a transfer is un/registered, if the test
		     fails either we are out of sync, a message out of order, or it's a
		     forged transfer
			Strictly monotonic value used to order transfers. The nonce starts at 1
	*/
	isInvalidNonce := evMsg.Nonce < 1 || evMsg.Nonce != fromState.nonce()+1
	//If a node data is damaged, then the channel will not work, so the data must not be damaged.
	if isInvalidNonce {
		/*
				may occur on normal operation
				??????Case:
			A-B????????????,??????A???????????????,B?????????,?????????A?????????????????????B????????????,B????????????nonce?????????????????????.
		*/
		log.Info(fmt.Sprintf("invalid nonce node=%s,from=%s,to=%s,expected nonce=%d,nonce=%d",
			utils.Pex(c.OurState.Address[:]), utils.Pex(fromState.Address[:]),
			utils.Pex(toState.Address[:]), fromState.nonce()+1, evMsg.Nonce))
		err = rerr.InvalidNonce(utils.StringInterface(tr, 3))
		return
	}
	//  transfer amount should never decrese.
	if evMsg.TransferAmount.Cmp(fromState.TransferAmount()) < 0 {
		log.Error(fmt.Sprintf("NEGATIVE TRANSFER node=%s,from=%s,to=%s,transfer=%s",
			utils.Pex(c.OurState.Address[:]), utils.Pex(fromState.Address[:]), utils.Pex(toState.Address[:]),
			utils.StringInterface(tr, 3))) //for nest struct
		err = rerr.ErrChannelTransferAmountDecrease
		return
	}
	return
}

/*
?????? unlock ??????:
1. nonce ,channel ??????
2. ???????????????????????????
3. transferAmount ?????????
4. locksroot ??????,????????????????????????
*/
/*
 *	registerUnlock : function to receive unlock message.
 *
 *		1. value of nonce and channel should be correct.
 *		2. verify that the secret actually unlock a related hashlock in Unlock message.
 *		3. transferAmount should be equal to the one in BalanceProof.
 *		4. locksroot should be correct, but the hashlock verified in step 2 has been removed.
 */
func (c *Channel) registerUnlock(tr *encoding.UnLock, blockNumber int64) (err error) {
	if c.IsClosed() {
		return rerr.ErrUpdateBalanceProofAfterClosed
	}
	fromState, _, err := c.PreCheckRecievedTransfer(tr)
	if err != nil {
		return
	}
	err = fromState.registerSecretMessage(tr)
	return err
}

/*
?????? DirectTransfer ??????:
1. nonce ,channel ??????
2. locksroot ?????????
3. ???????????????,??????????????????.
4. ???????????????????????????
*/
/*
 *	registerDirectTransfer : function to register direct transfer.
 *
 *		1. nonce and channel should be correct.
 *		2. locksroot should not have any change.
 *		3. transferAmount should increase, if no change, then throw error.
 *		4. sufficient tokens should remain in accounts in order to process transfer.
 */
func (c *Channel) registerDirectTransfer(tr *encoding.DirectTransfer, blockNumber int64) (err error) {
	if c.IsClosed() {
		return rerr.ErrUpdateBalanceProofAfterClosed
	}
	fromState, toState, err := c.PreCheckRecievedTransfer(tr)
	if err != nil {
		return
	}
	/*
		???????????????????????????
	*/
	// the amount of tokens this transfer takes.
	amount := new(big.Int).Set(tr.TransferAmount)
	amount = amount.Sub(amount, fromState.TransferAmount())
	/*
		??????????????????????????????????????????????????????,????????????
	*/
	// It is error that token amount is negative or above available balance.
	if amount.Cmp(utils.BigInt0) <= 0 {
		return rerr.ErrChannelTransferAmountMismatch.Errorf("direct transfer amount <0,amount=%s,message=%s", amount, tr)
	}
	if amount.Cmp(fromState.Distributable(toState)) > 0 {
		return rerr.ErrChannelTransferAmountMismatch.Errorf("direct transfer amount too large,amount=%s,availabe=%s", amount, fromState.Distributable(toState))
	}
	err = fromState.registerDirectTransfer(tr)
	return err
}

/*
?????? MediatedTransfer ??????:
1. nonce,channel ??????
2. locksroot ??????,???????????????????????????
3. transferAmount ?????????
4. ????????????,
*/
/*
 *	registerMediatedTransfer : function to register MediatedTransfer.
 *
 *		1. nonce and channel should be correct.
 *		2. locksroot should be correct but with one more lock.
 *		3. transferAmount should be equal.
 *		4. there should be sufficient fund deposited in
 */
func (c *Channel) registerMediatedTranser(tr *encoding.MediatedTransfer, blockNumber int64) (err error) {
	if c.IsClosed() {
		return rerr.ErrUpdateBalanceProofAfterClosed
	}
	fromState, toState, err := c.PreCheckRecievedTransfer(tr)
	if err != nil {
		return
	}
	/*
		???????????????????????????
	*/
	// the amount of tokens
	amount := tr.PaymentAmount
	/*
		??????????????????????????????????????????????????????,????????????
	*/
	// fault occurs that token amount is negative or above available amount.
	if amount.Cmp(utils.BigInt0) <= 0 {
		return rerr.ErrChannelTransferAmountMismatch.Errorf("mediated transfer amount <0,amount=%s,message=%s", amount, tr)
	}
	if amount.Cmp(fromState.Distributable(toState)) > 0 {
		return rerr.ErrInsufficientBalance
	}
	/*
				  For mediators: This is registering the *mediator* paying
		            transfer. The expiration of the lock must be `reveal_timeout`
		            blocks smaller than the *received* paying transfer. This cannot
		            be checked by the paying channel alone.

		            For the initiators: As there is no backing transfer, the
		            expiration is arbitrary, using the channel settle_timeout as an
		            upper limit because the node receiving the transfer will use it
		            as an upper bound while mediating.

		            For the receiver: A lock that expires after the settle period
		            just means there is more time to withdraw it.
	*/
	endSettlePeriod := c.GetSettleExpiration(blockNumber)
	expiresAfterSettle := tr.Expiration > endSettlePeriod
	/*
		????????????????????? settle timeout ?????????,?????????????????????
		???????????????????????? settle timeout ?????????,?????????????????????
		???????????????????????? settle timeout ???????????????????
		??????: A-B-C-D
		AB: settle timeout 1000
		BC settle timeout 10
		CD settle timeout 1000
		?????? ????????????20000,B ??????????????? A ??????????????????????????????21000
		B??? C ????????????21000,C??? D ????????????21000
		?????? BD ????????????,D ?????? B ??????, B close/settle ??????,?????? D ????????????????????????,???????????? token
	*/
	/*
	 *	I can not receive transfers after settle_timeout, not secure.
	 *	I can not send transfers after settle_timeout, not abide to contract rules.
	 *	Why my receiving transfers after settle_timeout is not secure?
	 *
	 *	Transfer : A-B-C-D
	 *	AB : settle_timeout 1000
	 *	BC : settle_timeout 10
	 *	CD : settle_timeout	1000
	 *
	 *	Assume that current block height is 20000, and transfer expiration that B received A is 21000.
	 *	transfer expiration in BC 21000, transfer expiration in CD 21000
	 * 	then BC can collude and D reveal secret to B, after B close/settle channel, D can register the secret on-chain
	 *	and steal tokens in BC.
	 */
	if expiresAfterSettle { //After receiving this lock, the party can close or updatetransfer on the chain, so that if the party does not have a password, he still can't get the money.
		log.Error(fmt.Sprintf("Lock expires after the settlement period. node=%s,from=%s,to=%s,lockexpiration=%d,currentblock=%d,end_settle_period=%d",
			utils.Pex(c.OurState.Address[:]), utils.Pex(fromState.Address[:]), utils.Pex(toState.Address[:]),
			tr.Expiration, blockNumber, endSettlePeriod))
		return rerr.ErrChannelLockExpirationTooLarge
	}
	err = fromState.registerMediatedMessage(tr)
	if err == nil {
		c.ExternState.funcRegisterChannelForHashlock(c, tr.LockSecretHash)
	}
	return err
}

/*
RegisterRemoveExpiredHashlockTransfer register a request to remove a expired hashlock and this hashlock must be sent out from the sender.
*/
func (c *Channel) RegisterRemoveExpiredHashlockTransfer(tr *encoding.RemoveExpiredHashlockTransfer, blockNumber int64) (err error) {
	return c.registerRemoveLock(tr, blockNumber, tr.LockSecretHash, true)
}

/*
RegisterAnnounceDisposedResponse ?????????????????????????????????????????????announceDisposedTransferResponse,
??????????????????????????????,?????????????????????????????????AnnounceDisposedTransfer.
*/
/*
 *	RegisterAnnounceDisposedResponse : function to register AnnounceDisposedRespnse, and send out or receive announceDisposedTransferResponse from channel partner.
 *
 *		Note that everytime a participant receives message from his partner, he must verify the AnnounceDisposedTransfer he sent out beforehand.
 */
func (c *Channel) RegisterAnnounceDisposedResponse(response *encoding.AnnounceDisposedResponse, blockNumber int64) (err error) {
	return c.registerRemoveLock(response, blockNumber, response.LockSecretHash, false)
}
func (c *Channel) registerRemoveLock(messager encoding.EnvelopMessager, blockNumber int64, lockSecretHash common.Hash, mustExpired bool) (err error) {
	if c.IsClosed() {
		return rerr.ErrUpdateBalanceProofAfterClosed
	}
	msg := messager.GetEnvelopMessage()
	fromState, _, err := c.PreCheckRecievedTransfer(messager)
	if err != nil {
		return
	}
	/*
		transfer amount should not change.
	*/
	if msg.TransferAmount.Cmp(fromState.TransferAmount()) != 0 {
		err = rerr.ErrChannelTransferAmountMismatch
		return
	}
	_, newtree, newlocksroot, err := fromState.TryRemoveHashLock(lockSecretHash, blockNumber, mustExpired)
	if err != nil {
		return err
	}
	/*
		locksroot????????????.
	*/
	if newlocksroot != msg.Locksroot {
		return rerr.InvalidLocksRoot(newlocksroot, msg.Locksroot)
	}
	fromState.Tree = newtree
	err = fromState.registerRemoveLock(messager, lockSecretHash)
	if err == nil {
		c.ExternState.db.RemoveLock(c.ChannelIdentifier.ChannelIdentifier, fromState.Address, lockSecretHash)
	}
	return err
}
func (c *Channel) isChannelIdentifierValid(evMsg *encoding.EnvelopMessage) bool {
	return evMsg.ChannelIdentifier == c.ChannelIdentifier.ChannelIdentifier &&
		evMsg.OpenBlockNumber == c.ChannelIdentifier.OpenBlockNumber
}

//GetNextNonce change nonce  means banlance proof state changed
func (c *Channel) GetNextNonce() uint64 {
	if c.OurState.nonce() != 0 {
		return c.OurState.nonce() + 1
	}
	// 0 must not be used since in the netting contract it represents null.
	return 1
}

/*
CreateDirectTransfer return a DirectTransfer message.

This message needs to be signed and registered with the channel before
sent.
*/
func (c *Channel) CreateDirectTransfer(amount *big.Int) (tr *encoding.DirectTransfer, err error) {
	if !c.CanTransfer() {
		return nil, rerr.ChannelStateError(c.State).Errorf("transfer not possible, no funding or channel closed")
	}
	from := c.OurState
	to := c.PartnerState
	distributable := from.Distributable(to)
	if amount.Cmp(utils.BigInt0) <= 0 || amount.Cmp(distributable) > 0 {
		log.Debug(fmt.Sprintf("Insufficient funds : amount=%s, Distributable=%s", amount, distributable))
		return nil, rerr.ErrInsufficientBalance
	}
	transferAmount := new(big.Int).Add(from.TransferAmount(), amount)
	currentLocksroot := from.Tree.MerkleRoot()
	nonce := c.GetNextNonce()
	bp := encoding.NewBalanceProof(nonce, transferAmount, currentLocksroot, &c.ChannelIdentifier)
	tr = encoding.NewDirectTransfer(bp)
	return
}

/*
CreateMediatedTransfer return a MediatedTransfer message.

This message needs to be signed and registered with the channel before
sent.

Args:
    initiator : The node that requested the transfer.
    target : The final destination node of the transfer
    amount : How much of a token is being transferred.
    expiration : The maximum block number until the transfer
        message can be received.
	fee: ?????????
*/
func (c *Channel) CreateMediatedTransfer(initiator, target common.Address, fee *big.Int, amount *big.Int, expiration int64, lockSecretHash common.Hash, path []common.Address) (tr *encoding.MediatedTransfer, err error) {
	if !c.CanTransfer() {
		return nil, rerr.ChannelStateError(c.State).Errorf("transfer not possible, no funding or channel closed")
	}
	if amount.Cmp(utils.BigInt0) <= 0 || amount.Cmp(c.Distributable()) > 0 {
		log.Info(fmt.Sprintf("Insufficient funds  amount=%s,Distributable=%s", amount, c.Distributable()))
		return nil, rerr.ErrInsufficientBalance
	}
	from := c.OurState
	lock := &mtree.Lock{
		Amount:         amount,
		Expiration:     expiration,
		LockSecretHash: lockSecretHash,
	}
	_, updatedLocksroot := from.computeMerkleRootWith(lock)
	transferAmount := from.TransferAmount()
	nonce := c.GetNextNonce()
	bp := encoding.NewBalanceProof(nonce, transferAmount, updatedLocksroot, &c.ChannelIdentifier)
	tr = encoding.NewMediatedTransfer(bp, lock, target, initiator, fee, path)
	return
}

//CreateUnlock creates  a unlock message
func (c *Channel) CreateUnlock(lockSecretHash common.Hash) (tr *encoding.UnLock, err error) {
	if c.IsClosed() {
		return nil, rerr.ErrUpdateBalanceProofAfterClosed
	}
	from := c.OurState
	lock, secret, err := from.getSecretByLockSecretHash(lockSecretHash)
	if err != nil {
		return nil, rerr.ErrChannelLockSecretHashNotFound.Errorf("no such lock for lockSecretHash:%s", utils.HPex(lockSecretHash))
	}
	_, locksrootWithPendingLockRemoved, err := from.computeMerkleRootWithout(lock)
	if err != nil {
		return
	}
	transferAmount := new(big.Int).Add(from.TransferAmount(), lock.Amount)
	nonce := c.GetNextNonce()
	bp := encoding.NewBalanceProof(nonce, transferAmount, locksrootWithPendingLockRemoved, &c.ChannelIdentifier)
	tr = encoding.NewUnlock(bp, secret)
	return
}

/*
CreateRemoveExpiredHashLockTransfer create this transfer to notify my patner that this hashlock is expired and i want to remove it .
*/
func (c *Channel) CreateRemoveExpiredHashLockTransfer(lockSecretHash common.Hash, blockNumber int64) (tr *encoding.RemoveExpiredHashlockTransfer, err error) {
	if c.IsClosed() {
		return nil, rerr.ErrUpdateBalanceProofAfterClosed
	}
	_, _, newlocksroot, err := c.OurState.TryRemoveHashLock(lockSecretHash, blockNumber, true)
	if err != nil {
		return
	}
	nonce := c.GetNextNonce()
	transferAmount := c.OurState.TransferAmount()
	bp := encoding.NewBalanceProof(nonce, transferAmount, newlocksroot, &c.ChannelIdentifier)
	tr = encoding.NewRemoveExpiredHashlockTransfer(bp, lockSecretHash)
	return
}

/*
CreateAnnounceDisposedResponse ????????????????????????AnnouceDisposedTransfer, ??????????????????.
*/
/*
 *	CreateAnnounceDisposedResponse : function to create message of AnnounceDisposedResponse.
 *	Note that a channel participant must first receive AnnounceDisposedTransfer, then he can
 */
func (c *Channel) CreateAnnounceDisposedResponse(lockSecretHash common.Hash, blockNumber int64) (tr *encoding.AnnounceDisposedResponse, err error) {
	if c.IsClosed() {
		return nil, rerr.ErrUpdateBalanceProofAfterClosed
	}
	_, _, newlocksroot, err := c.OurState.TryRemoveHashLock(lockSecretHash, blockNumber, false)
	if err != nil {
		return
	}
	nonce := c.GetNextNonce()
	transferAmount := c.OurState.TransferAmount()
	bp := encoding.NewBalanceProof(nonce, transferAmount, newlocksroot, &c.ChannelIdentifier)
	tr = encoding.NewAnnounceDisposedResponse(bp, lockSecretHash)
	return
}

/*
CreateAnnouceDisposed  ?????????????????????????????????
*/
/*
 *	CreateAnnouceDisposed : function to create message of AnnounceDisposed
 *	Note that it claims that I have abandoned a lock.
 */
func (c *Channel) CreateAnnouceDisposed(lockSecretHash common.Hash, blockNumber int64, reason rerr.StandardError) (tr *encoding.AnnounceDisposed, err error) {
	lock, _, _, err := c.PartnerState.TryRemoveHashLock(lockSecretHash, blockNumber, false)
	if err != nil {
		return
	}
	rp := &encoding.AnnounceDisposedProof{
		Lock: lock,
	}
	rp.ChannelIdentifier = c.ChannelIdentifier.ChannelIdentifier
	rp.OpenBlockNumber = c.ChannelIdentifier.OpenBlockNumber
	tr = encoding.NewAnnounceDisposed(rp, reason.ErrorCode, reason.ErrorMsg)
	return
}

func (c *Channel) preCheckChannelID(tr encoding.SignedMessager, id *encoding.ChannelIDInMessage) error {
	if c.ChannelIdentifier.ChannelIdentifier != id.ChannelIdentifier ||
		c.ChannelIdentifier.OpenBlockNumber != id.OpenBlockNumber {
		return rerr.ErrChannelIdentifierMismatch
	}
	if tr.GetSender() != c.OurState.Address && tr.GetSender() != c.PartnerState.Address {
		return rerr.ErrChannelInvalidSender
	}
	return nil
}

/*
RegisterAnnouceDisposed ??????????????? AnnouceDisposed ??????
??????????????????????????????.
*/
/*
 *	RegisterAnnouceDisposed : function to register message of AnnounceDisposed.
 *  Note that signature verification has been undergone.
 */
func (c *Channel) RegisterAnnouceDisposed(tr *encoding.AnnounceDisposed) (err error) {
	err = c.preCheckChannelID(tr, &tr.ChannelIDInMessage)
	if err != nil {
		return
	}
	var state = c.PartnerState
	if tr.GetSender() == c.PartnerState.Address {
		state = c.OurState
	}
	mlock := tr.Lock
	lock := state.GetUnkownSecretLockByHashlock(mlock.LockSecretHash)
	if lock == nil || mlock.LockSecretHash != lock.LockSecretHash ||
		mlock.Expiration != lock.Expiration ||
		mlock.Amount.Cmp(lock.Amount) != 0 {
		return rerr.ErrChannelLockMisMatch.Errorf("RegisterAnnouceDisposed lock not match,receive=%s, mine=%s", mlock, lock)
	}
	return nil
}

/*
CreateWithdrawRequest ???????????????????????????,??????????????????????????????????????????.
*/
/*
 *	CreateWithdrawRequest : function to create message of request withdraw.
 *	Note that there must not be any lock, or conflict will reside in token allocation.
 */
func (c *Channel) CreateWithdrawRequest(withdrawAmount *big.Int) (w *encoding.WithdrawRequest, err error) {
	/*
		withdraw ????????????????????????????????????
		??????????????? withdraw ??????,????????????????????????
		???????????????????????? close/settle.
		??????????????????????????????,???????????????????????????,??????????????? withdraw
	*/
	if len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
		err = rerr.ErrChannelWithdrawButHasLocks
	}
	d := new(encoding.WithdrawRequestData)
	d.ChannelIdentifier = c.ChannelIdentifier.ChannelIdentifier
	d.OpenBlockNumber = c.ChannelIdentifier.OpenBlockNumber
	d.Participant1 = c.OurState.Address
	d.Participant2 = c.PartnerState.Address
	d.Participant1Balance = c.OurState.Balance(c.PartnerState)
	d.Participant1Withdraw = withdrawAmount
	if withdrawAmount.Cmp(d.Participant1Balance) > 0 {
		err = rerr.ErrChannelWithdrawAmount.Errorf("withdraw amount too large,current=%s,withdraw=%s", w.Participant1Balance, withdrawAmount)
		return
	}
	w = encoding.NewWithdrawRequest(d)
	return
}

func (c *Channel) preCheckSettleDataInMessage(tr encoding.SignedMessager, sd *encoding.SettleDataInMessage) (err error) {
	if c.ChannelIdentifier.ChannelIdentifier != sd.ChannelIdentifier ||
		c.ChannelIdentifier.OpenBlockNumber != sd.OpenBlockNumber {
		return rerr.ErrChannelIdentifierMismatch
	}
	var state1, state2 *EndState
	if tr.GetSender() == c.OurState.Address {
		state1 = c.OurState
		state2 = c.PartnerState
	} else if tr.GetSender() == c.PartnerState.Address {
		state1 = c.PartnerState
		state2 = c.OurState
	} else {
		return rerr.ErrChannelInvalidSender
	}
	/*
		state1 ,state2??? participant1,participant2??????????????????,?????????????????????.
	*/
	if (state1.Address != sd.Participant1 && state1.Address != sd.Participant2) ||
		(state2.Address != sd.Participant1 && state2.Address != sd.Participant2) ||
		sd.Participant1 == sd.Participant2 {
		return rerr.ErrChannelNotParticipant
	}
	if state1.Address == sd.Participant1 {
		if state1.Balance(state2).Cmp(sd.Participant1Balance) != 0 ||
			state2.Balance(state1).Cmp(sd.Participant2Balance) != 0 {
			return rerr.ErrChannelBalanceNotMatch
		}
	} else {
		if state2.Balance(state1).Cmp(sd.Participant1Balance) != 0 ||
			state1.Balance(state2).Cmp(sd.Participant2Balance) != 0 {
			return rerr.ErrChannelBalanceNotMatch
		}
	}

	return nil
}

func (c *Channel) hasAnyLock() bool {
	if len(c.PartnerState.Lock2UnclaimedLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2UnclaimedLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 {
		return true
	}
	return false
}

/*RegisterWithdrawRequest :
1. ??????????????????
2. ????????????????????????StateWithdraw
*/
/*
 *	RegisterWithdrawRequest : function to register WithdrawRequest.
 *
 *		1. verify the information is correct.
 *		2. channel state must switch to StateWithdraw.
 */
func (c *Channel) RegisterWithdrawRequest(tr *encoding.WithdrawRequest) (err error) {
	if c.ChannelIdentifier.ChannelIdentifier != tr.ChannelIdentifier ||
		c.ChannelIdentifier.OpenBlockNumber != tr.OpenBlockNumber {
		return rerr.ErrChannelIdentifierMismatch
	}
	if tr.GetSender() != c.PartnerState.Address {
		return rerr.ErrChannelInvalidSender
	}
	if c.PartnerState.Balance(c.OurState).Cmp(tr.Participant1Balance) != 0 {
		return rerr.ErrChannelBalanceNotMatch
	}
	/*
		????????????????????? request ????????????,???????????????????????????,
		????????????????????????,????????????????????? announce disposed ????????????
		?????????????????????,???????????????????????????.
	*/
	if len(c.PartnerState.Lock2UnclaimedLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2UnclaimedLocks) > 0 {
		return rerr.ErrChannelWithdrawButHasLocks
	}
	c.State = channeltype.StatePartnerWithdrawing
	return nil
}

//HasAnyUnkonwnSecretTransferOnRoad ????????????????????????????????????,??????????????????????????????
/*
 *	HasAnyUnknownSecretTransferOnRoad : function to check whether there is any transfer sent out from me
 * 		that my partner has no idea about the secret.
 */
func (c *Channel) HasAnyUnkonwnSecretTransferOnRoad() bool {
	return len(c.OurState.Lock2PendingLocks) > 0
}

/*
CreateWithdrawResponse :
?????????????????????,????????? withdrawRequest ?????????,???????????????,
????????????????????????????????????.
??????????????????????????? withdrawRequest ?????????,????????????????????????,
???????????????????????????,??????????????????????????????.,?????????????????????,?????????????????????.????????????????????????
?????? withdraw ??? cooperative settle??????????????????????????????????????????,?????? statemanager ???????????????.
*/
/*
 *	CreateWithdrawResponse : function to create message of WithdrawResponse.
 *
 *	Note that there is possibilities that I send out another transfer when receiving `withdrawRequest` from my partner.
 * 	With no doubt that this transfer will fail because my partner has no chance to accept it. Even he accepts it, he still
 * 	can not get the token.
 * 	So withdraw and cooperative settle may both impact ongoing transfers which statemanager should deal with.
 */
func (c *Channel) CreateWithdrawResponse(req *encoding.WithdrawRequest) (w *encoding.WithdrawResponse, err error) {
	if len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 {
		log.Warn(fmt.Sprintf("CreateWithdrawResponse ,but i'm sending transfer on road,these transfer should canceled immediately"))
	}
	if len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
		panic("should no locks for partner state when  CreateWithdrawResponse")
	}
	wd := new(encoding.WithdrawReponseData)
	wd.ChannelIdentifier = c.ChannelIdentifier.ChannelIdentifier
	wd.OpenBlockNumber = c.ChannelIdentifier.OpenBlockNumber
	wd.Participant1 = c.PartnerState.Address
	wd.Participant2 = c.OurState.Address
	wd.Participant1Balance = c.PartnerState.Balance(c.OurState)
	wd.Participant1Withdraw = req.Participant1Withdraw
	w = encoding.NewWithdrawResponse(wd, rerr.ErrSuccess.ErrorCode, rerr.ErrSuccess.ErrorMsg)
	/*
		???????????????????????????,
	*/
	// re-verify message to ensure correctness.
	if req.Participant1Balance.Cmp(w.Participant1Balance) != 0 {
		panic(fmt.Sprintf("withdrawequest=%s,\nwithdrawresponse=%s", req, w))
	}
	return
}

//RegisterWithdrawResponse check withdraw response
//?????????????????????????????????????????????
/*
 *	RegisterWithdrawResponse : function to check withdraw response.
 *
 *	Explicit verify that withdraw response should be consistent with withdraw request.
 */
func (c *Channel) RegisterWithdrawResponse(tr *encoding.WithdrawResponse) error {
	if c.ChannelIdentifier.ChannelIdentifier != tr.ChannelIdentifier ||
		c.ChannelIdentifier.OpenBlockNumber != tr.OpenBlockNumber {
		return rerr.ErrChannelIdentifierMismatch
	}
	if tr.GetSender() != c.PartnerState.Address {
		return rerr.ErrChannelInvalidSender
	}
	if c.OurState.Balance(c.PartnerState).Cmp(tr.Participant1Balance) != 0 {
		return rerr.ErrChannelBalanceNotMatch
	}
	if len(c.PartnerState.Lock2UnclaimedLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2UnclaimedLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 {
		return rerr.ErrChannelWithdrawButHasLocks
	}
	if c.State != channeltype.StateWithdraw {
		return rerr.ErrChannelState.Printf("receive withdraw response but my channel state is %s", c.State)
	}
	return nil
}

/*
CreateCooperativeSettleRequest ???????????????????????????,??????????????????????????????????????????.
*/
/*
 *	CreateCooperativeSettleRequest : function to create message of CooperativeSettleRequest.
 *	Note that there should be no lock, or both participants may have conflict with token allocation.
 */
func (c *Channel) CreateCooperativeSettleRequest() (s *encoding.SettleRequest, err error) {
	/*
		SettleRequest ????????????????????????????????????
		??????????????? cooperative settle ??????,????????????????????????
		???????????????????????? close/settle.
		??????????????????????????????,???????????????????????????,??????????????? cooperative settle
	*/
	/*
	 *	Once SettleRequest sent out, channel has to be closed.
	 *	Channel reopens after being closed, via cooperative settle,
	 *	or participant send close/settle.
	 *	No matter which is the case, if one participant holds locks and has dispute about token amount,
	 *	they can not do cooperativesettle.
	 */
	if len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
		err = rerr.ErrChannelCooperativeSettleButHasLocks
	}
	wd := new(encoding.SettleRequestData)
	wd.ChannelIdentifier = c.ChannelIdentifier.ChannelIdentifier
	wd.OpenBlockNumber = c.ChannelIdentifier.OpenBlockNumber
	wd.Participant1 = c.OurState.Address
	wd.Participant2 = c.PartnerState.Address
	wd.Participant1Balance = c.OurState.Balance(c.PartnerState)
	wd.Participant2Balance = c.PartnerState.Balance(c.OurState)
	s = encoding.NewSettleRequest(wd)
	return
}

//RegisterCooperativeSettleRequest check settle request and update state
//???????????????????????????CooperativeRequest?????????,????????????CooperativeRequest???????????????????????????
func (c *Channel) RegisterCooperativeSettleRequest(msg *encoding.SettleRequest) error {
	err := c.preCheckSettleDataInMessage(msg, &msg.SettleDataInMessage)
	if err != nil {
		return err
	}
	/*
		?????????????????????,??????????????? settle request ?????????,?????????????????????
		???????????????????????????,????????????????????????
		?????????????????????????????????,?????????????????????????????? annouce disposed ????????????.
		?????????????????? settle request,?????? cooperative settle ??????????????????????!!
	*/
	/*
	 *	Can't hold any lock, except that I am sending out transfers right before settlerequest.
	 *	If I am the transfer initiator, assume sending transfer fails.
	 *	If I am the mediator, then handle transfer like announce disposed event.
	 *	which needs settle request, if cooperative settle failed, then how to deal with that?
	 */
	if len(c.PartnerState.Lock2UnclaimedLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2UnclaimedLocks) > 0 {
		return rerr.ErrChannelCooperativeSettleButHasLocks
	}
	c.State = channeltype.StatePartnerCooperativeSettling
	return nil
}

/*
CreateCooperativeSettleResponse :
?????????????????????,????????? settleRequest ?????????,???????????????,
????????????????????????????????????.
??????????????????????????? settleRequest ?????????,????????????????????????,
???????????????????????????,??????????????????????????????.,?????????????????????,?????????????????????.????????????????????????
?????? withdraw ??? cooperative settle??????????????????????????????????????????,?????? statemanager ???????????????.
*/
/*
 *	CreateCooperativeSettleResponse : function to create message of CooperativeSettleResponse.
 *	Note that a channel participant may send out another transfer to his partner, while receiving partner's settleRequest.
 *	With no doubt that this new transfer will fail because his channel partner has no chance to accept it.
 *	Even he accepts it, he cannot get that token.
 * 	So withdraw and cooperative settle may both impact ongoing transfers, which statemanager should handle.
 */
func (c *Channel) CreateCooperativeSettleResponse(req *encoding.SettleRequest) (res *encoding.SettleResponse, err error) {
	if len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2UnclaimedLocks) > 0 {
		log.Warn(fmt.Sprintf("CreateCooperativeSettleResponse ,but i'm sending transfer on road,these transfer should canceled immediately"))
	}
	if len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
		panic("should no locks for partner state when  CreateCooperativeSettleResponse")
	}
	d := new(encoding.SettleResponseData)
	d.ChannelIdentifier = c.ChannelIdentifier.ChannelIdentifier
	d.OpenBlockNumber = c.ChannelIdentifier.OpenBlockNumber
	d.Participant2 = c.OurState.Address
	d.Participant2Balance = c.OurState.Balance(c.PartnerState)
	d.Participant1 = c.PartnerState.Address
	d.Participant1Balance = c.PartnerState.Balance(c.OurState)

	res = encoding.NewSettleResponse(d, rerr.ErrSuccess.ErrorCode, rerr.ErrSuccess.ErrorMsg)
	/*
		???????????????????????????,
	*/
	// Re-verify message correctness.
	if req.Participant1Balance.Cmp(d.Participant1Balance) != 0 ||
		req.Participant2Balance.Cmp(d.Participant2Balance) != 0 {
		panic(fmt.Sprintf("settle request=%s,\n settle re=%s", req, res))
	}
	return
}

//RegisterCooperativeSettleResponse check settle response and update state
func (c *Channel) RegisterCooperativeSettleResponse(msg *encoding.SettleResponse) error {
	err := c.preCheckSettleDataInMessage(msg, &msg.SettleDataInMessage)
	if err != nil {
		return err
	}
	if c.State != channeltype.StateCooprativeSettle {
		return rerr.ErrChannelState.Printf("receive cooperative settle response but my channel state is %s", c.State)
	}
	return nil
}

/*
PrepareForWithdraw :
?????? withdraw ??? ??????settle ???????????????????????????,??????????????????????????????????????????
??????????????????????????????
*/
/*
 *	PrepareForWithdraw : function to change channel state to StatePrepareForWithdraw
 *	Note that because withdraw and cooperative settle require no lock,
 *	hence we should tag that any new transfer is forbidden, and after ongoing transfers finish,
 * 	we can do channel withdraw.
 */
func (c *Channel) PrepareForWithdraw() error {
	if c.State != channeltype.StateOpened {
		return rerr.ErrChannelNotAllowWithdraw.Printf("state must be opened when withdraw, but state is %s", c.State)
	}
	c.State = channeltype.StatePrepareForWithdraw
	return nil
}

/*
PrepareForCooperativeSettle :
?????? withdraw ??? ??????settle ???????????????????????????,??????????????????????????????????????????
??????????????????????????????
*/
/*
 *	PrepareForCooperativeSettle : function to switch channel state to StatePrepareForCooperativeSettle.
 *
 *	Note that because withdraw and cooperative settle require no lock,
 *	hence we should tag that any new transfer is forbidden, and after ongoing transfers finish,
 * 	we can do channel withdraw.
 */
func (c *Channel) PrepareForCooperativeSettle() error {
	if c.State != channeltype.StateOpened {
		return rerr.ChannelStateError(c.State)
	}
	c.State = channeltype.StatePrepareForCooperativeSettle
	return nil
}

/*
CancelWithdrawOrCooperativeSettle ??????????????????????????????????????????????????????,????????????
??????????????????????????? close
*/
/*
 *	CancelWithdrawCooperativeSettle : function to switch channel state to StateOpened.
 *
 *	Note that if we wait for some amount of time and found that we cannot cooperative settle, then we can cancel that
 *	Or directly invoke close.
 */
func (c *Channel) CancelWithdrawOrCooperativeSettle() error {
	if c.ExternState.ClosedBlock != 0 {
		return rerr.ChannelStateError(c.State)
	}
	if c.State != channeltype.StatePrepareForCooperativeSettle && c.State != channeltype.StatePrepareForWithdraw {
		return rerr.ChannelStateError(c.State) // fmt.Errorf("state is %s,cannot cancel withdraw or cooperative", c.State)
	}
	c.State = channeltype.StateOpened
	return nil
}

/*
CanWithdrawOrCooperativeSettle ?????????????????????????????????????????? withdraw ???cooperative settle
*/
/*
 *	CanWithdrawOrCooperativeSettle : function to check whether we can process Withdraw / CooperativeSettle.
 *
 *	Note that we can do withdraw / CooperativeSettle without lock.
 */
func (c *Channel) CanWithdrawOrCooperativeSettle() bool {
	if len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.OurState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2PendingLocks) > 0 ||
		len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
		return false
	}
	return true
}

//Close async close this channel
func (c *Channel) Close() (err error) {
	if c.State != channeltype.StateOpened {
		log.Warn(fmt.Sprintf("try to close channel %s,but it's state is %s", utils.HPex(c.ChannelIdentifier.ChannelIdentifier), c.State))
	}
	if c.State == channeltype.StateClosed ||
		c.State == channeltype.StateSettled {
		return rerr.ChannelStateError(c.State)
		//return fmt.Errorf("channel %s already closed or settled", utils.HPex(c.ChannelIdentifier.ChannelIdentifier))
	}
	/*
		????????????????????????????????????,?????????????????????????????????,??????????????????,
		????????????????????????,??????????????????????????????????????????,
		???????????????????????????,?????????????????????????????????.
		????????????:
		????????????,????????????????????????????????????,??????????????????????????????,??????unlock,????????????????????????????????????
		?????????????????????????????????????????????expiration
	*/
	//if len(c.PartnerState.Lock2PendingLocks) > 0 || len(c.PartnerState.Lock2UnclaimedLocks) > 0 {
	//	result = utils.NewAsyncResult()
	//	result.Result <- fmt.Errorf("try to close a channel,but I have partner's lock")
	//	return
	//}
	/*
		??????????????????????????????,???????????? tx ?????????,?????????????????????.?????????????????? state ??????,???????????? close
		????????????????????????????????????????????????????????????.
	*/
	/*
	 *	Things happen like crash while channel is closing, or failure when closing transaction.
	 *	We cannot forbid close just because channel state is abnormal.
	 *	State tag is used to prevent further receiving or sending transfers.
	 */

	bp := c.PartnerState.BalanceProofState
	err = c.ExternState.Close(bp)
	if err != nil {
		return
	}
	c.State = channeltype.StateClosing
	return nil
}

//Settle async settle this channel,blockNumber is the current blockNumber
func (c *Channel) Settle(blockNumber int64) (err error) {
	if c.State != channeltype.StateClosed {
		return rerr.ChannelStateError(c.State)
	}
	var MyTransferAmount, PartnerTransferAmount *big.Int
	var MyLocksroot, PartnerLocksroot common.Hash
	if c.OurState.BalanceProofState != nil {
		MyTransferAmount = c.OurState.BalanceProofState.ContractTransferAmount
		MyLocksroot = c.OurState.BalanceProofState.ContractLocksRoot
	} else {
		MyTransferAmount = utils.BigInt0
	}
	if c.PartnerState.BalanceProofState != nil {
		PartnerTransferAmount = c.PartnerState.BalanceProofState.ContractTransferAmount
		PartnerLocksroot = c.PartnerState.BalanceProofState.ContractLocksRoot
	} else {
		PartnerTransferAmount = utils.BigInt0
	}
	if c.ExternState.SettledBlock > blockNumber {
		return rerr.ErrChannelSettleTimeout
	}
	err = c.ExternState.Settle(MyTransferAmount, PartnerTransferAmount, MyLocksroot, PartnerLocksroot)
	if err != nil {
		return
	}
	c.State = channeltype.StateSettling
	return
}

//GetNeedRegisterSecrets find all secres need to reveal on secret
func (c *Channel) GetNeedRegisterSecrets(blockNumber int64) (secrets []common.Hash) {
	for _, l := range c.PartnerState.Lock2UnclaimedLocks {
		if l.Lock.Expiration > blockNumber-int64(c.RevealTimeout) && l.Lock.Expiration < blockNumber {
			//?????????????????????????????????
			// lower layer takes charge of handling issues that repeatitively happen.
			secrets = append(secrets, l.Secret)
		}
	}
	return
}

/*
CooperativeSettleChannel ??????????????? settle response, ??????????????????.
*/
/*
 *	CooperativeSettleChannel : function to undergo CooperativeSettle
 *
 *	Note that once a channel participant receives his partner's settle response, just close this channel.
 */
func (c *Channel) CooperativeSettleChannel(res *encoding.SettleResponse) (result *utils.AsyncResult) {
	w, err := c.CreateCooperativeSettleRequest()
	if err != nil {
		panic(err)
	}
	err = w.Sign(c.ExternState.privKey, w)
	if err != nil {
		panic(err)
	}
	return c.ExternState.TokenNetwork.CooperativeSettleAsync(
		res.Participant1, res.Participant2,
		res.Participant1Balance, res.Participant2Balance,
		w.Participant1Signature, res.Participant2Signature)
}

//CooperativeSettleChannelOnRequest ??????????????? settle requet, ????????????????????????,?????????????????????????????????
/*
 *	CooperativeSettleChannelOnRequest : function to handle channel cooperative channel request.
 *
 *	Note that this is case that a channel participant receives a cooperative settle request,
 *	but for some reasons that he has to close the channel immediately.
 */
func (c *Channel) CooperativeSettleChannelOnRequest(partnerSignature []byte, res *encoding.SettleResponse) (result *utils.AsyncResult) {
	return c.ExternState.TokenNetwork.CooperativeSettleAsync(
		res.Participant1, res.Participant2,
		res.Participant1Balance, res.Participant2Balance,
		partnerSignature, res.Participant2Signature,
	)
}

/*
Withdraw ??????????????? withdraw response,
???????????????????????????
*/
/*
 *	Withdraw : function to undergo channel withdraw.
 *
 *	Note that this function has to work after verify parameter is valid.
 */
func (c *Channel) Withdraw(res *encoding.WithdrawResponse) (result *utils.AsyncResult) {
	//????????????,??????????????????.
	// No record, need to re-write signature.
	w, err := c.CreateWithdrawRequest(res.Participant1Withdraw)
	if err != nil {
		panic(err)
	}
	err = w.Sign(c.ExternState.privKey, w)
	if err != nil {
		panic(err)
	}
	return c.ExternState.TokenNetwork.WithdrawAsync(
		res.Participant1, res.Participant2,
		res.Participant1Balance, res.Participant1Withdraw,
		w.Participant1Signature, res.Participant2Signature,
	)
}

// String fmt.Stringer
func (c *Channel) String() string {
	return fmt.Sprintf("{ContractBalance=%s,Balance=%s,Distributable=%s,locked=%s,transferAmount=%s,channelid=%s,partner=%s}",
		c.ContractBalance(), c.Balance(), c.Distributable(), c.Locked(), c.TransferAmount(), &c.ChannelIdentifier, utils.APex2(c.PartnerState.Address))
}

// NewChannelSerialization serialize the channel to save to database
func NewChannelSerialization(c *Channel) *channeltype.Serialization {
	var ourSecrets, partnerSecrets []*channeltype.KnownSecret
	for _, s := range c.OurState.Lock2UnclaimedLocks {
		ourSecrets = append(ourSecrets, &channeltype.KnownSecret{
			Secret:              s.Secret,
			IsRegisteredOnChain: s.IsRegisteredOnChain,
		})
	}
	for _, s := range c.PartnerState.Lock2UnclaimedLocks {
		partnerSecrets = append(partnerSecrets, &channeltype.KnownSecret{
			Secret:              s.Secret,
			IsRegisteredOnChain: s.IsRegisteredOnChain,
		})
	}
	s := &channeltype.Serialization{
		Key:                    c.ChannelIdentifier.ChannelIdentifier[:],
		ChannelIdentifier:      &c.ChannelIdentifier,
		TokenAddressBytes:      c.TokenAddress[:],
		PartnerAddressBytes:    c.PartnerState.Address[:],
		OurAddress:             c.OurState.Address,
		RevealTimeout:          c.RevealTimeout,
		OurBalanceProof:        c.OurState.BalanceProofState,
		PartnerBalanceProof:    c.PartnerState.BalanceProofState,
		OurLeaves:              c.OurState.Tree.Leaves,
		PartnerLeaves:          c.PartnerState.Tree.Leaves,
		OurKnownSecrets:        ourSecrets,
		PartnerKnownSecrets:    partnerSecrets,
		State:                  c.State,
		SettleTimeout:          c.SettleTimeout,
		OurContractBalance:     c.OurState.ContractBalance,
		PartnerContractBalance: c.PartnerState.ContractBalance,
		ClosedBlock:            c.ExternState.ClosedBlock,
		SettledBlock:           c.ExternState.SettledBlock,
	}
	return s
}
