package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"code.zeeve.net/client-projects/cronos-whitelabelling/internal/networkconfig"
)

const (
	defaultObserveSeconds = 60
	newBlockQuery         = "tm.event='NewBlock'"
	catchUpInterval       = 1 * time.Second  // aggressive polling for missed blocks
	idleLogInterval       = 5 * time.Second  // user-facing "waiting..." messages
	rpcCallTimeout        = 3 * time.Second  // per-RPC call timeout (reduced from 5s)
	fetchWorkers          = 8                // concurrent block-fetching goroutines
	finalCatchUpTimeout   = 30 * time.Second // timeout for the exhaustive final sweep
)

type observedBlock struct {
	height  int64
	time    time.Time
	txCount int
}

func main() {
	networkCfg := networkconfig.Load()
	observeSecs := parseObserveSeconds(os.Args[1:])
	rpcRemote, err := rpcRemoteFromWSEndpoint(networkCfg.WebSocketEndpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to normalize WebSocket endpoint: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🔍 Uniocean On-Chain TPS Checker\n")
	fmt.Printf("   RPC: %s\n", rpcRemote)
	fmt.Printf("   WS:  %s\n", networkCfg.WebSocketEndpoint)
	fmt.Printf("   Observation window: %ds\n\n", observeSecs)

	client, err := newCometClient(networkCfg.WebSocketEndpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create CometBFT client: %v\n", err)
		os.Exit(1)
	}
	if err := client.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to start CometBFT client: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.Stop() }()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	subscriberID := fmt.Sprintf("uniocean-tps-checker-%d", time.Now().UnixNano())
	events, err := client.Subscribe(ctx, subscriberID, newBlockQuery)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to subscribe to new blocks: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.UnsubscribeAll(context.Background(), subscriberID) }()

	startBlock, err := currentBlock(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to fetch start block: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📦 Start block: #%d at %s\n", startBlock.height, startBlock.time.Format("15:04:05"))

	deadline := time.NewTimer(time.Duration(observeSecs) * time.Second)
	defer deadline.Stop()

	// Separate tickers: fast catch-up (1s) vs slow idle log (5s)
	catchUpTicker := time.NewTicker(catchUpInterval)
	defer catchUpTicker.Stop()

	idleTicker := time.NewTicker(idleLogInterval)
	defer idleTicker.Stop()

	lastObserved := startBlock
	lastWallEventAt := time.Now()
	var totalTxs int64
	var totalBlocks int64

	finish := func() {
		fmt.Printf("\n⏳ Fetching all remaining blocks...\n")
		exhaustiveCatchUp(client, startBlock, &lastObserved, &totalBlocks, &totalTxs)
		printSummary(observeSecs, startBlock, lastObserved, totalBlocks, totalTxs)
	}

	for {
		select {
		case <-ctx.Done():
			finish()
			return

		case <-deadline.C:
			finish()
			return

		case event := <-events:
			block, ok := blockFromEvent(event)
			if !ok || block.height <= lastObserved.height {
				continue
			}
			lastWallEventAt = time.Now()
			recordObservedBlock(startBlock, block, &lastObserved, &totalBlocks, &totalTxs, "")

		case <-catchUpTicker.C:
			if catchUpMissingBlocks(client, startBlock, &lastObserved, &totalBlocks, &totalTxs) {
				lastWallEventAt = time.Now()
			}

		case <-idleTicker.C:
			if time.Since(lastWallEventAt) >= idleLogInterval {
				fmt.Printf("⏳ Waiting for next block... latest observed #%d\n", lastObserved.height)
			}
		}
	}
}

func parseObserveSeconds(args []string) int {
	if len(args) == 0 {
		return defaultObserveSeconds
	}

	observeSecs, err := strconv.Atoi(args[0])
	if err != nil || observeSecs <= 0 {
		return defaultObserveSeconds
	}
	return observeSecs
}

func newCometClient(wsEndpoint string) (*rpchttp.HTTP, error) {
	remote, wsPath, err := rpcRemoteAndWSPathFromEndpoint(wsEndpoint)
	if err != nil {
		return nil, err
	}
	return rpchttp.New(remote, wsPath)
}

