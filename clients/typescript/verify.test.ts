// Run with: node --test clients/typescript/
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { AttestationError, KEY_ID_HEADER, SIGNATURE_HEADER, canonicalize, verifyBody, verifyResponse } from "./verify.ts";

const vectors = JSON.parse(
  readFileSync(new URL("../../internal/attest/testdata/vectors.json", import.meta.url), "utf8"),
);

for (const c of vectors.cases) {
  test(`vector: ${c.name}`, async () => {
    const body = JSON.stringify(c.body);
    assert.equal(await verifyBody(body, c.signature, vectors.public_key), c.valid);
  });
}

test("canonical form matches the Go implementation", () => {
  const c = vectors.cases[0];
  const statement = {
    v: 1,
    network: c.body.network,
    wasm_hash: c.body.wasm_hash,
    interface: c.body.interface,
    contract_id: c.body.contract_id,
  };
  assert.equal(canonicalize(statement), c.canonical);
});

test("canonicalize sorts keys by UTF-16 code unit and rejects fractions", () => {
  assert.equal(canonicalize({ "\u{1F600}": 1, "ﬁ": 2 }), '{"\u{1F600}":1,"ﬁ":2}');
  assert.equal(canonicalize({ b: [true, null], a: "<&> " }), '{"a":"<&> ","b":[true,null]}');
  assert.throws(() => canonicalize({ n: 1.5 }), AttestationError);
});

test("a wrong key does not verify", async () => {
  const c = vectors.cases[0];
  const other = Buffer.alloc(32, 7).toString("base64");
  assert.equal(await verifyBody(JSON.stringify(c.body), c.signature, other), false);
});

test("verifyResponse checks headers, key id and signature", async () => {
  const c = vectors.cases[0];
  const keys = [{ id: vectors.key_id, algorithm: "ed25519", public_key: vectors.public_key, current: true }];
  const body = JSON.stringify(c.body);
  const respond = (headers: Record<string, string>, text = body) => new Response(text, { headers });

  const ok = await verifyResponse(respond({ [SIGNATURE_HEADER]: c.signature, [KEY_ID_HEADER]: vectors.key_id }), keys as never);
  assert.equal((ok as { network: string }).network, "testnet");

  await assert.rejects(verifyResponse(respond({}), keys as never), /not signed/);
  await assert.rejects(
    verifyResponse(respond({ [SIGNATURE_HEADER]: c.signature, [KEY_ID_HEADER]: "ffff" }), keys as never),
    /unknown key/,
  );
  await assert.rejects(
    verifyResponse(
      respond({ [SIGNATURE_HEADER]: c.signature, [KEY_ID_HEADER]: vectors.key_id }, body.replace("testnet", "public")),
      keys as never,
    ),
    /does not verify/,
  );
});
