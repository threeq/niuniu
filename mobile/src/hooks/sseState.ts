// Shared state to coordinate between global SSE and workspace SSE.
// When workspace SSE is active, global SSE should pause.

let _workspaceSSEActive = false;

export function setWorkspaceSSEActive(active: boolean): void {
  _workspaceSSEActive = active;
}

export function isWorkspaceSSEActive(): boolean {
  return _workspaceSSEActive;
}
