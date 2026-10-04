//go:build !linux

package main

// nativeZoom: elsewhere the frontend sizes the interface with CSS zoom.
const nativeZoom = false

func setPageZoom(float64) {}
