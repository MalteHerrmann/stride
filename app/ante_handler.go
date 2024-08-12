package app

import (
	"math/big"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	ibcante "github.com/cosmos/ibc-go/v7/modules/core/ante"
	ibckeeper "github.com/cosmos/ibc-go/v7/modules/core/keeper"
	consumerante "github.com/cosmos/interchain-security/v4/app/consumer/ante"
	ccvconsumerkeeper "github.com/cosmos/interchain-security/v4/x/ccv/consumer/keeper"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	evmosante "github.com/evmos/evmos/v18/app/ante"
	evmoscosmosante "github.com/evmos/evmos/v18/app/ante/cosmos"
	evmante "github.com/evmos/evmos/v18/app/ante/evm"
	evmosanteutils "github.com/evmos/evmos/v18/app/ante/utils"
	evmkeeper "github.com/evmos/evmos/v18/x/evm/keeper"
	evmtypes "github.com/evmos/evmos/v18/x/evm/types"
)

// HandlerOptions extend the SDK's AnteHandler options by requiring the IBC
// channel keeper.
type HandlerOptions struct {
	evmosante.HandlerOptions

	IBCKeeper         *ibckeeper.Keeper
	ConsumerKeeper    ccvconsumerkeeper.Keeper
	WasmConfig        *wasmtypes.WasmConfig
	WasmKeeper        *wasmkeeper.Keeper
	TXCounterStoreKey storetypes.StoreKey
}

// NewAnteHandler controls the handling of EVM and Cosmos transactions.
func NewAnteHandler(options HandlerOptions) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (newCtx sdk.Context, err error) {
		var anteHandler sdk.AnteHandler

		txWithExtensions, ok := tx.(authante.HasExtensionOptionsTx)
		if ok {
			opts := txWithExtensions.GetExtensionOptions()
			if len(opts) > 0 {
				switch typeURL := opts[0].TypeUrl; typeURL {
				case "/ethermint.evm.v1.ExtensionOptionsEthereumTx":
					// handle as *evmtypes.MsgEthereumTx
					anteHandler = newEvmAnteHandler(options.HandlerOptions)
				case "/ethermint.types.v1.ExtensionOptionDynamicFeeTx":
					// cosmos-sdk tx with dynamic fee extension
					anteHandler, err = newCosmosAnteHandler(options)
					if err != nil {
						panic(err)
					}
				default:
					return ctx, errorsmod.Wrapf(
						sdkerrors.ErrUnknownExtensionOptions,
						"rejecting tx with unsupported extension option: %s", typeURL,
					)
				}

				return anteHandler(ctx, tx, simulate)
			}
		}

		// handle as totally normal Cosmos SDK tx
		switch tx.(type) {
		case sdk.Tx:
			anteHandler, err = newCosmosAnteHandler(options)
			if err != nil {
				panic(err)
			}
		default:
			return ctx, errorsmod.Wrapf(sdkerrors.ErrUnknownRequest, "invalid transaction type: %T", tx)
		}

		return anteHandler(ctx, tx, simulate)
	}
}

func newCosmosAnteHandler(options HandlerOptions) (sdk.AnteHandler, error) {
	if options.AccountKeeper == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "account keeper is required for AnteHandler")
	}
	if options.BankKeeper == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "bank keeper is required for AnteHandler")
	}
	if options.SignModeHandler == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "sign mode handler is required for ante builder")
	}
	if options.WasmConfig == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "wasm config is required for ante builder")
	}
	if options.TXCounterStoreKey == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "tx counter key is required for ante builder")
	}

	var sigGasConsumer = options.SigGasConsumer
	if sigGasConsumer == nil {
		sigGasConsumer = authante.DefaultSigVerificationGasConsumer
	}

	anteDecorators := []sdk.AnteDecorator{
		evmoscosmosante.RejectMessagesDecorator{}, // reject MsgEthereumTxs
		evmoscosmosante.NewAuthzLimiterDecorator( // disable the Msg types that cannot be included on an authz.MsgExec msgs field
			sdk.MsgTypeURL(&evmtypes.MsgEthereumTx{}),
		),

		authante.NewSetUpContextDecorator(),
		wasmkeeper.NewLimitSimulationGasDecorator(options.WasmConfig.SimulationGasLimit),
		wasmkeeper.NewCountTXDecorator(options.TXCounterStoreKey),
		wasmkeeper.NewGasRegisterDecorator(options.WasmKeeper.GetGasRegister()),
		authante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		// temporarily disabled so that chain can be tested locally without the provider chain running
		consumerante.NewDisabledModulesDecorator("/cosmos.evidence", "/cosmos.slashing"),
		// authante.NewMempoolFeeDecorator(),
		authante.NewValidateBasicDecorator(),
		authante.NewTxTimeoutHeightDecorator(),
		authante.NewValidateMemoDecorator(options.AccountKeeper),
		authante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		authante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker),
		// SetPubKeyDecorator must be called before all signature verification decorators
		authante.NewSetPubKeyDecorator(options.AccountKeeper),
		authante.NewValidateSigCountDecorator(options.AccountKeeper),
		authante.NewSigGasConsumeDecorator(options.AccountKeeper, sigGasConsumer),
		authante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		authante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
		evmante.NewGasWantedDecorator(options.EvmKeeper, options.FeeMarketKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}

