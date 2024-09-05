// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package network

import (
	"errors"
	"math/big"

	"cosmossdk.io/math"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	testtx "github.com/evmos/evmos/v19/testutil/tx"
	evmostypes "github.com/evmos/evmos/v19/types"
	"github.com/evmos/evmos/v19/utils"
	evmtypes "github.com/evmos/evmos/v19/x/evm/types"
)

type Decimals uint32

const (
	EvmDenom                  = "uatom"
	BondDenom                 = utils.BaseDenom
	SixDecimals      Decimals = 6
	EighteenDecimals Decimals = 18
)

// DecimalToken represent a token in terms of its denom and decimals.
// FIX: this should be defined at protocol level not in tests
type DecimalToken struct {
	Denom    string
	Decimals Decimals
}

func NewDecimalToken(denom string, decimals Decimals) (DecimalToken, error) {
	if decimals != SixDecimals && decimals != EighteenDecimals {
		return DecimalToken{}, errors.New("decimals value not supported")
	}
	dt := DecimalToken{
		Denom:    denom,
		Decimals: decimals,
	}
	return dt, nil
}

// Config defines the configuration for a chain.
// It allows for customization of the network to adjust to
// testing needs.
type Config struct {
	chainID            string
	eip155ChainID      *big.Int
	amountOfValidators int
	preFundedAccounts  []sdktypes.AccAddress
	balances           []banktypes.Balance
	evmToken           DecimalToken
	bondToken          DecimalToken
	customGenesisState CustomGenesisState
}

type CustomGenesisState map[string]interface{}

// DefaultConfig returns the default configuration for a chain. The default
// configuration has different tokens for the staking and the payment of fees.
func DefaultConfig() Config {
	account, _ := testtx.NewAccAddressAndKey()
	return Config{
		chainID:            utils.MainnetChainID + "-1",
		eip155ChainID:      big.NewInt(9001),
		amountOfValidators: 3,
		// No funded accounts besides the validators by default
		preFundedAccounts: []sdktypes.AccAddress{account},
		// NOTE: Per default, the balances are left empty, and the pre-funded accounts are used.
		balances: nil,
		evmToken: DecimalToken{
			Denom:    EvmDenom,
			Decimals: SixDecimals,
		},
		bondToken: DecimalToken{
			Denom:    BondDenom,
			Decimals: EighteenDecimals,
		},
		customGenesisState: nil,
	}
}

// getGenAccountsAndBalances takes the network configuration and returns the used
// genesis accounts and balances. If no balances are provided, genesis accounts will
// be funded with both evm and bond denom in the same quantity and according to the
// decimals. If evm token and bond token are the same, the initial allocation will be
// doubled.
//
// NOTE: If the balances are set, the pre-funded accounts are ignored.
func getGenAccountsAndBalances(cfg Config) (genAccounts []authtypes.GenesisAccount, balances []banktypes.Balance) {
	if len(cfg.balances) > 0 {
		balances = cfg.balances
		accounts := getAccAddrsFromBalances(balances)
		genAccounts = createGenesisAccounts(accounts)
	} else {

		var defaultBalances sdktypes.Coins

		initialBalanceEvm := PrefundedAccountInitialBalance
		if cfg.evmToken.Decimals == SixDecimals {
			initialBalanceEvm = math.NewIntFromBigInt(
				evmtypes.Convert18To6DecimalsBigInt(initialBalanceEvm.BigInt()),
			)
		}
		defaultBalances = defaultBalances.Add(sdktypes.NewCoin(cfg.evmToken.Denom, initialBalanceEvm))

		initialBalanceBond := PrefundedAccountInitialBalance
		if cfg.bondToken.Decimals == SixDecimals {
			initialBalanceBond = math.NewIntFromBigInt(
				evmtypes.Convert18To6DecimalsBigInt(initialBalanceBond.BigInt()),
			)
		}
		defaultBalances = defaultBalances.Add(sdktypes.NewCoin(cfg.bondToken.Denom, initialBalanceBond))

		genAccounts = createGenesisAccounts(cfg.preFundedAccounts)
		balances = createBalances(cfg.preFundedAccounts, defaultBalances)
	}

	return
}

// ConfigOption defines a function that can modify the NetworkConfig.
// The purpose of this is to force to be declarative when the default configuration
// requires to be changed.
type ConfigOption func(*Config)

// WithChainID sets a custom chainID for the network. It panics if the chainID is invalid.
func WithChainID(chainID string) ConfigOption {
	chainIDNum, err := evmostypes.ParseChainID(chainID)
	if err != nil {
		panic(err)
	}
	return func(cfg *Config) {
		cfg.chainID = chainID
		cfg.eip155ChainID = chainIDNum
	}
}

// WithAmountOfValidators sets the amount of validators for the network.
func WithAmountOfValidators(amount int) ConfigOption {
	return func(cfg *Config) {
		cfg.amountOfValidators = amount
	}
}

// WithPreFundedAccounts sets the pre-funded accounts for the network.
func WithPreFundedAccounts(accounts ...sdktypes.AccAddress) ConfigOption {
	return func(cfg *Config) {
		cfg.preFundedAccounts = accounts
	}
}

// WithBalances sets the specific balances for the pre-funded accounts, that
// are being set up for the network.
func WithBalances(balances ...banktypes.Balance) ConfigOption {
	return func(cfg *Config) {
		cfg.balances = append(cfg.balances, balances...)
	}
}

// WithEvmToken sets the denom  and decimals for the evm token
// in the network.
func WithEvmToken(denom string, decimals Decimals) ConfigOption {
	return func(cfg *Config) {
		if cfg.bondToken.Denom == denom && cfg.bondToken.Decimals != decimals {
			panic("Bond denom and EVM denom are the same and cannot have different decimals")
		}
		cfg.evmToken = DecimalToken{
			Denom:    denom,
			Decimals: decimals,
		}
	}
}

// WithBondToken sets the denom and decimals for the staking token
// in the network.
// TODO: this should change the staking denom used too.
func WithBondToken(denom string, decimals Decimals) ConfigOption {
	return func(cfg *Config) {
		if cfg.evmToken.Denom == denom && cfg.evmToken.Decimals != decimals {
			panic("Bond denom and EVM denom are the same and cannot have different decimals")
		}
		cfg.bondToken = DecimalToken{
			Denom:    denom,
			Decimals: decimals,
		}
	}
}

// WithCustomGenesis sets the custom genesis of the network for specific modules.
func WithCustomGenesis(customGenesis CustomGenesisState) ConfigOption {
	return func(cfg *Config) {
		cfg.customGenesisState = customGenesis
	}
}
