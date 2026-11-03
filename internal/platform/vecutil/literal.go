// Package vecutil holds the pgvector text encoding shared by every adapter that
// writes or queries a halfvec column.
package vecutil

import (
	"fmt"
	"strings"
)

// Literal renders a vector as the text form pgvector accepts.
func Literal(values []float32) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%f", value))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
