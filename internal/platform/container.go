package platform

import "os"

// InContainer reports whether the server runs in a container: Docker and Podman each leave a file
// at the root. The network there is often a bridge that multicast does not cross.
func InContainer() bool {
	return inContainer(func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func inContainer(exists func(string) bool) bool {
	return exists("/.dockerenv") || exists("/run/.containerenv")
}
