import { defineConfig } from '@playwright/test'

const externalURL = process.env['KADRROOM_E2E_BASE_URL']
const baseURL = externalURL || 'http://127.0.0.1:5173'
const reuseExistingServer = !process.env['CI']

export default defineConfig({
    testDir: '.',
    testMatch: ['**/apps/frontend/e2e/*.spec.ts', '**/tests/e2e/*.spec.ts'],
    fullyParallel: true,
    use: { baseURL, trace: 'retain-on-failure' },
    projects: [
        { name: 'chromium' },
        { name: 'firefox', use: { browserName: 'firefox' } },
        {
            name: 'webkit',
            use: { browserName: 'webkit' },
        },
    ],
    ...(externalURL
        ? {}
        : {
              webServer: [
                  {
                      command: 'go run ./apps/catalog-api',
                      url: 'http://127.0.0.1:8080/healthz',
                      reuseExistingServer,
                      timeout: 120_000,
                  },
                  {
                      command: 'npm run dev',
                      url: baseURL,
                      reuseExistingServer,
                      timeout: 120_000,
                  },
              ],
          }),
})
