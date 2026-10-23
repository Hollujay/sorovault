/**
 * Verifies SoroVault interface attestations.
 *
 * When a registry has a signing key, GET /api/contracts/{id} and
 * GET /api/wasm/{hash} carry two headers:
 *
 *   X-SoroVault-Attestation  base64 Ed25519 signature
 *   X-SoroVault-Key-Id     which key signed (see GET /api/keys)
 *
 * The signature covers the string
 *
 *   "sorovault-attestation-v1\n" + canonical({
 *     v: 1, network, contract_id, wasm_hash, interface
 *   })
 *
 * built from the response body, where `canonical` is JSON with object keys
 * sorted by UTF-16 code unit and no whitespace (RFC 8785 for the integer-only
 * documents SoroVault serves), and `contract_id` is left out for
 * /api/wasm/{hash} responses. Because it is built from the body the client
 * already has, the check needs no further request, and it holds through any
 * cache or mirror that does not change the body.
 *
 * Uses WebCrypto's Ed25519, available in Node 20+ and current browsers. The
 * file uses only erasable TypeScript syntax, so Node 22.18+ runs it directly.
 */

export const SIGNATURE_HEADER = "X-SoroVault-Attestation";
export const KEY_ID_HEADER = "X-SoroVault-Key-Id";
const DOMAIN = "sorovault-attestation-v1\n";

/** A key as published by GET /api/keys. */
export interface PublishedKey {
  id: string;
  algorithm: "ed25519";
  /** 32-byte public key, base64. */
  public_key: string;
  current: boolean;
}

export class AttestationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AttestationError";
  }
}

/** Canonical JSON of a parsed JSON value. Throws on a non-integer number. */
export function canonicalize(value: unknown): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "boolean":
      return value ? "true" : "false";
    case "string":
      // JSON.stringify escapes exactly the characters the canonical form
      // does, and writes everything else (including U+2028) as itself.
      return JSON.stringify(value);
    case "number":
      if (!Number.isSafeInteger(value)) {
        throw new AttestationError(`${value} is not a safe integer`);
      }
      return String(value);
    case "object": {
      if (Array.isArray(value)) {
        return "[" + value.map(canonicalize).join(",") + "]";
      }
      const obj = value as Record<string, unknown>;
      // Array#sort on strings compares UTF-16 code units, as required.
      const keys = Object.keys(obj).sort();
      return (
        "{" +
        keys.map((k) => JSON.stringify(k) + ":" + canonicalize(obj[k])).join(",") +
        "}"
      );
    }
    default:
      throw new AttestationError(`unsupported value of type ${typeof value}`);
  }
}

function fromBase64(encoded: string): Uint8Array {
  // Accept the URL-safe alphabet and missing padding too.
  const std = encoded.replace(/-/g, "+").replace(/_/g, "/");
  const padded = std + "=".repeat((4 - (std.length % 4)) % 4);
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** The exact bytes the registry signed for a response body. */
export function attestationMessage(body: string): Uint8Array {
  const doc = JSON.parse(body) as Record<string, unknown>;
  const statement: Record<string, unknown> = {
    v: 1,
    network: doc.network,
    wasm_hash: doc.wasm_hash,
    interface: doc.interface,
  };
  if (typeof doc.contract_id === "string" && doc.contract_id !== "") {
    statement.contract_id = doc.contract_id;
  }
  return new TextEncoder().encode(DOMAIN + canonicalize(statement));
}

/**
 * Checks a response body against a detached signature and a public key.
 * Resolves false for a signature that does not verify; throws
 * AttestationError for input that cannot be checked at all.
 */
export async function verifyBody(
  body: string,
  signature: string,
  publicKey: string | Uint8Array,
): Promise<boolean> {
  const raw = typeof publicKey === "string" ? fromBase64(publicKey) : publicKey;
  if (raw.length !== 32) {
    throw new AttestationError("public key must be 32 bytes");
  }
  let sig: Uint8Array;
  try {
    sig = fromBase64(signature);
  } catch {
    return false;
  }
  const key = await crypto.subtle.importKey("raw", raw as BufferSource, "Ed25519", false, ["verify"]);
  return crypto.subtle.verify("Ed25519", key, sig as BufferSource, attestationMessage(body) as BufferSource);
}

/**
 * Verifies a fetch Response from a signing registry against the keys it
 * publishes. Resolves with the parsed body; rejects with AttestationError if
 * the response is unsigned, names an unknown key, or does not verify.
 *
 * Fetch the keys once from GET /api/keys, over a channel you trust, and pin
 * them: verifying against keys taken from the same mirror proves nothing.
 */
export async function verifyResponse(response: Response, keys: PublishedKey[]): Promise<unknown> {
  const signature = response.headers.get(SIGNATURE_HEADER);
  const keyId = response.headers.get(KEY_ID_HEADER);
  if (!signature || !keyId) {
    throw new AttestationError("response is not signed");
  }
  const key = keys.find((k) => k.id === keyId);
  if (!key) {
    throw new AttestationError(`response was signed by unknown key ${keyId}`);
  }
  const body = await response.text();
  if (!(await verifyBody(body, signature, key.public_key))) {
    throw new AttestationError("signature does not verify");
  }
  return JSON.parse(body);
}
