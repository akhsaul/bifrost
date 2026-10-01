import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'

/**
 * Page object for the Models → Extra Detection page, covering the token
 * counting configuration (endpoint credentials, tuning, counting rules).
 */
export class ExtraDetectionPage extends BasePage {
  readonly gotoLink: (path?: string) => Promise<void>

  // Plugin-level
  readonly pluginEnabledSwitch: Locator
  readonly estimateSwitch: Locator

  // Token counting: credentials and tuning
  readonly openAIKeyInput: Locator
  readonly geminiKeyInput: Locator
  readonly openAIBaseUrlInput: Locator
  readonly geminiBaseUrlInput: Locator
  readonly timeoutInput: Locator
  readonly paddingInput: Locator

  // Counting rules
  readonly addCountingRuleBtn: Locator
  readonly countingRuleRows: Locator

  // Counting rule sheet
  readonly sheetName: Locator
  readonly sheetDescription: Locator
  readonly sheetEnabledSwitch: Locator
  readonly sheetProviderInput: Locator
  readonly sheetEndpointTrigger: Locator
  readonly sheetModelPatternInput: Locator
  readonly sheetPatternTypeTrigger: Locator
  readonly sheetCountModelInput: Locator
  readonly sheetError: Locator
  readonly sheetSaveBtn: Locator
  readonly sheetCancelBtn: Locator

  constructor(page: Page) {
    super(page)
    this.gotoLink = async (path = '/workspace/extra-detection') => {
      await page.goto(path)
      await this.waitForPageLoad()
    }

    this.pluginEnabledSwitch = page.getByTestId('extra-detection-plugin-enabled-switch')
    this.estimateSwitch = page.getByTestId('extra-detection-estimate-switch')

    this.openAIKeyInput = page.getByTestId('extra-detection-counting-key-openai')
    this.geminiKeyInput = page.getByTestId('extra-detection-counting-key-gemini')
    this.openAIBaseUrlInput = page.getByTestId('extra-detection-counting-base-url-openai')
    this.geminiBaseUrlInput = page.getByTestId('extra-detection-counting-base-url-gemini')
    this.timeoutInput = page.getByTestId('extra-detection-counting-timeout')
    this.paddingInput = page.getByTestId('extra-detection-counting-padding')

    this.addCountingRuleBtn = page.getByTestId('extra-detection-counting-rule-add')
    this.countingRuleRows = page.locator('[data-testid^="extra-detection-counting-rule-row-"]')

    this.sheetName = page.getByTestId('extra-detection-counting-rule-sheet-name')
    this.sheetDescription = page.getByTestId('extra-detection-counting-rule-sheet-desc')
    this.sheetEnabledSwitch = page.getByTestId('extra-detection-counting-rule-sheet-enabled')
    this.sheetProviderInput = page.getByTestId('extra-detection-counting-rule-sheet-provider')
    this.sheetEndpointTrigger = page.getByTestId('extra-detection-counting-rule-sheet-endpoint')
    this.sheetModelPatternInput = page.getByTestId('extra-detection-counting-rule-sheet-model-pattern')
    this.sheetPatternTypeTrigger = page.getByTestId('extra-detection-counting-rule-sheet-pattern-type')
    this.sheetCountModelInput = page.getByTestId('extra-detection-counting-rule-sheet-count-model')
    this.sheetError = page.getByTestId('extra-detection-counting-rule-sheet-error')
    this.sheetSaveBtn = page.getByTestId('extra-detection-counting-rule-sheet-save')
    this.sheetCancelBtn = page.getByTestId('extra-detection-counting-rule-sheet-cancel')
  }

  /** Whether the page rendered the token counting section at all. */
  async isCountingSectionVisible(): Promise<boolean> {
    return this.addCountingRuleBtn.isVisible().catch(() => false)
  }

  /** Open the counting rule sheet in create mode. */
  async openCountingRuleSheet(): Promise<void> {
    await this.addCountingRuleBtn.click()
    await this.waitForSheetAnimation()
  }

  /**
   * Fill the counting rule sheet and save. Blank fields are left as-is so a
   * test can assert the form's own validation.
   */
  async fillCountingRuleSheet(values: {
    name?: string
    description?: string
    provider?: string
    modelPattern?: string
    countModel?: string
    endpoint?: 'openai' | 'gemini'
    patternType?: 'glob' | 'regex' | 'exact'
  }): Promise<void> {
    if (values.name !== undefined) await this.sheetName.fill(values.name)
    if (values.description !== undefined) await this.sheetDescription.fill(values.description)
    if (values.provider !== undefined) await this.sheetProviderInput.fill(values.provider)
    if (values.modelPattern !== undefined) await this.sheetModelPatternInput.fill(values.modelPattern)
    if (values.countModel !== undefined) await this.sheetCountModelInput.fill(values.countModel)
    if (values.endpoint) {
      await this.sheetEndpointTrigger.click()
      await this.page.getByRole('option', { name: values.endpoint === 'gemini' ? 'Gemini' : 'OpenAI' }).click()
    }
    if (values.patternType) {
      await this.sheetPatternTypeTrigger.click()
      await this.page.getByRole('option', { name: values.patternType, exact: true }).click()
    }
  }

  /** Save the counting rule sheet and wait for the success toast. */
  async saveCountingRuleSheet(): Promise<void> {
    await this.sheetSaveBtn.click()
    await this.waitForSuccessToast()
  }

  /** Delete a counting rule by id via its row action menu. */
  async deleteCountingRule(id: number): Promise<void> {
    await this.page.getByTestId(`extra-detection-counting-rule-actions-${id}`).click()
    await this.page.getByTestId(`extra-detection-counting-rule-delete-${id}`).click()
    await this.waitForSuccessToast()
  }

  /** Read a counting rule row's "matches / counts as" summary. */
  async countingRuleSummary(id: number): Promise<string> {
    return (await this.page.getByTestId(`extra-detection-counting-rule-row-${id}`).innerText()) ?? ''
  }

  /** Whether a counting rule with the given id exists. */
  async countingRuleExists(id: number): Promise<boolean> {
    return this.page.getByTestId(`extra-detection-counting-rule-row-${id}`).isVisible().catch(() => false)
  }
}
