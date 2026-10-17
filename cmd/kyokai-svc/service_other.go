//go:build !windows

package main

import "context"

// asService reports that no service manager runs this process (the macOS
// helper comes with the Mac pass).
func asService(func(ctx context.Context) error) (bool, error) { return false, nil }
