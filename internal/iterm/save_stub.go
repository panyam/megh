//go:build !darwin

package iterm

import "fmt"

func ReadITermProfile(string) (map[string]any, error) {
	return nil, errUnavailable
}

func Load(string, ...string) ([]string, error) {
	return nil, errUnavailable
}

func ProfileLoaded(string) bool { return false }

func SeedDefaultStore(string, string) (string, error) {
	return "", errUnavailable
}

func Save(storeDir, name string) (string, error) {
	return "", fmt.Errorf("%w", errUnavailable)
}
