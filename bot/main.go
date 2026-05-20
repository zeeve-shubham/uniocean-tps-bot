package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/go-bip39"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"

	"code.zeeve.net/client-projects/cronos-whitelabelling/internal/networkconfig"
	exchangetypes "code.zeeve.net/client-projects/cronos-whitelabelling/x/exchange/types"
	"github.com/informalsystems/tm-load-test/pkg/loadtest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultRESTEndpoint = "http://134.119.179.234:1317"
const defaultChainID = "uniocean_684-1"

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
	config.SetBech32PrefixForAccount("oceanx", "oceanxpub")
	config.Seal()

	// Parse arguments for mnemonic file
	if len(os.Args) < 2 {
		fmt.Println("Usage: uniocean-load-tester <mnemonics_file> [tm-load-test args...]")
		fmt.Println("Example: ./uniocean-load-tester funded-wallets.log -c 1 -T 30 -r 100 --broadcast-tx-method async --endpoints ws://...")
		fmt.Println("Debug: ./uniocean-load-tester funded-wallets.log --debug-sync-broadcast")
		os.Exit(1)
	}

	mnemonicFile := os.Args[1]
	chainID := envOrDefault("UNIOCEAN_CHAIN_ID", defaultChainID)
	restEndpoint := envOrDefault("UNIOCEAN_REST_ENDPOINT", defaultRESTEndpoint)
	grpcEndpoint := networkconfig.Load().GRPCEndpoint
	debugSyncBroadcast, remainingArgs := extractCustomArgs(os.Args[2:])
	wsEndpoint := extractWSEndpoint(remainingArgs)
	broadcastMethod := extractBroadcastMethod(remainingArgs)

	// Adjust os.Args for tm-load-test
	os.Args = append([]string{os.Args[0]}, remainingArgs...)
	reportStartupConfig(chainID, restEndpoint, grpcEndpoint, wsEndpoint, broadcastMethod)

	wallets, err := loadWallets(mnemonicFile, grpcEndpoint)
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

func extractWSEndpoint(args []string) string {
	return extractFlagValue(args, "--endpoints")
}

func extractBroadcastMethod(args []string) string {
	method := extractFlagValue(args, "--broadcast-tx-method")
	if method == "" {
		return "async"
	}
	return method
}

