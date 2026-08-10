import { setupServer } from 'msw/node';
import { handlers } from './handlers';

// Set up the server for testing
export const server = setupServer(...handlers);
