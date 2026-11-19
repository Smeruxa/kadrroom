import { defineConfig } from '@playwright/test'

const baseURL = 'http://127.0.0.1:5173'
const reuseExistingServer = !process.env['CI']

export default defineConfig({
    testDir: './apps/web/e2e',
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
})
