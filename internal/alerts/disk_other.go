//go:build !unix

package alerts

import "errors"

func diskFree(string) (uint64, error) { return 0, errors.New("disk usage unsupported") }
