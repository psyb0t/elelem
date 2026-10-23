package zaicoding

import "errors"

var (
	// ErrUnsupportedParameter marks a Z.ai Coding control that the selected
	// model cannot accept.
	ErrUnsupportedParameter = errors.New("unsupported Z.ai Coding parameter")

	// ErrSensitiveContent is Z.ai's refusal of input or output it classifies
	// as unsafe (business code 1301). Retrying the same request returns the
	// same refusal.
	ErrSensitiveContent = errors.New("sensitive content refused by Z.ai Coding")
)
