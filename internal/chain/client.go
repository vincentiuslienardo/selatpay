// Package chain is Selatpay's Solana integration surface: RPC and WebSocket
// client handling, SPL transfer parsing, and the watcher that converts
// on-chain activity into persisted onchain_payments rows.
package chain

import (
	"context"
	"fmt"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/ws"
)

// Client bundles the Solana RPC and WebSocket endpoints used by the watcher.
// RPC is constructed once and shared because rpc.Client is safe for
// concurrent use; WS is intentionally short-lived — each subscription loop
// dials its own connection, so reconnect logic stays visible in the caller
// rather than hidden inside a wrapper's retry path.
type Client struct {
	RPC   *rpc.Client
	wsURL string
}

// NewClient wires the RPC client against rpcURL and stores wsURL for later
// DialWS calls. It does not dial the websocket — construction must stay
// non-blocking so subcommands that never subscribe (api, recon) pay no
// handshake cost.
func NewClient(rpcURL, wsURL string) *Client {
	return &Client{
		RPC:   rpc.New(rpcURL),
		wsURL: wsURL,
	}
}

// DialWS opens a fresh websocket client. Callers own the returned client's
// lifecycle: the watcher's subscription loop dials a new client on every
// reconnection attempt so a stale connection can never linger, and every
// subscription closes when its parent client closes.
func (c *Client) DialWS(ctx context.Context) (*ws.Client, error) {
	wc, err := ws.Connect(ctx, c.wsURL)
	if err != nil {
		return nil, fmt.Errorf("chain: ws connect %s: %w", c.wsURL, err)
	}
	return wc, nil
}
