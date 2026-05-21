package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/informalsystems/tm-load-test/pkg/loadtest"

	exchangetypes "github.com/InjectiveLabs/injective-core/injective-chain/modules/exchange/types"
)

// BuildSubaccountID creates the 32-byte Injective-style subaccount ID from a bech32 address and a nonce index.
// Format: [20-byte account address] ++ [4 zero bytes] ++ [8-byte big-endian nonce], hex-encoded with 0x prefix.
func BuildSubaccountID(bech32Addr string, nonce uint64) (string, error) {
	addr, err := sdk.AccAddressFromBech32(bech32Addr)
	if err != nil {
		return "", fmt.Errorf("invalid bech32 address %q: %w", bech32Addr, err)
	}
	if len(addr) != 20 {
		return "", fmt.Errorf("expected 20-byte address, got %d", len(addr))
	}
	var buf [32]byte
	copy(buf[0:20], addr)
	binary.BigEndian.PutUint64(buf[24:], nonce)
	return "0x" + hex.EncodeToString(buf[:]), nil
}

type InjectiveClient struct {
	factory *InjectiveClientFactory
	rng     *rand.Rand
}

var _ loadtest.ClientFactory = (*InjectiveClientFactory)(nil)
var _ loadtest.Client = (*InjectiveClient)(nil)

func (f *InjectiveClientFactory) ValidateConfig(cfg loadtest.Config) error { return nil }

func (f *InjectiveClientFactory) NewClient(cfg loadtest.Config) (loadtest.Client, error) {
	return &InjectiveClient{
		factory: f,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func activeTxTypesFromEnv() ([]int, error) {
	raw := strings.TrimSpace(os.Getenv("INJ_TX_TYPES"))
	if raw == "" {
		// Default to bank sends.
		return []int{4}, nil
	}
	parts := strings.Split(raw, ",")
	active := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid INJ_TX_TYPES value %q: %w", part, err)
		}
		active = append(active, value)
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("INJ_TX_TYPES resolved to no transaction types")
	}
	return active, nil
}

func denomFromEnv() string {
	if d := strings.TrimSpace(os.Getenv("INJ_DENOM")); d != "" {
		return d
	}
	return defaultDenom
}

func subaccountIDForWallet(walletAddr string, rng *rand.Rand) (string, error) {
	if subaccountID := strings.TrimSpace(os.Getenv("INJ_SUBACCOUNT_ID")); subaccountID != "" {
		return subaccountID, nil
	}

	nonces := uint64(1)
	if raw := strings.TrimSpace(os.Getenv("INJ_SUBACCOUNT_NONCES")); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil && n > 0 {
			nonces = n
		}
	}

	nonce := uint64(0)
	if nonces > 1 {
		nonce = uint64(rng.Int63n(int64(nonces)))
	}
	return BuildSubaccountID(walletAddr, nonce)
}

