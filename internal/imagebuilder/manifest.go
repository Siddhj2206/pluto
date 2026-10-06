package imagebuilder

// Manifest is the builder's output manifest: the hashes of every artifact file
// and the pins the artifact was built from. Schema 2 drops the wall-clock
// built_at field for source_date_epoch, derived from the apt snapshot pin, so
// a rebuild on another day produces the same manifest bytes.
type Manifest struct {
	Schema          int                 `json:"schema"`
	SourceDateEpoch int64               `json:"source_date_epoch"`
	Kernel          KernelManifest      `json:"kernel"`
	Firecracker     FirecrackerManifest `json:"firecracker"`
	Rootfs          RootfsManifest      `json:"rootfs"`
	Agent           AgentManifest       `json:"agent"`
}

// KernelManifest records the kernel artifact.
type KernelManifest struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// FirecrackerManifest records the Firecracker release and binary hash.
type FirecrackerManifest struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// RootfsManifest records the rootfs image and the pins that shaped it.
type RootfsManifest struct {
	File        string `json:"file"`
	Base        string `json:"base"`
	AptSnapshot string `json:"apt_snapshot"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	DiskMB      int    `json:"disk_mb"`
}

// AgentManifest records the guest agent hash.
type AgentManifest struct {
	SHA256 string `json:"sha256"`
}
