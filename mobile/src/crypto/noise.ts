/**
 * Noise KK handshake state machine — initiator (mobile) and responder sides.
 *
 * Pattern: Noise_KK_25519_ChaChaPoly_SHA256
 *
 * Noise KK:
 *   -> s
 *   <- s
 *   ...
 *   -> e, es, ss   (msg 1 — initiator writes)
 *   <- e, ee, se   (msg 2 — responder writes / initiator reads)
 */

import { SHA256 } from '@stablelib/sha256';
import { ChaCha20Poly1305 } from '@stablelib/chacha20poly1305';
import { generateKeyPair, generateKeyPairFromSeed, sharedKey } from '@stablelib/x25519';
import type { Identity } from './identity';

/**
 * Test-only options. `ephemeralPriv` lets tests replay a handshake with a
 * deterministic ephemeral keypair (e.g. Go-generated fixtures); in production
 * callers must not set it so the ephemeral is drawn from crypto.getRandomValues.
 */
export interface KKOptions {
  ephemeralPriv?: Uint8Array;
}

// ─── Utilities ────────────────────────────────────────────────────────────────

function sha256(data: Uint8Array): Uint8Array {
  const h = new SHA256();
  h.update(data);
  return h.digest();
}

function concat(...arrays: Uint8Array[]): Uint8Array {
  const len = arrays.reduce((s, a) => s + a.length, 0);
  const out = new Uint8Array(len);
  let off = 0;
  for (const a of arrays) {
    out.set(a, off);
    off += a.length;
  }
  return out;
}

function hmacSHA256(key: Uint8Array, data: Uint8Array): Uint8Array {
  const BLOCK = 64;
  let k = key.length > BLOCK ? sha256(key) : key;
  if (k.length < BLOCK) {
    const padded = new Uint8Array(BLOCK);
    padded.set(k);
    k = padded;
  }
  const ipad = new Uint8Array(BLOCK);
  const opad = new Uint8Array(BLOCK);
  for (let i = 0; i < BLOCK; i++) {
    ipad[i] = k[i] ^ 0x36;
    opad[i] = k[i] ^ 0x5c;
  }
  return sha256(concat(opad, sha256(concat(ipad, data))));
}

/** HKDF extract+expand for 2 output blocks of 32 bytes each. */
function hkdf2(ck: Uint8Array, ikm: Uint8Array): [Uint8Array, Uint8Array] {
  const prk = hmacSHA256(ck, ikm);
  const t1 = hmacSHA256(prk, new Uint8Array([0x01]));
  const t2 = hmacSHA256(prk, concat(t1, new Uint8Array([0x02])));
  return [t1, t2];
}

/** ChaCha20Poly1305 nonce: counter as 8-byte LE in a 12-byte zero-prefixed nonce. */
function makeNonce(n: number): Uint8Array {
  const nonce = new Uint8Array(12);
  // counter occupies bytes 4-11 (little-endian) per Noise spec
  const view = new DataView(nonce.buffer);
  view.setUint32(4, n >>> 0, true);
  view.setUint32(8, Math.floor(n / 0x100000000) >>> 0, true);
  return nonce;
}

// ─── CipherState ─────────────────────────────────────────────────────────────

export interface CipherPair {
  encrypt(plaintext: Uint8Array): Uint8Array;
  decrypt(ciphertext: Uint8Array): Uint8Array;
  rekey(): void;
  /** Set the auto-rekey interval (number of encrypt/decrypt calls). 0 = disabled (default). */
  setRekeyInterval(n: number): void;
  /** Return the current rekey interval. */
  rekeyInterval(): number;
}

class CipherState {
  private k: Uint8Array;
  private n: number = 0;

  constructor(key: Uint8Array) {
    this.k = new Uint8Array(key);
  }

  encrypt(ad: Uint8Array, plaintext: Uint8Array): Uint8Array {
    const aead = new ChaCha20Poly1305(this.k);
    const nonce = makeNonce(this.n++);
    return aead.seal(nonce, plaintext, ad);
  }

  decrypt(ad: Uint8Array, ciphertext: Uint8Array): Uint8Array {
    const aead = new ChaCha20Poly1305(this.k);
    const nonce = makeNonce(this.n++);
    const pt = aead.open(nonce, ciphertext, ad);
    if (!pt) throw new Error('Noise: AEAD decryption failed');
    return pt;
  }

