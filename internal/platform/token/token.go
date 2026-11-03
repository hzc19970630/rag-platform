// Package token holds the approximate token budget shared by retrieval context
// trimming and the offline chat adapter.
package token

// Estimate approximates a model token count at one token per two runes. It is
// deliberately conservative and documented as approximate: the context limit is
// a safety bound, not an exact model count.
func Estimate(text string) int {
	return len([]rune(text))/2 + 1
}
