import js from '@eslint/js'
import angular from 'angular-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'
import tseslint from 'typescript-eslint'

export default defineConfig(
    globalIgnores([
        '**/.angular/**',
        '**/dist/**',
        '**/test-results/**',
        '**/playwright-report/**',
    ]),
    {
        files: ['apps/web/**/*.ts', 'packages/contracts/**/*.ts', 'playwright.config.ts'],
        extends: [js.configs.recommended, tseslint.configs.strictTypeChecked],
        languageOptions: {
            parserOptions: {
                projectService: true,
                tsconfigRootDir: import.meta.dirname,
            },
        },
        rules: {
            '@typescript-eslint/consistent-type-imports': 'error',
            'no-restricted-syntax': [
                'error',
                {
                    selector: 'TSTypeLiteral:not(TSTypeAliasDeclaration > TSTypeLiteral)',
                    message: 'Declare an interface or a named object type and reference it here.',
                },
            ],
        },
    },
    {
        files: ['apps/web/src/**/*.ts'],
        extends: [angular.configs.tsRecommended],
        processor: angular.processInlineTemplates,
    },
    {
        files: ['apps/web/src/**/*.html'],
        extends: [angular.configs.templateRecommended, angular.configs.templateAccessibility],
    },
)
