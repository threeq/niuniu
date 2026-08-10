import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

// MSW worker for browser mocking
export const worker = setupWorker(...handlers);

// Start the worker
export const startMsw = async () => {
  await worker.start({
    onUnhandledRequest: 'bypass',
  });
  console.log('MSW worker started');
};
