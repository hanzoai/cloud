package searchpartial

func Partials(full string) string {
	fullRunes := []rune(full)
	runes := []rune(full)
	for size := 3; size < len(fullRunes); size++ {
		for i := 0; i <= len(fullRunes)-size; i++ {
			runes = append(append(runes, ' '), fullRunes[i:i+size]...)
		}
	}
	return string(runes)
}
