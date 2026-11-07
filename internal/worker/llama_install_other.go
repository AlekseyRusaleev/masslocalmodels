//go:build !windows && !linux

package worker

import (
	"context"
	"fmt"
)

func installLlama(context.Context, EnsureLlamaOptions) (string, error) {
	return "", fmt.Errorf("llama.cpp для разделения слоёв программа ставит на Windows")
}
