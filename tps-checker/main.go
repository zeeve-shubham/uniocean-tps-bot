package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const rpcEndpoint = "https://uniocean-tps.zeeve.net/cosmos"

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

func getBlock(height int64) (*blockResponse, error) {
	var url string
	if height == 0 {
		url = rpcEndpoint + "/block"
	} else {
		url = fmt.Sprintf("%s/block?height=%d", rpcEndpoint, height)
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
	// Duration to observe (default 60s, override via arg)
	observeSecs := 60
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			observeSecs = n
		}
	}

	fmt.Printf("🔍 Uniocean On-Chain TPS Checker\n")
	fmt.Printf("   RPC: %s\n", rpcEndpoint)
	fmt.Printf("   Observation window: %ds\n\n", observeSecs)

	// Snapshot start block
	startBlock, err := getBlock(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to fetch start block: %v\n", err)
		os.Exit(1)
	}

	startHeight, _ := strconv.ParseInt(startBlock.Result.Block.Header.Height, 10, 64)
	startTime, _ := time.Parse(time.RFC3339Nano, startBlock.Result.Block.Header.Time)
	fmt.Printf("📦 Start block: #%d at %s\n", startHeight, startTime.Format("15:04:05"))

	// Tick every second and print live stats
	ticker := time.NewTicker(1 * time.Second)
	deadline := time.After(time.Duration(observeSecs) * time.Second)

	var lastHeight int64 = startHeight
	var totalTxs int64
	var totalBlocks int64
	elapsed := 0

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
