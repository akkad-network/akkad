package erc20_test

import (
	"math/big"
	"strings"
	"time"

	errorsmod "cosmossdk.io/errors"

	abci "github.com/cometbft/cometbft/abci/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/evmos/evmos/v15/precompiles/erc20"
	"github.com/evmos/evmos/v15/precompiles/erc20/testdata"
	"github.com/evmos/evmos/v15/precompiles/testutil"
	commonfactory "github.com/evmos/evmos/v15/testutil/integration/common/factory"
	"github.com/evmos/evmos/v15/testutil/integration/evmos/factory"
	utiltx "github.com/evmos/evmos/v15/testutil/tx"
	erc20types "github.com/evmos/evmos/v15/x/erc20/types"
	evmtypes "github.com/evmos/evmos/v15/x/evm/types"
)

// setupSendAuthz is a helper function to set up a SendAuthorization for
// a given grantee and granter combination for a given amount.
//
// NOTE: A default expiration of 1 hour after the current block time is used.
func (s *PrecompileTestSuite) setupSendAuthz(
	grantee sdk.AccAddress, granterPriv cryptotypes.PrivKey, amount sdk.Coins,
) {
	granter := sdk.AccAddress(granterPriv.PubKey().Address())
	expiration := s.network.GetContext().BlockHeader().Time.Add(time.Hour)
	sendAuthz := banktypes.NewSendAuthorization(
		amount,
		[]sdk.AccAddress{},
	)

	msgGrant, err := authz.NewMsgGrant(
		granter,
		grantee,
		sendAuthz,
		&expiration,
	)
	s.Require().NoError(err, "failed to create MsgGrant")

	// Create an authorization
	txArgs := commonfactory.CosmosTxArgs{Msgs: []sdk.Msg{msgGrant}}
	_, err = s.factory.ExecuteCosmosTx(granterPriv, txArgs)
	s.Require().NoError(err, "failed to execute MsgGrant")
}

// requireOut is a helper utility to reduce the amount of boilerplate code in the query tests.
//
// It requires the output bytes and error to match the expected values. Additionally, the method outputs
// are unpacked and the first value is compared to the expected value.
//
// NOTE: It's sufficient to only check the first value because all methods in the ERC20 precompile only
// return a single value.
func (s *PrecompileTestSuite) requireOut(
	bz []byte,
	err error,
	method abi.Method,
	expPass bool,
	errContains string,
	expValue interface{},
) {
	if expPass {
		s.Require().NoError(err, "expected no error")
		s.Require().NotEmpty(bz, "expected bytes not to be empty")

		// Unpack the name into a string
		out, err := method.Outputs.Unpack(bz)
		s.Require().NoError(err, "expected no error unpacking")

		// Check if expValue is a big.Int. Because of a difference in uninitialized/empty values for big.Ints,
		// this comparison is often not working as expected, so we convert to Int64 here and compare those values.
		bigExp, ok := expValue.(*big.Int)
		if ok {
			bigOut, ok := out[0].(*big.Int)
			s.Require().True(ok, "expected output to be a big.Int")
			s.Require().Equal(bigExp.Int64(), bigOut.Int64(), "expected different value")
		} else {
			s.Require().Equal(expValue, out[0], "expected different value")
		}
	} else {
		s.Require().Error(err, "expected error")
		s.Require().Contains(err.Error(), errContains, "expected different error")
	}
}

// requireSendAuthz is a helper function to check that a SendAuthorization
// exists for a given grantee and granter combination for a given amount.
//
// NOTE: This helper expects only one authorization to exist.
func (s *PrecompileTestSuite) requireSendAuthz(grantee, granter sdk.AccAddress, amount sdk.Coins, allowList []string) {
	grants, err := s.grpcHandler.GetGrantsByGrantee(grantee.String())
	s.Require().NoError(err, "expected no error querying the grants")
	s.Require().Len(grants, 1, "expected one grant")
	s.Require().Equal(grantee.String(), grants[0].Grantee, "expected different grantee")
	s.Require().Equal(granter.String(), grants[0].Granter, "expected different granter")

	authzs, err := s.grpcHandler.GetAuthorizationsByGrantee(grantee.String())
	s.Require().NoError(err, "expected no error unpacking the authorization")
	s.Require().Len(authzs, 1, "expected one authorization")

	sendAuthz, ok := authzs[0].(*banktypes.SendAuthorization)
	s.Require().True(ok, "expected send authorization")

	spendLimits := sendAuthz.SpendLimit
	s.Require().Equal(amount, spendLimits, "expected different spend limit amount")
	if len(allowList) == 0 {
		s.Require().Empty(sendAuthz.AllowList, "expected empty allow list")
	} else {
		s.Require().Equal(allowList, sendAuthz.AllowList, "expected different allow list")
	}
}

