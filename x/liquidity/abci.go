package liquidity

import (
	"time"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/tendermint/liquidity/x/liquidity/keeper"
	"github.com/tendermint/liquidity/x/liquidity/types"
)

// In the Begin blocker of the liquidity module,
// Reinitialize batch messages that were not executed in the previous batch and delete batch messages that were executed or ready to delete.
func BeginBlocker(ctx sdk.Context, k keeper.Keeper) {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), telemetry.MetricKeyBeginBlocker)
	//// SoftFork example
	//if ctx.BlockHeight() == types.Airdrop1SoftForkTargetHeight {
	//	k.SoftForkAirdrop(ctx, types.Airdrop1ProviderAddr, types.Airdrop1TargetAddrs, types.Airdrop1DistributionCoin)
	//}
	//// SoftForkMultipleCoins example
	//if ctx.BlockHeight() == types.Airdrop2SoftForkTargetHeight {
	//	err := k.SoftForkAirdropMultiCoins(ctx, types.Airdrop2ProviderAddr, types.Airdrop2Pairs)
	//	if err != nil {
	//		ctx.Logger().Error("#### softfork failed", err)
	//	}else {
	//		ctx.Logger().Info("#### softfork completed", types.Airdrop2Pairs)
	//	}
	//}
	k.DeleteAndInitPoolBatch(ctx)
}

// In case of deposit, withdraw, and swap msgs, unlike other normal tx msgs,
// collect them in the liquidity pool batch and perform an execution once at the endblock to calculate and use the universal price.
func EndBlocker(ctx sdk.Context, k keeper.Keeper) {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), telemetry.MetricKeyEndBlocker)
	k.ExecutePoolBatch(ctx)
}
