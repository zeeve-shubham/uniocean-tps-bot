package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const rpcEndpoint = "https://uniocean-tps.zeeve.net/cosmos"

func rpcEndpointFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("UNIOCEAN_RPC_ENDPOINT")); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(os.Getenv("TM_RPC_ENDPOINT")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return strings.TrimRight(rpcEndpoint, "/")
}

// Tendermint RPC response types

type blockResponse struct {
	Result struct {
		Block struct {
			Header struct {
				Height string `json:"height"`
				Time   string `json:"time"`
			} `json:"header"`
			Data struct {
				Txs []string `json:"txs"` // base64-encoded txs; length = tx count
			} `json:"data"`
		} `json:"block"`
	} `json:"result"`
}

type statusResponse struct {
	Result struct {
		NodeInfo struct {
			Network string `json:"network"`
		} `json:"node_info"`
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"`
		} `json:"sync_info"`
	} `json:"result"`
}

func getStatus() (*statusResponse, error) {
	rpc := rpcEndpointFromEnv()
	url := rpc + "/status"

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result statusResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func getBlock(height int64) (*blockResponse, error) {
	rpc := rpcEndpointFromEnv()
	var url string
	if height == 0 {
		url = rpc + "/block"
	} else {
		url = fmt.Sprintf("%s/block?height=%d", rpc, height)
	}

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result blockResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func main() {
	// Duration to observe (default 60s, override via arg).
	// Optional lookback blocks can be provided as a 2nd arg to compute TPS over
	// already-produced blocks (useful if you start the checker after a load test).
	observeSecs := 60
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			observeSecs = n
		}
	}
	lookbackBlocks := 0
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n >= 0 {
			lookbackBlocks = n
		}
	}

	fmt.Printf("🔍 Uniocean On-Chain TPS Checker\n")
	fmt.Printf("   RPC: %s\n", rpcEndpointFromEnv())
	if strings.TrimSpace(os.Getenv("UNIOCEAN_RPC_ENDPOINT")) == "" && strings.TrimSpace(os.Getenv("TM_RPC_ENDPOINT")) == "" {
		fmt.Printf("   Note: using default RPC (set UNIOCEAN_RPC_ENDPOINT to override)\n")
	}
	if status, err := getStatus(); err == nil {
		fmt.Printf("   Network: %s | Height: %s\n", status.Result.NodeInfo.Network, status.Result.SyncInfo.LatestBlockHeight)
	} else {
		fmt.Printf("   Warning: unable to read /status (%v)\n", err)
	}
	fmt.Printf("   Observation window: %ds\n", observeSecs)
	if lookbackBlocks > 0 {
		fmt.Printf("   Lookback blocks:    %d\n", lookbackBlocks)
	}
	fmt.Printf("\n")

	// Snapshot start block (or compute from lookback)
	startHeight := int64(0)
	startTime := time.Time{}
	startBlock, err := getBlock(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to fetch latest block: %v\n", err)
		os.Exit(1)
	}
	latestHeight, _ := strconv.ParseInt(startBlock.Result.Block.Header.Height, 10, 64)
	if lookbackBlocks > 0 {
		startHeight = latestHeight - int64(lookbackBlocks)
		if startHeight < 1 {
			startHeight = 1
		}
		b, err := getBlock(startHeight)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to fetch lookback start block #%d: %v\n", startHeight, err)
			os.Exit(1)
		}
		startTime, _ = time.Parse(time.RFC3339Nano, b.Result.Block.Header.Time)
		fmt.Printf("📦 Start block (lookback): #%d at %s\n", startHeight, startTime.Format("15:04:05"))
	} else {
		startHeight = latestHeight
		startTime, _ = time.Parse(time.RFC3339Nano, startBlock.Result.Block.Header.Time)
		fmt.Printf("📦 Start block: #%d at %s\n", startHeight, startTime.Format("15:04:05"))
	}

	// Tick every second and print live stats
	ticker := time.NewTicker(1 * time.Second)
	deadline := time.After(time.Duration(observeSecs) * time.Second)

	var lastHeight int64 = startHeight
	var totalTxs int64
	var totalBlocks int64
	elapsed := 0

	// If we're doing lookback, count historical blocks immediately.
	if lookbackBlocks > 0 {
		for h := startHeight; h <= latestHeight; h++ {
			b, err := getBlock(h)
			if err != nil {
				fmt.Printf("[init] ⚠️  Failed to fetch block #%d: %v\n", h, err)
				continue
			}
			txCount := int64(len(b.Result.Block.Data.Txs))
			totalTxs += txCount
			totalBlocks++
		}
		lastHeight = latestHeight
		fmt.Printf("[init] 📊 Lookback total: %d txs across %d blocks\n\n", totalTxs, totalBlocks)
	}

	for {
		select {
		case <-ticker.C:
			elapsed++
			latest, err := getBlock(0)
			if err != nil {
				fmt.Printf("[%3ds] ⚠️  RPC error: %v\n", elapsed, err)
				continue
			}

			latestHeight, _ := strconv.ParseInt(latest.Result.Block.Header.Height, 10, 64)
			if latestHeight <= lastHeight {
				fmt.Printf("[%3ds] ⏳ No new block yet (still at #%d)\n", elapsed, latestHeight)
				continue
			}

			// Count txs in all new blocks since last check
			var newTxs int64
			var newBlocks int64
			for h := lastHeight + 1; h <= latestHeight; h++ {
				b, err := getBlock(h)
				if err != nil {
					fmt.Printf("[%3ds] ⚠️  Failed to fetch block #%d: %v\n", elapsed, h, err)
					continue
				}
				txCount := int64(len(b.Result.Block.Data.Txs))
				newTxs += txCount
				newBlocks++
				fmt.Printf("[%3ds] 📦 Block #%d: %d txs\n", elapsed, h, txCount)
			}

			totalTxs += newTxs
			totalBlocks += newBlocks
			lastHeight = latestHeight

			// Live TPS: txs in this interval / seconds elapsed so far
			liveTPS := float64(totalTxs) / float64(elapsed)
			fmt.Printf("[%3ds] 📊 Running total: %d txs across %d blocks | Avg TPS: %.2f\n\n",
				elapsed, totalTxs, totalBlocks, liveTPS)

		case <-deadline:
			ticker.Stop()

			endBlock, _ := getBlock(0)
			endHeight, _ := strconv.ParseInt(endBlock.Result.Block.Header.Height, 10, 64)
			endTime, _ := time.Parse(time.RFC3339Nano, endBlock.Result.Block.Header.Time)

			actualDuration := endTime.Sub(startTime).Seconds()
			if actualDuration <= 0 {
				actualDuration = float64(observeSecs)
			}

			fmt.Println("═══════════════════════════════════════")
			fmt.Printf("✅ TPS Measurement Complete\n")
			fmt.Printf("   Blocks observed: #%d → #%d (%d blocks)\n", startHeight, endHeight, totalBlocks)
			fmt.Printf("   Wall time:        %ds\n", observeSecs)
			fmt.Printf("   Chain time:       %.1fs\n", actualDuration)
			fmt.Printf("   Total txs:        %d\n", totalTxs)
			fmt.Printf("   ──────────────────────────────────\n")
			fmt.Printf("   🚀 On-chain TPS: %.2f tx/s\n", float64(totalTxs)/actualDuration)
			fmt.Println("═══════════════════════════════════════")
			return
		}
	}
}