func rpcRemoteFromWSEndpoint(wsEndpoint string) (string, error) {
	remote, _, err := rpcRemoteAndWSPathFromEndpoint(wsEndpoint)
	return remote, err
}

func rpcRemoteAndWSPathFromEndpoint(wsEndpoint string) (string, string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(wsEndpoint))
	if err != nil {
		return "", "", err
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", "", fmt.Errorf("invalid websocket endpoint %q", wsEndpoint)
	}

	wsPath := parsedURL.EscapedPath()
	if wsPath == "" {
		wsPath = "/websocket"
	}

	switch parsedURL.Scheme {
	case "ws":
		parsedURL.Scheme = "http"
	case "wss":
		parsedURL.Scheme = "https"
	case "http", "https":
		// already normalized
	default:
		return "", "", fmt.Errorf("unsupported websocket scheme %q", parsedURL.Scheme)
	}

	parsedURL.Path = strings.TrimSuffix(parsedURL.Path, "/websocket")
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""

	remote := strings.TrimRight(parsedURL.String(), "/")
	return remote, wsPath, nil
}

func currentBlock(ctx context.Context, client *rpchttp.HTTP) (observedBlock, error) {
	callCtx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	result, err := client.Block(callCtx, nil)
	if err != nil {
		return observedBlock{}, err
	}
	return observedBlock{
		height:  result.Block.Height,
		time:    result.Block.Time,
		txCount: len(result.Block.Data.Txs),
	}, nil
}

func blockAtHeight(ctx context.Context, client *rpchttp.HTTP, height int64) (observedBlock, error) {
	callCtx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	result, err := client.Block(callCtx, &height)
	if err != nil {
		return observedBlock{}, err
	}
	return observedBlock{
		height:  result.Block.Height,
		time:    result.Block.Time,
		txCount: len(result.Block.Data.Txs),
	}, nil
}

// fetchBlocksConcurrent fetches blocks in [fromHeight, toHeight] using a pool
// of concurrent workers and returns them sorted by height.
func fetchBlocksConcurrent(client *rpchttp.HTTP, fromHeight, toHeight int64) []observedBlock {
	if fromHeight > toHeight {
		return nil
	}

	count := toHeight - fromHeight + 1
	heights := make(chan int64, count)
	results := make(chan observedBlock, count)

	workerCount := fetchWorkers
	if int64(workerCount) > count {
		workerCount = int(count)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range heights {
				ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
				result, err := client.Block(ctx, &h)
				cancel()
				if err == nil && result.Block != nil {
					results <- observedBlock{
						height:  result.Block.Height,
						time:    result.Block.Time,
						txCount: len(result.Block.Data.Txs),
					}
				}
			}
		}()
	}

	// Feed all heights into the buffered channel (won't block).
	for h := fromHeight; h <= toHeight; h++ {
		heights <- h
	}
	close(heights)

	// Close results once all workers are done.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect and sort.
	var blocks []observedBlock
	for b := range results {
		blocks = append(blocks, b)
	}

	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].height < blocks[j].height
	})

	return blocks
}

// catchUpMissingBlocks fetches any blocks between lastObserved and the
// current chain tip using concurrent RPC calls. Called every catchUpInterval (1s).
func catchUpMissingBlocks(
	client *rpchttp.HTTP,
	startBlock observedBlock,
	lastObserved *observedBlock,
	totalBlocks, totalTxs *int64,
) bool {
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()

	latestBlock, err := currentBlock(ctx, client)
	if err != nil || latestBlock.height <= lastObserved.height {
		return false
	}

	blocks := fetchBlocksConcurrent(client, lastObserved.height+1, latestBlock.height)
	if len(blocks) == 0 {
		return false
	}

	for _, block := range blocks {
		if block.height <= lastObserved.height {
			continue
		}
		recordObservedBlock(startBlock, block, lastObserved, totalBlocks, totalTxs, " [rpc catch-up]")
	}

	return true
}