func extractFlagValue(args []string, flagName string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, flagName+"=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, flagName+"="))
		}
		if arg == flagName && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func reportStartupConfig(chainID, restEndpoint, grpcEndpoint, wsEndpoint, broadcastMethod string) {
	txTypes, err := activeTxTypesFromEnv()
	if err != nil {
		fmt.Printf("[ERROR] Invalid UNIOCEAN_TX_TYPES: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[INFO] ChainID: %s\n", chainID)
	fmt.Printf("[INFO] REST endpoint: %s\n", restEndpoint)
	fmt.Printf("[INFO] gRPC endpoint: %s\n", grpcEndpoint)
	fmt.Printf("[INFO] Broadcast method: %s\n", broadcastMethod)
	if wsEndpoint != "" {
		fmt.Printf("[INFO] WS endpoint: %s\n", wsEndpoint)
	}
	fmt.Printf("[INFO] Active tx types: %v\n", txTypes)
	if broadcastMethod == "async" {
		fmt.Println("[WARN] broadcast_tx_async does not wait for CheckTx or block inclusion. Use sync for validation and the TPS checker for committed throughput.")
	}

	restHeight, restErr := fetchRESTLatestHeight(restEndpoint)
	if restErr != nil {
		fmt.Printf("[WARN] REST latest block check failed: %v\n", restErr)
	} else {
		fmt.Printf("[INFO] REST latest height: %s\n", restHeight)
	}

	if wsEndpoint != "" {
		rpcURL, convErr := statusURLFromWSEndpoint(wsEndpoint)
		if convErr != nil {
			fmt.Printf("[WARN] Could not derive RPC status URL from --endpoints: %v\n", convErr)
		} else {
			network, height, rpcErr := fetchRPCStatus(rpcURL)
			if rpcErr != nil {
				fmt.Printf("[WARN] RPC status check failed (%s): %v\n", rpcURL, rpcErr)
			} else {
				fmt.Printf("[INFO] RPC network: %s | latest height: %s\n", network, height)
				if network != "" && network != chainID {
					fmt.Printf("[WARN] Chain mismatch: expected %s but RPC reports %s\n", chainID, network)
				}
			}
		}
	}

	if hasExchangeTxType(txTypes) {
		fmt.Println("[WARN] Exchange tx types selected. Ensure market IDs exist on this endpoint, otherwise txs can fail with 'market not found'.")
	}
}

func hasExchangeTxType(txTypes []int) bool {
	for _, t := range txTypes {
		if t != 4 {
			return true
		}
	}
	return false
}

func fetchRESTLatestHeight(restEndpoint string) (string, error) {
	url := strings.TrimRight(restEndpoint, "/") + "/cosmos/base/tendermint/v1beta1/blocks/latest"
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}

	var data struct {
		Block struct {
			Header struct {
				Height string `json:"height"`
			} `json:"header"`
		} `json:"block"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	if data.Block.Header.Height == "" {
		return "", fmt.Errorf("missing height in response")
	}

	return data.Block.Header.Height, nil
}

func statusURLFromWSEndpoint(wsEndpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(wsEndpoint))
	if err != nil {
		return "", err
	}

	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	case "http", "https":
		// already normalized
	default:
		return "", fmt.Errorf("unsupported endpoint scheme %q", parsed.Scheme)
	}

	basePath := strings.TrimSuffix(parsed.Path, "/websocket")
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		parsed.Path = "/status"
	} else {
		parsed.Path = basePath + "/status"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

func fetchRPCStatus(statusURL string) (string, string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(statusURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("http %d", resp.StatusCode)
	}

	var data struct {
		Result struct {
			NodeInfo struct {
				Network string `json:"network"`
			} `json:"node_info"`
			SyncInfo struct {
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", err
	}

	return data.Result.NodeInfo.Network, data.Result.SyncInfo.LatestBlockHeight, nil
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
		&banktypes.MsgSend{},
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

func loadWallets(mnemonicFile, grpcEndpoint string) ([]*Wallet, error) {
	entries, err := readLines(mnemonicFile)
	if err != nil {
		return nil, err
	}

	conn, err := dialGRPC(grpcEndpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	authQueryClient := authtypes.NewQueryClient(conn)

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

		accNum, accSeq, err := queryAccountInfo(authQueryClient, address)
		if err != nil {
			fmt.Printf("Warning: failed to query account %s over gRPC: %v\n", address, err)
			continue
		}

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

	fmt.Printf("[INFO] Loaded %d wallets\n", len(wallets))
	return wallets, nil
}

func dialGRPC(grpcEndpoint string) (*grpc.ClientConn, error) {
	target := normalizeGRPCEndpoint(grpcEndpoint)
	if target == "" {
		return nil, fmt.Errorf("empty gRPC endpoint")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gRPC endpoint %s: %w", grpcEndpoint, err)
	}

	return conn, nil
}

func normalizeGRPCEndpoint(grpcEndpoint string) string {
	raw := strings.TrimSpace(grpcEndpoint)
	if raw == "" {
		return ""
	}

	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err == nil {
			if parsed.Host != "" {
				return parsed.Host
			}
			if parsed.Path != "" {
				return parsed.Path
			}
		}
	}

	return raw
}

func queryAccountInfo(client authtypes.QueryClient, address string) (uint64, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.AccountInfo(ctx, &authtypes.QueryAccountInfoRequest{Address: address})
	if err != nil {
		return 0, 0, err
	}
	if resp == nil || resp.Info == nil {
		return 0, 0, fmt.Errorf("empty account info response")
	}

	return resp.Info.AccountNumber, resp.Info.Sequence, nil
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
