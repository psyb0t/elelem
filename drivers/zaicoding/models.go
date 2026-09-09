package zaicoding

import (
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	modelGLM53      = "glm-5.3"
	modelGLM53Flash = "glm-5.3-flash"
)

type modelKind string

const (
	modelKindUnknown    modelKind = ""
	modelKindGLM53      modelKind = modelGLM53
	modelKindGLM53Flash modelKind = modelGLM53Flash
)

// KnownModels returns the Z.ai Coding models whose reasoning behavior this
// driver validates locally.
func KnownModels() []elelem.Model {
	return []elelem.Model{
		LookupModel(modelGLM53),
		LookupModel(modelGLM53Flash),
	}
}

// LookupModel returns known model metadata and leaves unknown IDs unchanged.
func LookupModel(id string) elelem.Model {
	model := elelem.Model{ID: id}

	switch classifyModel(id) {
	case modelKindUnknown:
		return model
	case modelKindGLM53Flash:
		model.SupportsReasoning = true
	case modelKindGLM53:
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
	case modelGLM53:
		return modelKindGLM53
	case modelGLM53Flash:
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

	switch classifyModel(model.ID) {
	case modelKindUnknown:
		capabilities.SupportsReasoningEffort = false
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortUnset
	case modelKindGLM53:
		capabilities.SupportsReasoningEffort = true
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortMax
	case modelKindGLM53Flash:
		capabilities.SupportsReasoningEffort = false
		capabilities.SupportsDisablingReasoning = false
		capabilities.MaxReasoningEffort = elelem.ReasoningEffortUnset
	}

	return capabilities
}