  rekey(): void {
    // k = ChaCha20Poly1305(k, nonce=0xffffffffffffffff, ad=[], pt=[0]*32)[:32]
    const maxNonce = new Uint8Array(12);
    const view = new DataView(maxNonce.buffer);
    view.setUint32(4, 0xffffffff, true);
    view.setUint32(8, 0xffffffff, true);
    const aead = new ChaCha20Poly1305(this.k);
    const ct = aead.seal(maxNonce, new Uint8Array(32), new Uint8Array(0));
    this.k = ct.slice(0, 32);
    this.n = 0;
  }
}

class CipherPairImpl implements CipherPair {
  private _rekeyEvery: number = 0;
  private _sendCount: number = 0;
  private _recvCount: number = 0;

  constructor(
    private send: CipherState,
    private recv: CipherState,
  ) {}

  setRekeyInterval(n: number): void {
    this._rekeyEvery = n;
  }

  rekeyInterval(): number {
    return this._rekeyEvery;
  }

  encrypt(plaintext: Uint8Array): Uint8Array {
    const ct = this.send.encrypt(new Uint8Array(0), plaintext);
    this._sendCount++;
    if (this._rekeyEvery > 0 && this._sendCount >= this._rekeyEvery) {
      this.send.rekey();
      this._sendCount = 0;
    }
    return ct;
  }

  decrypt(ciphertext: Uint8Array): Uint8Array {
    const pt = this.recv.decrypt(new Uint8Array(0), ciphertext);
    this._recvCount++;
    if (this._rekeyEvery > 0 && this._recvCount >= this._rekeyEvery) {
      this.recv.rekey();
      this._recvCount = 0;
    }
    return pt;
  }

  rekey(): void {
    this.send.rekey();
    this.recv.rekey();
    this._sendCount = 0;
    this._recvCount = 0;
  }
}

// ─── SymmetricState ───────────────────────────────────────────────────────────

const PROTOCOL_NAME = 'Noise_KK_25519_ChaChaPoly_SHA256';
const PROTOCOL_NAME_BYTES = new TextEncoder().encode(PROTOCOL_NAME);

class SymmetricState {
  private ck: Uint8Array;
  h: Uint8Array;
  private cipher: CipherState | null = null;
  private hasKey = false;
  private n = 0;

  constructor() {
    // InitializeSymmetric: if len(protocol_name) <= 32, pad to 32; else SHA256
    if (PROTOCOL_NAME_BYTES.length <= 32) {
      const padded = new Uint8Array(32);
      padded.set(PROTOCOL_NAME_BYTES);
      this.h = padded;
    } else {
      this.h = sha256(PROTOCOL_NAME_BYTES);
    }
    this.ck = new Uint8Array(this.h);
  }

  mixHash(data: Uint8Array): void {
    this.h = sha256(concat(this.h, data));
  }

  mixKey(ikm: Uint8Array): void {
    const [newCk, temp] = hkdf2(this.ck, ikm);
    this.ck = newCk;
    this.cipher = new CipherState(temp.slice(0, 32));
    this.hasKey = true;
    this.n = 0;
  }

  encryptAndHash(plaintext: Uint8Array): Uint8Array {
    if (!this.hasKey || !this.cipher) {
      this.mixHash(plaintext);
      return plaintext;
    }
    const ct = this.cipher.encrypt(this.h, plaintext);
    this.mixHash(ct);
    return ct;
  }

  decryptAndHash(ciphertext: Uint8Array): Uint8Array {
    if (!this.hasKey || !this.cipher) {
      this.mixHash(ciphertext);
      return ciphertext;
    }
    const pt = this.cipher.decrypt(this.h, ciphertext);
    this.mixHash(ciphertext);
    return pt;
  }

  split(): [CipherState, CipherState] {
    const [t1, t2] = hkdf2(this.ck, new Uint8Array(0));
    return [new CipherState(t1), new CipherState(t2)];
  }
}

// ─── HandshakeState ───────────────────────────────────────────────────────────

class HandshakeState {
  private ss: SymmetricState;
  private ePriv: Uint8Array | null = null;
  private ePub: Uint8Array | null = null;
  private pinnedEphemeralPriv: Uint8Array | null = null;

