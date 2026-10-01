import { expect, test } from '../../core/fixtures/base.fixture'

// The token counting config is stored on the extra-detection plugin, so these
// tests mutate shared state. Each test cleans up the counting rules it created
// and leaves the keys/tuning it touched at their prior values.
test.describe('Extra Detection — token counting', () => {
  test.beforeEach(async ({ extraDetectionPage }) => {
    await extraDetectionPage.gotoLink()
  })

  test.afterEach(async ({ extraDetectionPage }) => {
    // Close a sheet left open by a validation-failure test.
    await extraDetectionPage.sheetCancelBtn.click().catch(() => {})
    // Delete every counting rule so the suite does not leak config.
    const count = await extraDetectionPage.countingRuleRows.count()
    for (let i = 0; i < count; i++) {
      const row = extraDetectionPage.countingRuleRows.first()
      const testId = await row.getAttribute('data-testid')
      const id = Number(testId?.split('-').pop())
      await extraDetectionPage.deleteCountingRule(id).catch(() => {})
    }
  })

  test('should render the token counting section with both endpoint credentials', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await expect(extraDetectionPage.openAIKeyInput).toBeVisible()
    await expect(extraDetectionPage.geminiKeyInput).toBeVisible()
    await expect(extraDetectionPage.timeoutInput).toBeVisible()
    await expect(extraDetectionPage.paddingInput).toBeVisible()
    await expect(extraDetectionPage.openAIKeyInput).toHaveAttribute('type', 'password')
  })

  test('should start with no counting rules so requests are not counted', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await expect(extraDetectionPage.countingRuleRows).toHaveCount(0)
    await expect(extraDetectionPage.page.getByText(/No counting rules configured/i)).toBeVisible()
  })

  test('should create a counting rule that maps a provider and model to an upstream count model', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await extraDetectionPage.openCountingRuleSheet()
    await extraDetectionPage.fillCountingRuleSheet({
      name: 'openrouter gpt-5 → OpenAI',
      description: 'e2e counting rule',
      provider: 'openrouter',
      modelPattern: 'gpt-5*',
      countModel: 'gpt-5',
      endpoint: 'openai',
    })
    await extraDetectionPage.saveCountingRuleSheet()

    const id = 1
    expect(await extraDetectionPage.countingRuleExists(id)).toBe(true)
    const summary = await extraDetectionPage.countingRuleSummary(id)
    expect(summary).toContain('openrouter')
    expect(summary).toContain('gpt-5*')
    expect(summary).toContain('gpt-5')
    expect(summary).toContain('OpenAI')
  })

  test('should reject a counting rule with no count model', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await extraDetectionPage.openCountingRuleSheet()
    await extraDetectionPage.fillCountingRuleSheet({
      name: 'missing count model',
      provider: 'openai',
      modelPattern: '*',
      countModel: '',
    })
    await extraDetectionPage.sheetSaveBtn.click()

    await expect(extraDetectionPage.sheetError).toBeVisible()
    await expect(extraDetectionPage.sheetError).toContainText(/count model is required/i)
    // Nothing was persisted, so no rule row appeared.
    await expect(extraDetectionPage.countingRuleRows).toHaveCount(0)
  })

  test('should reject an invalid regex model pattern', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await extraDetectionPage.openCountingRuleSheet()
    await extraDetectionPage.fillCountingRuleSheet({
      name: 'bad regex',
      provider: 'openai',
      modelPattern: 'gemini-[0-9',
      countModel: 'gemini-3-flash',
      patternType: 'regex',
    })
    await extraDetectionPage.sheetSaveBtn.click()

    await expect(extraDetectionPage.sheetError).toBeVisible()
    await expect(extraDetectionPage.sheetError).toContainText(/not a valid regular expression/i)
    await expect(extraDetectionPage.countingRuleRows).toHaveCount(0)
  })

  test('should persist the count model for a Gemini-family rule', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await extraDetectionPage.openCountingRuleSheet()
    await extraDetectionPage.fillCountingRuleSheet({
      name: 'gemini family → Gemini',
      provider: '*',
      modelPattern: 'gemini-*',
      countModel: 'gemini-3-flash',
      endpoint: 'gemini',
    })
    await extraDetectionPage.saveCountingRuleSheet()

    const summary = await extraDetectionPage.countingRuleSummary(1)
    expect(summary).toContain('gemini-*')
    expect(summary).toContain('gemini-3-flash')
    expect(summary).toContain('Gemini')
  })

  test('should toggle a counting rule and delete it', async ({ extraDetectionPage }) => {
    const visible = await extraDetectionPage.isCountingSectionVisible()
    test.skip(!visible, 'Extra detection plugin is not enabled in this environment')

    await extraDetectionPage.openCountingRuleSheet()
    await extraDetectionPage.fillCountingRuleSheet({
      name: 'toggle me',
      provider: 'openai',
      modelPattern: 'gpt-4o',
      countModel: 'gpt-4o',
    })
    await extraDetectionPage.saveCountingRuleSheet()
    expect(await extraDetectionPage.countingRuleExists(1)).toBe(true)

    await extraDetectionPage.page.getByTestId('extra-detection-counting-rule-enabled-1').click()
    await extraDetectionPage.waitForNetworkIdle()

    await extraDetectionPage.deleteCountingRule(1)
    expect(await extraDetectionPage.countingRuleExists(1)).toBe(false)
  })
})
