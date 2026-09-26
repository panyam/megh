package iterm

// Settings holds resolved iTerm2 integration options (from megh.yaml and env).
type Settings struct {
	Profile  string
	Auto     bool
	StoreDir string // megh-managed profile JSON (megh iterm save/load)
}

// DefaultSettings is what megh uses when megh.yaml says nothing.
func DefaultSettings() Settings {
	return Settings{Profile: ProfileName, Auto: true}
}

func (s Settings) profileName() string {
	if s.Profile != "" {
		return s.Profile
	}
	return ProfileName
}
