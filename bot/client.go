package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	"github.com/informalsystems/tm-load-test/pkg/loadtest"
	exchangetypes "code.zeeve.net/client-projects/cronos-whitelabelling/x/exchange/types"
)

// decimals18 = 10^18 — the base unit multiplier for oceanx
var decimals18 = sdk.NewIntFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// BuildSubaccountID creates the 32-byte subaccount ID from a bech32 address and a nonce index.
//
//	Format: [20-byte eth address] ++ [4 zero bytes] ++ [8-byte big-endian nonce]
//	Result: "0x" + hex(32 bytes)
func BuildSubaccountID(bech32Addr string, nonce uint64) (string, error) {
	addr, err := sdk.AccAddressFromBech32(bech32Addr)
	if err != nil {
		return "", fmt.Errorf("invalid bech32 address %q: %w", bech32Addr, err)
	}
	// AccAddress is the 20-byte raw address (same as Ethereum address bytes)
	if len(addr) != 20 {
		return "", fmt.Errorf("expected 20-byte address, got %d", len(addr))
	}

	var buf [32]byte
	copy(buf[0:20], addr)          // bytes  0–19: address
	// bytes 20–23 stay zero      // bytes 20–23: 4-byte zero padding
	binary.BigEndian.PutUint64(buf[24:], nonce) // bytes 24–31: 8-byte nonce

	return "0x" + hex.EncodeToString(buf[:]), nil
}

// UnioceanClientFactory creates instances of UnioceanClient
type UnioceanClientFactory struct {
	TxConfig client.TxConfig
	Wallets  []*Wallet
	ChainID  string
}

// Wallet stores the private key, current sequence, and derived subaccount ID
type Wallet struct {
	PrivKey      cryptotypes.PrivKey
	Address      string
	SubaccountID string // precomputed at startup, nonce=0
	Seq          uint64
	Num          uint64
	mu           sync.Mutex
}

func (w *Wallet) GetAndIncrementSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	seq := w.Seq
	w.Seq++
	return seq
}

// UnioceanClient is responsible for generating transactions.
type UnioceanClient struct {
	factory *UnioceanClientFactory
	rng     *rand.Rand
}

var _ loadtest.ClientFactory = (*UnioceanClientFactory)(nil)
var _ loadtest.Client = (*UnioceanClient)(nil)

func (f *UnioceanClientFactory) ValidateConfig(cfg loadtest.Config) error {
	return nil
}

