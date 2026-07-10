package val

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

type ValidatorFunction interface{}

type Validator struct {
	Value     reflect.Value
	lastField string
	fnsMap    map[string][]ValidatorFunction
}

// Create a new Validator using a custom struct
func New() *Validator {
	return &Validator{fnsMap: make(map[string][]ValidatorFunction)}
}

// Helper to dereference all pointer layers
func depointer(value reflect.Value) reflect.Value {
	// Strip off all the pointers
	for value.Kind() == reflect.Ptr {
		value = reflect.Indirect(value)
	}
	return value
}

// Helper to deal with traversing dot notation
func traverseAddts(value reflect.Value, field string) reflect.Value {
	fields := strings.Split(field, ".")
	for _, field := range fields {
		switch value.Kind() {
		// Handle structs by looking up field
		case reflect.Struct:
			value = depointer(value.FieldByName(field))
		// Handle slices by parsing field to int and looking up index
		case reflect.Slice:
			if i, err := strconv.Atoi(field); err != nil {
				log.Panic("'%v' expected to be an int: %v", field, err)
			} else {
				value = depointer(value.Index(i))
			}
		}

	}
	return value
}

// Set the current field to be Validated
func (v *Validator) Check(field string) *Validator {
	if _, ok := v.fnsMap[field]; !ok {
		v.fnsMap[field] = make([]ValidatorFunction, 0)
	}

	v.lastField = field
	return v
}

// Generate Validation Errors
func (v *Validator) Exec(value interface{}) []error {
	v.Value = depointer(reflect.ValueOf(value))

	errs := make([]error, 0)
	// Loop over all the field values
	for field, fns := range v.fnsMap {
		value := traverseAddts(v.Value, field)

		// we have to add the & to make the fields on the value settable
		if !value.IsValid() {
			log.Panic("Field %v does not exist!", field)
		}

		// Loop over all validation functions for a field
		for _, fn := range fns {
			// Only append real errors
			fnVal := reflect.ValueOf(fn)
			errVals := fnVal.Call([]reflect.Value{value})
			if len(errVals) > 0 && !errVals[0].IsNil() {
				err := errVals[0].Interface().(error)
				if err != nil {
					errs = append(errs, NewFieldError(field, err.Error()))
				}
			}
		}
	}

	return errs
}

func (v *Validator) Add(fn ValidatorFunction) *Validator {
	field := v.lastField
	typ := reflect.TypeOf(fn)
	if typ.Kind() != reflect.Func {
		log.Panic("ValidatorFunction must be a function")
	}
	if typ.NumIn() != 1 {
		log.Panic("ValidatorFunction must have one argument")
	}

	v.fnsMap[field] = append(v.fnsMap[field], fn)
	return v
}

// Built in validation routines
func (v *Validator) Exists() *Validator {
	return v.Add(func(i interface{}) error {
		switch value := i.(type) {
		case string:
			if len(value) > 0 {
				return nil
			}
		}
		return errors.New("Field cannot be blank.")
	})
}

func (v *Validator) IsEmail() *Validator {
	return v.Add(func(value string) error {
		if strings.Contains(value, "@") &&
			strings.Contains(value, ".") &&
			strings.Index(value, "@") < strings.Index(value, ".") &&
			len(value) > 5 {
			return nil
		}
		return errors.New("Field must be an email.")
	})
}

func (v *Validator) MinLength(minLength int) *Validator {
	return v.Add(func(value string) error {
		if len(value) >= minLength {
			return nil
		}
		return errors.New(fmt.Sprintf("Field must be atleast %d characters long.", minLength))
	})
}

// Use for enums
func (v *Validator) Matches(strs ...string) *Validator {
	return v.Add(func(value string) error {
		for _, str := range strs {
			if str == value {
				return nil
			}
		}
		if len(strs) == 1 {
			return errors.New(fmt.Sprintf("Field must equal '%v', not '%v'.", strs[0], value))
		}
		return errors.New(fmt.Sprintf("Field must be one of ['%v'], not '%v'.", strings.Join(strs, "', '"), value))
	})
}

func (v *Validator) Ensure(fn ValidatorFunction) *Validator {
	return v.Add(fn)
}

// Higher Order Functions
func (v *Validator) IsPassword() *Validator {
	return v.MinLength(6)
}

type StringValidationContext struct {
	value   string
	IsValid bool
}

func Check(str string) *StringValidationContext {
	return &(StringValidationContext{str, true})
}

// // Higher Level Validation
// func (s *StringValidationContext) Empty() *StringValidationContext {
// 	return s.LengthIsGreaterThanOrEqualTo(1)
// }

// func (s *StringValidationContext) Exists() *StringValidationContext {
// 	return s.LengthIsGreaterThanOrEqualTo(1)
// }

