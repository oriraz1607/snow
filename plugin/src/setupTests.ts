import '@testing-library/jest-dom';

// Polyfill TextEncoder/TextDecoder or ResizeObserver if needed in jsdom
if (typeof global.ResizeObserver === 'undefined') {
  global.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}
