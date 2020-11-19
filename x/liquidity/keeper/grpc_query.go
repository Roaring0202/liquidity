package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tendermint/liquidity/x/liquidity/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) LiquidityPool(c context.Context, req *types.QueryLiquidityPoolRequest) (*types.QueryLiquidityPoolResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(c)

	pool, found := k.GetLiquidityPool(ctx, req.PoolId)
	if !found {
		return nil, status.Errorf(codes.NotFound, "liquidity pool %d doesn't exist", req.PoolId)
	}

	return &types.QueryLiquidityPoolResponse{LiquidityPool: pool}, nil
}

// TODO: after rebase latest stable sdk 0.40.0
func (k Keeper) LiquidityPools(c context.Context, req *types.QueryLiquidityPoolsRequest) (*types.QueryLiquidityPoolsResponse, error) {
	return nil, nil
}

func (k Keeper) LiquidityPoolBatch(c context.Context, req *types.QueryLiquidityPoolBatchRequest) (*types.QueryLiquidityPoolBatchResponse, error) {
	return nil, nil
}

func (k Keeper) PoolBatchSwapMsgs(c context.Context, req *types.QueryPoolBatchSwapMsgsRequest) (*types.QueryPoolBatchSwapMsgsResponse, error) {
	return nil, nil
}

func (k Keeper) PoolBatchDepositMsgs(c context.Context, req *types.QueryPoolBatchDepositMsgsRequest) (*types.QueryPoolBatchDepositMsgsResponse, error) {
	return nil, nil
}

func (k Keeper) PoolBatchWithdrawMsgs(c context.Context, req *types.QueryPoolBatchWithdrawMsgsRequest) (*types.QueryPoolBatchWithdrawMsgsResponse, error) {
	return nil, nil
}

func (k Keeper) Params(c context.Context, req *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	return nil, nil
}
