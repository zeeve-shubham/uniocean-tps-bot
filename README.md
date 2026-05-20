# 🌊 uniocean-tps-bot

> **Recommended GitHub Repository Name:** `uniocean-tps-bot`
> **Full URL:** `https://github.com/zeeve-shubham/uniocean-tps-bot`

A custom high-throughput load testing bot for the **Uniocean Network** (EVM-compatible Cosmos chain), built on top of the [`tm-load-test`](https://github.com/informalsystems/tm-load-test) framework. It simulates real exchange activity by broadcasting four types of on-chain transactions across 50 pre-funded wallets simultaneously.

---

## 📦 What This Bot Does

The bot connects to the Uniocean Tendermint WebSocket RPC, loads up to 50 wallets from a wallet log file, fetches their current account sequence numbers via the Cosmos REST API, and then floods the network with signed transactions at a configurable rate.

**Supported transaction types (randomly rotated per wallet):**
- `MsgDeposit` — Deposit tokens into the exchange subaccount
- `MsgCreateSpotLimitOrder` — Create a spot market limit order
- `MsgCreateDerivativeLimitOrder` — Create a derivative market limit order
- `MsgCreateBinaryOptionsLimitOrder` — Create a binary options limit order

---

## 🗂️ Project Structure

```
uniocean-tps-bot/
├── bot/
│   ├── main.go           # CLI entrypoint, wallet loading, REST account fetch
│   ├── client.go         # Transaction builder and signer (loadtest.Client interface)
│   └── eth_account.go    # Minimal EthAccount type for protobuf decoding
├── tps-checker/
│   └── main.go           # On-chain TPS measurement tool (polls Tendermint RPC)
├── x/exchange/types/
│   ├── tx.pb.go          # Protobuf-generated exchange transaction types
│   ├── msgs.go           # sdk.Msg interface implementations
│   └── ...               # Other generated protobuf files
├── tm-load-test/         # Vendored tm-load-test framework (submodule)
├── proto/                # Source .proto definitions
├── go.mod
├── go.sum
└── funded-wallets.log    # (NOT committed) Your wallet file
```

---

## 🖥️ Network Endpoints

| Service              | URL                                              |
|----------------------|--------------------------------------------------|
| Tendermint WebSocket | `wss://uniocean-tps.zeeve.net/websocket`         |
| Cosmos LCD/REST API  | `https://uniocean-tps.zeeve.net/api`             |
| Tendermint RPC       | `https://uniocean-tps.zeeve.net/cosmos/`         |
| EVM JSON-RPC         | `https://uniocean-tps.zeeve.net/uniocean-evm/`   |
| gRPC                 | `uniocean-tps-grpc.zeeve.net:19090`              |

---

## 🔧 Prerequisites

Make sure the following are installed on your system:

| Tool      | Version         | Install                                                      |
|-----------|-----------------|--------------------------------------------------------------|
| Go        | 1.21+           | https://go.dev/dl/                                           |
| Git       | any             | `sudo apt install git`                                       |
| `goimports` (optional, dev only) | any | `go install golang.org/x/tools/cmd/goimports@latest`   |

---

## 🚀 Server Setup (Step by Step)

### 1. Clone the Repository

```bash
git clone https://github.com/zeeve-shubham/uniocean-tps-bot.git
cd uniocean-tps-bot
```

### 2. Install Go (if not already installed)

```bash
# Download and install Go 1.22
wget https://go.dev/dl/go1.22.3.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.3.linux-amd64.tar.gz

# Add Go to your PATH (add this to ~/.bashrc or ~/.profile)
export PATH=$PATH:/usr/local/go/bin
source ~/.bashrc

# Verify
go version
```

### 3. Download Dependencies

```bash
go mod tidy
```

### 4. Build the Binary

```bash
go build -o uniocean-load-tester ./bot
```

This produces a single self-contained binary: `./uniocean-load-tester`

### 5. Prepare the Wallet File

The bot reads wallets from a log file in the format exported by the Uniocean keyring. Create a file (e.g., `funded-wallets.log`) with entries like:

```
[tps-user1] Mnemonic: west attack tank fitness wear solar pink erosion ...

- address: oceanx1a4u6mdx7phmhvev7mxpdsfwpzwla2tmm858zlp
  name: tps-user1
  pubkey: '{"@type":"/ethermint.crypto.v1.ethsecp256k1.PubKey","key":"..."}'
  type: local
```

> ⚠️ **Security Warning:** Never commit `funded-wallets.log` to any public repository! Add it to `.gitignore` immediately.

```bash
echo "funded-wallets.log" >> .gitignore
```

---

## ▶️ Running the Load Test

### Basic Usage

```bash
./uniocean-load-tester <wallet-file> [tm-load-test flags...]
```

### Recommended Test Command

```bash
./uniocean-load-tester funded-wallets.log \
  -c 1 \
  -T 60 \
  -r 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
```

> `async` is the fastest way to push load, but it does **not** mean the tx made it into a block. The load transport now surfaces `CheckTx` rejections from node responses during the run, and the TPS checker below measures what was actually committed.

### Flag Reference

| Flag | Description | Example |
|------|-------------|---------|
| `<wallet-file>` | Path to your wallet log file | `funded-wallets.log` |
| `-c` | Number of WebSocket connections | `-c 1` |
| `-T` | Duration of test in seconds | `-T 60` |
| `-r` | Target transaction rate (tx/sec) | `-r 500` |
| `-s` | Transaction batch size | `-s 250` |
| `--broadcast-tx-method` | How to broadcast: `sync`, `async`, or `commit` | `--broadcast-tx-method async` |
| `--endpoints` | Comma-separated list of Tendermint WebSocket URLs | `--endpoints wss://...` |

### High-TPS Example (Stress Test)

```bash
./uniocean-load-tester funded-wallets.log \
  -c 2 \
  -T 120 \
  -r 1000 \
  -s 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
```

### View All Available Flags

```bash
./uniocean-load-tester funded-wallets.log --help
```

---

## 🔁 Running as a Background Service (systemd)

To keep the bot running on a server without an active terminal session:

### 1. Copy the binary and wallet file to a permanent location

```bash
sudo mkdir -p /opt/uniocean-tps-bot
sudo cp uniocean-load-tester /opt/uniocean-tps-bot/
sudo cp funded-wallets.log /opt/uniocean-tps-bot/
```

### 2. Create a systemd service file

```bash
sudo tee /etc/systemd/system/uniocean-tps.service > /dev/null <<EOF
[Unit]
Description=Uniocean TPS Load Tester
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/uniocean-tps-bot
ExecStart=/opt/uniocean-tps-bot/uniocean-load-tester funded-wallets.log \
  -c 2 -T 300 -r 1000 -s 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
```

### 3. Enable and start the service

```bash
sudo systemctl daemon-reload
sudo systemctl enable uniocean-tps
sudo systemctl start uniocean-tps
```

### 4. Check logs

```bash
# Live logs
sudo journalctl -u uniocean-tps -f

# Last 100 lines
sudo journalctl -u uniocean-tps -n 100
```

### 5. Stop the service

```bash
sudo systemctl stop uniocean-tps
```

---

## 🐳 Running with Docker (Optional)

```bash
# Build the Docker image
docker build -t uniocean-tps-bot .

# Run the container
docker run --rm \
  -v $(pwd)/funded-wallets.log:/app/funded-wallets.log \
  uniocean-tps-bot \
  funded-wallets.log \
  -c 1 -T 60 -r 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
```

> **Note:** A `Dockerfile` is not included by default. Create one if needed using `golang:1.22-alpine` as the base image.

---

## 🧪 Quick Smoke Test (Dry Run)

To verify wallets are loaded correctly without running a full test:

```bash
./uniocean-load-tester funded-wallets.log --help
```

If you see `Loaded 50 wallets` printed before the help text, your wallet file is being parsed correctly.

---

## 🔍 Understanding the Load Tester Output

```
Loaded 50 wallets
INFO[0014] Connecting to remote endpoints        ctx=loadtest
INFO[0015] Connected to remote Tendermint RPC    ctx="transactor[wss://...]"
INFO[0015] Initiating load test                  ctx=loadtest
INFO[0016] Sending batch of transactions         toSend=500
INFO[0017] Sending batch of transactions         toSend=500
...
INFO[0075] Time limit reached for load testing   ctx="transactor[wss://...]"
INFO[0075] Load test complete!                   ctx=loadtest
```

> ⚠️ **Important:** `toSend=500` means **500 transactions were broadcast** per second, NOT that 500 txs were included in blocks. The chain may accept fewer depending on block gas limits, mempool pressure, and validator throughput. During the run, watch for `CheckTx rejected transaction` or `Broadcast health` logs from the sender, and use the **TPS Checker** below to measure what was actually committed on-chain.

---

## 📊 Measuring Real On-Chain TPS

The load tester tells you how many transactions were *sent*, and now also surfaces whether the node is rejecting them during `CheckTx`. To measure what was actually *included in blocks*, use the included `tps-checker` tool — it subscribes to new blocks over WebSocket and reports block-level tx counts as soon as they land.

### Build the TPS Checker

```bash
go build -o tps-checker ./tps-checker
```

### Run Alongside the Load Tester

Open **two terminals simultaneously**:

**Terminal 1 — Load Tester:**
```bash
./uniocean-load-tester funded-wallets.log \
  -c 1 -T 60 -r 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
```

For new server it is 

```bash
./uniocean-load-tester funded-new.log \
  -c 1 -T 60 -r 500 \
  --broadcast-tx-method async \
  --endpoints ws://134.119.179.234:26657/websocket
```

**Terminal 2 — TPS Checker (same duration):**
```bash
go run ./tps-checker/main.go 60
```

Or if already compiled:
```bash
./tps-checker/tps-checker 60
```

### Live Output

```
🔍 Uniocean On-Chain TPS Checker
   RPC: https://uniocean-tps.zeeve.net/cosmos
   WS:  wss://uniocean-tps.zeeve.net/websocket
   Observation window: 60s

📦 Start block: #382688 at 11:42:57
📦 Block #382689: 312 txs | Δt: 1.001s | Block TPS: 311.69 | Avg TPS: 311.69
📦 Block #382690: 489 txs | Δt: 0.998s | Block TPS: 489.98 | Avg TPS: 400.50
...
```

### Final Summary

```
═══════════════════════════════════════
✅ TPS Measurement Complete
   Blocks observed: #382688 → #382748 (60 blocks)
   Wall time:        60s
   Chain time:       62.3s
   Total txs:        28450
   ──────────────────────────────────
   🚀 On-chain TPS: 456.67 tx/s
═══════════════════════════════════════
```

### TPS Checker Flags

```bash
# Default: observe for 60 seconds
go run ./tps-checker/main.go

# Custom duration (e.g., 120 seconds)
go run ./tps-checker/main.go 120
```

### Sent vs Committed — What's the Difference?

| Metric | What it measures | Where to find it |
|--------|-----------------|------------------|
| **Sent TPS** | Txs broadcast to the mempool | Load tester output (`toSend=N`) |
| **On-chain TPS** | Txs actually committed in blocks | TPS Checker (`🚀 On-chain TPS`) |

A large gap between the two means the chain's block throughput is lower than the send rate — this is useful information for tuning block params.

---

## ⚙️ Configuration Constants (in code)

These values are currently hardcoded in `bot/main.go` and `bot/client.go`. Change them before building if needed:

| Constant | File | Default | Description |
|---|---|---|---|
| `chainID` | `main.go` | `uniocean_8888-1` | Your chain ID |
| `restEndpoint` | `main.go` | `https://uniocean-tps.zeeve.net/api` | Cosmos REST API for account queries |
| Gas limit | `client.go` | `300000` | Gas per transaction |
| Fee amount | `client.go` | `2000 oceanx` | Fee per transaction |
| HD path | `main.go` | `m/44'/60'/0'/0/0` | EVM-compatible derivation path |

---

## 🛠️ Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `Warning: failed to query account` | Wallet not funded or wrong address prefix | Ensure all wallets have funds on-chain |
| `No wallets loaded` | Wallet file format mismatch | Check that the file has `Mnemonic:` and `- address:` lines |
| `connection refused` on WebSocket | Wrong endpoint or firewall | Check network connectivity and endpoint URL |
| `account sequence mismatch` in tx errors | Stale sequence number or too much pending load on the same accounts | Restart the bot to re-fetch sequences, lower the rate, or add more funded wallets |
| `CheckTx rejected transaction` / `Broadcast health` logs | The node received the RPC call but rejected some txs before block inclusion | Inspect the rejection log, then tune fee/gas/tx type/rate until `checkTxRejected` stays near zero |
| `out of gas` | Gas limit too low | Increase gas limit in `bot/client.go` |

---

## 📋 .gitignore Recommendation

```gitignore
# Compiled binary
uniocean-load-tester

# Wallet files — NEVER commit these!
*.log
funded-wallets.log
mnemonics.txt
*.mnemonic

# Go build artifacts
*.test
```

---

## 📄 License

MIT — see [LICENSE](LICENSE) for details.

---

## 🙋 Support

For issues or questions, open a GitHub issue at:
`https://github.com/zeeve-shubham/uniocean-tps-bot/issues`