type EVMMonoDecorator struct {
	accountKeeper      evmtypes.AccountKeeper
	bankKeeper         evmtypes.BankKeeper
	feeMarketKeeper    evmante.FeeMarketKeeper
	evmKeeper          evmante.EVMKeeper
	distributionKeeper evmosanteutils.DistributionKeeper
	stakingKeeper      evmosanteutils.StakingKeeper
	maxGasWanted       uint64
}

func newEvmAnteHandler(options evmosante.HandlerOptions) sdk.AnteHandler {
	return sdk.ChainAnteDecorators(
		newEvmMonoDecorator(
			options.AccountKeeper,
			options.BankKeeper,
			options.FeeMarketKeeper,
			options.EvmKeeper,
			options.DistributionKeeper,
			options.StakingKeeper,
			options.MaxTxGasWanted,
		),
	)
}

func newEvmMonoDecorator(
	ak evmtypes.AccountKeeper,
	bk evmtypes.BankKeeper,
	fk evmante.FeeMarketKeeper,
	ek evmante.EVMKeeper,
	dk evmosanteutils.DistributionKeeper,
	sk evmosanteutils.StakingKeeper,
	maxGasWanted uint64,
) sdk.AnteDecorator {
	return EVMMonoDecorator{
		accountKeeper:      ak,
		bankKeeper:         bk,
		feeMarketKeeper:    fk,
		evmKeeper:          ek,
		distributionKeeper: dk,
		stakingKeeper:      sk,
		maxGasWanted:       maxGasWanted,
	}
}

