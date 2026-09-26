//go:build darwin

package iterm

// Save copies a profile from iTerm's preferences into the megh profile store.
func Save(storeDir, name string) (string, error) {
	profile, err := ReadITermProfile(name)
	if err != nil {
		return "", err
	}
	return WriteStoreFile(storeDir, name, profile)
}
