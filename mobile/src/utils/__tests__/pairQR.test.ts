import { parsePairingURL } from '../pairQR';

describe('parsePairingURL', () => {
  const VALID_URL =
    'niuniu-pair://v1?r=https%3A%2F%2Frelay.niuniu.dev&d=desktop-abc123&s=session-xyz&xk=AAABBBCCC%3D&sk=DDDEEEFFF%3D&n=NONCEB64%3D&t=1900000000';

  it('parses a fully valid pairing URL', () => {
    const result = parsePairingURL(VALID_URL);
    expect(result).not.toBeNull();
    expect(result!.relayHost).toBe('https://relay.niuniu.dev');
    expect(result!.desktopId).toBe('desktop-abc123');
    expect(result!.sessionId).toBe('session-xyz');
    expect(result!.desktopXpubB64).toBe('AAABBBCCC=');
    expect(result!.desktopSignPubB64).toBe('DDDEEEFFF=');
    expect(result!.nonceB64).toBe('NONCEB64=');
    expect(result!.expiresAt).toBe(1900000000);
  });

  it('returns null for a URL with wrong scheme', () => {
    expect(parsePairingURL('https://relay.niuniu.dev/pair?r=x&d=y&s=z')).toBeNull();
  });

  it('returns null for a URL missing required params', () => {
    // Missing sk
    const missingParam = 'niuniu-pair://v1?r=https%3A%2F%2Frelay.niuniu.dev&d=desktop-abc123&s=session-xyz&xk=AAABBBCCC%3D&n=NONCEB64%3D&t=1900000000';
    expect(parsePairingURL(missingParam)).toBeNull();
  });

  it('returns null for an empty string', () => {
    expect(parsePairingURL('')).toBeNull();
  });

  it('returns null for niuniu-pair://v1 with no query params', () => {
    expect(parsePairingURL('niuniu-pair://v1?')).toBeNull();
  });

  it('returns null when expiresAt is not a number', () => {
    const badT = 'niuniu-pair://v1?r=x&d=y&s=z&xk=a&sk=b&n=c&t=notanumber';
    expect(parsePairingURL(badT)).toBeNull();
  });

  it('returns a valid PairingQR with all required fields', () => {
    const result = parsePairingURL(VALID_URL);
    expect(result).toMatchObject({
      relayHost: expect.any(String),
      desktopId: expect.any(String),
      sessionId: expect.any(String),
      desktopXpubB64: expect.any(String),
      desktopSignPubB64: expect.any(String),
      nonceB64: expect.any(String),
      expiresAt: expect.any(Number),
    });
  });
});
