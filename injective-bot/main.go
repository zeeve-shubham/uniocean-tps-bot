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
	"sync"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/go-bip39"
	"github.com/informalsystems/tm-load-test/pkg/loadtest"

	injcrypto "github.com/InjectiveLabs/injective-core/injective-chain/crypto/ethsecp256k1"
	exchangetypes "github.com/InjectiveLabs/injective-core/injective-chain/modules/exchange/types"
)

const (
	defaultRESTEndpoint = "http://134.119.179.234:11337"
	defaultChainID      = "injective-1"
	defaultBech32Prefix = "inj"
	defaultDenom        = "inj"
)

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func main() {
	// Configure Cosmos SDK types
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount(
		envOrDefault("INJ_BECH32_PREFIX", defaultBech32Prefix),
		envOrDefault("INJ_BECH32_PUB_PREFIX", defaultBech32Prefix+"pub"),
	)
	config.Seal()

	if len(os.Args) < 2 {
		fmt.Println("Usage: injective-load-tester <wallet-file> [tm-load-test args...]")
		fmt.Println("Example: ./injective-load-tester funded-wallets.log -c 1 -T 30 -r 100 --broadcast-tx-method async --endpoints ws://134.119.179.234:27657/websocket")
		fmt.Println("Debug: ./injective-load-tester funded-wallets.log --debug-sync-broadcast")
		os.Exit(1)
	}

	walletFile := os.Args[1]
	chainID := envOrDefault("INJ_CHAIN_ID", defaultChainID)
	restEndpoint := envOrDefault("INJ_REST_ENDPOINT", defaultRESTEndpoint)
	debugSyncBroadcast, remainingArgs := extractCustomArgs(os.Args[2:])
	wsEndpoint := extractWSEndpoint(remainingArgs)

	// Adjust os.Args for tm-load-test
	os.Args = append([]string{os.Args[0]}, remainingArgs...)
	reportStartupConfig(chainID, restEndpoint, wsEndpoint)

	wallets, err := loadWallets(walletFile, restEndpoint)
	if err != nil {
		panic(fmt.Sprintf("failed to load wallets: %v", err))
	}
	if len(wallets) == 0 {
		panic("No wallets loaded")
	}

	factory := newClientFactory(wallets, chainID)

	if debugSyncBroadcast {
		if err := runDebugSyncBroadcast(factory, restEndpoint); err != nil {
			panic(err)
		}
		return
	}

	registerClientFactory(factory)
	loadtest.Run(&loadtest.CLIConfig{
		AppName:              "injective-load-tester",
		AppShortDesc:         "Load testing tool for Injective",
		AppLongDesc:          "Tool to spam Injective with bank and exchange transactions",
		DefaultClientFactory: "injective",
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

func extractWSEndpoint(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--endpoints=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, "--endpoints="))
		}
		if arg == "--endpoints" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func reportStartupConfig(chainID, restEndpoint, wsEndpoint string) {
	fmt.Printf("[INFO] ChainID: %s\n", chainID)
	fmt.Printf("[INFO] REST endpoint: %s\n", restEndpoint)
	if wsEndpoint != "" {
		fmt.Printf("[INFO] WS endpoint: %s\n", wsEndpoint)
	}
	fmt.Printf("[INFO] Denom: %s\n", envOrDefault("INJ_DENOM", defaultDenom))
}

type InjectiveClientFactory struct {
	TxConfig client.TxConfig
	Wallets  []*Wallet
	ChainID  string
}

type Wallet struct {
	PrivKey *injcrypto.PrivKey
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

func newClientFactory(wallets []*Wallet, chainID string) *InjectiveClientFactory {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
	exchangetypes.RegisterInterfaces(interfaceRegistry)

	protoCodec := codec.NewProtoCodec(interfaceRegistry)
	txConfig := authtx.NewTxConfig(protoCodec, authtx.DefaultSignModes)

	return &InjectiveClientFactory{
		TxConfig: txConfig,
		Wallets:  wallets,
		ChainID:  chainID,
	}
}

func registerClientFactory(factory *InjectiveClientFactory) {
	if err := loadtest.RegisterClientFactory("injective", factory); err != nil {
		panic(err)
	}
}

func loadWallets(walletFile, restEndpoint string) ([]*Wallet, error) {
	entries, err := readLines(walletFile)
	if err != nil {
		return nil, err
	}

	var wallets []*Wallet
	for _, entry := range entries {
		seed, err := bip39.NewSeedWithErrorChecking(entry.Mnemonic, "")
		if err != nil {
			return nil, err
		}

		master, ch := hd.ComputeMastersFromSeed(seed)
		privBytes, err := hd.DerivePrivateKeyForPath(master, ch, "m/44'/60'/0'/0/0")
		if err != nil {
			// fallback cosmos
			privBytes, _ = hd.DerivePrivateKeyForPath(master, ch, "m/44'/118'/0'/0/0")
		}
		privKey := &injcrypto.PrivKey{Key: privBytes}

		address := entry.Address

		resp, err := http.Get(fmt.Sprintf("%s/cosmos/auth/v1beta1/accounts/%s", strings.TrimRight(restEndpoint, "/"), address))
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

	fmt.Printf("[INFO] Loaded %d wallets\n", len(wallets))
	return wallets, nil
}

func runDebugSyncBroadcast(factory *InjectiveClientFactory, restEndpoint string) error {
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

	resp, err := http.Post(strings.TrimRight(restEndpoint, "/")+"/cosmos/tx/v1beta1/txs", "application/json", bytes.NewReader(payload))
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
			address := strings.TrimSpace(line[idx+len("- address: "):])
			if currentMnemonic != "" && address != "" {
				entries = append(entries, WalletEntry{Mnemonic: currentMnemonic, Address: address})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
