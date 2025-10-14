package proxyd

import (
	"fmt"
)

func wrapErr(err error, msg string) error {
	return fmt.Errorf("%s %w", msg, err)
}

// BlockMissingFieldsError represents an error when block response is missing required fields (number or hash)
type BlockMissingFieldsError struct {
	BackendName string
	Result      interface{}
	MissingFields []string
}

func (e *BlockMissingFieldsError) Error() string {
	return fmt.Sprintf("block response missing required fields %v from backend %s, result %v", e.MissingFields, e.BackendName, e.Result)
}

// NewBlockMissingFieldsError creates a new BlockMissingFieldsError
func NewBlockMissingFieldsError(backendName string, result interface{}, missingFields []string) *BlockMissingFieldsError {
	return &BlockMissingFieldsError{
		BackendName:   backendName,
		Result:        result,
		MissingFields: missingFields,
	}
}
