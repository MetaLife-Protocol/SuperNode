package mediator

import (
	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/encoding"

	"math/big"

	"time"

	"github.com/MetaLife-Protocol/SuperNode/channel/channeltype"
	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/params"
	"github.com/MetaLife-Protocol/SuperNode/rerr"
	"github.com/MetaLife-Protocol/SuperNode/transfer"
	"github.com/MetaLife-Protocol/SuperNode/transfer/mediatedtransfer"
	"github.com/MetaLife-Protocol/SuperNode/transfer/route"
	"github.com/MetaLife-Protocol/SuperNode/utils"
	"github.com/ethereum/go-ethereum/common"
)

//NameMediatorTransition name for state manager
const NameMediatorTransition = "MediatorTransition"

/*
 Reduce the lock expiration by some additional blocks to prevent this exploit:
 The payee could reveal the secret on it's lock expiration block, the lock
 would be valid and the previous lock can be safely unlocked so the mediator
 would follow the secret reveal with a balance-proof, at this point the secret
 is known, the payee transfer is payed, and if the payer expiration is exactly
 reveal_timeout blocks away the mediator will be forced to close the channel
 to be safe.
*/
var stateSecretKnownMaps = map[string]bool{
	mediatedtransfer.StatePayeeSecretRevealed: true,
	mediatedtransfer.StatePayeeBalanceProof:   true,

	mediatedtransfer.StatePayerSecretRevealed:        true,
	mediatedtransfer.StatePayerWaitingRegisterSecret: true,
	mediatedtransfer.StatePayerBalanceProof:          true,
}
var stateTransferPaidMaps = map[string]bool{
	mediatedtransfer.StatePayeeBalanceProof: true,
	mediatedtransfer.StatePayerBalanceProof: true,
}

var stateTransferFinalMaps = map[string]bool{
	mediatedtransfer.StatePayeeExpired:      true,
	mediatedtransfer.StatePayeeBalanceProof: true,

	mediatedtransfer.StatePayerExpired:      true,
	mediatedtransfer.StatePayerBalanceProof: true,
}

//True if the lock has not expired.
func isLockValid(tr *mediatedtransfer.LockedTransferState, blockNumber int64) bool {
	return blockNumber <= tr.Expiration
}

/*
IsSafeToWait returns True if there are more than enough blocks to safely settle on chain and
    waiting is safe.
*/
func IsSafeToWait(tr *mediatedtransfer.LockedTransferState, revealTimeout int, blockNumber int64) bool {
	// A node may wait for a new balance proof while there are reveal_timeout
	// left, at that block and onwards it is not safe to wait.
	return blockNumber < tr.Expiration-int64(revealTimeout)
}

//IsValidRefund returns True if the refund transfer matches the original transfer.
func IsValidRefund(originTr *mediatedtransfer.LockedTransferState, originRoute *route.State, st *mediatedtransfer.ReceiveAnnounceDisposedStateChange) bool {
	//Ignore a refund from the target
	if st.Sender == originTr.Target {
		return false
	}
	if st.Sender != originRoute.HopNode() {
		return false
	}
	return originTr.Amount.Cmp(st.Lock.Amount) == 0 &&
		originTr.LockSecretHash == st.Lock.LockSecretHash &&
		originTr.Token == st.Token &&
		/*
			??????????????????,????????????????????? hash
		*/
		originTr.Expiration == st.Lock.Expiration
}

/*
True if this node needs to register secret on chain

    Only close the channel to withdraw on chain if the corresponding payee node
    has received, this prevents attacks were the payee node burns it's payment
    to force a close with the payer channel.
*/
func isSecretRegisterNeeded(tr *mediatedtransfer.MediationPairState, blockNumber int64) bool {
	payeeReceived := stateTransferPaidMaps[tr.PayeeState]
	payerPayed := stateTransferPaidMaps[tr.PayerState]
	channelClosed := tr.PayerRoute.State() == channeltype.StateClosed
	AlreadyRegisterring := tr.PayerState == mediatedtransfer.StatePayerWaitingRegisterSecret
	safeToWait := IsSafeToWait(tr.PayerTransfer, tr.PayerRoute.RevealTimeout(), blockNumber)
	//??????payer?????????????????????????????????
	//????????????????????????????????????,????????????
	//?????????????????????,????????????
	//???????????????????????????,????????????.
	return ((payeeReceived && !safeToWait) || channelClosed) && !AlreadyRegisterring && !payerPayed
}

//Return the transfer pairs that are not at a final state.
func getPendingTransferPairs(pairs []*mediatedtransfer.MediationPairState) (pendingPairs []*mediatedtransfer.MediationPairState) {
	for _, pair := range pairs {
		if !stateTransferFinalMaps[pair.PayeeState] || !stateTransferFinalMaps[pair.PayerState] {
			pendingPairs = append(pendingPairs, pair)
		}
	}
	return pendingPairs
}

func getExpiredTransferPairs(pairs []*mediatedtransfer.MediationPairState) (pendingPairs []*mediatedtransfer.MediationPairState) {
	for _, pair := range pairs {
		if pair.PayeeState == mediatedtransfer.StatePayeeExpired {
			pendingPairs = append(pendingPairs, pair)
		}
	}
	return pendingPairs
}

/*
Return the timeout blocks, it's the base value from which the payee's
    lock timeout must be computed.

    The payee lock timeout is crucial for safety of the mediated transfer, the
    value must be chosen so that the payee hop is forced to reveal the secret
    with sufficient time for this node to claim the received lock from the
    payer hop.

    The timeout blocks must be the smallest of:

    - payerTransfer.expiration: The payer lock expiration, to force the payee
      to reveal the secret before the lock expires.
    - payerRoute.settleTimeout: Lock expiration must be lower than
      the settlement period since the lock cannot be claimed after the channel is
      settled.
    - payerRoute.ClosedBlock: If the channel is closed then the settlement
      period is running and the lock expiration must be lower than number of
      blocks left.
*/
func getTimeoutBlocks(payerRoute *route.State, payerTransfer *mediatedtransfer.LockedTransferState, blockNumber int64) int64 {
	blocksUntilSettlement := int64(payerRoute.SettleTimeout())
	if payerRoute.ClosedBlock() != 0 {
		if blockNumber < payerRoute.ClosedBlock() {
			panic("ClosedBlock bigger than the lastest blocknumber")
		}
		blocksUntilSettlement -= blockNumber - payerRoute.ClosedBlock()
	}
	if blocksUntilSettlement > payerTransfer.Expiration-blockNumber {
		blocksUntilSettlement = payerTransfer.Expiration - blockNumber
	}
	log.Debug(fmt.Sprintf("get transfer lockSecretHash=%s, expiration=%d, now=%d, blocksUntilSettlement=%d",
		utils.HPex(payerTransfer.LockSecretHash), payerTransfer.Expiration, blockNumber, blocksUntilSettlement))
	return blocksUntilSettlement
}

