package zaicoding

import "errors"

// ErrUnsupportedParameter marks a Z.ai Coding control that the selected model
// cannot accept.
var ErrUnsupportedParameter = errors.New("unsupported Z.ai Coding parameter")
