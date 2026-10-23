// Package attest signs and verifies SoroVault's interface attestations and
// publisher claims.
//
// Both are Ed25519 signatures over a canonical encoding of a JSON value, so
// that a client in any language can rebuild exactly the bytes that were
// signed from the JSON it received.
//
// # Canonical encoding
//
// The encoding is the subset of RFC 8785 (JSON Canonicalization Scheme) that
// SoroVault's documents use:
//
//   - no whitespace between tokens;
//   - object members sorted by key, comparing the keys' UTF-16 code units
//     (which is how JavaScript sorts strings);
//   - strings escape only `"`, `\`, and control characters below U+0020,
//     using \b \f \n \r \t where they exist and \u00xx (lower-case hex)
//     otherwise; every other character, including U+2028 and "<", is
//     written as itself in UTF-8;
//   - numbers are integers, written as they appear in the source JSON.
//
// SoroVault's documents contain no floating-point numbers, so the
// number-formatting rules of RFC 8785 never come into play; Canonicalize
// rejects a number that is not a plain integer instead of guessing.
package attest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize re-encodes a JSON document in the canonical form described
// in the package comment.
func Canonicalize(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("attest: decoding json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("attest: trailing data after json value")
	}

	var out bytes.Buffer
	if err := writeCanonical(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalizeValue marshals v with encoding/json and canonicalizes the
// result.
func CanonicalizeValue(v any) ([]byte, error) {
	doc, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("attest: encoding value: %w", err)
	}
	return Canonicalize(doc)
}

func writeCanonical(out *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		if err := checkInteger(v.String()); err != nil {
			return err
		}
		out.WriteString(v.String())
	case string:
		writeString(out, v)
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })

		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, k)
			out.WriteByte(':')
			if err := writeCanonical(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("attest: unsupported json type %T", v)
	}
	return nil
}

// checkInteger accepts an optionally negative run of digits with no leading
// zero, the only number form SoroVault's documents use.
func checkInteger(s string) error {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || (len(digits) > 1 && digits[0] == '0') || (s == "-0") {
		return fmt.Errorf("attest: number %q is not a canonical integer", s)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return fmt.Errorf("attest: number %q is not a canonical integer", s)
		}
	}
	return nil
}

const hexDigits = "0123456789abcdef"

func writeString(out *bytes.Buffer, s string) {
	// Invalid UTF-8 cannot appear in JSON received from a decoder, which
	// already replaced it; replace here too so a hand-built value cannot
	// produce bytes no other implementation could reproduce.
	s = strings.ToValidUTF8(s, "�")

	out.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\f':
			out.WriteString(`\f`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case r < 0x20:
			out.WriteString(`\u00`)
			out.WriteByte(hexDigits[r>>4])
			out.WriteByte(hexDigits[r&0xf])
		default:
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], r)
			out.Write(buf[:n])
		}
	}
	out.WriteByte('"')
}

// lessUTF16 orders strings by UTF-16 code units, as RFC 8785 requires and
// as JavaScript's default sort does. It differs from byte order only for
// characters outside the Basic Multilingual Plane.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
