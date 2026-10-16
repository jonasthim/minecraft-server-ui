//go:build linux

package main

import "context"

func runNativeService(context.Context, func(context.Context) error) (bool, error) { return false, nil }
