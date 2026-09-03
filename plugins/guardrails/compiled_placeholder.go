package guardrails

// compiledConfig is the runtime-compiled form of Config: patterns and CEL
// programs are built once at Init / config update time.
type compiledConfig struct {
	cfg      Config
	patterns map[int][]compiledPattern // provider id -> compiled patterns
	programs map[int]celProgram        // rule id -> compiled CEL program
}
