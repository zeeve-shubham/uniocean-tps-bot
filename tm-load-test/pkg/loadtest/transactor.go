package loadtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/informalsystems/tm-load-test/internal/logging"
)

const (
	defaultConnSendTimeout = 10 * time.Second
	// see https://github.com/tendermint/tendermint/blob/v0.32.x/rpc/lib/server/handlers.go
	connPingPeriod = (30 * 9 / 10) * time.Second

	defaultProgressCallbackInterval = 5 * time.Second
)

// Transactor represents a single wire-level connection to a Tendermint RPC
// endpoint, and this is responsible for sending transactions to that endpoint.
type Transactor struct {
	remoteAddr string  // The full URL of the remote WebSockets endpoint.
	config     *Config // The configuration for the load test.

	client            Client
	logger            logging.Logger
	conn              *websocket.Conn
	connSendTimeout   time.Duration
	broadcastTxMethod string
	wg                sync.WaitGroup
	nextRPCID         int64

	// Rudimentary statistics
	statsMtx  sync.RWMutex
	startTime time.Time // When did the transaction sending start?
	txCount   int       // How many transactions have been sent.
	txBytes   int64     // How many transaction bytes have been sent, cumulatively.
	txRate    float64   // The number of transactions sent, per second.

	// Broadcast result stats (best-effort; depends on broadcast method).
	resultMtx    sync.RWMutex
	rpcErrors    int
	checkTxOK    int
	checkTxError int
	lastReportOK int
	lastReportNG int
	lastReportRE int

	progressCallbackMtx      sync.RWMutex
	progressCallbackID       int                                      // A unique identifier for this transactor when calling the progress callback.
	progressCallbackInterval time.Duration                            // How frequently to call the progress update callback.
	progressCallback         func(id int, txCount int, txBytes int64) // Called with the total number of transactions executed so far.

	stopMtx sync.RWMutex
	stop    bool
	stopErr error // Did an error occur that triggered the stop?
}

// NewTransactor initiates a WebSockets connection to the given host address.
// Must be a valid WebSockets URL, e.g. "ws://host:port/websocket"
func NewTransactor(remoteAddr string, config *Config) (*Transactor, error) {
	u, err := url.Parse(remoteAddr)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("unsupported protocol: %s (only ws:// and wss:// are supported)", u.Scheme)
	}
	clientFactory, exists := clientFactories[config.ClientFactory]
	if !exists {
		return nil, fmt.Errorf("unrecognized client factory: %s", config.ClientFactory)
	}
	client, err := clientFactory.NewClient(*config)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("failed to connect to remote WebSockets endpoint %s: %s (status code %d)", remoteAddr, resp.Status, resp.StatusCode)
	}
	logger := logging.NewLogrusLogger(fmt.Sprintf("transactor[%s]", u.String()))
	logger.Info("Connected to remote Tendermint WebSockets RPC")

	sendTimeout := defaultConnSendTimeout
	if raw := strings.TrimSpace(os.Getenv("TM_LOAD_TEST_WS_WRITE_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			sendTimeout = d
		}
	}
	return &Transactor{
		remoteAddr:               u.String(),
		config:                   config,
		client:                   client,
		logger:                   logger,
		conn:                     conn,
		connSendTimeout:          sendTimeout,
		broadcastTxMethod:        "broadcast_tx_" + config.BroadcastTxMethod,
		progressCallbackInterval: defaultProgressCallbackInterval,
	}, nil
}

func (t *Transactor) SetProgressCallback(id int, interval time.Duration, callback func(int, int, int64)) {
	t.progressCallbackMtx.Lock()
	t.progressCallbackID = id
	t.progressCallbackInterval = interval
	t.progressCallback = callback
	t.progressCallbackMtx.Unlock()
}

// Start kicks off the transactor's operations in separate goroutines (one for
// reading from the WebSockets endpoint, and one for writing to it).
func (t *Transactor) Start() {
	t.logger.Debug("Starting transactor")
	t.wg.Add(2)
	go t.receiveLoop()
	go t.sendLoop()
}

// Cancel will indicate to the transactor that it must stop, but does not wait
// until it has completely stopped. To wait, call the Transactor.Wait() method.
func (t *Transactor) Cancel() {
	t.setStop(fmt.Errorf("transactor operations cancelled"))
}

// Wait will block until the transactor terminates.
func (t *Transactor) Wait() error {
	t.wg.Wait()
	t.stopMtx.RLock()
	defer t.stopMtx.RUnlock()
	return t.stopErr
}

// GetTxCount returns the total number of transactions sent thus far by this
// transactor.
func (t *Transactor) GetTxCount() int {
	t.statsMtx.RLock()
	defer t.statsMtx.RUnlock()
	return t.txCount
}

// GetTxBytes returns the cumulative total number of bytes (as transactions)
// sent thus far by this transactor.
func (t *Transactor) GetTxBytes() int64 {
	t.statsMtx.RLock()
	defer t.statsMtx.RUnlock()
	return t.txBytes
}

