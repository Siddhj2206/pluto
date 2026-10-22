// Package handshake defines the machine-readable contract a Pluto client uses
// to recognise a compatible host binary. The host prints it with
// `pluto version --json`; the client never assumes a version by parsing human
// text unless the binary is too old to answer the JSON form.
package handshake

// SchemaVersion is the shape of the JSON document itself.
const SchemaVersion = 1

// Protocol is the client/server wire contract this build speaks. Bump it only
// when an older client can no longer drive the host safely.
const Protocol = 1

// Capabilities are the host surfaces a client may rely on. Keep this list
// additive: a client ignores capabilities it does not know.
var Capabilities = []string{
	"json",
	"device",
	"doctor",
	"tasks",
	"runs",
	"images",
}

// Info is the payload of `pluto version --json`.
type Info struct {
	SchemaVersion int      `json:"schema_version"`
	Version       string   `json:"version"`
	Protocol      int      `json:"protocol"`
	Capabilities  []string `json:"capabilities"`
}

// New returns the handshake for a build version.
func New(version string) Info {
	return Info{
		SchemaVersion: SchemaVersion,
		Version:       version,
		Protocol:      Protocol,
		Capabilities:  append([]string(nil), Capabilities...),
	}
}