  constructor(
    private initiator: boolean,
    private sPriv: Uint8Array,
    private sPub: Uint8Array,
    private rsPub: Uint8Array, // remote static pubkey
    pinnedEphemeralPriv?: Uint8Array,
  ) {
    this.pinnedEphemeralPriv = pinnedEphemeralPriv ?? null;
    this.ss = new SymmetricState();
    // Noise spec §5.3: after InitializeSymmetric, MixHash(prologue) MUST be
    // called before processing pre-message tokens — even when the prologue is
    // empty, because MixHash(∅) still advances h to SHA256(h). Skipping it
    // causes h to diverge from any peer that follows the spec (e.g. flynn/noise
    // on the Go side), and since encryptAndHash feeds h as AEAD associated
    // data, the responder's decryptAndHash fails Poly1305 verification with
    // "chacha20poly1305: message authentication failed".
    this.ss.mixHash(new Uint8Array(0));

    // KK pre-messages: -> s, <- s
    // Both sides MixHash in the same order: initiator_static, responder_static.
    if (initiator) {
      this.ss.mixHash(sPub);   // initiator's own static
      this.ss.mixHash(rsPub);  // responder's static (peer)
    } else {
      this.ss.mixHash(rsPub);  // initiator's static (peer)
      this.ss.mixHash(sPub);   // responder's own static
    }
  }

  /**
   * msg 1 (initiator): -> e, es, ss
   */
  writeMessage(payload: Uint8Array = new Uint8Array(0)): Uint8Array {
    if (!this.initiator) throw new Error('Noise: writeMessage called on responder for msg1');

    // Generate ephemeral keypair (or reuse a test-pinned one).
    const ekp = this.pinnedEphemeralPriv
      ? generateKeyPairFromSeed(this.pinnedEphemeralPriv)
      : generateKeyPair();
    this.ePriv = ekp.secretKey;
    this.ePub = ekp.publicKey;

    // e: send ephemeral pub, mix hash
    this.ss.mixHash(this.ePub);

    // es: DH(e_priv, rs_pub)
    const es = sharedKey(this.ePriv, this.rsPub);
    this.ss.mixKey(es);

    // ss: DH(s_priv, rs_pub)
    const ss = sharedKey(this.sPriv, this.rsPub);
    this.ss.mixKey(ss);

    // Encrypt payload
    const ct = this.ss.encryptAndHash(payload);

    return concat(this.ePub, ct);
  }

  /**
   * msg 1 (responder): <- e, es, ss
   */
  readMessage1(msg: Uint8Array): Uint8Array {
    if (this.initiator) throw new Error('Noise: readMessage1 called on initiator');

    // Read ephemeral (32 bytes)
    const re = msg.slice(0, 32);
    this.ss.mixHash(re);

    // es: DH(s_priv, re)  — for responder: s_priv is responder's static
    const es = sharedKey(this.sPriv, re);
    this.ss.mixKey(es);

    // ss: DH(s_priv, rs_pub)  — for responder: rs_pub is initiator's static
    const ss = sharedKey(this.sPriv, this.rsPub);
    this.ss.mixKey(ss);

    // Decrypt payload
    const payload = this.ss.decryptAndHash(msg.slice(32));
    return payload;
  }

  /**
   * msg 2 (responder): -> e, ee, se
   */
  writeMessage2(payload: Uint8Array = new Uint8Array(0)): Uint8Array {
    if (this.initiator) throw new Error('Noise: writeMessage2 called on initiator');

    // Generate ephemeral (or reuse a test-pinned one).
    const ekp = this.pinnedEphemeralPriv
      ? generateKeyPairFromSeed(this.pinnedEphemeralPriv)
      : generateKeyPair();
    this.ePriv = ekp.secretKey;
    this.ePub = ekp.publicKey;

    this.ss.mixHash(this.ePub);

    // We need the remote ephemeral from msg1 — stored by readMessage1
    if (!this.remoteEphemeral) throw new Error('Noise: no remote ephemeral stored');

    // ee: DH(initiator.e, responder.e). Responder side = DH(r.e_priv, i.e_pub).
    const ee = sharedKey(this.ePriv, this.remoteEphemeral);
    this.ss.mixKey(ee);

    // se: DH(initiator.s, responder.e) per Noise spec §7.3 (flynn/noise
    // MessagePatternDHSE). Responder side = DH(r.e_priv, i.s_pub) — NOT
    // DH(r.s_priv, i.e_pub), which is the `es` token. Getting these mirrored
    // lets JS↔JS handshakes complete (both sides wrong in the same direction)
    // but breaks interop with any spec-compliant peer — the AEAD MAC on msg2
    // will fail because the derived key diverges from Go's.
    const se = sharedKey(this.ePriv, this.rsPub);
    this.ss.mixKey(se);

    const ct = this.ss.encryptAndHash(payload);
    return concat(this.ePub, ct);
  }

