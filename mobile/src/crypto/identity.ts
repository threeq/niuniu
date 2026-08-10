import * as SecureStore from 'expo-secure-store';
import {
  generateKeyPair as x25519KeyPair,
  generateKeyPairFromSeed as x25519FromSeed,
} from '@stablelib/x25519';
import {
  generateKeyPair as ed25519KeyPair,
  extractPublicKeyFromSecretKey as ed25519ExtractPub,
} from '@stablelib/ed25519';
import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';

export interface Identity {
  xPriv: Uint8Array;
  xPub: Uint8Array;
  edPriv: Uint8Array;
  edPub: Uint8Array;
}

// SecureStore on Android requires keys to match ^[\w.-]+$ — no colons.
const KEY_X_PRIV = 'niuniu.x-priv';
const KEY_X_PUB = 'niuniu.x-pub';
const KEY_ED_PRIV = 'niuniu.ed-priv';
const KEY_ED_PUB = 'niuniu.ed-pub';

export function generateIdentity(): Identity {
  const xKP = x25519KeyPair();
  const edKP = ed25519KeyPair();
  return {
    xPriv: xKP.secretKey,
    xPub: xKP.publicKey,
    edPriv: edKP.secretKey,
    edPub: edKP.publicKey,
  };
}

export async function saveIdentity(id: Identity): Promise<void> {
  await Promise.all([
    SecureStore.setItemAsync(KEY_X_PRIV, b64Encode(id.xPriv)),
    SecureStore.setItemAsync(KEY_X_PUB, b64Encode(id.xPub)),
    SecureStore.setItemAsync(KEY_ED_PRIV, b64Encode(id.edPriv)),
    SecureStore.setItemAsync(KEY_ED_PUB, b64Encode(id.edPub)),
  ]);
}

function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

/**
 * Returns the stored identity if present, with public keys derived fresh from
 * the stored private keys. The four-file storage layout is NOT transactional —
 * an interrupted save (or a partial legacy migration) can leave xPub stored
 * for one generation and xPriv for another, which then fails Noise KK handshake
 * with "chacha20poly1305: message authentication failed" because the mobile
 * signs msg1 with xPriv_A while the relay tells the desktop pub is xPub_B.
 *
 * Deriving pubs from privs makes the identity self-consistent regardless of
 * stored-pub drift. If the stored pubs disagree with the derived pubs, they
 * are overwritten so future pair flows send the correct pub.
 */
export async function getIdentity(): Promise<Identity | null> {
  const [xPrivB64, xPubB64, edPrivB64, edPubB64] = await Promise.all([
    SecureStore.getItemAsync(KEY_X_PRIV),
    SecureStore.getItemAsync(KEY_X_PUB),
    SecureStore.getItemAsync(KEY_ED_PRIV),
    SecureStore.getItemAsync(KEY_ED_PUB),
  ]);

  if (!xPrivB64 || !edPrivB64) return null;

  const xPriv = b64Decode(xPrivB64);
  const edPriv = b64Decode(edPrivB64);

  const xPub = x25519FromSeed(xPriv).publicKey;
  const edPub = ed25519ExtractPub(edPriv);

  const storedXPub = xPubB64 ? b64Decode(xPubB64) : null;
  const storedEdPub = edPubB64 ? b64Decode(edPubB64) : null;

  const xPubDrift = storedXPub && !constantTimeEqual(storedXPub, xPub);
  const edPubDrift = storedEdPub && !constantTimeEqual(storedEdPub, edPub);
  if (xPubDrift || edPubDrift || !storedXPub || !storedEdPub) {
    // Fire-and-forget: don't block the caller, but heal the stored pubs so the
    // next pair flow sends the values that actually correspond to the privs.
    void Promise.all([
      SecureStore.setItemAsync(KEY_X_PUB, b64Encode(xPub)),
      SecureStore.setItemAsync(KEY_ED_PUB, b64Encode(edPub)),
    ]).catch(() => {});
  }

  return { xPriv, xPub, edPriv, edPub };
}

/**
 * Returns stored identity; generates and persists one if none exists.
 */
export async function getOrCreateIdentity(): Promise<Identity> {
  const existing = await getIdentity();
  if (existing) return existing;
  const id = generateIdentity();
  await saveIdentity(id);
  return id;
}
