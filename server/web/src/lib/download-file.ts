// App-managed download flow for workspace files: fetch the bytes with
// progress, then hand them to the browser's save flow. This replaces bare
// `<a href download>` links, which give the app no completion/failure signal —
// in the desktop webview a click produced zero visible feedback.

/**
 * Streams `url` into a Blob, reporting percent progress when the response
 * carries Content-Length (callers get 0..100; small/instant responses just
 * report 100).
 */
export async function fetchFileWithProgress(
	url: string,
	onProgress?: (pct: number) => void,
): Promise<Blob> {
	const res = await fetch(url, { credentials: 'include' });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	const total = Number(res.headers.get('Content-Length'));
	if (!res.body || !Number.isFinite(total) || total <= 0) {
		onProgress?.(100);
		return await res.blob();
	}
	onProgress?.(0);
	const reader = res.body.getReader();
	const chunks: Uint8Array[] = [];
	let received = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) break;
		chunks.push(value);
		received += value.length;
		onProgress?.(Math.min(100, Math.round((received / total) * 100)));
	}
	return new Blob(chunks as BlobPart[], {
		type: res.headers.get('Content-Type') ?? 'application/octet-stream',
	});
}

/** saveBlobToLocal hands an in-memory blob to the browser's download flow. */
export function saveBlobToLocal(blob: Blob, name: string): void {
	const objectUrl = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = objectUrl;
	a.download = name;
	document.body.appendChild(a);
	a.click();
	a.remove();
	// Revoke late: the save may still be reading the blob after the anchor is
	// gone (large files, slow disk).
	setTimeout(() => URL.revokeObjectURL(objectUrl), 10_000);
}
