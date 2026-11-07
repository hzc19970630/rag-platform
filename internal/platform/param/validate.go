// Package param holds the numeric range checks shared by the YAML defaults and
// the knowledge-base/session override tiers. Messages stay stable because API
// clients match them, and every failure is an apperror validation error so the
// transport layer does not classify it by text.
package param

import (
	"github.com/hzc19970630/rag-platform/internal/apperror"
)

func IntRange(name string, value, min, max int) error {
	if value < min || value > max {
		return apperror.Invalidf("%s must be between %d and %d, got %d", name, min, max, value)
	}
	return nil
}

func FloatRange(name string, value, min, max float64) error {
	if value < min || value > max {
		return apperror.Invalidf("%s must be between %v and %v, got %v", name, min, max, value)
	}
	return nil
}

func NonNegativeInt(name string, value int) error {
	if value < 0 {
		return apperror.Invalidf("%s must not be negative", name)
	}
	return nil
}

func NonNegativeFloat(name string, value float64) error {
	if value < 0 {
		return apperror.Invalidf("%s must not be negative", name)
	}
	return nil
}

func PositiveInt(name string, value int) error {
	if value <= 0 {
		return apperror.Invalidf("%s must be a positive integer", name)
	}
	return nil
}