// GetTxRate returns the average number of transactions per second sent by
// this transactor over the duration of its operation.
func (t *Transactor) GetTxRate() float64 {
	t.statsMtx.RLock()
	defer t.statsMtx.RUnlock()
	return t.txRate
}

func (t *Transactor) receiveLoop() {
	defer t.wg.Done()
	for {
		_, raw, err := t.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				t.logger.Error("Failed to read response on connection", "err", err)
				return
			}
		}
		t.handleRPCResponse(raw)
		if t.mustStop() {
			return
		}
	}
}

func (t *Transactor) handleRPCResponse(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var resp RPCResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		// Don't spam logs; debug is enough.
		t.logger.Debug("Failed to decode RPC response", "err", err)
		return
	}

	if resp.Error != nil {
		t.resultMtx.Lock()
		t.rpcErrors++
		t.resultMtx.Unlock()
		// This is important even at info-level runs: it means the node didn't accept the request.
		t.logger.Info("RPC error response", "code", resp.Error.Code, "message", resp.Error.Message)
		return
	}

	// For broadcast_tx_sync and broadcast_tx_commit, CometBFT replies include CheckTx / DeliverTx codes.
	// For broadcast_tx_async, there is typically no CheckTx code (it can still fail later).
	type checkTxLike struct {
		Code int    `json:"code"`
		Log  string `json:"log"`
	}
	var parsed struct {
		CheckTx   *checkTxLike `json:"check_tx,omitempty"`
		DeliverTx *checkTxLike `json:"deliver_tx,omitempty"`
		Code      *int         `json:"code,omitempty"`
		Log       *string      `json:"log,omitempty"`
	}
	if err := json.Unmarshal(resp.Result, &parsed); err != nil {
		return
	}

	code := -1
	logLine := ""
	if parsed.CheckTx != nil {
		code = parsed.CheckTx.Code
		logLine = parsed.CheckTx.Log
	} else if parsed.DeliverTx != nil {
		code = parsed.DeliverTx.Code
		logLine = parsed.DeliverTx.Log
	} else if parsed.Code != nil {
		code = *parsed.Code
		if parsed.Log != nil {
			logLine = *parsed.Log
		}
	}

	if code == -1 {
		return
	}

	t.resultMtx.Lock()
	if code == 0 {
		t.checkTxOK++
	} else {
		t.checkTxError++
	}
	t.resultMtx.Unlock()

	if code != 0 && logLine != "" {
		// Surface at info level; this is the usual reason for "one block with txs then many empty blocks".
		t.logger.Info("CheckTx rejected transaction", "code", code, "log", logLine)
	}
}

func (t *Transactor) sendLoop() {
	defer t.wg.Done()
	t.conn.SetPingHandler(func(message string) error {
		err := t.conn.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(t.connSendTimeout))
		if err == websocket.ErrCloseSent {
			return nil
		}
		return err
	})

	pingTicker := time.NewTicker(connPingPeriod)
	timeLimitTicker := time.NewTicker(time.Duration(t.config.Time) * time.Second)
	sendTicker := time.NewTicker(time.Duration(t.config.SendPeriod) * time.Second)
	progressTicker := time.NewTicker(t.getProgressCallbackInterval())
	defer func() {
		pingTicker.Stop()
		timeLimitTicker.Stop()
		sendTicker.Stop()
		progressTicker.Stop()
	}()

	for {
		if t.config.Count > 0 && t.GetTxCount() >= t.config.Count {
			t.logger.Info("Maximum transaction limit reached", "count", t.GetTxCount())
			t.setStop(nil)
		}
		select {
		case <-sendTicker.C:
			if err := t.sendTransactions(); err != nil {
				t.logger.Error("Failed to send transactions", "err", err)
				t.setStop(err)
			}

		case <-progressTicker.C:
			t.reportProgress()

		case <-pingTicker.C:
			if err := t.sendPing(); err != nil {
				t.logger.Error("Failed to write ping message", "err", err)
				t.setStop(err)
			}

		case <-timeLimitTicker.C:
			t.logger.Info("Time limit reached for load testing")
			t.setStop(nil)
		}
		if t.mustStop() {
			t.close()
			return
		}
	}
}

func (t *Transactor) writeTx(tx []byte) error {
	txBase64 := base64.StdEncoding.EncodeToString(tx)
	paramsJSON, err := json.Marshal(map[string]interface{}{"tx": txBase64})
	if err != nil {
		return err
	}
	id := int(atomic.AddInt64(&t.nextRPCID, 1))
	_ = t.conn.SetWriteDeadline(time.Now().Add(t.connSendTimeout))
	return t.conn.WriteJSON(RPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  t.broadcastTxMethod,
		Params:  json.RawMessage(paramsJSON),
	})
}

