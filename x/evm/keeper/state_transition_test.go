// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package keeper_test

import (
	"fmt"
	"math"
	"math/big"

	sdkmath "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/evmos/evmos/v18/testutil/integration/evmos/factory"
	"github.com/evmos/evmos/v18/testutil/integration/evmos/grpc"
	testkeyring "github.com/evmos/evmos/v18/testutil/integration/evmos/keyring"
	"github.com/evmos/evmos/v18/testutil/integration/evmos/network"
	"github.com/evmos/evmos/v18/testutil/integration/evmos/utils"
	utiltx "github.com/evmos/evmos/v18/testutil/tx"
	"github.com/evmos/evmos/v18/x/evm/keeper"
	"github.com/evmos/evmos/v18/x/evm/types"
	feemarkettypes "github.com/evmos/evmos/v18/x/feemarket/types"
	"github.com/evmos/evmos/v18/x/evm/statedb"
)

func (suite *EvmKeeperTestSuite) TestGetHashFn() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	header := unitNetwork.GetContext().BlockHeader()
	h, _ := cmttypes.HeaderFromProto(&header)
	hash := h.Hash()

	testCases := []struct {
		msg      string
		height   uint64
		malleate func() sdk.Context
		expHash  common.Hash
	}{
		{
			"case 1.1: context hash cached",
			uint64(unitNetwork.GetContext().BlockHeight()),
			func() sdk.Context {
				return unitNetwork.GetContext().WithHeaderHash(
					tmhash.Sum([]byte("header")),
				)
			},
			common.BytesToHash(tmhash.Sum([]byte("header"))),
		},
		{
			"case 1.2: failed to cast Tendermint header",
			uint64(unitNetwork.GetContext().BlockHeight()),
			func() sdk.Context {
				header := tmproto.Header{}
				header.Height = unitNetwork.GetContext().BlockHeight()
				return unitNetwork.GetContext().WithBlockHeader(header)
			},
			common.Hash{},
		},
		{
			"case 1.3: hash calculated from Tendermint header",
			uint64(unitNetwork.GetContext().BlockHeight()),
			func() sdk.Context {
				return unitNetwork.GetContext().WithBlockHeader(header)
			},
			common.BytesToHash(hash),
		},
		{
			"case 2.1: height lower than current one, hist info not found",
			1,
			func() sdk.Context {
				return unitNetwork.GetContext().WithBlockHeight(10)
			},
			common.Hash{},
		},
		{
			"case 2.2: height lower than current one, invalid hist info header",
			1,
			func() sdk.Context {
				suite.Require().NoError(unitNetwork.App.StakingKeeper.SetHistoricalInfo(unitNetwork.GetContext(), 1, &stakingtypes.HistoricalInfo{}))
				return unitNetwork.GetContext().WithBlockHeight(10)
			},
			common.Hash{},
		},
		{
			"case 2.3: height lower than current one, calculated from hist info header",
			1,
			func() sdk.Context {
				histInfo := &stakingtypes.HistoricalInfo{
					Header: header,
				}
				suite.Require().NoError(unitNetwork.App.StakingKeeper.SetHistoricalInfo(unitNetwork.GetContext(), 1, histInfo))
				return unitNetwork.GetContext().WithBlockHeight(10)
			},
			common.BytesToHash(hash),
		},
		{
			"case 3: height greater than current one",
			200,
			func() sdk.Context { return unitNetwork.GetContext() },
			common.Hash{},
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.msg), func() {
			ctx := tc.malleate()

			// Function being tested
			hash := unitNetwork.App.EvmKeeper.GetHashFn(ctx)(tc.height)
			suite.Require().Equal(tc.expHash, hash)

			err := unitNetwork.NextBlock()
			suite.Require().NoError(err)
		})
	}
}

