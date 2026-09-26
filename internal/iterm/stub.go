//go:build !darwin

package iterm

import "errors"

var errUnavailable = errors.New("iTerm integration is macOS only")

// Available reports whether iTerm2 integration can run on this OS.
func Available() bool { return false }

// InMeghProfile is always false off macOS.
func InMeghProfile(_ string) bool { return false }

// ProfileInstalled is always false off macOS.
func ProfileInstalled() bool { return false }

// Install seeds and loads the default profile on macOS only.
func Install(_ Settings) error { return errUnavailable }

// TryDelegate never delegates off macOS.
func TryDelegate(_ []string, _ Settings) bool { return false }
