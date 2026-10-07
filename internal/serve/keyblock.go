package serve

import "strings"

// DefaultRegistryEnv is the note's name for the registry pull token when
// megh.yaml's registries[0].token_env names none.
const DefaultRegistryEnv = "GH_MEGH_TOKEN"

// ParseKeyBlock reads a control-plane note: KEY=value assignments, several to
// a line or one per line, with or without "export", quotes or comments. Only
// RUNPOD_API_KEY, HCLOUD_TOKEN, VULTR_API_KEY, the two MEGH_TAILSCALE_CLIENT_*
// names and registryEnv (the registry pull token, under whatever name
// registries[0].token_env gives it, so the note matches the secrets file) are
// kept. An empty registryEnv means DefaultRegistryEnv. It follows
// the same rules as the page's parseKeys (web/app.js); both are tested against
// testdata/keyblock_cases.json so they cannot drift.
func ParseKeyBlock(text, registryEnv string) Keys {
	if registryEnv == "" {
		registryEnv = DefaultRegistryEnv
	}
	var k Keys
	for _, line := range strings.Split(text, "\n") {
		for _, w := range shellWords(strings.TrimRight(line, "\r")) {
			eq := strings.IndexByte(w, '=')
			if eq < 1 {
				continue
			}
			name, v := w[:eq], w[eq+1:]
			if name == registryEnv {
				k.Registry = v
				continue
			}
			switch name {
			case "RUNPOD_API_KEY":
				k.RunPod = v
			case "HCLOUD_TOKEN":
				k.Hetzner = v
			case "VULTR_API_KEY":
				k.Vultr = v
			case "MEGH_TAILSCALE_CLIENT_ID":
				k.TSClientID = v
			case "MEGH_TAILSCALE_CLIENT_SECRET":
				k.TSClientSecret = v
			}
		}
	}
	return k
}

// shellWords splits one line roughly as a shell would for simple assignments:
// whitespace and ";" separate words, '...' and "..." group with the quotes
// dropped, and an unquoted "#" at the start of a word begins a comment.
func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '#' && !inWord:
			return words
		case c == ' ' || c == '\t' || c == ';':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}