func (c *InjectiveClient) GenerateTx() ([]byte, error) {
	wallet := c.factory.NextWallet()
	if wallet == nil {
		return nil, fmt.Errorf("no wallets loaded")
	}

	activeTxTypes, err := activeTxTypesFromEnv()
	if err != nil {
		return nil, err
	}
	txType := activeTxTypes[c.rng.Intn(len(activeTxTypes))]

	var msg sdk.Msg
	switch txType {
	case 4:
		// bank.MsgSend
		toAddr := wallet.Address
		if len(c.factory.Wallets) > 1 {
			for {
				candidate := c.factory.Wallets[c.rng.Intn(len(c.factory.Wallets))].Address
				if candidate != wallet.Address {
					toAddr = candidate
					break
				}
			}
		}
		msg = &banktypes.MsgSend{
			FromAddress: wallet.Address,
			ToAddress:   toAddr,
			Amount:      sdk.NewCoins(sdk.NewCoin(denomFromEnv(), sdkmath.NewInt(1))),
		}

	case 0:
		// exchange.MsgDeposit
		subaccountID, err := subaccountIDForWallet(wallet.Address, c.rng)
		if err != nil {
			return nil, err
		}
		msg = &exchangetypes.MsgDeposit{
			Sender:       wallet.Address,
			SubaccountId: subaccountID,
			Amount:       sdk.NewCoin(denomFromEnv(), sdkmath.NewInt(1)),
		}

	case 1:
		// exchange.MsgCreateSpotLimitOrder
		marketID := strings.TrimSpace(os.Getenv("INJ_SPOT_MARKET_ID"))
		if marketID == "" {
			return nil, fmt.Errorf("INJ_SPOT_MARKET_ID is required for spot orders")
		}
		subaccountID, err := subaccountIDForWallet(wallet.Address, c.rng)
		if err != nil {
			return nil, err
		}
		price := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_SPOT_PRICE", "1"))
		quantity := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_SPOT_QTY", "1"))
		feeRecipient := strings.TrimSpace(os.Getenv("INJ_FEE_RECIPIENT"))
		if feeRecipient == "" {
			feeRecipient = wallet.Address
		}
		isBuy := c.rng.Intn(2) == 0
		orderType := exchangetypes.OrderType_SELL
		if isBuy {
			orderType = exchangetypes.OrderType_BUY
		}
		msg = &exchangetypes.MsgCreateSpotLimitOrder{
			Sender: wallet.Address,
			Order: exchangetypes.SpotOrder{
				MarketId: marketID,
				OrderInfo: exchangetypes.OrderInfo{
					SubaccountId: subaccountID,
					FeeRecipient: feeRecipient,
					Price:        price,
					Quantity:     quantity,
				},
				OrderType: orderType,
			},
		}

	case 2:
		// exchange.MsgCreateSpotMarketOrder
		marketID := strings.TrimSpace(os.Getenv("INJ_SPOT_MARKET_ID"))
		if marketID == "" {
			return nil, fmt.Errorf("INJ_SPOT_MARKET_ID is required for spot orders")
		}
		subaccountID, err := subaccountIDForWallet(wallet.Address, c.rng)
		if err != nil {
			return nil, err
		}
		// Market orders still require price/quantity fields in the OrderInfo.
		// Keep values simple and configurable.
		price := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_SPOT_PRICE", "1"))
		quantity := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_SPOT_QTY", "1"))
		feeRecipient := strings.TrimSpace(os.Getenv("INJ_FEE_RECIPIENT"))
		if feeRecipient == "" {
			feeRecipient = wallet.Address
		}
		isBuy := c.rng.Intn(2) == 0
		orderType := exchangetypes.OrderType_SELL
		if isBuy {
			orderType = exchangetypes.OrderType_BUY
		}
		msg = &exchangetypes.MsgCreateSpotMarketOrder{
			Sender: wallet.Address,
			Order: exchangetypes.SpotOrder{
				MarketId: marketID,
				OrderInfo: exchangetypes.OrderInfo{
					SubaccountId: subaccountID,
					FeeRecipient: feeRecipient,
					Price:        price,
					Quantity:     quantity,
				},
				OrderType: orderType,
			},
		}

	case 3:
		// exchange.MsgCreateBinaryOptionsLimitOrder (uses DerivativeOrder)
		marketID := strings.TrimSpace(os.Getenv("INJ_BINARY_MARKET_ID"))
		if marketID == "" {
			return nil, fmt.Errorf("INJ_BINARY_MARKET_ID is required for binary options orders")
		}
		subaccountID, err := subaccountIDForWallet(wallet.Address, c.rng)
		if err != nil {
			return nil, err
		}
		price := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_PRICE", "0.5"))
		quantity := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_QTY", "1"))
		margin := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_MARGIN", "1"))
		feeRecipient := strings.TrimSpace(os.Getenv("INJ_FEE_RECIPIENT"))
		if feeRecipient == "" {
			feeRecipient = wallet.Address
		}
		isBuy := c.rng.Intn(2) == 0
		orderType := exchangetypes.OrderType_SELL
		if isBuy {
			orderType = exchangetypes.OrderType_BUY
		}
		msg = &exchangetypes.MsgCreateBinaryOptionsLimitOrder{
			Sender: wallet.Address,
			Order: exchangetypes.DerivativeOrder{
				MarketId: marketID,
				OrderInfo: exchangetypes.OrderInfo{
					SubaccountId: subaccountID,
					FeeRecipient: feeRecipient,
					Price:        price,
					Quantity:     quantity,
				},
				OrderType: orderType,
				Margin:    margin,
			},
		}

	case 5:
		// exchange.MsgCreateBinaryOptionsMarketOrder (uses DerivativeOrder)
		marketID := strings.TrimSpace(os.Getenv("INJ_BINARY_MARKET_ID"))
		if marketID == "" {
			return nil, fmt.Errorf("INJ_BINARY_MARKET_ID is required for binary options orders")
		}
		subaccountID, err := subaccountIDForWallet(wallet.Address, c.rng)
		if err != nil {
			return nil, err
		}
		price := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_PRICE", "0.5"))
		quantity := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_QTY", "1"))
		margin := sdkmath.LegacyMustNewDecFromStr(envOrDefault("INJ_BINARY_MARGIN", "1"))
		feeRecipient := strings.TrimSpace(os.Getenv("INJ_FEE_RECIPIENT"))
		if feeRecipient == "" {
			feeRecipient = wallet.Address
		}
		isBuy := c.rng.Intn(2) == 0
		orderType := exchangetypes.OrderType_SELL
		if isBuy {
			orderType = exchangetypes.OrderType_BUY
		}
		msg = &exchangetypes.MsgCreateBinaryOptionsMarketOrder{
			Sender: wallet.Address,
			Order: exchangetypes.DerivativeOrder{
				MarketId: marketID,
				OrderInfo: exchangetypes.OrderInfo{
					SubaccountId: subaccountID,
					FeeRecipient: feeRecipient,
					Price:        price,
					Quantity:     quantity,
				},
				OrderType: orderType,
				Margin:    margin,
			},
		}

	default:
		return nil, fmt.Errorf("unsupported tx type %d (valid: 0 deposit, 1 spot limit, 2 spot market, 3 binary limit, 5 binary market, 4 bank send)", txType)
	}

	txBuilder := c.factory.TxConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(msg); err != nil {
		return nil, err
	}

	gasLimit := uint64(300000)
	if gasEnv := os.Getenv("INJ_GAS_LIMIT"); gasEnv != "" {
		if g, err := strconv.ParseUint(gasEnv, 10, 64); err == nil {
			gasLimit = g
		}
	}
	txBuilder.SetGasLimit(gasLimit)

	feeAmount := sdkmath.NewInt(50_000_000_000_000)
	if feeEnv := os.Getenv("INJ_FEE_AMOUNT"); feeEnv != "" {
		if f, ok := sdkmath.NewIntFromString(feeEnv); ok {
			feeAmount = f
		}
	}
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(denomFromEnv(), feeAmount)))

	seq := wallet.GetAndIncrementSeq()

	sigV2 := signing.SignatureV2{
		PubKey: wallet.PrivKey.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  signing.SignMode_SIGN_MODE_DIRECT,
			Signature: nil,
		},
		Sequence: seq,
	}
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, err
	}

	signerData := authsigning.SignerData{
		ChainID:       c.factory.ChainID,
		AccountNumber: wallet.Num,
		Sequence:      seq,
	}

	sigV2, err = clienttx.SignWithPrivKey(
		context.Background(),
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		wallet.PrivKey,
		c.factory.TxConfig,
		seq,
	)
	if err != nil {
		return nil, err
	}
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, err
	}

	txBytes, err := c.factory.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, err
	}

	if os.Getenv("INJ_LOG_TX_SIZE") != "" {
		fmt.Fprintf(os.Stderr, "[DEBUG] tx_bytes=%d msg_type=%T\n", len(txBytes), msg)
	}

	return txBytes, nil
}