//Check invariants that must hold.
//return error is better for production environment
func sanityCheck(state *mediatedtransfer.MediatorState) {
	if len(state.TransfersPair) == 0 {
		return
	}
	//if a transfer is paid we must know the secret
	for _, pair := range state.TransfersPair {
		if stateTransferPaidMaps[pair.PayerState] && state.Secret == utils.EmptyHash {
			panic(fmt.Sprintf("payer:a transfer is paid but we don't know the secret, payerstate=%s,payeestate=%s", pair.PayerState, pair.PayeeState))
		}
		if stateTransferPaidMaps[pair.PayeeState] && state.Secret == utils.EmptyHash {
			panic(fmt.Sprintf("payee:a transfer is paid but we don't know the secret,payerstate=%s,payeestate=%s", pair.PayerState, pair.PayeeState))
		}
	}
	//the "transitivity" for these values is checked below as part of
	//almost_equal check
	if len(state.TransfersPair) > 0 {
		firstPair := state.TransfersPair[0]
		if state.Hashlock != firstPair.PayerTransfer.LockSecretHash {
			panic("sanity check failed:state.LockSecretHash!=firstPair.PayerTransfer.LockSecretHash")
		}
		if state.Secret != utils.EmptyHash {
			if firstPair.PayerTransfer.Secret != state.Secret {
				panic("sanity check failed:firstPair.PayerTransfer.Secret!=state.Secret")
			}
		}
	}
	for _, p := range state.TransfersPair {
		if !p.PayerTransfer.AlmostEqual(p.PayeeTransfer) {
			panic("sanity check failed:PayerTransfer.AlmostEqual(p.PayeeTransfer)")
		}
		if p.PayerTransfer.Expiration < p.PayeeTransfer.Expiration {
			panic("sanity check failed:PayerTransfer.Expiration<=p.PayeeTransfer.Expiration")
		}
		if !mediatedtransfer.ValidPayerStateMap[p.PayerState] {
			panic(fmt.Sprint("sanity check failed: payerstate invalid :", p.PayerState))
		}
		if !mediatedtransfer.ValidPayeeStateMap[p.PayeeState] {
			panic(fmt.Sprint("sanity check failed: payee invalid :", p.PayeeState))
		}
	}
	pairs2 := state.TransfersPair[0 : len(state.TransfersPair)-1]
	for i := range pairs2 {
		original := state.TransfersPair[i]
		refund := state.TransfersPair[i+1]
		if !original.PayeeTransfer.AlmostEqual(refund.PayerTransfer) {
			panic("sanity check failed:original.PayeeTransfer.AlmostEqual(refund.PayerTransfer)")
		}
		if original.PayeeRoute.HopNode() == refund.PayerRoute.HopNode() {
			panic("sanity check failed:original.PayeeRoute.HopNode==refund.PayerRoute.HopNode")
		}
		if original.PayeeTransfer.Expiration < refund.PayerTransfer.Expiration {
			panic("sanity check failed:original.PayeeTransfer.Expiration>refund.PayerTransfer.Expiration")
		}
	}
}

//Clear the state if all transfer pairs have finalized
func clearIfFinalized(result *transfer.TransitionResult) *transfer.TransitionResult {
	if result.NewState == nil {
		return result
	}
	state := result.NewState.(*mediatedtransfer.MediatorState)
	isAllFinalized := true
	for _, p := range state.TransfersPair {
		if !stateTransferPaidMaps[p.PayeeState] || !stateTransferPaidMaps[p.PayerState] {
			isAllFinalized = false
			break
		}
	}
	if isAllFinalized {
		// ??????????????????state manager???
		return &transfer.TransitionResult{
			NewState: nil,
			Events: append(result.Events, &mediatedtransfer.EventRemoveStateManager{
				Key: utils.Sha3(state.LockSecretHash[:], state.Token[:]),
			}),
		}
	}
	return result
}

/*
Finds the first route available that may be used.
        rss  : Current available routes that may be used,
            it's assumed that the available_routes list is ordered from best to
            worst.
        timeoutBlocks : Base number of available blocks used to compute
            the lock timeout.
        transferAmount : The amount of tokens that will be transferred
            through the given route.
    Returns:
         The next route.
*/
/*
????????????????????????????????? route
1.??????????????????
2.??????????????? ????????????
3.?????????????????????
*/

func nextRoute(fromRoute *route.State, rss *route.RoutesState, timeoutBlocks int, transferAmount, fee *big.Int) (routeCanUse *route.State, err error) {
	for len(rss.AvailableRoutes) > 0 {
		route := rss.AvailableRoutes[0]
		ch := route.Channel()
		rss.AvailableRoutes = rss.AvailableRoutes[1:]
		lockTimeout := timeoutBlocks - route.RevealTimeout()
		// ??????????????????
		if !route.CanTransfer() {
			err = rerr.ErrNoAvailabeRoute.Errorf("channel with %s-%s can not transfer because state=%s",
				utils.APex(ch.OurState.Address),
				utils.APex(ch.PartnerState.Address),
				ch.State)
			rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
			continue
		}
		// ??????????????????
		if route.AvailableBalance().Cmp(transferAmount) < 0 {
			err = rerr.ErrNoAvailabeRoute.Errorf("channel with %s-%s can not transfer because balance not enough",
				utils.APex(ch.OurState.Address),
				utils.APex(ch.PartnerState.Address))
			rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
			continue
		}
		// ???????????????lock??????????????????
		if lockTimeout <= 0 {
			err = rerr.ErrNoAvailabeRoute.Errorf("channel with %s-%s can not transfer because too near to lock expiration",
				utils.APex(ch.OurState.Address),
				utils.APex(ch.PartnerState.Address))
			rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
			continue
		}
		// ???????????????
		if fee.Cmp(route.Fee) < 0 {
			err = rerr.ErrNoAvailabeRoute.Errorf("channel with %s-%s can not transfer because no enough fee: need %d ,left fee %d",
				utils.APex(ch.OurState.Address),
				utils.APex(ch.PartnerState.Address),
				route.Fee.Int64(),
				fee.Int64())
			rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
			continue
		}
		if route.HopNode() == fromRoute.HopNode() {
			err = rerr.ErrNoAvailabeRoute.Errorf("channel with %s-%s can not transfer because cycle route",
				utils.APex(ch.OurState.Address),
				utils.APex(ch.PartnerState.Address))
			rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
			continue
		}
		routeCanUse = route
		break
		/*
				1.??????????????????
				2. ?????????????????????
				3. ???????????????
				4. ????????????????????????
				5. ??????????????????????????????????????????.
			 ??????????????????????????????,????????????????????????????????????????????????,??????????????????????????????????????? lockedTransfer
		*/
		//if route.CanTransfer() && route.AvailableBalance().Cmp(transferAmount) >= 0 && lockTimeout > 0 && fee.Cmp(route.Fee) >= 0 && route.HopNode() != fromRoute.HopNode() {
		//	return route
		//}
		//rss.IgnoredRoutes = append(rss.IgnoredRoutes, route)
	}
	return
}

