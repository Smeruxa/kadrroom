import { defineConfig } from '@playwright/test'

const baseURL = 'http://127.0.0.1:5174'

export default defineConfig({
    testDir: './e2e',
    use: { baseURL, trace: 'retain-on-failure' },
    webServer: {
        command: 'npm run dev',
        cwd: import.meta.dirname,
        url: baseURL,
        reuseExistingServer: !process.env['CI'],
    },
})
