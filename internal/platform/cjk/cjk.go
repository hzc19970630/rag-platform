// Package cjk holds the CJK detection and bigram split shared by BM25 indexing
// and the offline query-term extractor.
package cjk

func Contains(value string) bool {
	for _, r := range value {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// Bigrams returns every adjacent rune pair. Chinese is written without spaces,
// so a whole-run token would rarely match the indexed text.
func Bigrams(value string) []string {
	runes := []rune(value)
	result := make([]string, 0, max(0, len(runes)-1))
	for index := 0; index+1 < len(runes); index++ {
		result = append(result, string(runes[index:index+2]))
	}
	return result
}