/*
Given a payer transfer tries a new route to proceed with the mediation.

        payerRoute  : The previous route in the path that provides
            the token for the mediation.
        payerTransfer : The transfer received from the
            payerRoute.
        routesState  : Current available routes that may be used,
            it's assumed that the available_routes list is ordered from best to
            worst.
        timeoutBlocks : Base number of available blocks used to compute
            the lock timeout.
        blockNumber  : The current block number.
*/
func nextTransferPair(payerRoute *route.State, payerTransfer *mediatedtransfer.LockedTransferState,
	routesState *route.RoutesState, timeoutBlocks int, blockNumber int64) (
	transferPair *mediatedtransfer.MediationPairState, events []transfer.Event, err error) {
	if timeoutBlocks <= 0 {
		panic("timeoutBlocks<=0")
	}
	if int64(timeoutBlocks) > payerTransfer.Expiration-blockNumber {
		panic("timeoutBlocks >payerTransfer.Expiration-blockNumber")
	}
	payeeRoute, err := nextRoute(payerRoute, routesState, timeoutBlocks, payerTransfer.Amount, payerTransfer.Fee)
	if payeeRoute == nil {
		return
	}
	/*
				????????? payeeroute ??? settle timeout ?????????,????????????????????????lockexpiration ?????????,??????????????????.
			??????????????????????????????,???????????????????
		?????? timeoutBlocks ?????? settle timout ?????????????????????

	*/
	if timeoutBlocks >= payeeRoute.SettleTimeout() {
		timeoutBlocks = payeeRoute.SettleTimeout()
	}
	//??????????????????,???????????????,??????????????????????????? payee ??? settle timeout ??????
	lockTimeout := timeoutBlocks //- payeeRoute.RevealTimeout()
	lockExpiration := int64(lockTimeout) + blockNumber
	payeeTransfer := &mediatedtransfer.LockedTransferState{
		TargetAmount:   payerTransfer.TargetAmount,
		Amount:         big.NewInt(0).Sub(payerTransfer.Amount, payeeRoute.Fee),
		Token:          payerTransfer.Token,
		Initiator:      payerTransfer.Initiator,
		Target:         payerTransfer.Target,
		Expiration:     lockExpiration,
		LockSecretHash: payerTransfer.LockSecretHash,
		Secret:         payerTransfer.Secret,
		Fee:            big.NewInt(0).Sub(payerTransfer.Fee, payeeRoute.Fee),
	}
	if payeeRoute.HopNode() == payeeTransfer.Target {
		//i'm the last hop,so take the rest of the fee
		payeeTransfer.Fee = utils.BigInt0
		payeeTransfer.Amount = payerTransfer.TargetAmount
	}
	//todo log how many tokens fee for this transfer .
	transferPair = mediatedtransfer.NewMediationPairState(payerRoute, payeeRoute, payerTransfer, payeeTransfer)
	eventSendMediatedTransfer := mediatedtransfer.NewEventSendMediatedTransfer(payeeTransfer, payeeRoute.HopNode(), payeeRoute.Path)
	eventSendMediatedTransfer.FromChannel = payerRoute.ChannelIdentifier
	events = []transfer.Event{eventSendMediatedTransfer}
	return
}

/*
Set the state of a transfer *sent* to a payee and check the secret is
    being revealed backwards.

        The elements from transfers_pair are changed in place, the list must
        contain all the known transfers to properly check reveal order.
*/
func setPayeeStateAndCheckRevealOrder(transferPair []*mediatedtransfer.MediationPairState, payeeAddress common.Address,
	newPayeeState string) []transfer.Event {
	if !mediatedtransfer.ValidPayeeStateMap[newPayeeState] {
		panic(fmt.Sprintf("invalid payeestate:%s", newPayeeState))
	}
	WrongRevealOrder := false
	for j := len(transferPair) - 1; j >= 0; j-- {
		back := transferPair[j]
		if back.PayeeRoute.HopNode() == payeeAddress {
			back.PayeeState = newPayeeState
			break
		} else if !stateSecretKnownMaps[back.PayeeState] {
			WrongRevealOrder = true
		}
	}
	if WrongRevealOrder {
		/*
					   TODO: Append an event for byzantine behavior.
			         XXX: With the current events_for_withdraw implementation this may
			         happen, should the notification about byzantine behavior removed or
			         fix the events_for_withdraw function fixed?
		*/
		return nil
	}
	return nil
}

/*
Set the state of expired transfers, and return the failed events
?????????????????????
payer??? expiration>=payee ??? expiration ????????????
*/
/*
 *	set the state of expired transfers, and return the failed events.
 *	According to current rule, that expiration between payer and payee can be equal.
 */
