# Uniocean Load Testing Bot Context

## Overview
This project is a high-throughput load-testing client for the Uniocean blockchain, an EVM-compatible Cosmos chain. It uses the `tm-load-test` framework and is designed to simulate real exchange activity.

## Key Components
- **`bot/`**: Main application logic.
    - `main.go`: Entry point, parses mnemonics, fetches account sequences via REST API, and initializes the load tester.
    - `client.go`: Implements the `loadtest.Client` interface. Builds, signs, and sends `MsgDeposit`, `MsgCreateSpotLimitOrder`, and `MsgCreateBinaryOptionsLimitOrder` transactions.
    - `ethsecp256k1.go`: Provides `EthPrivKey` and `EthPubKey` implementations. Crucial for EVM compatibility, as it uses Keccak256 for address derivation and ensures the correct Protobuf Type URL (`ethermint.crypto.v1.ethsecp256k1.PubKey`).
- **`tps-checker/`**: A standalone tool to monitor on-chain TPS by polling the Tendermint RPC and counting transactions in each block.

## Critical Technical Decisions
1. **REST over gRPC for Sequence**: Used `/cosmos/auth/v1beta1/accounts/` REST endpoint to fetch account numbers and sequences, avoiding complex Protobuf registration issues with Ethermint account types.
2. **Custom Crypto Wrapper**: Implemented a custom crypto wrapper using `github.com/ethereum/go-ethereum/crypto` to solve the "invalid pubkey" error, ensuring signatures and addresses match the chain's expected EVM format.
3. **Fee Adjustment**: Set transaction fees to `50,000,000,000,000 oceanx` base units to satisfy the chain's minimum global fee requirement.

## Usage
### Build
```bash
go build -o uniocean-load-tester ./bot
go build -o tps-checker ./tps-checker
```

### Run Load Test
```bash
./uniocean-load-tester funded-wallets.log \
  -c 1 -T 60 -r 500 \
  --broadcast-tx-method async \
  --endpoints wss://uniocean-tps.zeeve.net/websocket
```

### Run TPS Checker
```bash
./tps-checker 60
```

## Current Status
The bot is fully functional, supporting multiple transaction types with correct signing, addressing, and fee configurations. On-chain TPS can be verified using the `tps-checker` tool.
