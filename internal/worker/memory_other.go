//go:build !linux && !darwin

package worker

import (
	"context"
	"errors"
)

const memorySupported = false

func sampleRSS(context.Context, []int) (map[int]int64, error) {
	return nil, errors.New("worker memory sampling is not supported on this OS")
}