// setupERC20Precompile is a helper function to set up an instance of the ERC20 precompile for
// a given token denomination, set the token pair in the ERC20 keeper and adds the precompile
// to the available and active precompiles.
func (s *PrecompileTestSuite) setupERC20Precompile(denom string) *erc20.Precompile {
	tokenPair := erc20types.NewTokenPair(utiltx.GenerateAddress(), denom, erc20types.OWNER_MODULE)
	s.network.App.Erc20Keeper.SetTokenPair(s.network.GetContext(), tokenPair)

	precompile, err := erc20.NewPrecompile(
		tokenPair,
		s.network.App.BankKeeper,
		s.network.App.AuthzKeeper,
		s.network.App.TransferKeeper,
	)
	s.Require().NoError(err, "failed to create %q erc20 precompile", denom)

	err = s.network.App.EvmKeeper.AddEVMExtensions(s.network.GetContext(), precompile)
	s.Require().NoError(err, "failed to add %q erc20 precompile to EVM extensions", denom)

	return precompile
}

// callType constants to differentiate between direct calls and calls through a contract.
const (
	directCall = iota + 1
	contractCall
)

// getCallArgs is a helper function to return the correct call arguments for a given call type.
//
// In case of a direct call to the precompile, the precompile's ABI is used. Otherwise, the
// ERC20CallerContract's ABI is used and the given contract address.
func (s *PrecompileTestSuite) getTxAndCallArgs(callType int, contractAddr common.Address) (evmtypes.EvmTxArgs, factory.CallArgs) {
	txArgs := evmtypes.EvmTxArgs{}
	callArgs := factory.CallArgs{}

	switch callType {
	case directCall:
		precompileAddr := s.precompile.Address()
		txArgs.To = &precompileAddr
		callArgs.ContractABI = s.precompile.ABI
	case contractCall:
		txArgs.To = &contractAddr
		callArgs.ContractABI = testdata.ERC20CallerContract.ABI
	}

	return txArgs, callArgs
}

// callContractAndCheckLogs is a helper function to call a contract and check the logs using
// the integration test utilities.
//
// It returns the Cosmos Tx response, the decoded Ethereum Tx response and an error. This error value
// is nil, if the expected logs are found and the VM error is the expected one, should one be expected.
//
// TODO: add this to network utils?
func (s *PrecompileTestSuite) callContractAndCheckLogs(
	priv cryptotypes.PrivKey,
	txArgs evmtypes.EvmTxArgs,
	callArgs factory.CallArgs,
	logCheckArgs testutil.LogCheckArgs,
) (abci.ResponseDeliverTx, *evmtypes.MsgEthereumTxResponse, error) {
	res, err := s.factory.ExecuteContractCall(priv, txArgs, callArgs)
	logCheckArgs.Res = res
	if err != nil {
		// NOTE: here we are still passing the response to the log check function,
		// because we want to check the logs and expected error in case of a VM error.
		//
		// TODO: refactor CheckLogs function
		return abci.ResponseDeliverTx{}, nil, CheckError(err, logCheckArgs)
	}

	ethRes, err := evmtypes.DecodeTxResponse(res.Data)
	if err != nil {
		return abci.ResponseDeliverTx{}, nil, err
	}

	return res, ethRes, testutil.CheckLogs(logCheckArgs)
}

// CheckError is a helper function to check if the error is the expected one.
//
// TODO: improve this
func CheckError(err error, logCheckArgs testutil.LogCheckArgs) error {
	if (err == nil) && logCheckArgs.ExpPass {
		return nil
	}

	if (err == nil) && !logCheckArgs.ExpPass {
		return errorsmod.Wrap(err, "expected error but got none")
	}

	if (err != nil) && logCheckArgs.ExpPass {
		return errorsmod.Wrap(err, "expected no error but got one")
	}

	if logCheckArgs.ErrContains == "" {
		// NOTE: if err contains is empty, we return the error as it is
		return errorsmod.Wrap(err, "ErrContains needs to be filled")
	}

	if !strings.Contains(err.Error(), logCheckArgs.ErrContains) {
		return errorsmod.Wrap(err, "expected different error")
	}

	return nil
}
