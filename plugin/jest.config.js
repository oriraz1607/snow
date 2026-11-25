module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'jsdom',
  roots: ['<rootDir>/src'],
  testMatch: ['**/*.test.ts', '**/*.test.tsx'],
  setupFiles: ['<rootDir>/src/setupPolyfills.ts'],
  setupFilesAfterEnv: ['<rootDir>/src/setupTests.ts'],
  moduleNameMapper: {
    '\\.(css|less|scss)$': '<rootDir>/src/__mocks__/styleMock.ts',
  },
  transformIgnorePatterns: [
    '/node_modules/(?!(ol|d3-.*|internmap|delaunator|robust-predicates|@grafana/.*|lodash-es|marked|nanoid|react-inlinesvg|@react-aria/.*|@react-stately/.*|@react-types/.*|@internationalized/.*|quickselect|rbush|react-calendar)/)',
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
