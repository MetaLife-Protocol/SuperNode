package initiator

import (
	"fmt"

	"math/big"

	"github.com/MetaLife-Protocol/SuperNode/log"
	"github.com/MetaLife-Protocol/SuperNode/params"
	"github.com/MetaLife-Protocol/SuperNode/rerr"
	"github.com/MetaLife-Protocol/SuperNode/transfer"
	mt "github.com/MetaLife-Protocol/SuperNode/transfer/mediatedtransfer"
	"github.com/MetaLife-Protocol/SuperNode/transfer/mediatedtransfer/mediator"
	"github.com/MetaLife-Protocol/SuperNode/transfer/route"
	"github.com/MetaLife-Protocol/SuperNode/utils"
)

//NameInitiatorTransition name for state manager
const NameInitiatorTransition = "InitiatorTransition"

/*
Clear current state and try a new route.

- Discards the current secret
- Add the current route to the canceled list
- Add the current message to the canceled transfers
*/
func cancelCurrentRoute(state *mt.InitiatorState, reason string) *transfer.TransitionResult {
	if state.RevealSecret != nil {
		panic("cannot cancel a transfer with a RevealSecret in flight")
	}
	state.Routes.CanceledRoutes = append(state.Routes.CanceledRoutes, &route.CanceledRoute{
		Route:  state.Route,
		Reason: reason,
	})
	state.Message = nil
	state.Route = nil
	state.SecretRequest = nil

	return tryNewRoute(state)
}

//Cancel the current in-transit message
func userCancelTransfer(state *mt.InitiatorState) *transfer.TransitionResult {
	if state.RevealSecret != nil {
		panic("cannot cancel a transfer with a RevealSecret in flight")
	}
	state.Transfer.Secret = utils.EmptyHash
	//state.Transfer.LockSecretHash = utils.EmptyHash // need by remove
	state.Message = nil
	//state.Route = nil // need by remove
	state.SecretRequest = nil
	state.RevealSecret = nil
	cancel := &transfer.EventTransferSentFailed{
		LockSecretHash: state.Transfer.LockSecretHash,
		Reason:         "user canceled transfer",
		Target:         state.Transfer.Target,
		Token:          state.Transfer.Token,
	}
	/*
		need state exist to send remove msg after expired
	*/
	return &transfer.TransitionResult{
		NewState: state,
		Events:   []transfer.Event{cancel},
	}
}