func (t *Transactor) mustStop() bool {
	t.stopMtx.RLock()
	defer t.stopMtx.RUnlock()
	return t.stop
}

func (t *Transactor) setStop(err error) {
	t.stopMtx.Lock()
	t.stop = true
	if err != nil {
		t.stopErr = err
	}
	t.stopMtx.Unlock()
}

func (t *Transactor) sendTransactions() error {
	// send as many transactions as we can, up to the send rate
	totalSent := t.GetTxCount()
	toSend := t.config.Rate
	if (t.config.Count > 0) && ((totalSent + toSend) > t.config.Count) {
		toSend = t.config.Count - totalSent
		t.logger.Debug("Nearing max transaction count", "totalSent", totalSent, "maxTxCount", t.config.Count, "toSend", toSend)
	}
	if totalSent == 0 {
		t.trackStartTime()
	}
	var sent int
	var sentBytes int64
	defer func() { t.trackSentTxs(sent, sentBytes) }()
	t.logger.Info("Sending batch of transactions", "toSend", toSend)
	batchStartTime := time.Now()
	batchDuration := time.Duration(t.config.SendPeriod) * time.Second
	batchEnd := batchStartTime.Add(batchDuration)
	pace := true
	if raw := strings.TrimSpace(os.Getenv("TM_LOAD_TEST_PACE")); raw != "" {
		pace = raw != "0" && !strings.EqualFold(raw, "false") && !strings.EqualFold(raw, "no")
	}
	for ; sent < toSend; sent++ {
		if pace && toSend > 1 {
			// Spread the batch across the send period to avoid bursting the WS socket.
			target := batchStartTime.Add(time.Duration(sent) * batchDuration / time.Duration(toSend))
			if now := time.Now(); target.After(now) {
				time.Sleep(target.Sub(now))
			}
		}
		tx, err := t.client.GenerateTx()
		if err != nil {
			return err
		}
		if err := t.writeTx(tx); err != nil {
			return err
		}
		sentBytes += int64(len(tx))
		// if we have to make way for the next batch
		if time.Now().After(batchEnd) {
			break
		}
	}
	return nil
}

func (t *Transactor) trackStartTime() {
	t.statsMtx.Lock()
	t.startTime = time.Now()
	t.txRate = 0.0
	t.statsMtx.Unlock()
}

func (t *Transactor) trackSentTxs(count int, byteCount int64) {
	t.statsMtx.Lock()
	defer t.statsMtx.Unlock()

	t.txCount += count
	t.txBytes += byteCount
	elapsed := time.Since(t.startTime).Seconds()
	if elapsed > 0 {
		t.txRate = float64(t.txCount) / elapsed
	} else {
		t.txRate = 0
	}
}

func (t *Transactor) sendPing() error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(t.connSendTimeout))
	return t.conn.WriteMessage(websocket.PingMessage, []byte{})
}

func (t *Transactor) reportProgress() {
	txCount := t.GetTxCount()
	txRate := t.GetTxRate()
	txBytes := t.GetTxBytes()
	t.logger.Debug("Statistics", "txCount", txCount, "txRate", fmt.Sprintf("%.3f txs/sec", txRate))

	t.resultMtx.RLock()
	checkOK := t.checkTxOK
	checkErr := t.checkTxError
	rpcErrs := t.rpcErrors
	lastOK := t.lastReportOK
	lastNG := t.lastReportNG
	lastRE := t.lastReportRE
	t.resultMtx.RUnlock()

	// Log deltas at info-level so you can correlate with "empty block" periods.
	dOK := checkOK - lastOK
	dNG := checkErr - lastNG
	dRE := rpcErrs - lastRE
	if dOK != 0 || dNG != 0 || dRE != 0 {
		t.logger.Info("Broadcast health", "checkTxOK(+)", dOK, "checkTxError(+)", dNG, "rpcErrors(+)", dRE, "checkTxOK", checkOK, "checkTxError", checkErr, "rpcErrors", rpcErrs)
		t.resultMtx.Lock()
		t.lastReportOK = checkOK
		t.lastReportNG = checkErr
		t.lastReportRE = rpcErrs
		t.resultMtx.Unlock()
	}

	t.progressCallbackMtx.RLock()
	defer t.progressCallbackMtx.RUnlock()
	if t.progressCallback != nil {
		t.progressCallback(t.progressCallbackID, txCount, txBytes)
	}
}

func (t *Transactor) getProgressCallbackInterval() time.Duration {
	t.progressCallbackMtx.RLock()
	defer t.progressCallbackMtx.RUnlock()
	return t.progressCallbackInterval
}

func (t *Transactor) close() {
	// try to cleanly shut down the connection
	_ = t.conn.SetWriteDeadline(time.Now().Add(t.connSendTimeout))
	err := t.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		t.logger.Error("Failed to write close message", "err", err)
	} else {
		t.logger.Debug("Wrote close message to remote endpoint")
	}
}