func (suite *EvmKeeperTestSuite) TestGetCoinbaseAddress() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	validators := unitNetwork.GetValidators()
	proposerAddressHex := utils.ValidatorConsAddressToHex(
		validators[0].OperatorAddress,
	)

	testCases := []struct {
		msg      string
		malleate func() sdk.Context
		expPass  bool
	}{
		{
			"validator not found",
			func() sdk.Context {
				header := unitNetwork.GetContext().BlockHeader()
				header.ProposerAddress = []byte{}
				return unitNetwork.GetContext().WithBlockHeader(header)
			},
			false,
		},
		{
			"success",
			func() sdk.Context {
				return unitNetwork.GetContext()
			},
			true,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.msg), func() {
			ctx := tc.malleate()
			proposerAddress := ctx.BlockHeader().ProposerAddress

			// Function being tested
			coinbase, err := unitNetwork.App.EvmKeeper.GetCoinbaseAddress(
				ctx,
				sdk.ConsAddress(proposerAddress),
			)

			if tc.expPass {
				suite.Require().NoError(err)
				suite.Require().Equal(proposerAddressHex, coinbase)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

func (suite *EvmKeeperTestSuite) TestGetEthIntrinsicGas() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	testCases := []struct {
		name               string
		data               []byte
		accessList         gethtypes.AccessList
		height             int64
		isContractCreation bool
		noError            bool
		expGas             uint64
	}{
		{
			"no data, no accesslist, not contract creation, not homestead, not istanbul",
			nil,
			nil,
			1,
			false,
			true,
			params.TxGas,
		},
		{
			"with one zero data, no accesslist, not contract creation, not homestead, not istanbul",
			[]byte{0},
			nil,
			1,
			false,
			true,
			params.TxGas + params.TxDataZeroGas*1,
		},
		{
			"with one non zero data, no accesslist, not contract creation, not homestead, not istanbul",
			[]byte{1},
			nil,
			1,
			true,
			true,
			params.TxGas + params.TxDataNonZeroGasFrontier*1,
		},
		{
			"no data, one accesslist, not contract creation, not homestead, not istanbul",
			nil,
			[]gethtypes.AccessTuple{
				{},
			},
			1,
			false,
			true,
			params.TxGas + params.TxAccessListAddressGas,
		},
		{
			"no data, one accesslist with one storageKey, not contract creation, not homestead, not istanbul",
			nil,
			[]gethtypes.AccessTuple{
				{StorageKeys: make([]common.Hash, 1)},
			},
			1,
			false,
			true,
			params.TxGas + params.TxAccessListAddressGas + params.TxAccessListStorageKeyGas*1,
		},
		{
			"no data, no accesslist, is contract creation, is homestead, not istanbul",
			nil,
			nil,
			2,
			true,
			true,
			params.TxGasContractCreation,
		},
		{
			"with one zero data, no accesslist, not contract creation, is homestead, is istanbul",
			[]byte{1},
			nil,
			3,
			false,
			true,
			params.TxGas + params.TxDataNonZeroGasEIP2028*1,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			params := unitNetwork.App.EvmKeeper.GetParams(
				unitNetwork.GetContext(),
			)
			ethCfg := params.ChainConfig.EthereumConfig(
				unitNetwork.App.EvmKeeper.ChainID(),
			)
			ethCfg.HomesteadBlock = big.NewInt(2)
			ethCfg.IstanbulBlock = big.NewInt(3)
			signer := gethtypes.LatestSignerForChainID(unitNetwork.App.EvmKeeper.ChainID())

			ctx := unitNetwork.GetContext().WithBlockHeight(tc.height)

			addr := keyring.GetAddr(0)
			krSigner := utiltx.NewSigner(keyring.GetPrivKey(0))
			nonce := unitNetwork.App.EvmKeeper.GetNonce(ctx, addr)
			m, err := newNativeMessage(
				nonce,
				ctx.BlockHeight(),
				addr,
				ethCfg,
				krSigner,
				signer,
				gethtypes.AccessListTxType,
				tc.data,
				tc.accessList,
			)
			suite.Require().NoError(err)

			// Function being tested
			gas, err := unitNetwork.App.EvmKeeper.GetEthIntrinsicGas(
				ctx,
				m,
				ethCfg,
				tc.isContractCreation,
			)

			if tc.noError {
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}

			suite.Require().Equal(tc.expGas, gas)
		})
	}
}

func (suite *EvmKeeperTestSuite) TestGasToRefund() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	testCases := []struct {
		name           string
		gasconsumed    uint64
		refundQuotient uint64
		expGasRefund   uint64
		expPanic       bool
	}{
		{
			"gas refund 5",
			5,
			1,
			5,
			false,
		},
		{
			"gas refund 10",
			10,
			1,
			10,
			false,
		},
		{
			"gas refund availableRefund",
			11,
			1,
			10,
			false,
		},
		{
			"gas refund quotient 0",
			11,
			0,
			0,
			true,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			vmdb := unitNetwork.GetStateDB()
			vmdb.AddRefund(10)

			if tc.expPanic {
				panicF := func() {
					//nolint:staticcheck
					keeper.GasToRefund(vmdb.GetRefund(), tc.gasconsumed, tc.refundQuotient)
				}
				suite.Require().Panics(panicF)
			} else {
				gr := keeper.GasToRefund(vmdb.GetRefund(), tc.gasconsumed, tc.refundQuotient)
				suite.Require().Equal(tc.expGasRefund, gr)
			}
		})
	}
}