  private remoteEphemeral: Uint8Array | null = null;

  /**
   * msg 2 (initiator): reads e, ee, se from responder.
   *
   * Noise spec §7.3 token conventions (first letter = initiator's key, second
   * letter = responder's key):
   *   ee = DH(initiator.e, responder.e)
   *   se = DH(initiator.s, responder.e)
   * On the initiator side we have our own s/e privs and the peer's e pub.
   */
  readMessage2(msg: Uint8Array): Uint8Array {
    if (!this.initiator) throw new Error('Noise: readMessage2 called on responder');
    if (!this.ePriv) throw new Error('Noise: no ephemeral key — writeMessage not called yet');

    // Read remote ephemeral
    const re = msg.slice(0, 32);
    this.ss.mixHash(re);

    // ee: DH(initiator.e, responder.e) = DH(i.e_priv, r.e_pub)
    const ee = sharedKey(this.ePriv, re);
    this.ss.mixKey(ee);

    // se: DH(initiator.s, responder.e) = DH(i.s_priv, r.e_pub). Initiator uses
    // its own static priv against the just-read responder ephemeral pub — this
    // mirrors the responder's DH(r.e_priv, i.s_pub).
    const se = sharedKey(this.sPriv, re);
    this.ss.mixKey(se);

    return this.ss.decryptAndHash(msg.slice(32));
  }

  split(): [CipherState, CipherState] {
    return this.ss.split();
  }

  // Expose so responder can store re from msg1
  storeRemoteEphemeral(re: Uint8Array): void {
    this.remoteEphemeral = re;
  }
}

// ─── Public KKState ───────────────────────────────────────────────────────────

export interface KKState {
  writeMessage(payload?: Uint8Array): Uint8Array;
  readMessage(msg: Uint8Array): Uint8Array;
  cipher(): CipherPair | null;
}

class KKInitiator implements KKState {
  private hs: HandshakeState;
  private _cipher: CipherPair | null = null;

  constructor(self: Identity, peerStaticPub: Uint8Array, opts?: KKOptions) {
    this.hs = new HandshakeState(true, self.xPriv, self.xPub, peerStaticPub, opts?.ephemeralPriv);
  }

  writeMessage(payload: Uint8Array = new Uint8Array(0)): Uint8Array {
    return this.hs.writeMessage(payload);
  }

  readMessage(msg: Uint8Array): Uint8Array {
    const payload = this.hs.readMessage2(msg);
    const [send, recv] = this.hs.split();
    this._cipher = new CipherPairImpl(send, recv);
    return payload;
  }

  cipher(): CipherPair | null {
    return this._cipher;
  }
}

class KKResponder implements KKState {
  private hs: HandshakeState;
  private _cipher: CipherPair | null = null;
  private step = 0;

  constructor(self: Identity, peerStaticPub: Uint8Array, opts?: KKOptions) {
    this.hs = new HandshakeState(false, self.xPriv, self.xPub, peerStaticPub, opts?.ephemeralPriv);
  }

  readMessage(msg: Uint8Array): Uint8Array {
    if (this.step === 0) {
      // Store remote ephemeral for msg1
      this.hs.storeRemoteEphemeral(msg.slice(0, 32));
      const payload = this.hs.readMessage1(msg);
      this.step = 1;
      return payload;
    }
    throw new Error('Noise responder: unexpected readMessage after msg1');
  }

  writeMessage(payload: Uint8Array = new Uint8Array(0)): Uint8Array {
    if (this.step !== 1) throw new Error('Noise responder: writeMessage2 before readMessage1');
    const out = this.hs.writeMessage2(payload);
    const [t1, t2] = this.hs.split();
    // Noise spec: initiator uses (t1=send, t2=recv); responder uses (t1=recv, t2=send)
    this._cipher = new CipherPairImpl(t2, t1);
    this.step = 2;
    return out;
  }

  cipher(): CipherPair | null {
    return this._cipher;
  }
}

export function newKKInitiator(
  self: Identity,
  peerStaticPub: Uint8Array,
  opts?: KKOptions,
): KKState {
  return new KKInitiator(self, peerStaticPub, opts);
}

export function newKKResponder(
  self: Identity,
  peerStaticPub: Uint8Array,
  opts?: KKOptions,
): KKState {
  return new KKResponder(self, peerStaticPub, opts);
}
