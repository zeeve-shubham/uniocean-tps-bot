package main

import (
	"math/rand"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"

	"github.com/informalsystems/tm-load-test/pkg/loadtest"
	exchangetypes "code.zeeve.net/client-projects/cronos-whitelabelling/x/exchange/types"
)

// UnioceanClientFactory creates instances of UnioceanClient
type UnioceanClientFactory struct {
	TxConfig   client.TxConfig
	Wallets    []*Wallet
	ChainID    string
}

// Wallet stores the private key and current sequence
type Wallet struct {
	PrivKey cryptotypes.PrivKey
	Address string
	Seq     uint64
	Num     uint64
	mu      sync.Mutex
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
	
	// 2. Randomly select one of the four transactions
	txType := c.rng.Intn(4)
	var msg sdk.Msg

	switch txType {
	case 0:
		// Deposit
		msg = &exchangetypes.MsgDeposit{
			Sender:       wallet.Address,
			SubaccountId: wallet.Address, // Assuming subaccount is same as address for this test
			Amount:       sdk.NewCoin("oceanx", sdk.NewInt(1000000)), // 1 oceanx
		}
	case 1:
		// MsgCreateSpotLimitOrder
		msg = &exchangetypes.MsgCreateSpotLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.Address,
			MarketId:     "spot-oceanx-usdt",
			Price:        sdk.MustNewDecFromStr("1.0"),
			Quantity:     sdk.MustNewDecFromStr("10.0"),
			IsBuy:        c.rng.Intn(2) == 0,
			IsPostOnly:   false,
		}
	case 2:
		// MsgCreateDerivativeLimitOrder
		msg = &exchangetypes.MsgCreateDerivativeLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.Address,
			MarketId:     "perp-oceanx-usdt",
			Price:        sdk.MustNewDecFromStr("1.0"),
			Quantity:     sdk.MustNewDecFromStr("10.0"),
			Margin:       sdk.MustNewDecFromStr("5.0"),
			IsBuy:        c.rng.Intn(2) == 0,
			IsReduceOnly: false,
			IsPostOnly:   false,
		}
	case 3:
		// MsgCreateBinaryOptionsLimitOrder
		msg = &exchangetypes.MsgCreateBinaryOptionsLimitOrder{
			Sender:       wallet.Address,
			SubaccountId: wallet.Address,
			MarketId:     "binary-oceanx-usdt",
			Price:        sdk.MustNewDecFromStr("0.5"),
			Quantity:     sdk.MustNewDecFromStr("10.0"),
			Margin:       sdk.MustNewDecFromStr("5.0"),
			IsBuy:        c.rng.Intn(2) == 0,
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
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("oceanx", sdk.NewInt(3000))))
	txBuilder.SetMemo("load-test")

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
