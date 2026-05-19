package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/go-bip39"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"

	"code.zeeve.net/client-projects/cronos-whitelabelling/internal/networkconfig"
	exchangetypes "code.zeeve.net/client-projects/cronos-whitelabelling/x/exchange/types"
	"github.com/informalsystems/tm-load-test/pkg/loadtest"
)

func main() {
	networkCfg := networkconfig.Load()

	// Configure Cosmos SDK types
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("oceanx", "oceanxpub")
	config.Seal()

	// Parse arguments for mnemonic file
	if len(os.Args) < 2 {
		fmt.Println("Usage: uniocean-load-tester <mnemonics_file> [tm-load-test args...]")
		fmt.Printf("Example: ./uniocean-load-tester funded-wallets.log -c 1 -T 30 -r 100 --broadcast-tx-method async --endpoints %s\n", networkCfg.WebSocketEndpoint)
		fmt.Println("Debug: ./uniocean-load-tester funded-wallets.log --debug-sync-broadcast")
		os.Exit(1)
	}

	mnemonicFile := os.Args[1]
	chainID := networkCfg.ChainID
	debugSyncBroadcast, remainingArgs := extractCustomArgs(os.Args[2:])

	// Adjust os.Args for tm-load-test
	os.Args = append([]string{os.Args[0]}, remainingArgs...)

	wallets, err := loadWallets(mnemonicFile, networkCfg.RESTEndpoint)
	if err != nil {
		panic(fmt.Sprintf("failed to load wallets: %v", err))
	}
	if len(wallets) == 0 {
		panic("No wallets loaded")
	}

	factory := newClientFactory(wallets, chainID)

	if debugSyncBroadcast {
		if err := runDebugSyncBroadcast(factory, networkCfg.RESTEndpoint); err != nil {
			panic(err)
		}
		return
	}

	registerClientFactory(factory)
	loadtest.Run(&loadtest.CLIConfig{
		AppName:              "uniocean-load-tester",
		AppShortDesc:         "Load testing tool for Uniocean Network",
		AppLongDesc:          "Tool to spam Uniocean Network with Exchange transactions",
		DefaultClientFactory: "uniocean",
	})
}

func extractCustomArgs(args []string) (bool, []string) {
	debugSyncBroadcast := false
	remaining := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--debug-sync-broadcast":
			debugSyncBroadcast = true
		default:
			remaining = append(remaining, arg)
		}
	}
	return debugSyncBroadcast, remaining
}

func newClientFactory(wallets []*Wallet, chainID string) *UnioceanClientFactory {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(interfaceRegistry)
	interfaceRegistry.RegisterImplementations((*cryptotypes.PubKey)(nil), &EthPubKey{})
	interfaceRegistry.RegisterImplementations((*sdk.Msg)(nil),
		&exchangetypes.MsgDeposit{},
		&exchangetypes.MsgCreateSpotLimitOrder{},
		&exchangetypes.MsgCreateDerivativeLimitOrder{},
		&exchangetypes.MsgCreateBinaryOptionsLimitOrder{},
	)
	protoCodec := codec.NewProtoCodec(interfaceRegistry)
	txConfig := authtx.NewTxConfig(protoCodec, authtx.DefaultSignModes)

	return &UnioceanClientFactory{
		TxConfig: txConfig,
		Wallets:  wallets,
		ChainID:  chainID,
	}
}

func registerClientFactory(factory *UnioceanClientFactory) {
	if err := loadtest.RegisterClientFactory("uniocean", factory); err != nil {
		panic(err)
	}
}

func loadWallets(mnemonicFile, restEndpoint string) ([]*Wallet, error) {
	entries, err := readLines(mnemonicFile)
	if err != nil {
		return nil, err
	}

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
		privKey := &EthPrivKey{Key: privBytes}

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

		// Derive the subaccount ID for nonce=0 (default subaccount)
		subaccountID, err := BuildSubaccountID(address, 0)
		if err != nil {
			fmt.Printf("Warning: failed to compute subaccount ID for %s: %v\n", address, err)
			continue
		}

		wallets = append(wallets, &Wallet{
			PrivKey:      privKey,
			Address:      address,
			SubaccountID: subaccountID,
			Seq:          accSeq,
			Num:          accNum,
		})
	}

	return wallets, nil
}

func runDebugSyncBroadcast(factory *UnioceanClientFactory, restEndpoint string) error {
	client, err := factory.NewClient(loadtest.Config{})
	if err != nil {
		return err
	}

	txBytes, err := client.GenerateTx()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{
		"tx_bytes": base64.StdEncoding.EncodeToString(txBytes),
		"mode":     "BROADCAST_MODE_SYNC",
	})
	if err != nil {
		return err
	}

	resp, err := http.Post(restEndpoint+"/cosmos/tx/v1beta1/txs", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("HTTP %d\n%s\n", resp.StatusCode, string(body))
	return nil
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
