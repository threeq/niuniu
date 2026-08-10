import { useTrustedRelaysStore } from '../trustedRelaysStore';

// The store uses SecureStore which is already mocked via jest.config.js -> __mocks__/expo-secure-store.js

describe('trustedRelaysStore', () => {
  beforeEach(() => {
    useTrustedRelaysStore.setState({ origins: ['relay.niuniu.dev'] });
  });

  it('defaults to relay.niuniu.dev', () => {
    expect(useTrustedRelaysStore.getState().origins).toContain('relay.niuniu.dev');
  });

  it('isTrusted returns true for default origin', () => {
    expect(useTrustedRelaysStore.getState().isTrusted('relay.niuniu.dev')).toBe(true);
  });

  it('isTrusted returns false for unknown origin', () => {
    expect(useTrustedRelaysStore.getState().isTrusted('evil.example.com')).toBe(false);
  });

  it('addOrigin adds a new host', () => {
    useTrustedRelaysStore.getState().addOrigin('my-relay.example.com');
    expect(useTrustedRelaysStore.getState().isTrusted('my-relay.example.com')).toBe(true);
  });

  it('addOrigin normalises to lowercase', () => {
    useTrustedRelaysStore.getState().addOrigin('MyRelay.Example.Com');
    expect(useTrustedRelaysStore.getState().isTrusted('myrelay.example.com')).toBe(true);
  });

  it('addOrigin does not duplicate', () => {
    useTrustedRelaysStore.getState().addOrigin('relay.niuniu.dev');
    const { origins } = useTrustedRelaysStore.getState();
    expect(origins.filter((o) => o === 'relay.niuniu.dev').length).toBe(1);
  });

  it('removeOrigin removes the host', () => {
    useTrustedRelaysStore.getState().removeOrigin('relay.niuniu.dev');
    expect(useTrustedRelaysStore.getState().isTrusted('relay.niuniu.dev')).toBe(false);
    expect(useTrustedRelaysStore.getState().origins).not.toContain('relay.niuniu.dev');
  });
});