func (suite *EvmKeeperTestSuite) TestRefundGas() {
	// FeeCollector account is pre-funded with enough tokens
	// for refund to work
	// NOTE: everything should happen within the same block for
	// feecollector account to remain funded
	coins := sdk.NewCoins(sdk.NewCoin(
		types.DefaultEVMDenom,
		sdkmath.NewInt(6e18),
	))
	balances := []banktypes.Balance{
		{
			Address: authtypes.NewModuleAddress(authtypes.FeeCollectorName).String(),
			Coins:   coins,
		},
	}
	bankGenesis := banktypes.DefaultGenesisState()
	bankGenesis.Balances = balances
	customGenesis := network.CustomGenesisState{}
	customGenesis[banktypes.ModuleName] = bankGenesis

	keyring := testkeyring.New(2)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
		network.WithCustomGenesis(customGenesis),
	)
	grpcHandler := grpc.NewIntegrationHandler(unitNetwork)
	txFactory := factory.New(unitNetwork, grpcHandler)

	sender := keyring.GetKey(0)
	recipient := keyring.GetAddr(1)

	testCases := []struct {
		name           string
		leftoverGas    uint64
		refundQuotient uint64
		noError        bool
		expGasRefund   uint64
		gasPrice       *big.Int
	}{
		{
			name:           "leftoverGas more than tx gas limit",
			leftoverGas:    params.TxGas + 1,
			refundQuotient: params.RefundQuotient,
			noError:        false,
			expGasRefund:   params.TxGas + 1,
		},
		{
			name:           "leftoverGas equal to tx gas limit, insufficient fee collector account",
			leftoverGas:    params.TxGas,
			refundQuotient: params.RefundQuotient,
			noError:        true,
			expGasRefund:   0,
		},
		{
			name:           "leftoverGas less than to tx gas limit",
			leftoverGas:    params.TxGas - 1,
			refundQuotient: params.RefundQuotient,
			noError:        true,
			expGasRefund:   0,
		},
		{
			name:           "no leftoverGas, refund half used gas ",
			leftoverGas:    0,
			refundQuotient: params.RefundQuotient,
			noError:        true,
			expGasRefund:   params.TxGas / params.RefundQuotient,
		},
		{
			name:           "invalid GasPrice in message",
			leftoverGas:    0,
			refundQuotient: params.RefundQuotient,
			noError:        false,
			expGasRefund:   params.TxGas / params.RefundQuotient,
			gasPrice:       big.NewInt(-100),
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			coreMsg, err := txFactory.GenerateGethCoreMsg(
				sender.Priv,
				types.EvmTxArgs{
					To:       &recipient,
					Amount:   big.NewInt(100),
					GasPrice: tc.gasPrice,
				},
			)
			suite.Require().NoError(err)
			transactionGas := coreMsg.Gas()

			vmdb := unitNetwork.GetStateDB()
			vmdb.AddRefund(params.TxGas)

			if tc.leftoverGas > transactionGas {
				return
			}

			gasUsed := transactionGas - tc.leftoverGas
			refund := keeper.GasToRefund(vmdb.GetRefund(), gasUsed, tc.refundQuotient)
			suite.Require().Equal(tc.expGasRefund, refund)

			err = unitNetwork.App.EvmKeeper.RefundGas(
				unitNetwork.GetContext(),
				coreMsg,
				refund,
				unitNetwork.GetDenom(),
			)
			if tc.noError {
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

func (suite *EvmKeeperTestSuite) TestResetGasMeterAndConsumeGas() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	testCases := []struct {
		name        string
		gasConsumed uint64
		gasUsed     uint64
		expPanic    bool
	}{
		{
			"gas consumed 5, used 5",
			5,
			5,
			false,
		},
		{
			"gas consumed 5, used 10",
			5,
			10,
			false,
		},
		{
			"gas consumed 10, used 10",
			10,
			10,
			false,
		},
		{
			"gas consumed 11, used 10, NegativeGasConsumed panic",
			11,
			10,
			true,
		},
		{
			"gas consumed 1, used 10, overflow panic",
			1,
			math.MaxUint64,
			true,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			panicF := func() {
				gm := storetypes.NewGasMeter(10)
				gm.ConsumeGas(tc.gasConsumed, "")
				ctx := unitNetwork.GetContext().WithGasMeter(gm)
				unitNetwork.App.EvmKeeper.ResetGasMeterAndConsumeGas(ctx, tc.gasUsed)
			}

			if tc.expPanic {
				suite.Require().Panics(panicF)
			} else {
				suite.Require().NotPanics(panicF)
			}
		})
	}
}

func (suite *EvmKeeperTestSuite) TestEVMConfig() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	proposerAddress := unitNetwork.GetContext().BlockHeader().ProposerAddress
	eip155ChainID := unitNetwork.GetEIP155ChainID()
	cfg, err := unitNetwork.App.EvmKeeper.EVMConfig(
		unitNetwork.GetContext(),
		proposerAddress,
		eip155ChainID,
	)
	suite.Require().NoError(err)
	suite.Require().Equal(evmtypes.DefaultParams(), cfg.Params)
	// london hardfork is enabled by default
	suite.Require().Equal(big.NewInt(0), cfg.BaseFee)
	suite.Require().Equal(suite.address, cfg.CoinBase)
	suite.Require().Equal(types.DefaultParams().ChainConfig.EthereumConfig(big.NewInt(9000)), cfg.ChainConfig)

	validators := unitNetwork.GetValidators()
	proposerHextAddress := utils.ValidatorConsAddressToHex(validators[0].OperatorAddress)
	suite.Require().Equal(proposerHextAddress, cfg.CoinBase)

	networkChainID := unitNetwork.GetEIP155ChainID()
	networkConfig := types.DefaultParams().ChainConfig.EthereumConfig(networkChainID)
	suite.Require().Equal(networkConfig, cfg.ChainConfig)
}

