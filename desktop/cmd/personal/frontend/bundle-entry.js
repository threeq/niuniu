// Bundle entry - imports generated bindings and re-exports for use in HTML
import * as App from './bindings/github.com/niuniu-dev/niuniu-desktop/app.js';

// Re-export all App methods
export const GetConnections = App.GetConnections;
export const GetDiscoveredInstances = App.GetDiscoveredInstances;
export const AddConnection = App.AddConnection;
export const RemoveConnection = App.RemoveConnection;
export const SetDefaultConnection = App.SetDefaultConnection;
export const ConnectByID = App.ConnectByID;
export const ConnectToAddress = App.ConnectToAddress;
export const RefreshTrayMenu = App.RefreshTrayMenu;

// Make available globally for non-module scripts
window.WailsBindings = {
  GetConnections,
  GetDiscoveredInstances,
  AddConnection,
  RemoveConnection,
  SetDefaultConnection,
  ConnectByID,
  ConnectToAddress,
  RefreshTrayMenu
};