func setExpiredPairs(transfersPairs []*mediatedtransfer.MediationPairState, blockNumber int64) (events []transfer.Event, allExpired bool) {
	pendingTransfersPairs := getPendingTransferPairs(transfersPairs)
	allExpired = len(pendingTransfersPairs) == 0
	for _, pair := range pendingTransfersPairs {
		if blockNumber > pair.PayerTransfer.Expiration {
			if pair.PayeeState != mediatedtransfer.StatePayeeExpired {
				//????????????, ????????? expiration ?????????????????????
				// we cannot be certain, both expiration can be identical.
			}
			if pair.PayeeTransfer.Expiration > pair.PayerTransfer.Expiration {
				panic("PayeeTransfer.Expiration>=pair.PayerTransfer.Expiration")
			}
			if pair.PayerState != mediatedtransfer.StatePayerExpired {
				pair.PayerState = mediatedtransfer.StatePayerExpired
				withdrawFailed := &mediatedtransfer.EventWithdrawFailed{
					LockSecretHash:    pair.PayerTransfer.LockSecretHash,
					ChannelIdentifier: pair.PayerRoute.ChannelIdentifier,
					Reason:            "lock expired",
				}
				events = append(events, withdrawFailed)
			}
		}
		/*
			?????????????????????,?????????????????????????????????remove
			??????????????????????????????:
			1. ??????????????????,??????payee???reveal secret,????????????????????????????????????BalanceProof
			2. ??????????????????,??????payee???AnnouceDisposed, ?????????????????????,??????????????????
			3. ??????????????????,?????????????????????????????????,????????????????????????????????????????????????,??????????????????
		*/
		if blockNumber-params.ForkConfirmNumber > pair.PayeeTransfer.Expiration {
			/*
			   For safety, the correct behavior is:

			   - If the payee has been paid, then the payer must pay too.

			     And the corollary:

			   - If the payer transfer has expired, then the payee transfer must
			     have expired too.

			   The problem is that this corollary cannot be asserted. If a user
			   is running Photon without a monitoring service, then it may go
			   offline after having paid a transfer to a payee, but without
			   getting a balance proof of the payer, and once it comes back
			   online the transfer may have expired.
			*/
			if pair.PayeeTransfer.Expiration > pair.PayerTransfer.Expiration {
				panic("PayeeTransfer.Expiration>=pair.PayerTransfer.Expiration")
			}
			if pair.PayeeState != mediatedtransfer.StatePayeeExpired {
				pair.PayeeState = mediatedtransfer.StatePayeeExpired
				unlockFailed := &mediatedtransfer.EventUnlockFailed{
					LockSecretHash:    pair.PayeeTransfer.LockSecretHash,
					ChannelIdentifier: pair.PayeeRoute.ChannelIdentifier,
					Reason:            "lock expired",
				}
				events = append(events, unlockFailed)
			}
		}
	}
	//// ?????????????????????,?????????????????????????????????remove
	//expiredPairs := getExpiredTransferPairs(transfersPairs)
	//for _, pair := range expiredPairs {
	//	if blockNumber-params.ForkConfirmNumber > pair.PayeeTransfer.Expiration {
	//		unlockFailed := &mediatedtransfer.EventUnlockFailed{
	//			LockSecretHash:    pair.PayeeTransfer.LockSecretHash,
	//			ChannelIdentifier: pair.PayeeRoute.ChannelIdentifier,
	//			Reason:            "lock expired",
	//		}
	//		events = append(events, unlockFailed)
	//	}
	//}
	return
}

/*
Refund the transfer.

        refundRoute   The original route that sent the mediated
            transfer to this node.
        refundTransfer (LockedTransferState): The original mediated transfer
            from the refundRoute.
    Returns:
        create a annouceDisposed event
*/
func eventsForRefund(refundRoute *route.State, refundTransfer *mediatedtransfer.LockedTransferState, reason rerr.StandardError) (events []transfer.Event) {
	/*
		????????????????????????????????????
	*/
	// abandon this lock just fine.
	rtr2 := &mediatedtransfer.EventSendAnnounceDisposed{
		Token:          refundTransfer.Token,
		Amount:         new(big.Int).Set(refundTransfer.Amount),
		LockSecretHash: refundTransfer.LockSecretHash,
		Expiration:     refundTransfer.Expiration,
		Receiver:       refundRoute.HopNode(),
		Reason:         reason,
	}
	events = append(events, rtr2)
	return
}

/*
Reveal the secret backwards.

    This node is named N, suppose there is a mediated transfer with two refund
    transfers, one from B and one from C:

        A-N-B...B-N-C..C-N-D

    Under normal operation N will first learn the secret from D, then reveal to
    C, wait for C to inform the secret is known before revealing it to B, and
    again wait for B before revealing the secret to A.

    If B somehow sent a reveal secret before C and D, then the secret will be
    revealed to A, but not C and D, meaning the secret won't be propagated
    forward. Even if D sent a reveal secret at about the same time, the secret
    will only be revealed to B upon confirmation from C.

    Even though B somehow learnt the secret out-of-order N is safe to proceed
    with the protocol, the transitBlocks configuration adds enough time for
    the reveal secrets to propagate backwards and for B to send the balance
    proof. If the proof doesn't arrive in time and the lock's expiration is at
    risk, N won't lose tokens since it knows the secret can go on-chain at any
    time.
??????transfersPair,??????????????????????????????
????????????????????????????????????????????????????????? reveal timeout,??????????????????????????????????????????,????????????????????????????????????????????????????
?????????????????? reveal secret ?????????????
*/
/*
 *	Reveal the secret backwards.
 *
 *   This node is named N, suppose there is a mediated transfer with two refund
 *   transfers, one from B and one from C:
 *
 *       A-N-B...B-N-C..C-N-D
 *
 *   Under normal operation N will first learn the secret from D, then reveal to
 *  C, wait for C to inform the secret is known before revealing it to B, and
 *   again wait for B before revealing the secret to A.
 *
 *   If B somehow sent a reveal secret before C and D, then the secret will be
 *   revealed to A, but not C and D, meaning the secret won't be propagated
 *   forward. Even if D sent a reveal secret at about the same time, the secret
 *   will only be revealed to B upon confirmation from C.
 *
 *   Even though B somehow learnt the secret out-of-order N is safe to proceed
 *   with the protocol, the transitBlocks configuration adds enough time for
 *   the reveal secrets to propagate backwards and for B to send the balance
 *   proof. If the proof doesn't arrive in time and the lock's expiration is at
 *   risk, N won't lose tokens since it knows the secret can go on-chain at any
 *   time.
 *
 *   All these transfersPair are mediated transfers I involve in.
 *	 If the time is near reveal timeout set by previous node, does this leads to interlock effect that all participants register secret on-chain?
 */
