package val

// JSON API:
// {
// 	"error": {
// 		"type": "api-error"
// 		"message": "you suck dick"
// 	}
// }

// {
// 	"error": {
// 		"type": "validation",
// 		"message": "Validation failed on user",
// 		"fields": {
// 			"username": "Username is empty",
// 			"password": "Password is empty"
// 		}
// 	}
// }

// Top level validation error
type Error struct {
	Fields  []error
	Message string
	Type    string
}

func (e Error) Error() string {
	return e.Message
}

// Return a map of field error messages
func (e Error) Messages() map[string]string {
	m := make(map[string]string)
	for _, f := range e.Fields {
		if err, ok := f.(*FieldError); ok {
			m[err.Field] = err.Message
		}
	}

	return m
}

func NewError(message string) *Error {
	err := new(Error)
	err.Message = message
	err.Type = "validation"
	return err
}

// Error at individual field level
type FieldError struct {
	Field   string
	Message string
}

func (f FieldError) Error() string {
	return f.Message
}

func NewFieldError(field string, message string) *FieldError {
	return &FieldError{field, message}
}