func tryNewRoute(state *mt.InitiatorState) *transfer.TransitionResult {
	if state.Route != nil {
		panic("cannot try a new route while one is being used")
	}
	var tryRoute *route.State
	for len(state.Routes.AvailableRoutes) > 0 {
		r := state.Routes.AvailableRoutes[0]
		state.Routes.AvailableRoutes = state.Routes.AvailableRoutes[1:]
		//if !r.CanTransfer() /*????????????????????????????????????*/ || r.AvailableBalance().Cmp(new(big.Int).Add(state.Transfer.TargetAmount, r.Fee)) < 0 {
		if !r.CanTransfer() || r.AvailableBalance().Cmp(state.Transfer.TargetAmount) < 0 {
			state.Routes.IgnoredRoutes = append(state.Routes.IgnoredRoutes, r)
		} else {
			tryRoute = r
			break
		}
	}
	if tryRoute == nil {
		/*
					 No available route has sufficient balance for the current transfer,
			         cancel it.

			         At this point we can just discard all the state data, this is only
			         valid because we are the initiator and we know that the secret was
			         not released.
		*/
		transferFailed := &transfer.EventTransferSentFailed{
			LockSecretHash: state.Transfer.LockSecretHash,
			//Reason:         "no route available",
			Target: state.Transfer.Target,
			Token:  state.Transfer.Token,
		}
		for _, canceledRoute := range state.Routes.CanceledRoutes {
			transferFailed.Reason = fmt.Sprintf("%s,%s", transferFailed.Reason, canceledRoute.Reason)
		}
		if transferFailed.Reason == "" {
			transferFailed.Reason = "no route available"
		}
		events := []transfer.Event{transferFailed}
		removeManager := &mt.EventRemoveStateManager{
			Key: utils.Sha3(state.LockSecretHash[:], state.Transfer.Token[:]),
		}
		events = append(events, removeManager)
		return &transfer.TransitionResult{
			NewState: nil,
			Events:   events,
		}
	}
	/*
				  The initiator doesn't need to learn the secret, so there is no need
		         to decrement reveal_timeout from the lock timeout.

		         The lock_expiration could be set to a value larger than
		         settle_timeout, this is not useful since the next hop will take this
		         channel settle_timeout as an upper limit for expiration.

		         The two nodes will most likely disagree on latest block, as far as
		         the expiration goes this is no problem.
	*/
	lockExpiration := state.BlockNumber + int64(tryRoute.SettleTimeout()) - int64(params.DefaultRevealTimeout) // - revealTimeout for test
	if lockExpiration > state.Transfer.Expiration && state.Transfer.Expiration != 0 {
		lockExpiration = state.Transfer.Expiration
	}
	tr := &mt.LockedTransferState{
		TargetAmount:   state.Transfer.TargetAmount,
		Amount:         new(big.Int).Add(state.Transfer.TargetAmount, tryRoute.TotalFee),
		Token:          state.Transfer.Token,
		Initiator:      state.Transfer.Initiator,
		Target:         state.Transfer.Target,
		Expiration:     lockExpiration,
		LockSecretHash: state.LockSecretHash,
		Secret:         state.Secret,
		Fee:            tryRoute.TotalFee,
		Data:           state.Transfer.Data,
	}
	msg := mt.NewEventSendMediatedTransfer(tr, tryRoute.HopNode(), tryRoute.Path)
	if len(state.Routes.CanceledRoutes) > 0 {
		/*
			?????????????????????????????????,????????????????????????AnnounceDisposed?????????,??????????????????,???????????????
		*/
		// Store route info of previous one, or when receiving AnnounceDisposed message and try a new route, error occurs.
		msg.FromChannel = state.Routes.CanceledRoutes[len(state.Routes.CanceledRoutes)-1].Route.ChannelIdentifier
	}
	state.Route = tryRoute
	state.Transfer = tr
	state.Message = msg
	log.Trace(fmt.Sprintf("send mediated transfer id=%s,amount=%s,token=%s,target=%s,secret=%s,data=%s", utils.HPex(tr.LockSecretHash), tr.Amount, utils.APex(tr.Token), utils.APex(tr.Target), tr.Secret.String(), tr.Data))
	events := []transfer.Event{msg}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}
func expiredHashLockEvents(state *mt.InitiatorState) (events []transfer.Event) {
	if state.BlockNumber-params.ForkConfirmNumber > state.Transfer.Expiration {
		if state.Route != nil && !state.Db.IsThisLockRemoved(state.Route.ChannelIdentifier, state.OurAddress, state.Transfer.LockSecretHash) {
			unlockFailed := &mt.EventUnlockFailed{
				LockSecretHash:    state.Transfer.LockSecretHash,
				ChannelIdentifier: state.Route.ChannelIdentifier,
				Reason:            "lock expired",
			}
			transferFailed := &transfer.EventTransferSentFailed{
				LockSecretHash: state.Transfer.LockSecretHash,
				Reason:         "no route available",
				Target:         state.Transfer.Target,
				Token:          state.Transfer.Token,
			}
			events = append(events, unlockFailed, transferFailed)
		}
	}
	return
}

