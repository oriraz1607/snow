module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'jsdom',
  testTimeout: 15000,
  roots: ['<rootDir>/src'],
  testMatch: ['**/*.test.ts', '**/*.test.tsx'],
  setupFiles: ['<rootDir>/src/setupPolyfills.ts'],
  setupFilesAfterEnv: ['<rootDir>/src/setupTests.ts'],
  moduleNameMapper: {
    '\\.(css|less|scss)$': '<rootDir>/src/__mocks__/styleMock.ts',
  },
  transformIgnorePatterns: [
    '/node_modules/(?!(ol|d3-.*|internmap|delaunator|robust-predicates|@grafana/.*|lodash-es|marked|nanoid|react-inlinesvg|@react-aria/.*|@react-stately/.*|@react-types/.*|@internationalized/.*|@react-hookz/.*|@ver0/.*|@rc-component/.*|@floating-ui/.*|@tanstack/.*|date-fns.*|react-router-dom-v5-compat|get-user-locale|memoize|mimic-function|@wojtekmaj/.*|quickselect|rbush|react-calendar)/)',
  ],
  transform: {
    '^.+\\.([tj]sx?|mjs)$': [
      'ts-jest',
      {
        tsconfig: {
          allowJs: true,
          esModuleInterop: true,
          jsx: 'react-jsx',
          module: 'CommonJS',
          moduleResolution: 'node',
        },
        diagnostics: false,
      },
    ],
  },
};
