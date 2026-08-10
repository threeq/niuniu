import zhCN from '../resources/zh-CN/common.json';
import enUS from '../resources/en-US/common.json';

type JsonValue = string | number | boolean | null | JsonObject | JsonValue[];
interface JsonObject { [key: string]: JsonValue }

/**
 * Recursively collect all dot-separated leaf key paths from a JSON object.
 */
function collectKeys(obj: JsonObject, prefix = ''): string[] {
  return Object.entries(obj).flatMap(([key, value]) => {
    const fullKey = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      return collectKeys(value as JsonObject, fullKey);
    }
    return [fullKey];
  });
}

describe('i18n resource parity', () => {
  const zhKeys = new Set(collectKeys(zhCN as unknown as JsonObject));
  const enKeys = new Set(collectKeys(enUS as unknown as JsonObject));

  test('all zh-CN keys are present in en-US', () => {
    const missing: string[] = [];
    for (const key of zhKeys) {
      if (!enKeys.has(key)) {
        missing.push(key);
      }
    }
    if (missing.length > 0) {
      throw new Error(
        `Keys present in zh-CN but missing in en-US:\n  ${missing.join('\n  ')}`,
      );
    }
  });

  test('all en-US keys are present in zh-CN', () => {
    const missing: string[] = [];
    for (const key of enKeys) {
      if (!zhKeys.has(key)) {
        missing.push(key);
      }
    }
    if (missing.length > 0) {
      throw new Error(
        `Keys present in en-US but missing in zh-CN:\n  ${missing.join('\n  ')}`,
      );
    }
  });

  test('both files have the same number of leaf keys', () => {
    expect(zhKeys.size).toBe(enKeys.size);
  });
});
