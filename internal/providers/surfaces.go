package providers

// Surface is a web surface a box serves: its port inside the box, a short
// label, the browser path to open, and the `megh enable` feature that provides
// it (empty when the surface is baked into every image and so cannot be
// missing).
//
// The catalog lives here rather than in cmd/ because two callers need it and a
// second copy would drift: `megh browse` decides what to offer, and the docker
// backend decides which ports to publish. A surface added to one and not the
// other is either unreachable or unadvertised.
type Surface struct {
	Port    int
	Label   string
	Path    string
	Feature string
}

// Surfaces is every web surface megh knows a box can serve. All of them bind
// 127.0.0.1 INSIDE the box (CONSTRAINTS.md C4), so reaching one is always
// either an SSH tunnel or, on a local box, a loopback publish.
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

// SurfacePorts is the catalog's ports, for a backend deciding what to publish.
func SurfacePorts() []int {
	out := make([]int, 0, len(Surfaces))
	for _, s := range Surfaces {
		out = append(out, s.Port)
	}
	return out
}
