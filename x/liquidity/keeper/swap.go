package keeper

import (
	"sort"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/tendermint/liquidity/x/liquidity/types"
)

// Execute Swap of the pool batch, Collect swap messages in batch for transact the same price for each batch and run them on endblock.
func (k Keeper) SwapExecution(ctx sdk.Context, liquidityPoolBatch types.PoolBatch) (uint64, error) {
	// get all swap message batch states that are not executed, not succeeded, and not to be deleted.
	swapMsgStates := k.GetAllNotProcessedPoolBatchSwapMsgStates(ctx, liquidityPoolBatch)
	if len(swapMsgStates) == 0 {
		return 0, nil
	}

	pool, found := k.GetPool(ctx, liquidityPoolBatch.PoolId)
	if !found {
		return 0, types.ErrPoolNotExists
	}

	// set executed states of all messages to true
	for _, sms := range swapMsgStates {
		sms.Executed = true
	}
	k.SetPoolBatchSwapMsgStatesByPointer(ctx, pool.Id, swapMsgStates)

	currentHeight := ctx.BlockHeight()
	types.ValidateStateAndExpireOrders(swapMsgStates, currentHeight, false)

	// get reserve coins from the liquidity pool and calculate the current pool price (p = x / y)
	reserveCoins := k.GetReserveCoins(ctx, pool)

	X := reserveCoins[0].Amount.ToDec()
	Y := reserveCoins[1].Amount.ToDec()
	currentPoolPrice := X.Quo(Y)
	denomX := reserveCoins[0].Denom
	denomY := reserveCoins[1].Denom

	// make orderMap, orderbook by sort orderMap
	orderMap, XtoY, YtoX := types.MakeOrderMap(swapMsgStates, denomX, denomY, false)
	orderBook := orderMap.SortOrderBook()

	// check orderbook validity and compute batchResult(direction, swapPrice, ..)
	result := orderBook.Match(X, Y)

	// find order match, calculate pool delta with the total x, y amounts for the invariant check
	var matchResultXtoY, matchResultYtoX []types.MatchResult

	poolXdelta := sdk.ZeroDec()
	poolYdelta := sdk.ZeroDec()

	if result.MatchType != types.NoMatch {
		var poolXDeltaXtoY, poolXDeltaYtoX, poolYDeltaYtoX, poolYDeltaXtoY sdk.Dec
		matchResultXtoY, _, poolXDeltaXtoY, poolYDeltaXtoY = types.FindOrderMatch(types.DirectionXtoY, XtoY, result.EX, result.SwapPrice, currentHeight)
		matchResultYtoX, _, poolXDeltaYtoX, poolYDeltaYtoX = types.FindOrderMatch(types.DirectionYtoX, YtoX, result.EY, result.SwapPrice, currentHeight)
		poolXdelta = poolXDeltaXtoY.Add(poolXDeltaYtoX)
		poolYdelta = poolYDeltaXtoY.Add(poolYDeltaYtoX)
	}

	executedMsgCount := uint64(len(swapMsgStates))

	if result.MatchType == 0 {
		return executedMsgCount, nil
	}

	XtoY, YtoX, X, Y, poolXdelta2, poolYdelta2, fractionalCntX, fractionalCntY, decimalErrorX, decimalErrorY :=
		k.UpdateState(X, Y, XtoY, YtoX, matchResultXtoY, matchResultYtoX)

	lastPrice := X.Quo(Y)

	if invariantCheckFlag {
		SwapPriceInvariants(XtoY, YtoX, matchResultXtoY, matchResultYtoX, fractionalCntX, fractionalCntY,
			poolXdelta, poolYdelta, poolXdelta2, poolYdelta2, decimalErrorX, decimalErrorY, result)
	}

	types.ValidateStateAndExpireOrders(XtoY, currentHeight, false)
	types.ValidateStateAndExpireOrders(YtoX, currentHeight, false)

	orderMapExecuted, _, _ := types.MakeOrderMap(append(XtoY, YtoX...), denomX, denomY, true)
	orderBookExecuted := orderMapExecuted.SortOrderBook()
	if !orderBookExecuted.Validate(lastPrice) {
		panic(types.ErrOrderBookInvalidity)
	}

	types.ValidateStateAndExpireOrders(XtoY, currentHeight, true)
	types.ValidateStateAndExpireOrders(YtoX, currentHeight, true)

	// make index map for match result
	matchResultMap := make(map[uint64]types.MatchResult)
	for _, msg := range append(matchResultXtoY, matchResultYtoX...) {
		if _, ok := matchResultMap[msg.OrderMsgIndex]; ok {
			panic("duplicated match order")
		}
		matchResultMap[msg.OrderMsgIndex] = msg
	}

	if invariantCheckFlag {
		OrdersWithExecutedAndNotExecutedStateInvariants(matchResultXtoY, matchResultYtoX, matchResultMap, swapMsgStates,
			XtoY, YtoX, result, currentPoolPrice, denomX)
	}

	// execute transact, refund, expire, send coins with escrow, update state by TransactAndRefundSwapLiquidityPool
	if err := k.TransactAndRefundSwapLiquidityPool(ctx, swapMsgStates, matchResultMap, pool, result); err != nil {
		panic(err)
	}

	return executedMsgCount, nil
}

