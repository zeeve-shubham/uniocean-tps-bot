package networkconfig

import (
	"net/url"
	"os"
	"strings"
)

const (
	defaultBaseURL           = "http://134.119.179.234"
	defaultRESTEndpoint      = "http://134.119.179.234:1317"
	defaultRPCEndpoint       = "http://92.205.119.214:26657"
	defaultWebSocketEndpoint = "ws://134.119.179.234:26657/websocket"
	defaultChainID           = "uniocean_684-1"
)

type Config struct {
	BaseURL           string
	RESTEndpoint      string
	RPCEndpoint       string
	GRPCEndpoint      string
	WebSocketEndpoint string
	ChainID           string
}

func Load() Config {
	baseURL, baseURLSet := getOptionalEnv("UNIOCEAN_BASE_URL")
	if !baseURLSet {
		baseURL = defaultBaseURL
	}
	baseURL = normalizeURL(baseURL)

	restDefault := defaultRESTEndpoint
	rpcDefault := defaultRPCEndpoint
	webSocketDefault := defaultWebSocketEndpoint
	if baseURLSet {
		restDefault = joinURL(baseURL, "/api")
		rpcDefault = joinURL(baseURL, "/cosmos")
		webSocketDefault = websocketEndpoint(baseURL)
	}

	return Config{
		BaseURL:           baseURL,
		RESTEndpoint:      normalizeURL(getEnv("UNIOCEAN_REST_ENDPOINT", restDefault)),
		RPCEndpoint:       normalizeURL(getEnv("UNIOCEAN_RPC_ENDPOINT", rpcDefault)),
		GRPCEndpoint:      getEnv("UNIOCEAN_GRPC_ENDPOINT", "134.119.179.234:9090"),
		WebSocketEndpoint: normalizeURL(getEnv("UNIOCEAN_WS_ENDPOINT", webSocketDefault)),
		ChainID:           getEnv("UNIOCEAN_CHAIN_ID", defaultChainID),
	}
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getOptionalEnv(key string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", false
	}
	return value, true
}

func normalizeURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func joinURL(baseURL, suffix string) string {
	return normalizeURL(baseURL) + suffix
}

func websocketEndpoint(baseURL string) string {
	parsedURL, err := url.Parse(normalizeURL(baseURL))
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return joinURL(baseURL, "/websocket")
	}

	switch parsedURL.Scheme {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	}

	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/") + "/websocket"
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""

	return normalizeURL(parsedURL.String())
}
