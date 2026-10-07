package serve

import "strings"

// ParseKeyBlock reads a control-plane note: KEY=value assignments, several to
// a line or one per line, with or without "export", quotes or comments. Only
// RUNPOD_API_KEY, HCLOUD_TOKEN, VULTR_API_KEY and the two
// MEGH_TAILSCALE_CLIENT_* names are kept. It follows
// the same rules as the page's parseKeys (web/app.js); both are tested against
// testdata/keyblock_cases.json so they cannot drift.
func ParseKeyBlock(text string) Keys {
	var k Keys
	for _, line := range strings.Split(text, "\n") {
		for _, w := range shellWords(strings.TrimRight(line, "\r")) {
			eq := strings.IndexByte(w, '=')
			if eq < 1 {
				continue
			}
			switch v := w[eq+1:]; w[:eq] {
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