// Update Buy, Sell swap batch messages using the result of match.
func (k Keeper) UpdateState(X, Y sdk.Dec, XtoY, YtoX []*types.SwapMsgState, matchResultXtoY, matchResultYtoX []types.MatchResult) (
	[]*types.SwapMsgState, []*types.SwapMsgState, sdk.Dec, sdk.Dec, sdk.Dec, sdk.Dec, int, int, sdk.Dec, sdk.Dec) {
	sort.SliceStable(XtoY, func(i, j int) bool {
		return XtoY[i].Msg.OrderPrice.GT(XtoY[j].Msg.OrderPrice)
	})
	sort.SliceStable(YtoX, func(i, j int) bool {
		return YtoX[i].Msg.OrderPrice.LT(YtoX[j].Msg.OrderPrice)
	})

	poolXdelta := sdk.ZeroDec()
	poolYdelta := sdk.ZeroDec()
	matchedIndexMapXtoY := make(map[uint64]sdk.Coin)
	matchedIndexMapYtoX := make(map[uint64]sdk.Coin)
	fractionalCntX := 0
	fractionalCntY := 0

	// Variables to accumulate and offset the values of int 1 caused by decimal error
	decimalErrorX := sdk.ZeroDec()
	decimalErrorY := sdk.ZeroDec()

	for _, match := range matchResultXtoY {
		poolXdelta = poolXdelta.Add(match.TransactedCoinAmt)
		poolYdelta = poolYdelta.Sub(match.ExchangedDemandCoinAmt)
		if match.BatchMsg.Msg.OfferCoin.Amount.ToDec().Equal(match.TransactedCoinAmt) ||
			match.BatchMsg.RemainingOfferCoin.Amount.ToDec().Equal(match.TransactedCoinAmt) {
			// full match
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(
				sdk.NewCoin(match.BatchMsg.RemainingOfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))

			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			if match.BatchMsg.RemainingOfferCoin.Amount.Add(match.BatchMsg.ExchangedOfferCoin.Amount).
				GT(match.BatchMsg.Msg.OfferCoin.Amount) ||
				!match.BatchMsg.RemainingOfferCoin.Equal(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, sdk.ZeroInt())) ||
				match.BatchMsg.ReservedOfferCoinFee.IsGTE(sdk.NewCoin(match.BatchMsg.ReservedOfferCoinFee.Denom, sdk.NewInt(2))) {
				panic("remaining not matched 1")
			} else {
				match.BatchMsg.Succeeded = true
				match.BatchMsg.ToBeDeleted = true
			}
		} else if match.BatchMsg.Msg.OfferCoin.Amount.ToDec().Sub(match.TransactedCoinAmt).Equal(sdk.OneDec()) ||
			match.BatchMsg.RemainingOfferCoin.Amount.ToDec().Sub(match.TransactedCoinAmt).Equal(sdk.OneDec()) {
			// TODO: add testcase for coverage
			decimalErrorX = decimalErrorX.Add(sdk.OneDec())
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(
				sdk.NewCoin(match.BatchMsg.RemainingOfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))
			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			if match.BatchMsg.RemainingOfferCoin.Amount.Equal(sdk.OneInt()) {
				match.BatchMsg.RemainingOfferCoin.Amount = sdk.ZeroInt()
			}
			if match.BatchMsg.RemainingOfferCoin.Amount.Add(match.BatchMsg.ExchangedOfferCoin.Amount).
				GT(match.BatchMsg.Msg.OfferCoin.Amount) ||
				!match.BatchMsg.RemainingOfferCoin.Equal(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, sdk.ZeroInt())) ||
				match.BatchMsg.ReservedOfferCoinFee.IsGTE(sdk.NewCoin(match.BatchMsg.ReservedOfferCoinFee.Denom, sdk.NewInt(2))) {
				panic("remaining not matched 2")
			} else {
				match.BatchMsg.Succeeded = true
				match.BatchMsg.ToBeDeleted = true
			}
		} else {
			// fractional match
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))
			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			matchedIndexMapXtoY[match.BatchMsg.MsgIndex] = match.BatchMsg.RemainingOfferCoin
			match.BatchMsg.Succeeded = true
			match.BatchMsg.ToBeDeleted = false
			fractionalCntX += 1
		}
	}
	for _, match := range matchResultYtoX {
		poolXdelta = poolXdelta.Sub(match.ExchangedDemandCoinAmt)
		poolYdelta = poolYdelta.Add(match.TransactedCoinAmt)
		if match.BatchMsg.Msg.OfferCoin.Amount.ToDec().Equal(match.TransactedCoinAmt) ||
			match.BatchMsg.RemainingOfferCoin.Amount.ToDec().Equal(match.TransactedCoinAmt) {
			// full match
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(
				sdk.NewCoin(match.BatchMsg.RemainingOfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))
			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			if match.BatchMsg.RemainingOfferCoin.Amount.Add(match.BatchMsg.ExchangedOfferCoin.Amount).
				GT(match.BatchMsg.Msg.OfferCoin.Amount) ||
				!match.BatchMsg.RemainingOfferCoin.Equal(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, sdk.ZeroInt())) ||
				match.BatchMsg.ReservedOfferCoinFee.IsGTE(sdk.NewCoin(match.BatchMsg.ReservedOfferCoinFee.Denom, sdk.NewInt(2))) {
				panic("remaining not matched 3")
			} else {
				match.BatchMsg.Succeeded = true
				match.BatchMsg.ToBeDeleted = true
			}
		} else if match.BatchMsg.Msg.OfferCoin.Amount.ToDec().Sub(match.TransactedCoinAmt).Equal(sdk.OneDec()) ||
			match.BatchMsg.RemainingOfferCoin.Amount.ToDec().Sub(match.TransactedCoinAmt).Equal(sdk.OneDec()) {
			// TODO: add testcase for coverage
			decimalErrorY = decimalErrorY.Add(sdk.OneDec())
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(
				sdk.NewCoin(match.BatchMsg.RemainingOfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))
			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			// TODO: verify RemainingOfferCoin about decimal errors one to pool
			if match.BatchMsg.RemainingOfferCoin.Amount.Equal(sdk.OneInt()) {
				match.BatchMsg.RemainingOfferCoin.Amount = sdk.ZeroInt()

			}
			if match.BatchMsg.RemainingOfferCoin.Amount.Add(match.BatchMsg.ExchangedOfferCoin.Amount).
				GT(match.BatchMsg.Msg.OfferCoin.Amount) ||
				!match.BatchMsg.RemainingOfferCoin.Equal(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, sdk.ZeroInt())) ||
				match.BatchMsg.ReservedOfferCoinFee.IsGTE(sdk.NewCoin(match.BatchMsg.ReservedOfferCoinFee.Denom, sdk.NewInt(2))) {
				panic("remaining not matched 4")
			} else {
				match.BatchMsg.Succeeded = true
				match.BatchMsg.ToBeDeleted = true
			}
		} else {
			// fractional match
			match.BatchMsg.ExchangedOfferCoin = match.BatchMsg.ExchangedOfferCoin.Add(sdk.NewCoin(match.BatchMsg.Msg.OfferCoin.Denom, match.TransactedCoinAmt.TruncateInt()))
			match.BatchMsg.RemainingOfferCoin = types.CoinSafeSubAmount(match.BatchMsg.RemainingOfferCoin, match.TransactedCoinAmt.TruncateInt())
			match.BatchMsg.ReservedOfferCoinFee = types.CoinSafeSubAmount(match.BatchMsg.ReservedOfferCoinFee, match.OfferCoinFeeAmt.TruncateInt())
			matchedIndexMapYtoX[match.BatchMsg.MsgIndex] = match.BatchMsg.RemainingOfferCoin
			match.BatchMsg.Succeeded = true
			match.BatchMsg.ToBeDeleted = false
			fractionalCntY += 1
		}
	}

	// Offset accumulated decimal error values
	poolXdelta = poolXdelta.Add(decimalErrorX)
	poolYdelta = poolYdelta.Add(decimalErrorY)

	X = X.Add(poolXdelta)
	Y = Y.Add(poolYdelta)

	return XtoY, YtoX, X, Y, poolXdelta, poolYdelta, fractionalCntX, fractionalCntY, decimalErrorX, decimalErrorY
}
