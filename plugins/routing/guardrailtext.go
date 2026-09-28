package routing

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// guardrailTextEvaluator returns the currently wired guardrail evaluator, or
// nil when no guardrail plugin is loaded.
func (p *RoutingPlugin) guardrailTextEvaluator() schemas.GuardrailTextEvaluator {
	if ptr := p.guardrailEvaluator.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// SetGuardrailTextEvaluator wires (or clears, with nil) the guardrail plugin
// whose input rules gate the text complexity routing forwards to its classifier
// providers. Safe for concurrent use with classification and plugin reloads.
//
// Classification runs in PreRequestHook, before any PreLLMHook, and its
// embedding/LLM sub-requests are marked to skip the plugin pipeline so they
// cannot recurse. Without this wiring the classifier provider would receive text
// no guardrail ever inspected; with it, the router asks the guardrail plugin
// about exactly the text it is about to send. Wired by the HTTP server after the
// bifrost client and the plugins exist, like the embedding and chat executors.
func (p *RoutingPlugin) SetGuardrailTextEvaluator(evaluator schemas.GuardrailTextEvaluator) {
	if evaluator == nil {
		p.guardrailEvaluator.Store(nil)
		return
	}
	p.guardrailEvaluator.Store(&evaluator)
}

// evaluateComplexityInputGuardrails applies the wired guardrail rules to the
// user text a classification is about to forward. It returns the possibly
// redacted input and the violation when a blocking rule matched; a nil error
// means the input may be classified.
//
// Both classifier mechanisms read the same ComplexityInput — the semantic
// embedder joins it and the llm fallback puts it in the judge prompt — so
// evaluating once here guards both outbound paths, and the redacted text is
// what either one sees. The system prompt is not evaluated: it is never
// embedded, and the extractor already excludes assistant turns.
//
// Every user text carried by the input is evaluated, including prior turns
// outside the configured history window. That is intentional and mirrors how
// the hook path evaluates the whole conversation rather than a window of it;
// a match on an older turn therefore skips classification instead of being
// silently forwarded. Text that is empty is skipped, and an evaluation error
// from one text aborts the rest: the caller publishes no tier.
func (p *RoutingPlugin) evaluateComplexityInputGuardrails(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	input complexity.ComplexityInput,
) (complexity.ComplexityInput, *schemas.BifrostError) {
	evaluator := p.guardrailTextEvaluator()
	if evaluator == nil {
		return input, nil
	}

	if input.LastUserText != "" {
		evaluated, bifrostErr := evaluator.EvaluateInputText(ctx, req, input.LastUserText)
		if bifrostErr != nil {
			return input, bifrostErr
		}
		input.LastUserText = evaluated
	}
	if len(input.PriorUserTexts) > 0 {
		prior := make([]string, len(input.PriorUserTexts))
		for i, text := range input.PriorUserTexts {
			evaluated, bifrostErr := evaluator.EvaluateInputText(ctx, req, text)
			if bifrostErr != nil {
				return input, bifrostErr
			}
			prior[i] = evaluated
		}
		input.PriorUserTexts = prior
	}
	return input, nil
}