// func (s *StringValidationContext) IsEmail() *StringValidationContext {
// 	return s.StringBeforeString("@", ".").LengthIsGreaterThanOrEqualTo(5) //a@b.c
// }

func (s *StringValidationContext) IsPassword() *StringValidationContext {
	// We should ahve char restrictions here but whatever
	return s.LengthIsGreaterThanOrEqualTo(6) // Min Length 6
}

// func (s *StringValidationContext) StringBeforeString(a string, b string) *StringValidationContext {
// 	s.Contains(a).Contains(b)
// 	s.IsValid = s.IsValid && (strings.Index(s.value, a) < strings.LastIndex(s.value, b))
// 	return s
// }

// Basic Validation
func (s *StringValidationContext) LengthIsGreaterThanOrEqualTo(n int) *StringValidationContext {
	s.IsValid = s.IsValid && (len(s.value) >= n)
	log.Debug("%v >= %v is %v", s.value, n, s.IsValid)
	return s
}

// func (s *StringValidationContext) EqualTo(str string) *StringValidationContext {
// 	s.IsValid = s.IsValid && (str == s.value)
// 	log.Debug("%v is equal to %v is %v", s.value, str, s.IsValid)
// 	return s
// }

// func (s *StringValidationContext) Contains(str string) *StringValidationContext {
// 	s.IsValid = s.IsValid && (strings.Contains(s.value, str))
// 	log.Debug("%v contains %v is %v", s.value, str, s.IsValid)
// 	return s
// }

// func ValidateUser(user *models.User, errs []string) []string {
// 	if !Check(user.Email).IsEmail().IsValid {
// 		log.Debug("Form posted invalid email")
// 		errs = append(errs, "Please enter a valid email.")
// 	}

// 	if !Check(user.FirstName).Exists().IsValid {
// 		log.Debug("Form posted without first name")
// 		errs = append(errs, "Please enter a first name.")
// 	}

// 	if !Check(user.LastName).Exists().IsValid {
// 		log.Debug("Form posted without last name")
// 		errs = append(errs, "Please enter a last name.")
// 	}

// 	// Add we care?
// 	// if !Check(user.Phone).Exists().IsValid {
// 	// 	log.Debug("Form posted without phone number")
// 	// 	errs = append(errs, "Please enter a phone number.")
// 	// }

// 	return errs
// }

// func SanitizeUser(user *models.User) {
// 	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
// 	user.FirstName = strings.Title(user.FirstName)
// 	user.LastName = strings.Title(user.LastName)
// }

// func ValidateUser2(u *user.User, errs []string) []string {
// 	if !Check(u.Email).IsEmail().IsValid {
// 		log.Debug("Form posted invalid email")
// 		errs = append(errs, "Please enter a valid email.")
// 	}

// 	if !Check(u.FirstName).Exists().IsValid {
// 		log.Debug("Form posted without first name")
// 		errs = append(errs, "Please enter a first name.")
// 	}
// rrrrrr
// 	if !Check(u.LastName).Exists().IsValid {
// 		log.Debug("Form posted without last name")
// 		errs = append(errs, "Please enter a last name.")
// 	}

// 	// Do we care?
// 	// if !Check(u.Phone).Exists().IsValid {
// 	// 	log.Debug("Form posted without phone number")
// 	// 	errs = append(errs, "Please enter a phone number.")
// 	// }

// 	return errs
// }

// func SanitizeUser2(u *user.User) {
// 	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
// 	u.FirstName = strings.Title(u.FirstName)
// 	u.LastName = strings.Title(u.LastName)
// }

// func ValidateAddress(address *models.Address, errs []string) []string {
// 	if !Check(address.Line1).Exists().IsValid {
// 		log.Debug("Form posted without address")
// 		errs = append(errs, "Please enter an address.")
// 	}

// 	if !Check(address.City).Exists().IsValid {
// 		log.Debug("Form posted without city")
// 		errs = append(errs, "Please enter a city.")
// 	}

// 	if !Check(address.State).Exists().IsValid {
// 		log.Debug("Form posted without state")
// 		errs = append(errs, "Please enter a state.")
// 	}

// 	if !Check(address.PostalCode).Exists().IsValid {
// 		log.Debug("Form posted without postal code")
// 		errs = append(errs, "Please enter a zip/postal code.")
// 	}

// 	if !Check(address.Country).Exists().IsValid {
// 		log.Debug("Form posted without country")
// 		errs = append(errs, "Please enter a country.")
// 	}
// 	return errs
// }

func ValidatePassword(password string, errs []string) []string {
	if !Check(password).IsPassword().IsValid {
		log.Debug("Form posted invalid password")
		errs = append(errs, "Password Must be atleast 6 characters long.")
	}
	return errs
}
