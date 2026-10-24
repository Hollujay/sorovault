package stellar

import (
	"context"
	"encoding/hex"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

var _ UpdateFollower = (*RPCFetcher)(nil)

// executableUpdateTopic is the first topic of the system event the host
// emits when a contract replaces its own executable
// (update_current_contract_wasm). Its topics are
//
//	["executable_update", <old executable>, <new executable>]
//
// where each executable is the vec ["Wasm", <32-byte hash>] for uploaded
// code, or ["StellarAsset"] for the built-in asset contract. The event's
// contract is the one that was upgraded.
const executableUpdateTopic = "executable_update"

// LatestLedger returns the newest ledger the RPC has closed.
func (f *RPCFetcher) LatestLedger(ctx context.Context) (uint32, error) {
	resp, err := f.client.GetLatestLedger(ctx)
	if err != nil {
		return 0, fmt.Errorf("stellar: getLatestLedger: %w", err)
	}
	return resp.Sequence, nil
}

// ExecutableUpdates reads the network's executable_update system events.
//
// Events are filtered by type on the server and by topic here: the topic
// filter language matches XDR segments, and matching one in code keeps this
// working if the host appends topics later.
func (f *RPCFetcher) ExecutableUpdates(ctx context.Context, from UpdatePosition, limit int) (*UpdatePage, error) {
	if limit <= 0 {
		limit = 200
	}

	req := protocol.GetEventsRequest{
		Filters: []protocol.EventFilter{{EventType: protocol.EventTypeSet{protocol.EventTypeSystem: nil}}},
	}
	pagination := &protocol.PaginationOptions{Limit: uint(limit)}
	if from.Cursor != "" {
		cursor, err := protocol.ParseCursor(from.Cursor)
		if err != nil {
			return nil, fmt.Errorf("%w: unreadable cursor %q", ErrPositionExpired, from.Cursor)
		}
		pagination.Cursor = &cursor
	} else {
		req.StartLedger = from.StartLedger
	}
	req.Pagination = pagination

	resp, err := f.client.GetEvents(ctx, req)
	if err != nil {
		if isOutOfRange(err) {
			return nil, fmt.Errorf("%w: %w", ErrPositionExpired, err)
		}
		return nil, fmt.Errorf("stellar: getEvents: %w", err)
	}

	page := &UpdatePage{
		Cursor:       resp.Cursor,
		LatestLedger: resp.LatestLedger,
		More:         len(resp.Events) >= limit,
	}
	for _, event := range resp.Events {
		update, ok, err := parseExecutableUpdate(event)
		if err != nil {
			return nil, err
		}
		if ok {
			page.Updates = append(page.Updates, update)
		}
	}
	return page, nil
}

// parseExecutableUpdate reads one system event. ok is false for a system
// event that is not an executable update, or whose new executable is not
// uploaded code (a Stellar asset contract has no WASM to refresh).
func parseExecutableUpdate(event protocol.EventInfo) (update ExecutableUpdate, ok bool, err error) {
	if event.EventType != protocol.EventTypeSystem || len(event.TopicXDR) < 3 || event.ContractID == "" {
		return update, false, nil
	}

	var name xdr.ScVal
	if err := unmarshalBase64(event.TopicXDR[0], &name); err != nil {
		return update, false, fmt.Errorf("stellar: decoding event topic: %w", err)
	}
	if sym, isSym := name.GetSym(); !isSym || string(sym) != executableUpdateTopic {
		return update, false, nil
	}

	oldHash, _, err := topicWasmHash(event.TopicXDR[1])
	if err != nil {
		return update, false, err
	}
	newHash, isWasm, err := topicWasmHash(event.TopicXDR[2])
	if err != nil {
		return update, false, err
	}
	if !isWasm {
		return update, false, nil
	}
	if event.Ledger < 0 {
		return update, false, fmt.Errorf("upgrade event for %s has negative ledger %d", event.ContractID, event.Ledger)
	}

	return ExecutableUpdate{
		ContractID:  event.ContractID,
		OldWasmHash: oldHash,
		NewWasmHash: newHash,
		Ledger:      uint32(event.Ledger),
	}, true, nil
}

// topicWasmHash extracts the hash from an executable topic, ["Wasm", hash].
func topicWasmHash(topic string) (hash string, isWasm bool, err error) {
	var val xdr.ScVal
	if err := unmarshalBase64(topic, &val); err != nil {
		return "", false, fmt.Errorf("stellar: decoding event topic: %w", err)
	}
	vec, ok := val.GetVec()
	if !ok || vec == nil || len(*vec) != 2 {
		return "", false, nil
	}
	tag, ok := (*vec)[0].GetSym()
	if !ok || string(tag) != "Wasm" {
		return "", false, nil
	}
	raw, ok := (*vec)[1].GetBytes()
	if !ok {
		return "", false, nil
	}
	return hex.EncodeToString(raw), true, nil
}