func eventsForRevealSecret(transfersPair []*mediatedtransfer.MediationPairState, ourAddress common.Address, blockNumber int64) (events []transfer.Event) {
	for j := len(transfersPair) - 1; j >= 0; j-- {
		pair := transfersPair[j]
		isPayeeSecretKnown := stateSecretKnownMaps[pair.PayeeState]
		isPayerSecretKnown := stateSecretKnownMaps[pair.PayerState]
		// ??????????????????,??????????????????????????????,?????????secret?????????
		isExpired := blockNumber > pair.PayerTransfer.Expiration
		tr := pair.PayerTransfer
		if isPayeeSecretKnown && !isPayerSecretKnown && !isExpired {
			pair.PayerState = mediatedtransfer.StatePayerSecretRevealed
			revealSecret := &mediatedtransfer.EventSendRevealSecret{
				LockSecretHash: tr.LockSecretHash,
				Secret:         tr.Secret,
				Token:          tr.Token,
				Receiver:       pair.PayerRoute.HopNode(),
				Sender:         ourAddress,
			}
			events = append(events, revealSecret)
		}
		if tr.Fee.Cmp(big.NewInt(0)) > 0 {
			events = append(events, &mediatedtransfer.EventSaveFeeChargeRecord{
				LockSecretHash: tr.LockSecretHash,
				TokenAddress:   tr.Token,
				TransferFrom:   tr.Initiator,
				TransferTo:     tr.Target,
				TransferAmount: tr.TargetAmount,
				InChannel:      pair.PayerRoute.ChannelIdentifier,
				OutChannel:     pair.PayeeRoute.ChannelIdentifier,
				Fee:            new(big.Int).Sub(pair.PayerTransfer.Fee, pair.PayeeTransfer.Fee),
				Timestamp:      time.Now().Unix(),
				BlockNumber:    blockNumber,
			})
		}
	}
	return events
}

//Send the balance proof to nodes that know the secret.
func eventsForBalanceProof(transfersPair []*mediatedtransfer.MediationPairState, blockNumber int64) (events []transfer.Event) {
	for j := len(transfersPair) - 1; j >= 0; j-- {
		pair := transfersPair[j]
		payeeKnowsSecret := stateSecretKnownMaps[pair.PayeeState]
		payeePayed := stateTransferPaidMaps[pair.PayeeState]
		payeeChannelOpen := pair.PayeeRoute.State() == channeltype.StateOpened
		/*
			??????????????????????????????,???????????????unlock,??????????????????????????????,??????????????????????????????
		*/
		// If previous channel closed, don't send unlock and let next node to register secret
		payerChannelOpen := pair.PayerRoute.State() == channeltype.StateOpened

		/*
			????????????????????????????????????????????????????????? reveal timeout,??????????????????????????????????????????.
				????????????????????????,?????????????????????????????????,????????????????????????????????????????????????????????????,??????????????????.
		*/
		// If the time I receive secret from my previous node is near reveal_timeout, then we better do nothing.
		// to force failure of this transfer or next node to register secret.
		// My partner should not reveal this secret to me near reveal_timeout instead that he should reveal it ahead.
		payerTransferInDanger := blockNumber > pair.PayerTransfer.Expiration-int64(pair.PayerRoute.RevealTimeout())

		lockValid := isLockValid(pair.PayeeTransfer, blockNumber)
		if payerChannelOpen && payeeChannelOpen && payeeKnowsSecret && !payeePayed && lockValid && !payerTransferInDanger {
			pair.PayeeState = mediatedtransfer.StatePayeeBalanceProof
			tr := pair.PayeeTransfer
			balanceProof := &mediatedtransfer.EventSendBalanceProof{
				LockSecretHash:    tr.LockSecretHash,
				ChannelIdentifier: pair.PayeeRoute.ChannelIdentifier,
				Token:             tr.Token,
				Receiver:          pair.PayeeRoute.HopNode(),
			}
			unlockSuccess := &mediatedtransfer.EventUnlockSuccess{
				LockSecretHash: pair.PayerTransfer.LockSecretHash,
			}
			events = append(events, balanceProof, unlockSuccess)
		}
	}
	return
}

/*
Close the channels that are in the unsafe region prior to an on-chain
    withdraw
??????????????????????????????????????????????????????,??????????????? pair ??????
???????????????????????????????????????????
*/
/*
 *	Close the channels that are in the unsafe region prior to an on-chain withdraw
 *	All channel participants should be responsbile to send reveal secret when necessary.
 */
func eventsForRegisterSecret(transfersPair []*mediatedtransfer.MediationPairState, blockNumber int64) (events []transfer.Event) {
	pendings := getPendingTransferPairs(transfersPair)
	needRegisterSecret := false
	for j := len(pendings) - 1; j >= 0; j-- {
		pair := pendings[j]
		if isSecretRegisterNeeded(pair, blockNumber) {
			//??????????????????????????????,????????? pair ????????????????????????StatePayerWaitingRegisterSecret
			// we only need to send reveal secret once, all pairs state should switch to StatePayerWaitingRegisterSecret.
			if needRegisterSecret {
				pair.PayerState = mediatedtransfer.StatePayerWaitingRegisterSecret
			} else {
				needRegisterSecret = true
				pair.PayerState = mediatedtransfer.StatePayerWaitingRegisterSecret
				registerSecretEvent := &mediatedtransfer.EventContractSendRegisterSecret{
					Secret: pair.PayeeTransfer.Secret,
				}
				events = append(events, registerSecretEvent)
			}
		}
	}
	return
}

/*
Set the state of the `payeeAddress` transfer, check the secret is
    being revealed backwards, and if necessary send out RevealSecret,
    SendBalanceProof, and Withdraws.
*/
func secretLearned(state *mediatedtransfer.MediatorState, secret common.Hash, payeeAddress common.Address, newPayeeState string) *transfer.TransitionResult {
	if !stateSecretKnownMaps[newPayeeState] {
		panic(fmt.Sprintf("%s not in STATE_SECRET_KNOWN", newPayeeState))
	}
	if state.Secret == utils.EmptyHash {
		state.SetSecret(secret)
	}
	var events []transfer.Event
	eventsWrongOrder := setPayeeStateAndCheckRevealOrder(state.TransfersPair, payeeAddress, newPayeeState)
	eventsSecretReveal := eventsForRevealSecret(state.TransfersPair, state.OurAddress, state.BlockNumber)
	eventBalanceProof := eventsForBalanceProof(state.TransfersPair, state.BlockNumber)
	eventsRegisterSecretEvent := eventsForRegisterSecret(state.TransfersPair, state.BlockNumber)
	events = append(events, eventsWrongOrder...)
	events = append(events, eventsSecretReveal...)
	events = append(events, eventBalanceProof...)
	events = append(events, eventsRegisterSecretEvent...)
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}

