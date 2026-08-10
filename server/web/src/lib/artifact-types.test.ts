import { describe, it, expect } from 'vitest';
import { artifactKind, isPreviewableArtifact, extOf } from './artifact-types';

describe('artifact-types', () => {
  it('maps office and image extensions to kinds', () => {
    expect(artifactKind('a/b.docx')).toBe('doc');
    expect(artifactKind('Q2.XLSX')).toBe('sheet');
    expect(artifactKind('deck.pptx')).toBe('slide');
    expect(artifactKind('poster.png')).toBe('image');
    expect(artifactKind('manual.pdf')).toBe('pdf');
    expect(artifactKind('report.html')).toBe('html');
    expect(artifactKind('README.md')).toBe('markdown');
    expect(artifactKind('data.csv')).toBe('sheet');
    expect(artifactKind('notes.txt')).toBe('text');
    expect(artifactKind('config.json')).toBe('text');
    expect(artifactKind('main.go')).toBe('other');
  });

  it('treats common document/data files as previewable, but not source code', () => {
    expect(isPreviewableArtifact('report.xlsx')).toBe(true);
    expect(isPreviewableArtifact('logo.svg')).toBe(true);
    expect(isPreviewableArtifact('summary.md')).toBe(true);
    expect(isPreviewableArtifact('data.csv')).toBe(true);
    expect(isPreviewableArtifact('notes.txt')).toBe(true);
    // Source code stays out of the deliverables panel.
    expect(isPreviewableArtifact('script.go')).toBe(false);
    expect(isPreviewableArtifact('build.js')).toBe(false);
  });

  it('extracts the lowercase extension', () => {
    expect(extOf('A.PNG')).toBe('png');
    expect(extOf('noext')).toBe('noext');
  });
});
