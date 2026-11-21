package store

import "strings"

const maxIDDigits = 18

func IDNum(id string) int64 {
	i := strings.IndexByte(id, '-')
	if i < 0 {
		return 0
	}
	var n int64
	digits := 0
	for _, c := range id[i+1:] {
		if c < '0' || c > '9' || digits == maxIDDigits {
			break
		}
		n = n*10 + int64(c-'0')
		digits++
	}
	return n
}