func (f *UnioceanClientFactory) NewClient(cfg loadtest.Config) (loadtest.Client, error) {
	return &UnioceanClient{
		factory: f,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (c *UnioceanClient) GenerateTx() ([]byte, error) {
	// 1. Pick a random wallet
	wallet := c.factory.Wallets[c.rng.Intn(len(c.factory.Wallets))]

	// 2. Randomly select transaction type
	// Active: Deposit (0), SpotLimitOrder (1), BinaryOptionsLimitOrder (3)
	// Skipped: DerivativeLimitOrder (2) — enable when market ID is configured
	activeTxTypes := []int{0, 1, 3}
	txType := activeTxTypes[c.rng.Intn(len(activeTxTypes))]
	var msg sdk.Msg

	switch txType {
	case 0:
		// Deposit — 0.001 oceanx = 1_000_000_000_000_000 base units (10^15)
		// Using a small amount to avoid draining balance quickly during stress tests
		amount := sdk.NewInt(1_000_000_000_000_000) // 0.001 oceanx
		msg = &exchangetypes.MsgDeposit{
			Sender:       wallet.Address,
			SubaccountId: wallet.SubaccountID,
			Amount:       sdk.NewCoin("oceanx", amount),
		}

	case 1:
		// MsgCreateSpotLimitOrder — PHO/OCN market
		// Market:              market_2
		// Ticker:              PHO/OCN
		// Base denom:          aphoton
		// Quote denom:         oceanx
		// min_price_tick_size:    0.0000001 (1e-7)
		// min_quantity_tick_size: 0.0000001 (1e-7)
		//
		// Price must be a multiple of 0.0000001 → pick 1–10 ticks randomly
		// Quantity must be a multiple of 0.0000001 → pick 1–100 ticks randomly
		minTick := sdk.MustNewDecFromStr("0.0000001")
		priceTicks := int64(c.rng.Intn(10) + 1)   // 1–10 ticks
		quantityTicks := int64(c.rng.Intn(100) + 1) // 1–100 ticks
		price := minTick.MulInt64(priceTicks)
		quantity := minTick.MulInt64(quantityTicks)

		msg = &exchangetypes.MsgCreateSpotLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.SubaccountID,
			MarketId:     "market_2",
			Price:        price,
			Quantity:     quantity,
			IsBuy:        c.rng.Intn(2) == 0,
			IsPostOnly:   false,
		}

	case 2:
		// MsgCreateDerivativeLimitOrder
		// Margin must cover at least Price * Quantity / Leverage (using 1x here)
		msg = &exchangetypes.MsgCreateDerivativeLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.SubaccountID,
			MarketId:     "perp-oceanx-usdt",
			Price:        sdk.MustNewDecFromStr("1.0"),
			Quantity:     sdk.MustNewDecFromStr("0.01"),
			Margin:       sdk.MustNewDecFromStr("0.01"), // margin >= price * quantity
			IsBuy:        c.rng.Intn(2) == 0,
			IsReduceOnly: false,
			IsPostOnly:   false,
		}

	case 3:
		// MsgCreateBinaryOptionsLimitOrder — BTC/USDT binary market
		// Market ID:             0x4acff2c273ac823dbc762793d0f937970b296a61d186cc422f36f741adb731d0
		// Ticker:                BTC/USDT  (BTC-GT-95K)
		// Quote denom:           oceanx
		// min_price_tick_size:    0.0000001
		// min_quantity_tick_size: 0.0000001
		// min_notional:           0.1  (price * quantity >= 0.1 for buy)
		//
		// Price in binary options is a probability in (0, 1).
		// We use 0.5 so margin is symmetric for buy and sell sides.
		// Quantity = 1.0 → notional = 0.5 >= min_notional (0.1) ✓
		// Margin (buy)  = price * quantity        = 0.5
		// Margin (sell) = (1 - price) * quantity  = 0.5
		isBuy := c.rng.Intn(2) == 0
		boPrice := sdk.MustNewDecFromStr("0.5")
		boQuantity := sdk.MustNewDecFromStr("1.0")
		var boMargin sdk.Dec
		if isBuy {
			boMargin = boPrice.Mul(boQuantity) // 0.5
		} else {
			boMargin = sdk.OneDec().Sub(boPrice).Mul(boQuantity) // 0.5
		}
		msg = &exchangetypes.MsgCreateBinaryOptionsLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.SubaccountID,
			MarketId:     "0x4acff2c273ac823dbc762793d0f937970b296a61d186cc422f36f741adb731d0",
			Price:        boPrice,
			Quantity:     boQuantity,
			Margin:       boMargin,
			IsBuy:        isBuy,
			IsReduceOnly: false,
			IsPostOnly:   false,
		}
	}

	// 3. Build and Sign Transaction
	txBuilder := c.factory.TxConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(msg); err != nil {
		return nil, err
	}

	txBuilder.SetGasLimit(300000)
	// Fee: 2000 oceanx base units (negligible, just enough to pass fee checks)
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("oceanx", sdk.NewInt(2000))))
	txBuilder.SetMemo("uniocean-tps-bot")

	seq := wallet.GetAndIncrementSeq()

	// First set empty sig so pubkey is included in the tx
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

	sigV2, err := clienttx.SignWithPrivKey(
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

	// 4. Encode
	txBytes, err := c.factory.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, err
	}

	return txBytes, nil
}
