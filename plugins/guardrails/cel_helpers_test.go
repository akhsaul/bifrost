package guardrails

import "testing"

// mustCompileCEL compiles an expression in tests; any failure is fatal.
func mustCompileCEL(t *testing.T, expr string) celProgram {
	t.Helper()
	prg, err := compileCEL(expr)
	if err != nil {
		t.Fatalf("compileCEL(%q): %v", expr, err)
	}
	return prg
}
