import { deriveSAS } from '../sas';
import * as fs from 'fs';
import * as path from 'path';

interface SASVector {
  nonce: string;
  desktop_x: string;
  mobile_x: string;
  desktop_sign: string;
  mobile_sign: string;
  expected: string;
}

function hexToBytes(hex: string): Uint8Array {
  const h = hex.replace(/^0x/, '');
  const bytes = new Uint8Array(h.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

describe('deriveSAS — Go interop vectors', () => {
  const fixturesPath = path.resolve(
    __dirname,
    '../../../../go-shared/pairingcrypto/testdata/sas_vectors.json',
  );

  let vectors: SASVector[];

  beforeAll(() => {
    const raw = fs.readFileSync(fixturesPath, 'utf8');
    vectors = JSON.parse(raw);
  });

  it('loads at least 5 vectors', () => {
    expect(vectors.length).toBeGreaterThanOrEqual(5);
  });

  it.each(
    // Use index as display name since vectors are loaded in beforeAll
    [0, 1, 2, 3, 4]
  )('vector[%i] matches expected Go output', (idx) => {
    const v = vectors[idx];
    const result = deriveSAS(
      hexToBytes(v.nonce),
      hexToBytes(v.desktop_x),
      hexToBytes(v.mobile_x),
      hexToBytes(v.desktop_sign),
      hexToBytes(v.mobile_sign),
    );
    expect(result).toBe(v.expected);
  });
});
