import { defineConfig } from '@playwright/test';

const PORT = Number(process.env.E2E_PORT || 18200);

// Chromium ohne Extension. Der Go-Server wird in global-setup.mjs gebaut und gestartet.
export default defineConfig({
  testDir: './tests',
  globalSetup: './global-setup.mjs',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'report' }]],
  outputDir: 'results',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    browserName: 'chromium',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
});
