//go:build !windows

package devinwire

import "runtime"

func runtimeOS() string { return runtime.GOOS }