/*
make sure not call this when transfer already finished , state is nil means finished.
*/
func handleBlock(state *mt.InitiatorState, stateChange *transfer.BlockStateChange) *transfer.TransitionResult {
	var events []transfer.Event
	if state.BlockNumber < stateChange.BlockNumber {
		state.BlockNumber = stateChange.BlockNumber
	}
	// ?????????????????????,?????????????????????????????????remove
	if state.BlockNumber-params.ForkConfirmNumber > state.Transfer.Expiration {
		// ??????
		// ??????????????????????????????,????????????remove expired lock,????????????state manager
		// ??????????????????????????????,?????????????????????????????????reveal secret ??? ????????????????????????,?????????????????????????????????,??????????????????RemoveExpiredHashlock,?????????????????????.????????????state manager
		// timeout
		// If I have not sent secret, then just send removeExpiredLock, and remove stateManager.
		// If I have already sent secret, then assume transfer timeout failure, send remove expired, and remove state manager.
		events = expiredHashLockEvents(state)
		events = append(events, &mt.EventRemoveStateManager{
			Key: utils.Sha3(state.LockSecretHash[:], state.Transfer.Token[:]),
		})
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   events,
	}
}

func handleRefund(state *mt.InitiatorState, stateChange *mt.ReceiveAnnounceDisposedStateChange) *transfer.TransitionResult {
	if mediator.IsValidRefund(state.Transfer, state.Route, stateChange) {
		it := cancelCurrentRoute(state, rerr.StandardError{
			ErrorCode: stateChange.Message.ErrorCode,
			ErrorMsg:  stateChange.Message.ErrorMsg,
		}.Error())
		ev := &mt.EventSendAnnounceDisposedResponse{
			LockSecretHash: stateChange.Lock.LockSecretHash,
			Token:          state.Transfer.Token,
			Receiver:       stateChange.Sender,
		}
		it.Events = append(it.Events, ev)
		return it
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
}

func handleCancelRoute(state *mt.InitiatorState, stateChange *mt.ActionCancelRouteStateChange) *transfer.TransitionResult {
	if stateChange.LockSecretHash == state.Transfer.LockSecretHash {
		return cancelCurrentRoute(state, "initiator cancel")
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
}

func handleCancelTransfer(state *mt.InitiatorState) *transfer.TransitionResult {
	return userCancelTransfer(state)
}

func handleSecretRequest(state *mt.InitiatorState, stateChange *mt.ReceiveSecretRequestStateChange) *transfer.TransitionResult {
	isValid := stateChange.Sender == state.Transfer.Target &&
		stateChange.LockSecretHash == state.Transfer.LockSecretHash &&
		stateChange.Amount.Cmp(state.Transfer.TargetAmount) == 0
	//????????????secret request?????????????????????,???????????????????????????,???????????????????????????
	if isValid && !state.CancelByExceptionSecretRequest && state.BlockNumber < state.Transfer.Expiration {
		/*
		   Reveal the secret to the target node and wait for its confirmation,
		   at this point the transfer is not cancellable anymore either the lock
		   timeouts or a secret reveal is received.

		   Note: The target might be the first hop

		*/
		tr := state.Transfer
		revealSecret := &mt.EventSendRevealSecret{
			LockSecretHash: tr.LockSecretHash,
			Secret:         tr.Secret,
			Token:          tr.Token,
			Receiver:       tr.Target,
			Sender:         state.OurAddress,
			Data:           tr.Data,
		}
		state.RevealSecret = revealSecret
		return &transfer.TransitionResult{
			NewState: state,
			Events:   []transfer.Event{revealSecret},
		}
	}
	/*
		?????????????????????secret request,??????????????????????????????????????????,?????????????????????????????????secret request,
		?????????????????????,??????remove
	*/
	state.CancelByExceptionSecretRequest = true
	/*
		BUG : ????????????????????????????????????,????????????????????????,????????????????????????
	*/
	//if isInvalid {
	//	return cancelCurrentRoute(state)
	//}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
}

/*
????????????????????????,???????????????????????????,?????????????????????????????? reveal secret, ????????????????????? unlock ??????.
*/
/*
 *	handleSecretRevealOnChain : function to handle event of RevealSecretOnChain.
 *
 *	Note : Once the secret has been registered on chain, all nodes act like they receives reveal secret from their partner,
 *	then send unlock to their partner.
 */
func handleSecretRevealOnChain(state *mt.InitiatorState, st *mt.ContractSecretRevealOnChainStateChange) *transfer.TransitionResult {
	if st.LockSecretHash != state.LockSecretHash {
		//??????????????? token swap, ??????????????? locksecrethash,??????????????????????????????
		// we should know locksecrethash no matter whether it is token swap, otherwise implementation has problem.
		panic(fmt.Sprintf("my locksecrethash=%s,received=%s", state.LockSecretHash.String(), st.LockSecretHash.String()))
	}
	log.Trace(fmt.Sprintf("Check lock's expiration, state.Transfer.Expiration=%d, st.BlockNumber=%d\n", state.Transfer.Expiration, st.BlockNumber))
	if state.Transfer.Expiration < st.BlockNumber {
		//??????????????????????????????????????????. ???????????? ??????????????????.
		// As to me this transfer expired, should send RemoveExpiredLock message.
		events := expiredHashLockEvents(state)
		events = append(events, &mt.EventRemoveStateManager{
			Key: utils.Sha3(state.LockSecretHash[:], state.Transfer.Token[:]),
		})
		return &transfer.TransitionResult{
			NewState: state,
			Events:   events,
		}
	}
	//?????????????????????
	// assume transfer succeed.
	return &transfer.TransitionResult{
		NewState: state,
		Events:   transferSuccessEvents(state),
	}
}

func transferSuccessEvents(state *mt.InitiatorState) (events []transfer.Event) {
	tr := state.Transfer
	unlockLock := &mt.EventSendBalanceProof{
		LockSecretHash:    tr.LockSecretHash,
		ChannelIdentifier: state.Route.ChannelIdentifier,
		Token:             tr.Token,
		Receiver:          state.Route.HopNode(),
	}
	transferSuccess := &transfer.EventTransferSentSuccess{
		LockSecretHash:    tr.LockSecretHash,
		Amount:            tr.Amount,
		Target:            tr.Target,
		ChannelIdentifier: state.Route.ChannelIdentifier,
		Token:             tr.Token,
		Data:              tr.Data,
	}
	unlockSuccess := &mt.EventUnlockSuccess{
		LockSecretHash: tr.LockSecretHash,
	}
	removeManager := &mt.EventRemoveStateManager{
		Key: utils.Sha3(tr.LockSecretHash[:], tr.Token[:]),
	}
	events = []transfer.Event{unlockLock, transferSuccess, unlockSuccess, removeManager}
	return events
}

/*
Send a balance proof to the next hop with the current mediated transfer
    lock removed and the balance updated.
*/
func handleSecretReveal(state *mt.InitiatorState, st *mt.ReceiveSecretRevealStateChange) *transfer.TransitionResult {
	/*
		???????????????????????????,?????????????????????. ????????????????????????????????????,???????????????.
	*/
	// Consider that crash happened for a long time, if transfer still goes on, that's not reasonable.
	if state.BlockNumber >= state.Transfer.Expiration {
		return &transfer.TransitionResult{
			NewState: state,
			Events:   nil,
		}
	}
	if st.Sender == state.Route.HopNode() && st.Secret == state.Transfer.Secret {
		/*
					   next hop learned the secret, unlock the token locally and send the
			         unlock message to next hop
		*/
		return &transfer.TransitionResult{
			NewState: nil,
			Events:   transferSuccessEvents(state),
		}
	}
	return &transfer.TransitionResult{
		NewState: state,
		Events:   nil,
	}
}

/*
StateTransition is State machine for a node starting a mediated transfer.
    originalState: The current State that is transitioned from.
    st: The state_change that will be applied.
*/
func StateTransition(originalState transfer.State, st transfer.StateChange) *transfer.TransitionResult {
	/*
	   Transfers added to the canceled list by an ActionCancelRoute are stale in
	   the channels merkle tree, while this doesn't increase the messages sizes
	   nor does it interfere with the guarantees of finality it increases memory
	   usage for each end, since the full merkle tree must be saved to compute
	   it's root.
	*/
	it := &transfer.TransitionResult{
		NewState: originalState,
		Events:   nil,
	}
	state, ok := originalState.(*mt.InitiatorState)
	if !ok {
		if originalState != nil {
			panic("InitiatorState StateTransition get type error")
		}
		state = nil //originalState is nil
	}
	if state == nil {
		staii, ok := st.(*mt.ActionInitInitiatorStateChange)
		if ok {
			state = &mt.InitiatorState{
				OurAddress:                     staii.OurAddress,
				Transfer:                       staii.Tranfer,
				Routes:                         staii.Routes,
				BlockNumber:                    staii.BlockNumber,
				LockSecretHash:                 staii.LockSecretHash,
				Secret:                         staii.Secret,
				Db:                             staii.Db,
				CancelByExceptionSecretRequest: false,
			}
			return tryNewRoute(state)
		}
		/*
			?????????????????????,????????? Unlock ??????,??????????????????,??????????????????????????????????????????
		*/
		// As transfer initiator, we assume that this transfer completes once we send unlock and my partner receive it.
		log.Warn(fmt.Sprintf("originalState,statechange should not be here originalState=\n%s\n,statechange=\n%s",
			utils.StringInterface1(originalState), utils.StringInterface1(st)))
	} else {
		switch st2 := st.(type) {
		case *transfer.BlockStateChange:
			it = handleBlock(state, st2)
			//??????????????????,???????????????secret ,????????????????????????,????????????????????????(?????????token swap??????????????????????????????) . ???????????????????????????,?????????????????????????????????, ?????????tokenswap?????????maker?????????????????????reveal secret
			/*
					?????? token swap
					?????? ????????? reveal secret ?????????????????????????????????,???????????????????????????.
				 maker:
				1. maker??????????????? reveal secret ?????????,????????? lock ??????????????? statemanager ??????????????????,
				?????????????????????????????????,???????????????????????????, maker ?????????secret request ?????????????????????,????????????????????? state manager ???????????????,
				??????????????????.
			*/
			/*
			 *	As long as secret correct, then we should send secret. There might be problematic about this procedure but result is correct.
			 *	Because according to protocol layer, same message won't send repeatedly, which leads to maker can't send reveal secret in tokenswap.
			 *
			 *	As to token swap, maybe redundency occurs because both participants send / receive revealsecret twice.
			 *
			 *	maker :
			 *		1. when maker sends reveal secret to his partner, two statemanager of a lock should know the secret.
			 *			Because maybe partner is fraudulent node, and he never responds to secret request, which leads to one stateManager without secret.
			 *
			 */
		case *mt.ReceiveSecretRevealStateChange:
			it = handleSecretReveal(state, st2)
		case *mt.ContractSecretRevealOnChainStateChange:
			it = handleSecretRevealOnChain(state, st2)
		case *mt.ReceiveSecretRequestStateChange:
			if state.RevealSecret == nil {
				it = handleSecretRequest(state, st2)
			} else {
				log.Warn(fmt.Sprintf("recevie secret request but initiator have already sent reveal secret"))
			}
		case *mt.ReceiveAnnounceDisposedStateChange:
			if state.RevealSecret == nil {
				it = handleRefund(state, st2)
			} else {
				log.Warn(fmt.Sprintf("secret already revealed ,but initiator recevied announce disposed %s", utils.StringInterface(st, 3)))
			}
		case *mt.ActionCancelRouteStateChange:
			if state.RevealSecret == nil {
				it = handleCancelRoute(state, st2)
			} else {
				panic(fmt.Sprintf("secret already revealed,route cannot canceled"))
			}
		case *transfer.ActionCancelTransferStateChange:
			if state.RevealSecret == nil {
				it = handleCancelTransfer(state)
			} else {
				panic(fmt.Sprintf("secret already revealed,transfer cannot canceled"))
			}
		case *mt.ContractCooperativeSettledStateChange:
			it = cancelCurrentRoute(state, "partner cooperative settle channel with me")
		case *mt.ContractChannelWithdrawStateChange:
			it = cancelCurrentRoute(state, "partner withdraw on channel with me")
		default:
			log.Error(fmt.Sprintf("initiator received unkown state change %s", utils.StringInterface(st, 3)))
		}
	}
	return it
}
