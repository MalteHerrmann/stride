package keeper

import (
	"testing"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	strideapp "github.com/Stride-Labs/stride/v22/app"
	"github.com/Stride-Labs/stride/v22/utils"
	"github.com/Stride-Labs/stride/v22/x/epochs/keeper"
)

func EpochsKeeper(t testing.TB) (*keeper.Keeper, sdk.Context) {
	chainID := utils.StrideLocalChainID
	app := strideapp.InitStrideTestApp(true, chainID)
	epochsKeeper := app.EpochsKeeper
	ctx := app.BaseApp.NewContext(false, tmproto.Header{Height: 1, ChainID: chainID, Time: time.Now().UTC()})

	return &epochsKeeper, ctx
}
