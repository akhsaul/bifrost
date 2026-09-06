package guardrails

// compiledConfig is the runtime-compiled form of Config: patterns and CEL
// programs are built once at Init / config update time.
type compiledConfig struct {
	cfg      Config
	patterns map[int][]compiledPattern    // provider id -> compiled regex patterns (provider_name "regex")
	secrets  map[int]*betterleaksDetector // provider id -> secrets detector (provider_name "secrets")
	judges   map[int]*promptJudge         // provider id -> LLM judge (provider_name "prompt-guardrail")
	programs map[int]celProgram           // rule id -> compiled CEL program
}
