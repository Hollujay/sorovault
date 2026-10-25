package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrMalformedCursor reports a cursor ListContracts cannot read. The API
// maps it to 400; anything else a cursor breaks would be a 500 for a
// client-typed string, which is the wrong shape of failure.
var ErrMalformedCursor = errors.New("store: malformed cursor")

// listCursor is the decoded form of the opaque cursor ListContracts pages
// with: the sort key of the last row of the previous page.
//
// The listing's order is last_refreshed descending, then contract ID and
// network ascending — the network breaks the tie the primary key allows,
// where one contract ID runs on two networks and both refresh at the same
// instant. A cursor that carried only the timestamp and ID could not say
// which of those two rows it saw, so the next page might repeat one of
// them; carrying all three makes (time, id, network) a key every row sits
// uniquely at, so "somewhere after the cursor" has exactly one meaning.
type listCursor struct {
	LastRefreshed string `json:"l"`
	ContractID    string `json:"id"`
	Network       string `json:"n"`
}

// encodeListCursor writes a row's sort key as an opaque cursor: JSON over
// base64url without padding, so the value is safe in a URL's query string
// and,being opaque, can gain fields later without breaking clients.
func encodeListCursor(c Contract) (string, error) {
	raw, err := json.Marshal(listCursor{
		LastRefreshed: c.LastRefreshed.UTC().Format(time.RFC3339Nano),
		ContractID:    c.ContractID,
		Network:       c.Network,
	})
	if err != nil {
		return "", fmt.Errorf("store: encoding cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeListCursor reads a cursor produced by encodeListCursor.
func decodeListCursor(cursor string) (listCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return listCursor{}, fmt.Errorf("%w: not base64url: %s", ErrMalformedCursor, cursor)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	var lc listCursor
	if err := dec.Decode(&lc); err != nil {
		return listCursor{}, fmt.Errorf("%w: not the expected document: %s", ErrMalformedCursor, cursor)
	}
	if lc.ContractID == "" || lc.Network == "" {
		return listCursor{}, fmt.Errorf("%w: missing the contract ID or network", ErrMalformedCursor)
	}
	if _, err := time.Parse(time.RFC3339Nano, lc.LastRefreshed); err != nil {
		return listCursor{}, fmt.Errorf("%w: bad timestamp %q", ErrMalformedCursor, lc.LastRefreshed)
	}
	return lc, nil
}

// afterListCursor reports whether a row sorts strictly after a cursor, in
// the listing's order: last_refreshed descending, then contract ID and
// network ascending. It is the same predicate the SQL expresses, so the
// two stores page identically.
func afterListCursor(c Contract, cur listCursor) bool {
	ref, _ := time.Parse(time.RFC3339Nano, cur.LastRefreshed)
	if !c.LastRefreshed.Equal(ref) {
		return c.LastRefreshed.Before(ref)
	}
	if c.ContractID != cur.ContractID {
		return c.ContractID > cur.ContractID
	}
	return c.Network > cur.Network
}
