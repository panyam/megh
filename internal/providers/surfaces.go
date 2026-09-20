package providers

// Surface is a web surface a box serves: its port inside the box, a short
// label, the browser path to open, and the `megh enable` feature that provides
// it (empty when the surface is baked into every image and so cannot be
// missing).
//
// The catalog is what `megh browse` probes and labels by default. It is not a
// limit: `megh browse <port>` forwards any listening port, and a port outside
// the catalog just prints without a label.
type Surface struct {
	Port    int
	Label   string
	Path    string
	Feature string
}

// Surfaces is every web surface megh knows a box can serve. All of them bind
// 127.0.0.1 INSIDE the box (CONSTRAINTS.md C4), so reaching one is always an
// SSH tunnel or `tailscale serve`, never a publish.
var Surfaces = []Surface{
	{7681, "shell", "/", ""},
	{7682, "webterm", "/", ""},
	{6080, "vnc", "/vnc.html", "vnc"},
	{8080, "code", "/", "code"},
}

// SurfaceFor describes a port, falling back to a generic entry for one the
// catalog does not name.
func SurfaceFor(port int) Surface {
	for _, s := range Surfaces {
		if s.Port == port {
			return s
		}
	}
	return Surface{Port: port, Label: "port", Path: "/"}
}

// SurfacePorts is the catalog's ports, in catalog order.
func SurfacePorts() []int {
	out := make([]int, 0, len(Surfaces))
	for _, s := range Surfaces {
		out = append(out, s.Port)
	}
	return out
}
