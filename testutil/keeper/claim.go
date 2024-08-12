package keeper

import (
	"testing"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	strideapp "github.com/Stride-Labs/stride/v22/app"
	"github.com/Stride-Labs/stride/v22/utils"
	"github.com/Stride-Labs/stride/v22/x/claim/keeper"
)

func ClaimKeeper(t testing.TB) (*keeper.Keeper, sdk.Context) {
	app := strideapp.InitStrideTestApp(true, utils.StrideLocalChainID)
	claimKeeper := app.ClaimKeeper
	ctx := app.BaseApp.NewContext(false, tmproto.Header{Height: 1, ChainID: utils.StrideLocalChainID, Time: time.Now().UTC()})

	return &claimKeeper, ctx
}
