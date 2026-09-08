package zaicoding

import (
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	modelGLM45Air = "glm-4.5-air"
	modelGLM47    = "glm-4.7"
	modelGLM51    = "glm-5.1"
	modelGLM52    = "glm-5.2"
	modelGLM53    = "glm-5.3"

	glm45AirContextSize = 128_000
	glm47ContextSize    = 200_000
)

type modelKind string

const (
	modelKindUnknown  modelKind = ""
	modelKindGLM45Air modelKind = modelGLM45Air
	modelKindGLM47    modelKind = modelGLM47
	modelKindGLM51    modelKind = modelGLM51
	modelKindGLM52    modelKind = modelGLM52
	modelKindGLM53    modelKind = modelGLM53
)

// KnownModels returns the Z.ai Coding models whose reasoning behavior this
// driver validates locally.
func KnownModels() []elelem.Model {
	return []elelem.Model{
		LookupModel(modelGLM45Air),
		LookupModel(modelGLM47),
		LookupModel(modelGLM51),
		LookupModel(modelGLM52),
		LookupModel(modelGLM53),
	}
}

// LookupModel returns known model metadata and leaves unknown IDs unchanged.
func LookupModel(id string) elelem.Model {
	model := elelem.Model{ID: id}

	switch classifyModel(id) {
	case modelKindUnknown:
		return model
	case modelKindGLM45Air:
		model.ContextSize = glm45AirContextSize
		model.SupportsReasoning = true
	case modelKindGLM47:
		model.ContextSize = glm47ContextSize
		model.SupportsReasoning = true
	case modelKindGLM51:
		model.SupportsReasoning = true
	case modelKindGLM52, modelKindGLM53:
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
	case modelGLM45Air:
		return modelKindGLM45Air
	case modelGLM47:
		return modelKindGLM47
	case modelGLM51:
		return modelKindGLM51
	case modelGLM52:
		return modelKindGLM52
	case modelGLM53:
		return modelKindGLM53
	default:
		return modelKindUnknown
	}
}

func capabilities(
	model elelem.Model,
	inherited elelem.Capabilities,
) elelem.Capabilities {
	capabilities := inherited

	switch classifyModel(model.ID) {
	case modelKindUnknown:
		capabilities.SupportsReasoningEffort = false
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortUnset
	case modelKindGLM45Air, modelKindGLM47, modelKindGLM51:
		capabilities.SupportsReasoningEffort = false
		capabilities.SupportsDisablingReasoning = true
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortUnset
	case modelKindGLM52:
		capabilities.SupportsReasoningEffort = true
		capabilities.SupportsDisablingReasoning = true
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortMax
	case modelKindGLM53:
		capabilities.SupportsReasoningEffort = true
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortMax
	}

	return capabilities
}
