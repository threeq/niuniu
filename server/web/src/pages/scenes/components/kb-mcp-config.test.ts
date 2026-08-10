import { describe, it, expect } from 'vitest';
import type { SceneDefinition } from '@/types/api';
import {
  applyKBConfig,
  buildKBCredentialBody,
  kbCredentialAlias,
  slugify,
  validateKBForm,
  KB_CREDENTIAL_PROVIDER,
} from './kb-mcp-config';

// A minimal stand-in for a forked kb-* preset definition.
function presetDef(): SceneDefinition {
  return {
    mcp: [
      {
        name: 'kb',
        config: {
          type: 'http',
          url: 'https://your-kb-endpoint.example.com/mcp',
          headers: { Authorization: 'Bearer ${cred:kb-api.token}' },
        },
      },
    ],
    plugins: [],
    assets: {},
    prompts: [],
    required_credentials: [
      { alias: 'kb-api', provider: 'knowledge-base', purpose: 'x', optional: false },
    ],
    match: {},
  };
}

describe('slugify', () => {
  it('lowercases and dash-joins ascii', () => {
    expect(slugify('My Shop KB ')).toBe('my-shop-kb');
  });
  it('drops non-ascii, yielding empty for all-Chinese', () => {
    expect(slugify('电商知识库')).toBe('');
  });
});

describe('applyKBConfig', () => {
  it('points the kb server at the user endpoint with a per-scene alias header', () => {
    const out = applyKBConfig(presetDef(), { slug: 'my-shop', endpoint: '  https://kb.acme.com/mcp  ' });
    const cfg = out.mcp[0].config as Record<string, unknown>;
    expect(cfg.url).toBe('https://kb.acme.com/mcp');
    expect((cfg.headers as Record<string, unknown>).Authorization).toBe(
      'Bearer ${cred:kb-my-shop.token}',
    );
    expect(cfg.type).toBe('http');
  });

  it('repoints required_credentials to the per-scene alias, keeping provider', () => {
    const out = applyKBConfig(presetDef(), { slug: 'my-shop', endpoint: 'https://kb.acme.com/mcp' });
    expect(out.required_credentials[0].alias).toBe('kb-my-shop');
    expect(out.required_credentials[0].provider).toBe(KB_CREDENTIAL_PROVIDER);
  });

  it('does not mutate the input definition (pure)', () => {
    const input = presetDef();
    applyKBConfig(input, { slug: 'my-shop', endpoint: 'https://kb.acme.com/mcp' });
    const cfg = input.mcp[0].config as Record<string, unknown>;
    expect(cfg.url).toBe('https://your-kb-endpoint.example.com/mcp');
    expect(input.required_credentials[0].alias).toBe('kb-api');
  });
});

describe('buildKBCredentialBody', () => {
  it('builds a knowledge-base credential with the token in config', () => {
    const body = buildKBCredentialBody('my-shop', '  sk-123  ');
    expect(body).toEqual({
      provider: 'knowledge-base',
      alias: 'kb-my-shop',
      config: { token: 'sk-123' },
    });
  });
});

describe('kbCredentialAlias', () => {
  it('prefixes the slug', () => {
    expect(kbCredentialAlias('legal')).toBe('kb-legal');
  });
});

describe('validateKBForm', () => {
  it('accepts a valid form (no errors)', () => {
    expect(validateKBForm('My KB', 'https://kb.acme.com/mcp', 'sk-1')).toEqual({});
  });
  it('flags an empty-slug name', () => {
    expect(validateKBForm('电商', 'https://kb.acme.com/mcp', 'sk-1').name).toBe('kb.err_name');
  });
  it('flags a non-url endpoint', () => {
    expect(validateKBForm('KB', 'not-a-url', 'sk-1').endpoint).toBe('kb.err_endpoint_url');
  });
  it('flags a missing endpoint and token', () => {
    const e = validateKBForm('KB', '', '');
    expect(e.endpoint).toBe('kb.err_endpoint_required');
    expect(e.token).toBe('kb.err_token');
  });
});