func (suite *EvmKeeperTestSuite) TestApplyMessage() {
	keyring := testkeyring.New(2)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)
	grpcHandler := grpc.NewIntegrationHandler(unitNetwork)
	txFactory := factory.New(unitNetwork, grpcHandler)

	proposerAddress := unitNetwork.GetContext().BlockHeader().ProposerAddress
	config, err := unitNetwork.App.EvmKeeper.EVMConfig(
		unitNetwork.GetContext(),
		proposerAddress,
		unitNetwork.GetEIP155ChainID(),
	)
	suite.Require().NoError(err)

	// Generate a transfer tx message
	sender := keyring.GetKey(0)
	recipient := keyring.GetAddr(1)
	transferArgs := types.EvmTxArgs{
		To:     &recipient,
		Amount: big.NewInt(100),
	}
	coreMsg, err := txFactory.GenerateGethCoreMsg(
		sender.Priv,
		transferArgs,
	)
	suite.Require().NoError(err)

	tracer := unitNetwork.App.EvmKeeper.Tracer(
		unitNetwork.GetContext(),
		coreMsg,
		config.ChainConfig,
	)
	res, err := unitNetwork.App.EvmKeeper.ApplyMessage(
		unitNetwork.GetContext(),
		coreMsg,
		tracer,
		true,
	)
	suite.Require().NoError(err)
	suite.Require().False(res.Failed())

	// Compare gas to a transfer tx gas
	expectedGasUsed := params.TxGas
	suite.Require().Equal(expectedGasUsed, res.GasUsed)
}

