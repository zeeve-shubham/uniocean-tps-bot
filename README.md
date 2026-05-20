# uniocean-tps-bot

Short runbook for the commands used most often to test load locally or on a server.

## What this repo gives you

- `./uniocean-load-tester` — sends load to a CometBFT/Cosmos RPC node
- `./tps-checker/tps-checker` — measures **committed** TPS from blocks

Default tx type is bank send (`UNIOCEAN_TX_TYPES=4`), which is the safest option for TPS testing.

## Build

```bash
go build -o uniocean-load-tester ./bot
go build -o tps-checker/tps-checker ./tps-checker
```

## Wallet file

Use a funded wallet log such as `funded-new.log` or `funded-wallets.log`.

Expected format:

```text
[user1] Mnemonic: ...

- address: oceanx1...
```

Do **not** commit wallet logs.

## Default endpoints in this repo

| Service | Default |
| --- | --- |
| WebSocket RPC | `ws://134.119.179.234:26657/websocket` |
| REST | `http://134.119.179.234:1317` |
| gRPC | `134.119.179.234:9090` |

Override when needed:

```bash
export UNIOCEAN_REST_ENDPOINT=http://<host>:1317
export UNIOCEAN_GRPC_ENDPOINT=<host>:9090
export UNIOCEAN_WS_ENDPOINT=ws://<host>:26657/websocket
```

## Commands you will use most

### 1. Sync smoke test

Use this first. It is slower, but it catches sequence and validation problems quickly.

```bash
./uniocean-load-tester funded-new.log \
  -c 1 -T 10 -r 20 \
  --broadcast-tx-method sync \
  --endpoints ws://134.119.179.234:26657/websocket
```

### 2. Main async load test

Use this when you want to push load.

```bash
./uniocean-load-tester funded-new.log \
  -c 1 -T 30 -r 800 \
  --broadcast-tx-method async \
  --endpoints ws://134.119.179.234:26657/websocket
```

### 3. Real committed TPS

Run this in another terminal **at the same time** as the load test.

```bash
./tps-checker/tps-checker 30
```

If you have not built it yet:

```bash
go run ./tps-checker/main.go 30
```

### 4. Single transaction debug

Useful when you want the exact chain response for one tx.

```bash
./uniocean-load-tester funded-new.log --debug-sync-broadcast
```

### 5. Same flow on another server

```bash
export UNIOCEAN_REST_ENDPOINT=http://<host>:1317
export UNIOCEAN_GRPC_ENDPOINT=<host>:9090
export UNIOCEAN_WS_ENDPOINT=ws://<host>:26657/websocket

./uniocean-load-tester funded-new.log \
  -c 1 -T 30 -r 800 \
  --broadcast-tx-method async \
  --endpoints "$UNIOCEAN_WS_ENDPOINT"

./tps-checker/tps-checker 30
```

## How to read the output

- `toSend=N` means txs were **sent to the RPC node**
- `sync` is for validation
- `async` is for pressure / load
- `🚀 On-chain TPS` from `tps-checker` is the real committed TPS

If you see `CheckTx rejected transaction` or `Broadcast health`, the node is receiving txs but rejecting some of them before block inclusion.

## Common problems

| Problem | Meaning | What to do |
| --- | --- | --- |
| `account sequence mismatch` | stale sequence or stuck pending txs on the sender node | restart/clear the sender node mempool, then rerun the sync smoke test |
| `CheckTx rejected transaction` | tx failed before inclusion | lower rate, verify fees/gas, keep tx type simple |
| load tester sends but TPS stays low | chain throughput is lower than send rate | trust the checker, not `toSend` |
| checker shows 0 during heavy load | subscription may miss live events | use the rebuilt checker; it now backfills missed blocks over RPC |

Check sender-node mempool quickly:

```bash
curl -s http://134.119.179.234:26657/num_unconfirmed_txs
```

If `n_txs` is huge, the sender node is backed up.

## Practical testing order

1. Build binaries
2. Run the **sync smoke test**
3. Run the **async load test**
4. Run the **TPS checker** in parallel
5. If results look wrong, run the **single transaction debug**