/*
Try a new route or fail back to a refund.

    The mediator can safely try a new route knowing that the tokens from
    payer_transfer will cover the expenses of the mediation. If there is no
    route available that may be used at the moment of the call the mediator may
    send a refund back to the payer, allowing the payer to try a different
    route.
*/
func mediateTransfer(state *mediatedtransfer.MediatorState, payerRoute *route.State, payerTransfer *mediatedtransfer.LockedTransferState) *transfer.TransitionResult {
	var transferPair *mediatedtransfer.MediationPairState
	var events []transfer.Event

	timeoutBlocks := int(getTimeoutBlocks(payerRoute, payerTransfer, state.BlockNumber))
	//log.Trace(fmt.Sprintf("timeoutBlocks=%d,payerroute=%s,payertransfer=%s,blocknumber=%d",
	//	timeoutBlocks, utils.StringInterface(payerRoute, 3), utils.StringInterface(payerTransfer, 3),
	//	state.BlockNumber,
	//))
	var err error
	if timeoutBlocks > 0 {
		transferPair, events, err = nextTransferPair(payerRoute, payerTransfer, state.Routes, timeoutBlocks, state.BlockNumber)
	}
	if transferPair == nil {
		if err != nil {
			log.Warn(err.Error())
		} else {
			log.Warn("no usable route, reject")
			err = rerr.ErrNoAvailabeRoute
		}
		/*
			???????????????,?????????????????????????????????

		*/
		/*
		 *	Exit this transfer, like never received it.
		 */
		originalTransfer := payerTransfer
		originalRoute := payerRoute
		refundEvents := eventsForRefund(originalRoute, originalTransfer, err.(rerr.StandardError))
		return &transfer.TransitionResult{
			NewState: state,
			Events:   refundEvents,
		}
	}
	/*
		????????????reveal_timeout????????????????????????,?????????????????????????????????????????????,??????????????????????????????????????????,???????????????????????????????????????,
		????????????????????????????????????????????????????????????,???????????????????????????
		??????????????????????????????announce disposed??????????????????,????????????????????????,???????????????????????????
		???????????????????????????reveal_timeout????????????
	*/
	/*
		Here, the number of locks held is limited according to reveal_timeout. If the locks held at the same time are more than a certain value,
		the other party may not be a trustworthy node and no longer receive transactions from the other party.
		This is to avoid the cooperation between the payer and payee in using time difference to attack me, causing me to lose money.

		It is possible to reject the transaction directly through announcement disposed, so that the purpose of rejection is achieved and the transaction is not stuck.
		Reveal_timeout is used directly as a threshold value temporarily.
	*/
	payerChannel := transferPair.PayerRoute.Channel()
	if len(payerChannel.PartnerState.Lock2PendingLocks)+len(payerChannel.PartnerState.Lock2UnclaimedLocks) > payerChannel.RevealTimeout {
		log.Warn(fmt.Sprintf("holding too much lock of %s, reject new mediated transfer from him", utils.APex2(payerChannel.PartnerState.Address)))
		return &transfer.TransitionResult{
			NewState: state,
			Events:   eventsForRefund(payerRoute, payerTransfer, rerr.ErrRejectTransferBecauseChannelHoldingTooMuchLock),
		}
	}
	/*
				   the list must be ordered from high to low expiration, expiration
		         handling depends on it
	*/
	state.TransfersPair = append(state.TransfersPair, transferPair)
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}

/*

 */
func cancelCurrentRoute(state *mediatedtransfer.MediatorState, refundChannelIdentify common.Hash) *transfer.TransitionResult {
	var it = &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
	l := len(state.TransfersPair)
	if l <= 0 {
		log.Error(fmt.Sprintf("recevie refund ,but has no transfer pair ,must be a attack!!"))
		return it
	}
	transferPair := state.TransfersPair[l-1]
	state.TransfersPair = state.TransfersPair[:l-1] //??????????????????
	/*
		if refund msg came from payer, panic, something must wrong!
	*/
	if refundChannelIdentify == transferPair.PayerRoute.ChannelIdentifier {
		panic("receive refund/withdraw/cooperateSettle from payer,that should happen")
	}
	/*
		?????????????????????payer???????????????,????????????????????????????????????open???,?????????????????????????????????.
		????????????????????????close????????????balance proof,????????????????????????,??????????????????????????????,???????????????????????????????????????unlock
		?????????????????????gas,????????????????????????unlock????????????????????????,????????????????????????,????????????
	*/
	/*
		Here I need to determine the state of the payer channel, and if the state of the channel is no longer open, you should not continue to try new routing.
		Because I've already submitted balance proof at channel close, and if this transaction continues and eventually succeeds,
		I need to register secret and unlock it.
		Not only does it cost gas, but there's a risk of losing money by not unlocking it in time, so there's no need to go on.
		Refuse by announce disposed.
	*/
	if transferPair.PayerRoute.ClosedBlock() != 0 {
		log.Warn("channel already closed, stop trying new route")
		it.Events = eventsForRefund(transferPair.PayerRoute, transferPair.PayerTransfer, rerr.ErrRejectTransferBecausePayerChannelClosed)
		return it
	}
	it = mediateTransfer(state, transferPair.PayerRoute, transferPair.PayerTransfer)
	return it
}

/*
?????????????????? mediatedtransfer
*/
// receive another mediatedTransfer
func handleMediatedTransferAgain(state *mediatedtransfer.MediatorState, st *mediatedtransfer.MediatorReReceiveStateChange) *transfer.TransitionResult {
	return mediateTransfer(state, st.FromRoute, st.FromTransfer)
}

