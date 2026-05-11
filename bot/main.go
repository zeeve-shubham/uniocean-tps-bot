package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/go-bip39"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"

	exchangetypes "code.zeeve.net/client-projects/cronos-whitelabelling/x/exchange/types"
	"github.com/informalsystems/tm-load-test/pkg/loadtest"
)

func main() {
	// Configure Cosmos SDK types
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("oceanx", "oceanxpub")
	config.Seal()

	// Parse arguments for mnemonic file
	if len(os.Args) < 2 {
		fmt.Println("Usage: uniocean-load-tester <mnemonics_file> [tm-load-test args...]")
		fmt.Println("Example: ./uniocean-load-tester funded-wallets.log -c 1 -T 30 -r 100 --broadcast-tx-method async --endpoints wss://uniocean-tps.zeeve.net/websocket")
		os.Exit(1)
	}

	mnemonicFile := os.Args[1]
	chainID := "uniocean_8888-1" // Please change if your chain ID is different

	// Adjust os.Args for tm-load-test
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

	// Read mnemonics
	entries, err := readLines(mnemonicFile)
	if err != nil {
		panic(fmt.Sprintf("failed to read mnemonics: %v", err))
	}

	// Connect to gRPC to fetch sequence numbers
	restEndpoint := "https://uniocean-tps.zeeve.net/api"

	var wallets []*Wallet
	for _, entry := range entries {
		// Derive private key
		seed, err := bip39.NewSeedWithErrorChecking(entry.Mnemonic, "")
		if err != nil {
			panic(err)
		}
		master, ch := hd.ComputeMastersFromSeed(seed)
		// Try using 60' (EVM path)
		privBytes, err := hd.DerivePrivateKeyForPath(master, ch, "m/44'/60'/0'/0/0")
		if err != nil {
			// Fallback to 118'
			privBytes, _ = hd.DerivePrivateKeyForPath(master, ch, "m/44'/118'/0'/0/0")
		}
		// IMPORTANT: Since your wallets use ethsecp256k1, you must use the ethermint secp256k1
		// If you get errors about pubkey type, change the below line to use your repo's ethermint package:
		// privKey := &ethsecp256k1.PrivKey{Key: privBytes}
		privKey := &secp256k1.PrivKey{Key: privBytes}

		address := entry.Address

		// Fetch account info via REST API
		resp, err := http.Get(fmt.Sprintf("%s/cosmos/auth/v1beta1/accounts/%s", restEndpoint, address))
		if err != nil {
			fmt.Printf("Warning: failed to query account %s: %v\n", address, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Printf("Warning: failed to read account %s: %v\n", address, err)
			continue
		}

		var accResp struct {
			Account struct {
				BaseAccount struct {
					AccountNumber string `json:"account_number"`
					Sequence      string `json:"sequence"`
				} `json:"base_account"`
			} `json:"account"`
		}

		if err := json.Unmarshal(body, &accResp); err != nil {
			fmt.Printf("Warning: failed to unmarshal account %s: %v\n", address, err)
			continue
		}

		accNum, _ := strconv.ParseUint(accResp.Account.BaseAccount.AccountNumber, 10, 64)
		accSeq, _ := strconv.ParseUint(accResp.Account.BaseAccount.Sequence, 10, 64)

		wallets = append(wallets, &Wallet{
			PrivKey: privKey,
			Address: address,
			Seq:     accSeq,
			Num:     accNum,
		})
	}

	if len(wallets) == 0 {
		panic("No wallets loaded")
	}

	fmt.Printf("Loaded %d wallets\n", len(wallets))

	// Setup Codec and TxConfig
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(interfaceRegistry)
	interfaceRegistry.RegisterImplementations((*sdk.Msg)(nil),
		&exchangetypes.MsgDeposit{},
		&exchangetypes.MsgCreateSpotLimitOrder{},
		&exchangetypes.MsgCreateDerivativeLimitOrder{},
		&exchangetypes.MsgCreateBinaryOptionsLimitOrder{},
	)
	protoCodec := codec.NewProtoCodec(interfaceRegistry)
	txConfig := authtx.NewTxConfig(protoCodec, authtx.DefaultSignModes)

	factory := &UnioceanClientFactory{
		TxConfig: txConfig,
		Wallets:  wallets,
		ChainID:  chainID,
	}

	if err := loadtest.RegisterClientFactory("uniocean", factory); err != nil {
		panic(err)
	}

	loadtest.Run(&loadtest.CLIConfig{
		AppName:              "uniocean-load-tester",
		AppShortDesc:         "Load testing tool for Uniocean Network",
		AppLongDesc:          "Tool to spam Uniocean Network with Exchange transactions",
		DefaultClientFactory: "uniocean",
	})
}

type WalletEntry struct {
	Mnemonic string
	Address  string
}

func readLines(path string) ([]WalletEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []WalletEntry
	var currentMnemonic string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, "Mnemonic: "); idx != -1 {
			currentMnemonic = strings.TrimSpace(line[idx+len("Mnemonic: "):])
		} else if idx := strings.Index(line, "- address: "); idx != -1 {
			addr := strings.TrimSpace(line[idx+len("- address: "):])
			if currentMnemonic != "" && addr != "" {
				entries = append(entries, WalletEntry{
					Mnemonic: currentMnemonic,
					Address:  addr,
				})
				currentMnemonic = ""
			}
		}
	}
	return entries, scanner.Err()
}
