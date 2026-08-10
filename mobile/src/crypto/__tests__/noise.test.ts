import { newKKInitiator, newKKResponder } from '../noise';
import { generateIdentity } from '../identity';

describe('Noise KK — JS↔JS round-trip', () => {
  it('completes 2-message handshake and encrypts/decrypts', () => {
    const aliceId = generateIdentity();
    const bobId = generateIdentity();

    const initiator = newKKInitiator(aliceId, bobId.xPub);
    const responder = newKKResponder(bobId, aliceId.xPub);

    // msg 1: initiator → responder
    const msg1 = initiator.writeMessage(new TextEncoder().encode('hello'));

    // responder reads msg 1
    const payload1 = responder.readMessage(msg1);
    expect(new TextDecoder().decode(payload1)).toBe('hello');

    // msg 2: responder → initiator
    const msg2 = responder.writeMessage(new TextEncoder().encode('world'));

    // initiator reads msg 2
    const payload2 = initiator.readMessage(msg2);
    expect(new TextDecoder().decode(payload2)).toBe('world');

    // Both sides should now have cipher pairs
    const aliceCipher = initiator.cipher();
    const bobCipher = responder.cipher();
    expect(aliceCipher).not.toBeNull();
    expect(bobCipher).not.toBeNull();

    // Alice encrypts, Bob decrypts
    const plaintext = new TextEncoder().encode('secret message from Alice');
    const ct = aliceCipher!.encrypt(plaintext);
    const decrypted = bobCipher!.decrypt(ct);
    expect(new TextDecoder().decode(decrypted)).toBe('secret message from Alice');

    // Bob encrypts, Alice decrypts
    const replyPt = new TextEncoder().encode('reply from Bob');
    const replyCt = bobCipher!.encrypt(replyPt);
    const replyDecrypted = aliceCipher!.decrypt(replyCt);
    expect(new TextDecoder().decode(replyDecrypted)).toBe('reply from Bob');
  });

  it('cipher is null before handshake completes', () => {
    const aliceId = generateIdentity();
    const bobId = generateIdentity();

    const initiator = newKKInitiator(aliceId, bobId.xPub);
    expect(initiator.cipher()).toBeNull();
  });

  it('rekey does not break encryption', () => {
    const aliceId = generateIdentity();
    const bobId = generateIdentity();

    const initiator = newKKInitiator(aliceId, bobId.xPub);
    const responder = newKKResponder(bobId, aliceId.xPub);

    responder.readMessage(initiator.writeMessage());
    initiator.readMessage(responder.writeMessage());

    const aliceCipher = initiator.cipher()!;
    const bobCipher = responder.cipher()!;

    // Rekey both sides symmetrically
    aliceCipher.rekey();
    bobCipher.rekey();

    // Should still be able to encrypt/decrypt after rekey
    const pt = new TextEncoder().encode('after rekey');
    const ct = aliceCipher.encrypt(pt);
    const decrypted = bobCipher.decrypt(ct);
    expect(new TextDecoder().decode(decrypted)).toBe('after rekey');
  });
});