func (suite *EvmKeeperTestSuite) TestApplyMessageWithConfig() {
	keyring := testkeyring.New(2)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)
	grpcHandler := grpc.NewIntegrationHandler(unitNetwork)
	txFactory := factory.New(unitNetwork, grpcHandler)

	testCases := []struct {
		name               string
		getMessage         func() core.Message
		getEVMParams       func() types.Params
		getFeeMarketParams func() feemarkettypes.Params
		expErr             bool
		expectedGasUsed    uint64
	}{
		{
			"success - messsage applied ok with default params",
			func() core.Message {
				sender := keyring.GetKey(0)
				recipient := keyring.GetAddr(1)
				msg, err := txFactory.GenerateGethCoreMsg(sender.Priv, types.EvmTxArgs{
					To:     &recipient,
					Amount: big.NewInt(100),
				})
				suite.Require().NoError(err)
				return msg
			},
			types.DefaultParams,
			feemarkettypes.DefaultParams,
			false,
			false,
			false,
		},
		{
			"call contract tx with config param EnableCall = false",
			func() {
				config.Params.AccessControl = evmtypes.AccessControl{
					Call: evmtypes.AccessControlType{
						AccessType: evmtypes.AccessTypeRestricted,
					},
				}
				msg, err = newNativeMessage(
					vmdb.GetNonce(suite.address),
					suite.ctx.BlockHeight(),
					suite.address,
					chainCfg,
					suite.signer,
					signer,
					ethtypes.AccessListTxType,
					nil,
					nil,
				)
				suite.Require().NoError(err)
				return msg
			},
			false,
			true,
			0,
		},
		{
			"create contract tx with config param EnableCreate = false",
			func() {
				msg, err = suite.createContractGethMsg(vmdb.GetNonce(suite.address), signer, chainCfg, big.NewInt(2))
				suite.Require().NoError(err)
				config.Params.AccessControl = evmtypes.AccessControl{
					Create: evmtypes.AccessControlType{
						AccessType: evmtypes.AccessTypeRestricted,
					},
				}
			},
			false,
			true,
			0,
		},
		{
			"fail - fix panic when minimumGasUsed is not uint64",
			func() core.Message {
				sender := keyring.GetKey(0)
				recipient := keyring.GetAddr(1)
				msg, err := txFactory.GenerateGethCoreMsg(sender.Priv, types.EvmTxArgs{
					To:     &recipient,
					Amount: big.NewInt(100),
				})
				suite.Require().NoError(err)
				return msg
			},
			types.DefaultParams,
			func() feemarkettypes.Params {
				paramsRes, err := grpcHandler.GetFeeMarketParams()
				suite.Require().NoError(err)
				params := paramsRes.GetParams()
				params.MinGasMultiplier = sdkmath.LegacyNewDec(math.MaxInt64).MulInt64(100)
				return params
			},
			true,
			false,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			msg := tc.getMessage()
			evmParams := tc.getEVMParams()
			err := unitNetwork.App.EvmKeeper.SetParams(
				unitNetwork.GetContext(),
				evmParams,
			)
			suite.Require().NoError(err)
			feeMarketparams := tc.getFeeMarketParams()
			err = unitNetwork.App.FeeMarketKeeper.SetParams(
				unitNetwork.GetContext(),
				feeMarketparams,
			)
			suite.Require().NoError(err)

			txConfig := unitNetwork.App.EvmKeeper.TxConfig(
				unitNetwork.GetContext(),
				common.Hash{},
			)
			proposerAddress := unitNetwork.GetContext().BlockHeader().ProposerAddress
			config, err := unitNetwork.App.EvmKeeper.EVMConfig(
				unitNetwork.GetContext(),
				proposerAddress,
				unitNetwork.GetEIP155ChainID(),
			)
			suite.Require().NoError(err)

			// Function being tested
			res, err := unitNetwork.App.EvmKeeper.ApplyMessageWithConfig(
				unitNetwork.GetContext(),
				msg,
				nil,
				true,
				config,
				txConfig,
			)

			if tc.expErr {
				suite.Require().Error(err)
			} else {
				suite.Require().NoError(err)
				suite.Require().False(res.Failed())
				suite.Require().Equal(tc.expectedGasUsed, res.GasUsed)
			}

			err = unitNetwork.NextBlock()
			if tc.expVMErr {
				suite.Require().NotEmpty(res.VmError)
				return
			}

			if tc.expVMErr {
				suite.Require().NotEmpty(res.VmError)
				return
			}

			suite.Require().NoError(err)
		})
	}
}

