package zaicoding

import (
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	modelGLM53       = "glm-5.3"
	modelGLM53Flash  = "glm-5.3-flash"
	modelGLM53FlashX = "glm-5.3-flashx"

	// The Coding Plan routes these retired IDs to a current model, so they
	// take that model's parameters.
	modelGLM52Routed = "glm-5.2"
	modelGLM51Routed = "glm-5.1"
	modelGLM47Routed = "glm-4.7"

	// glm53ContextSize is the 1M-token window Z.ai documents for GLM 5.3,
	// and GLM 5.3 Flash shares its text parameters. The Coding Plan setup
	// guides set the window to exactly 1000000.
	glm53ContextSize = 1_000_000

	// glm53MaxOutputTokens is the documented max_tokens ceiling for GLM 5.3
	// and GLM 5.3 Flash.
	glm53MaxOutputTokens = 131_072
)

type modelKind string

const (
	modelKindUnknown    modelKind = ""
	modelKindGLM53      modelKind = modelGLM53
	modelKindGLM53Flash modelKind = modelGLM53Flash
)

// KnownModels returns the models the Z.ai Coding Plan serves.
func KnownModels() []elelem.Model {
	return []elelem.Model{
		LookupModel(modelGLM53),
		LookupModel(modelGLM53Flash),
	}
}

// LookupModel returns known model metadata and leaves unknown IDs unchanged.
// GLM 5.3 Flash X and the retired IDs the Coding Plan routes to GLM 5.3 or
// GLM 5.3 Flash get the metadata of the model that serves them.
func LookupModel(id string) elelem.Model {
	model := elelem.Model{ID: id}

	switch classifyModel(id) {
	case modelKindUnknown:
		return model
	case modelKindGLM53, modelKindGLM53Flash:
		model.ContextSize = glm53ContextSize
		model.SupportsReasoning = true
		model.ReasoningLevels = reasoningLevels()
	}

	return model
}

func reasoningLevels() elelem.ReasoningLevels {
	return elelem.ReasoningLevels{
		Min:    elelem.ReasoningEffortLow,
		Low:    elelem.ReasoningEffortLow,
		Medium: elelem.ReasoningEffortMedium,
		High:   elelem.ReasoningEffortHigh,
		Max:    elelem.ReasoningEffortMax,
	}
}

func classifyModel(id string) modelKind {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case modelGLM53, modelGLM52Routed, modelGLM51Routed:
		return modelKindGLM53
	case modelGLM53Flash, modelGLM53FlashX, modelGLM47Routed:
		return modelKindGLM53Flash
	default:
		return modelKindUnknown
	}
}

func capabilities(
	model elelem.Model,
	inherited elelem.Capabilities,
) elelem.Capabilities {
	capabilities := inherited

	// The endpoint documents only auto, its default, and silently treats
	// required or a named tool as auto. Claiming tool choice would let the
	// engine build a request whose forced call never comes.
	capabilities.SupportsToolChoice = false

	switch classifyModel(model.ID) {
	case modelKindUnknown:
		capabilities.SupportsReasoningEffort = false
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortUnset
	case modelKindGLM53:
		capabilities.SupportsReasoningEffort = true
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortMax
		// GLM 5.3 is text-only: the endpoint answers any other content part
		// with business code 1210.
		capabilities.SupportsImageInput = false
		capabilities.SupportsAudioInput = false
		capabilities.SupportsFileInput = false
	case modelKindGLM53Flash:
		capabilities.SupportsReasoningEffort = true
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortMax
	}

	return capabilities
}