/*
After Photon learns about a new block this function must be called to
    handle expiration of the hash time locks.
        state : The current state.

    Return:
        TransitionResult: The resulting iteration
*/
func handleBlock(state *mediatedtransfer.MediatorState, st *transfer.BlockStateChange) *transfer.TransitionResult {
	blockNumber := state.BlockNumber
	if blockNumber < st.BlockNumber {
		blockNumber = st.BlockNumber
	}
	state.BlockNumber = blockNumber
	closeEvents := eventsForRegisterSecret(state.TransfersPair, blockNumber)
	unlockfailEvents, allExpired := setExpiredPairs(state.TransfersPair, blockNumber)
	var events []transfer.Event
	events = append(events, closeEvents...)
	events = append(events, unlockfailEvents...)
	if allExpired {
		//?????????mediatedtransfer ??????????????????,?????????????????? stateManager ???
		// All mediatedTransfers are expired, feel safe to remove this StateManager.
		events = append(events, &mediatedtransfer.EventRemoveStateManager{
			Key: utils.Sha3(state.LockSecretHash[:], state.Token[:]),
		})
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}

/*
Validate and handle a ReceiveTransferRefund state change.

    A node might participate in mediated transfer more than once because of
    refund transfers, eg. A-B-C-F-B-D-T, B tried to mediate the transfer through
    C, which didn't have an available route to proceed and refunds B, at this
    point B is part of the path again and will try a new partner to proceed
    with the mediation through D, D finally reaches the target T.

    In the above scenario B has two pairs of payer and payee transfers:

        payer:A payee:C from the first SendMediatedTransfer
        payer:F payee:D from the following SendRefundTransfer

        state : Current state.
        st : The state change.

    Returns:
        TransitionResult: The resulting iteration.
*/
func handleAnnouceDisposed(state *mediatedtransfer.MediatorState, st *mediatedtransfer.ReceiveAnnounceDisposedStateChange) *transfer.TransitionResult {
	it := &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
	if state.Secret != utils.EmptyHash {
		panic("refunds are not allowed if the secret is revealed")
	}
	/*
			  The last sent transfer is the only one thay may be refunded, all the
		     previous ones are refunded already.

	*/
	l := len(state.TransfersPair)
	if l <= 0 {
		log.Error(fmt.Sprintf("recevie refund ,but has no transfer pair ,must be a attack!!"))
		return it
	}
	transferPair := state.TransfersPair[l-1]
	payeeTransfer := transferPair.PayeeTransfer
	payeeRoute := transferPair.PayeeRoute

	/*
			A-B-C-F-B-G-D
			B????????????????????? C ???refund ???????????????????????????????????????

		??????????????????,???????????????????????????????????????payee???AnnounceDisposed,
		??????????????????????????????????????????,?????????????????????????????????,????????????????????????????????????????????????.
		??????????????????????????????: ??????payee??????????????????,????????????.

	*/
	// A-B-C-F-B-G-D
	// If B first receives refund of C, how to deal with that?
	if IsValidRefund(payeeTransfer, payeeRoute, st) {
		if payeeTransfer.Expiration > state.BlockNumber {
			/*
					?????????????????????
				AB BC
				EB BF
				???????????????????????? F ??? refund, ????????????????????? payeeTransfer ?????????,
				?????????????????????????????? E ??? transfer, ????????????????????????

			*/
			/*
			 *	Assume that order in this queue is
			 *	AB BC EB BF
			 *	which means we receive refund of F, then we should assume that payeeTransfer invalid,
			 *  which acts like receiving transfer of E, then begin to find a route again.
			 */
			it = cancelCurrentRoute(state, st.Message.ChannelIdentifier)
			ev := &mediatedtransfer.EventSendAnnounceDisposedResponse{
				Token:          state.Token,
				LockSecretHash: st.Lock.LockSecretHash,
				Receiver:       st.Sender,
			}
			it.Events = append(it.Events, ev)
		} else {
			log.Warn(fmt.Sprintf("receive expired EventSendAnnounceDisposedResponse,expiration=%d,currentblock=%d,response=%s",
				payeeTransfer.Expiration, state.BlockNumber, utils.StringInterface(st, 3),
			))
		}

	}
	return it
}

/*
Validate and handle a ReceiveSecretReveal state change.

    The Secret must propagate backwards through the chain of mediators, this
    function will record the learned secret, check if the secret is propagating
    backwards (for the known paths), and send the SendBalanceProof/RevealSecret if
    necessary.
*/
func handleSecretReveal(state *mediatedtransfer.MediatorState, st *mediatedtransfer.ReceiveSecretRevealStateChange) *transfer.TransitionResult {
	secret := st.Secret
	if utils.ShaSecret(secret[:]) != state.Hashlock {
		panic("must a implementation error")
	}
	return secretLearned(state, secret, st.Sender, mediatedtransfer.StatePayeeSecretRevealed)
}

/*
???????????????????????????,
??????????????????????????????,
?????????????????????,??????????????????????????? remove expired,
????????????????????????????????????
*/
/*
 *	Received secret has been registered on-chain,
 *	this transfer completes.
 *	For those expired transfers, partner should receive remove expired Hashlock
 *	For those unexpired transfers, they are here.
 */
func handleSecretRevealOnChain(state *mediatedtransfer.MediatorState, st *mediatedtransfer.ContractSecretRevealOnChainStateChange) *transfer.TransitionResult {
	var it = &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
	var events []transfer.Event
	if state.LockSecretHash != st.LockSecretHash {
		panic("impementation error")
	}
	state.SetSecret(st.Secret)
	for _, pair := range state.TransfersPair {
		if true {
			tr := pair.PayeeTransfer
			route := pair.PayeeRoute
			//????????????,?????????????????????,????????????unlock
			// ????????????????????????,????????????,??????????????????????????????,??????????????????????????????,??????????????????.
			//??????????????????????????????,???????????????????????????BalanceProof,????????????????????????balance proof???.
			if tr.Expiration >= st.BlockNumber {
				//????????????,??????????????? unlock ??????,???????????????????????????????????????,?????? settle ??????????????????.
				// if not be reveal_timeout, then we should send unlock, do not care current channel state.
				ev := &mediatedtransfer.EventSendBalanceProof{
					LockSecretHash:    tr.LockSecretHash,
					ChannelIdentifier: route.ChannelIdentifier,
					Token:             tr.Token,
					Receiver:          route.HopNode(),
				}
				events = append(events, ev)
				pair.PayeeState = mediatedtransfer.StatePayeeBalanceProof
				//?????? payer ??????,??????????????????????????????,???????????? gas ??????????????????.
				// As for payer, he will not be impacted even he does not send BalanceProof, but cost gas to on-chain secret register.

				// ???????????????????????????????????????,????????????
				if tr.Fee.Cmp(big.NewInt(0)) > 0 {
					events = append(events, &mediatedtransfer.EventSaveFeeChargeRecord{
						LockSecretHash: tr.LockSecretHash,
						TokenAddress:   tr.Token,
						TransferFrom:   tr.Initiator,
						TransferTo:     tr.Target,
						TransferAmount: tr.TargetAmount,
						InChannel:      pair.PayerRoute.ChannelIdentifier,
						OutChannel:     pair.PayeeRoute.ChannelIdentifier,
						Fee:            pair.PayerRoute.Fee,
						Timestamp:      time.Now().Unix(),
					})
				}
			}
		}
		if true { //??????????????????
			route := pair.PayerRoute
			//????????????,???????????????????????????,???????????????????????????balance proof,????????????unlock???,??????unlock????????????
			if route.State() == channeltype.StateClosed {
				events = append(events, &mediatedtransfer.EventContractSendUnlock{
					LockSecretHash:    st.LockSecretHash,
					ChannelIdentifier: route.ChannelIdentifier,
				})
				pair.PayerState = mediatedtransfer.StatePayerBalanceProof
			}
		}
	}
	it.Events = events
	return it
}

// Handle a ReceiveBalanceProof state change.
func handleBalanceProof(state *mediatedtransfer.MediatorState, st *mediatedtransfer.ReceiveUnlockStateChange) *transfer.TransitionResult {
	var events []transfer.Event
	/*
		??????????????????????????????????????????,???????????????unlock??????,?????????????????????????????????????????????????????????,????????????????????????????????????
	*/
	state.SetSecret(st.Message.(*encoding.UnLock).LockSecret)
	//????????????,??????????????????,??????????????????,balanceProof????????????,??????????????????pair,???????????????????????????pair
	for _, pair := range state.TransfersPair {
		//??????????????????,????????????B,??????A-B-C-D-B-E ,??????B????????????A???unlock????????????,?????????unlock?????????????????????C,E,???????????????.
		if pair.PayerRoute.HopNode() != st.NodeAddress {
			continue
		}
		if pair.PayeeState != mediatedtransfer.StatePayeeBalanceProof {
			/*
				????????????unlock?????????,????????????????????????unlock,??????
			*/
			// If not receiving unlock from next node when receiving unlock, re-send it.
			pair.PayeeState = mediatedtransfer.StatePayeeBalanceProof
			tr := pair.PayeeTransfer
			balanceProof := &mediatedtransfer.EventSendBalanceProof{
				LockSecretHash:    tr.LockSecretHash,
				ChannelIdentifier: pair.PayeeRoute.ChannelIdentifier,
				Token:             tr.Token,
				Receiver:          pair.PayeeRoute.HopNode(),
			}
			unlockSuccess := &mediatedtransfer.EventUnlockSuccess{
				LockSecretHash: pair.PayerTransfer.LockSecretHash,
			}
			events = append(events, balanceProof, unlockSuccess)
		}

		withdraw := &mediatedtransfer.EventWithdrawSuccess{
			LockSecretHash: pair.PayeeTransfer.LockSecretHash,
		}
		events = append(events, withdraw)
		pair.PayerState = mediatedtransfer.StatePayerBalanceProof
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}

//StateTransition is State machine for a node mediating a transfer.
func StateTransition(originalState transfer.State, stateChange transfer.StateChange) (it *transfer.TransitionResult) {
	/*
			  Notes:
		     - A user cannot cancel a mediated transfer after it was initiated, she
		       may only reject to mediate before hand. This is because the mediator
		       doesn't control the secret reveal and needs to wait for the lock
		       expiration before safely discarding the transfer.
	*/
	it = &transfer.TransitionResult{
		NewState: originalState,
		Events:   nil,
	}
	state, ok := originalState.(*mediatedtransfer.MediatorState)
	if !ok {
		if originalState != nil {
			panic("MediatorState StateTransition get type error ")
		}
		state = nil
	}
	if state == nil {
		if aim, ok := stateChange.(*mediatedtransfer.ActionInitMediatorStateChange); ok {
			state = &mediatedtransfer.MediatorState{
				OurAddress:     aim.OurAddress,
				Routes:         aim.Routes,
				BlockNumber:    aim.BlockNumber,
				Hashlock:       aim.FromTranfer.LockSecretHash,
				Db:             aim.Db,
				Token:          aim.FromTranfer.Token,
				LockSecretHash: aim.FromTranfer.LockSecretHash,
			}
			it = mediateTransfer(state, aim.FromRoute, aim.FromTranfer)
		}
	} else {
		switch st2 := stateChange.(type) {
		case *transfer.BlockStateChange:
			it = handleBlock(state, st2)
		case *mediatedtransfer.ReceiveAnnounceDisposedStateChange:
			if state.Secret == utils.EmptyHash {
				it = handleAnnouceDisposed(state, st2)
			} else {
				log.Error(fmt.Sprintf("mediator state manager ,already knows secret,but recevied announce disposed, must be a error"))
			}

		case *mediatedtransfer.ReceiveSecretRevealStateChange:
			it = handleSecretReveal(state, st2)
		case *mediatedtransfer.ContractSecretRevealOnChainStateChange:
			it = handleSecretRevealOnChain(state, st2)
		case *mediatedtransfer.ReceiveUnlockStateChange:
			if state.Secret == utils.EmptyHash {
				/*
					??????????????????????????????????????????,???????????????unlock??????,?????????????????????????????????????????????????????????,????????????????????????????????????
				*/
				log.Warn(fmt.Sprintf("mediated state manager recevie unlock,but i don't know secret,this maybe a error "))
			}
			it = handleBalanceProof(state, st2)
		case *mediatedtransfer.MediatorReReceiveStateChange:
			if state.Secret == utils.EmptyHash {
				it = handleMediatedTransferAgain(state, st2)
			} else {
				log.Error(fmt.Sprintf("already known secret,but recevie medaited tranfer again:%s", st2.Message))
			}
		/*
			only receive from channel with payee,
			never receive from channel with payer
		*/
		case *mediatedtransfer.ContractCooperativeSettledStateChange:
			it = cancelCurrentRoute(state, st2.ChannelIdentifier)
		case *mediatedtransfer.ContractChannelWithdrawStateChange:
			it = cancelCurrentRoute(state, st2.ChannelIdentifier.ChannelIdentifier)
		default:
			log.Info(fmt.Sprintf("unknown statechange :%s", utils.StringInterface(st2, 3)))
		}
	}
	// this is the place for paranoia
	if it.NewState != nil {
		sanityCheck(it.NewState.(*mediatedtransfer.MediatorState))
	}
	return clearIfFinalized(it)
}