func (suite *KeeperTestSuite) createContractGethMsg(nonce uint64, signer ethtypes.Signer, cfg *params.ChainConfig, gasPrice *big.Int) (core.Message, error) {
	ethMsg, err := suite.createContractMsgTx(nonce, signer, gasPrice)
	if err != nil {
		return nil, err
	}

	msgSigner := ethtypes.MakeSigner(cfg, big.NewInt(suite.ctx.BlockHeight()))
	return ethMsg.AsMessage(msgSigner, nil)
}

func (suite *KeeperTestSuite) createContractMsgTx(nonce uint64, signer ethtypes.Signer, gasPrice *big.Int) (*evmtypes.MsgEthereumTx, error) {
	contractCreateTx := &ethtypes.AccessListTx{
		GasPrice: gasPrice,
		Gas:      params.TxGasContractCreation + 1000, // account for data length
		To:       nil,
		Data:     []byte("contract_data"),
		Nonce:    nonce,
	}
	ethTx := ethtypes.NewTx(contractCreateTx)
	ethMsg := &evmtypes.MsgEthereumTx{}
	err := ethMsg.FromEthereumTx(ethTx)
	suite.Require().NoError(err)
	ethMsg.From = suite.address.Hex()

	return ethMsg, ethMsg.Sign(signer, suite.signer)
}

func (suite *EvmKeeperTestSuite) TestGetProposerAddress() {
	keyring := testkeyring.New(1)
	unitNetwork := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)
	address := sdk.ConsAddress(keyring.GetAddr(0).Bytes())
	proposerAddress := sdk.ConsAddress(unitNetwork.GetContext().BlockHeader().ProposerAddress)
	testCases := []struct {
		msg    string
		addr   sdk.ConsAddress
		expAdr sdk.ConsAddress
	}{
		{
			"proposer address provided",
			address,
			address,
		},
		{
			"nil proposer address provided",
			nil,
			proposerAddress,
		},
		{
			"typed nil proposer address provided",
			sdk.ConsAddress{},
			proposerAddress,
		},
	}
	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.msg), func() {
			suite.Require().Equal(
				tc.expAdr,
				keeper.GetProposerAddress(unitNetwork.GetContext(), tc.addr),
			)
		})
	}
}
