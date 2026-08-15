package setup

// parseInt extracts the decimal digits from a string and returns the parsed int.
// Several platform-specific service managers use it when command output mixes
// numeric values with surrounding whitespace.
func parseInt(s string) int {
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	return result
}