// exhaustiveCatchUp performs a final comprehensive sweep to fetch ALL blocks
// between lastObserved and the chain tip. Uses concurrent workers with a
// generous timeout since this is the last chance to get accurate data.
func exhaustiveCatchUp(
	client *rpchttp.HTTP,
	startBlock observedBlock,
	lastObserved *observedBlock,
	totalBlocks, totalTxs *int64,
) {
	ctx, cancel := context.WithTimeout(context.Background(), finalCatchUpTimeout)
	defer cancel()

	latestBlock, err := currentBlock(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Could not fetch latest block for final catch-up: %v\n", err)
		return
	}

	if latestBlock.height <= lastObserved.height {
		return
	}

	fromHeight := lastObserved.height + 1
	toHeight := latestBlock.height
	gap := toHeight - fromHeight + 1

	fmt.Printf("🔄 Catching up %d blocks (#%d → #%d) with %d workers...\n",
		gap, fromHeight, toHeight, fetchWorkers)

	blocks := fetchBlocksConcurrent(client, fromHeight, toHeight)
	fetched := 0
	for _, block := range blocks {
		if block.height <= lastObserved.height {
			continue
		}
		recordObservedBlock(startBlock, block, lastObserved, totalBlocks, totalTxs, " [final catch-up]")
		fetched++
	}

	if fetched < int(gap) {
		fmt.Printf("⚠️  Fetched %d/%d blocks (some RPC calls may have timed out)\n", fetched, gap)
	} else {
		fmt.Printf("✅ Caught up all %d blocks\n", fetched)
	}
}

func recordObservedBlock(
	startBlock, block observedBlock,
	lastObserved *observedBlock,
	totalBlocks, totalTxs *int64,
	suffix string,
) {
	*totalBlocks += 1
	*totalTxs += int64(block.txCount)

	blockInterval := block.time.Sub(lastObserved.time).Seconds()
	blockTPS := 0.0
	if blockInterval > 0 {
		blockTPS = float64(block.txCount) / blockInterval
	}

	rollingDuration := block.time.Sub(startBlock.time).Seconds()
	rollingTPS := 0.0
	if rollingDuration > 0 {
		rollingTPS = float64(*totalTxs) / rollingDuration
	}

	fmt.Printf(
		"📦 Block #%d: %d txs | Δt: %.3fs | Block TPS: %.2f | Avg TPS: %.2f%s\n",
		block.height,
		block.txCount,
		maxFloat(blockInterval, 0),
		blockTPS,
		rollingTPS,
		suffix,
	)

	*lastObserved = block
}

func blockFromEvent(event coretypes.ResultEvent) (observedBlock, bool) {
	switch data := event.Data.(type) {
	case cmttypes.EventDataNewBlock:
		if data.Block == nil {
			return observedBlock{}, false
		}
		return observedBlock{
			height:  data.Block.Height,
			time:    data.Block.Time,
			txCount: len(data.Block.Data.Txs),
		}, true
	case *cmttypes.EventDataNewBlock:
		if data == nil || data.Block == nil {
			return observedBlock{}, false
		}
		return observedBlock{
			height:  data.Block.Height,
			time:    data.Block.Time,
			txCount: len(data.Block.Data.Txs),
		}, true
	default:
		return observedBlock{}, false
	}
}

func printSummary(
	observeSecs int,
	startBlock observedBlock,
	lastObserved observedBlock,
	totalBlocks, totalTxs int64,
) {
	endBlock := lastObserved

	actualDuration := endBlock.time.Sub(startBlock.time).Seconds()
	if actualDuration <= 0 {
		actualDuration = float64(observeSecs)
	}

	onChainTPS := 0.0
	if actualDuration > 0 {
		onChainTPS = float64(totalTxs) / actualDuration
	}

	fmt.Println("═══════════════════════════════════════")
	fmt.Printf("✅ TPS Measurement Complete\n")
	fmt.Printf("   Blocks observed: #%d → #%d (%d blocks)\n", startBlock.height, endBlock.height, totalBlocks)
	fmt.Printf("   Wall time:        %ds\n", observeSecs)
	fmt.Printf("   Chain time:       %.1fs\n", actualDuration)
	fmt.Printf("   Total txs:        %d\n", totalTxs)
	fmt.Printf("   ──────────────────────────────────\n")
	fmt.Printf("   🚀 On-chain TPS: %.2f tx/s\n", onChainTPS)
	fmt.Println("═══════════════════════════════════════")
}

func maxFloat(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}
