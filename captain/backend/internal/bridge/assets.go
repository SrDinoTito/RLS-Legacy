package bridge

import "embed"

//go:embed assets/install.sh
var installAssets embed.FS

// InstallScript returns the public installer template served by Captain.
func InstallScript() ([]byte, error) {
	return installAssets.ReadFile("assets/install.sh")
}
