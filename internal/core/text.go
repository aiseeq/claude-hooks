package core

// UTF16Len — длина строки так, как её считает Claude Code (JS): в единицах
// UTF-16, символ вне базовой плоскости — две
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}
