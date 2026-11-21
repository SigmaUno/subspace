package fixture

import "errors"

var errDivZero = errors.New("division by zero")

func Positive(n int) bool {
	return n > 0
}

func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, errDivZero
	}
	return a / b, nil
}

func Sub(a, b int) int {
	return a - b
}

func Countdown(n int) int {
	steps := 0
	for n != 0 {
		n -= 1
		steps++
	}
	return steps
}

func Both(a, b bool) bool {
	return a && b
}

func Half(n int) (int, error) {
	q, err := Div(n, 2)
	if err != nil {
		return 0, err
	}
	return q, nil
}

var names = [2]string{"zero", "one"}

func Name(i int) string {
	return names[i]
}