func (md EVMMonoDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	var (
		err       error
		txFeeInfo *txtypes.Fee
	)

	if !ctx.IsReCheckTx() {
		txFeeInfo, err = evmante.ValidateTx(tx)
		if err != nil {
			return ctx, err
		}
	}

	// 1. setup ctx
	ctx, err = evmante.SetupContext(ctx, tx, md.evmKeeper)
	if err != nil {
		return ctx, err
	}

	// 2. get utils
	decUtils, err := evmante.NewMonoDecoratorUtils(ctx, md.evmKeeper, md.feeMarketKeeper)
	if err != nil {
		return ctx, err
	}

	// Use the lowest priority of all the messages as the final one.
	for i, msg := range tx.GetMsgs() {
		ethMsg, txData, from, err := evmtypes.UnpackEthMsg(msg)
		if err != nil {
			return ctx, err
		}

		feeAmt := txData.Fee()
		gas := txData.GetGas()
		fee := sdkmath.LegacyNewDecFromBigInt(feeAmt)
		gasLimit := sdkmath.LegacyNewDecFromBigInt(new(big.Int).SetUint64(gas))

		// 2. mempool inclusion fee
		if ctx.IsCheckTx() && !simulate {
			if err := evmante.CheckMempoolFee(fee, decUtils.MempoolMinGasPrice, gasLimit, decUtils.Rules.IsLondon); err != nil {
				return ctx, err
			}
		}

		// 3. min gas price (global min fee)
		if txData.TxType() == ethtypes.DynamicFeeTxType && decUtils.BaseFee != nil {
			feeAmt = txData.EffectiveFee(decUtils.BaseFee)
			fee = sdkmath.LegacyNewDecFromBigInt(feeAmt)
		}

		if err := evmante.CheckGlobalFee(fee, decUtils.GlobalMinGasPrice, gasLimit); err != nil {
			return ctx, err
		}

		// 4. validate msg contents
		err = evmante.ValidateMsg(
			decUtils.EvmParams,
			txData,
			from,
		)
		if err != nil {
			return ctx, err
		}

		// 5. signature verification
		if err := evmante.SignatureVerification(
			ethMsg,
			decUtils.Signer,
			decUtils.EvmParams.AllowUnprotectedTxs,
		); err != nil {
			return ctx, err
		}

		// NOTE: sender address has been verified and cached
		from = ethMsg.GetFrom()

		// 6. account balance verification
		fromAddr := common.HexToAddress(ethMsg.From)
		// TODO: Use account from AccountKeeper instead
		account := md.evmKeeper.GetAccount(ctx, fromAddr)
		if err := evmante.VerifyAccountBalance(
			ctx,
			md.accountKeeper,
			account,
			fromAddr,
			txData,
		); err != nil {
			return ctx, err
		}

		// 7. can transfer
		coreMsg, err := ethMsg.AsMessage(decUtils.Signer, decUtils.BaseFee)
		if err != nil {
			return ctx, errorsmod.Wrapf(
				err,
				"failed to create an ethereum core.Message from signer %T", decUtils.Signer,
			)
		}

		if err := evmante.CanTransfer(
			ctx,
			md.evmKeeper,
			coreMsg,
			decUtils.BaseFee,
			decUtils.EthConfig,
			decUtils.EvmParams,
			decUtils.Rules.IsLondon,
		); err != nil {
			return ctx, err
		}

		// 8. gas consumption
		msgFees, err := evmkeeper.VerifyFee(
			txData,
			decUtils.EvmDenom,
			decUtils.BaseFee,
			decUtils.Rules.IsHomestead,
			decUtils.Rules.IsIstanbul,
			ctx.IsCheckTx(),
		)
		if err != nil {
			return ctx, err
		}

		err = evmante.ConsumeFeesAndEmitEvent(
			ctx,
			&evmante.ConsumeGasKeepers{
				Bank:         md.bankKeeper,
				Distribution: md.distributionKeeper,
				Evm:          md.evmKeeper,
				Staking:      md.stakingKeeper,
			},
			msgFees,
			from,
		)
		if err != nil {
			return ctx, err
		}

		gasWanted := evmante.UpdateCumulativeGasWanted(
			ctx,
			txData.GetGas(),
			md.maxGasWanted,
			decUtils.GasWanted,
		)
		decUtils.GasWanted = gasWanted

		minPriority := evmante.GetMsgPriority(
			txData,
			decUtils.MinPriority,
			decUtils.BaseFee,
		)
		decUtils.MinPriority = minPriority

		txFee := evmante.UpdateCumulativeTxFee(
			decUtils.TxFee,
			txData.Fee(),
			decUtils.EvmDenom,
		)
		decUtils.TxFee = txFee
		decUtils.TxGasLimit += gas

		// 10. increment sequence
		acc := md.accountKeeper.GetAccount(ctx, from)
		if acc == nil {
			// safety check: shouldn't happen
			return ctx, errorsmod.Wrapf(sdkerrors.ErrUnknownAddress,
				"account %s does not exist", acc)
		}

		if err := evmante.IncrementNonce(ctx, md.accountKeeper, acc, txData.GetNonce()); err != nil {
			return ctx, err
		}

		// 11. gas wanted
		if err := evmante.CheckGasWanted(ctx, md.feeMarketKeeper, tx, decUtils.Rules.IsLondon); err != nil {
			return ctx, err
		}

		// 12. emit events
		txIdx := uint64(i) // nosec: G701
		evmante.EmitTxHashEvent(ctx, ethMsg, decUtils.BlockTxIndex, txIdx)
	}

	if err := evmante.CheckTxFee(txFeeInfo, decUtils.TxFee, decUtils.TxGasLimit); err != nil {
		return ctx, err
	}

	ctx, err = evmante.CheckBlockGasLimit(ctx, decUtils.GasWanted, decUtils.MinPriority)
	if err != nil {
		return ctx, err
	}
	// handle Cosmos transactions
	return next(ctx, tx, simulate)
}
