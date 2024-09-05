package utils_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	grpchandler "github.com/evmos/evmos/v19/testutil/integration/evmos/grpc"
	testkeyring "github.com/evmos/evmos/v19/testutil/integration/evmos/keyring"
	"github.com/evmos/evmos/v19/testutil/integration/evmos/network"
	"github.com/evmos/evmos/v19/testutil/integration/evmos/utils"
	evmtypes "github.com/evmos/evmos/v19/x/evm/types"
	"github.com/stretchr/testify/require"
)

func TestCheckBalances(t *testing.T) {
	keyring := testkeyring.New(1)
	address := keyring.GetAccAddr(0).String()

	defaultSixDecAmount := sdk.NewIntFromBigInt(evmtypes.Convert18To6DecimalsBigInt(network.PrefundedAccountInitialBalance.BigInt()))
	defaultEighteenDecAmount := sdk.NewIntFromBigInt(network.PrefundedAccountInitialBalance.BigInt())

	testcases := []struct {
		name          string
		evmToken      network.DecimalToken
		bondToken     network.DecimalToken
		expEvmAmount  math.Int
		expBondAmount math.Int
		expPass       bool
		errContains   string
	}{
		{
			name:          "pass",
			evmToken:      network.DecimalToken{Denom: "uatom", Decimals: network.SixDecimals},
			bondToken:     network.DecimalToken{Denom: "aevmos", Decimals: network.EighteenDecimals},
			expEvmAmount:  defaultSixDecAmount,
			expBondAmount: defaultEighteenDecAmount,
			expPass:       true,
		},
		{
			name:          "fail - wrong amount",
			evmToken:      network.DecimalToken{Denom: "uatom", Decimals: network.SixDecimals},
			bondToken:     network.DecimalToken{Denom: "aevmos", Decimals: network.EighteenDecimals},
			expEvmAmount:  defaultSixDecAmount,
			expBondAmount: defaultSixDecAmount,
			errContains:   "expected balance",
		},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			nw := network.New(
				network.WithEvmToken(tc.evmToken.Denom, tc.evmToken.Decimals),
				network.WithBondToken(tc.bondToken.Denom, tc.bondToken.Decimals),
				network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
			)
			handler := grpchandler.NewIntegrationHandler(nw)

			balances := []banktypes.Balance{
				{
					Address: address,
					Coins: sdk.NewCoins(
						sdk.NewCoin(tc.evmToken.Denom, tc.expEvmAmount),
					),
				},
				{
					Address: address,
					Coins: sdk.NewCoins(
						sdk.NewCoin(tc.bondToken.Denom, tc.expBondAmount),
					),
				},
			}

			err := utils.CheckBalances(handler, balances)
			if tc.expPass {
				require.NoError(t, err, "unexpected error checking balances")
			} else {
				require.Error(t, err, "expected error checking balances")
				require.ErrorContains(t, err, tc.errContains, "expected different error checking balances")
			}
		})
	}
}
