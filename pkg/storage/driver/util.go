package driver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"helm.sh/helm/v3/pkg/storage"
)

var r = regexp.MustCompile(`^(\w+).v(\d+)$`)

// ParseKey returns release name and version from a key generated by makeKey.
// ref: https://github.com/helm/helm/blob/241785c70fb38b2c074d7b3ddf0925812fb3fc69/pkg/storage/storage.go#L242-L250
func ParseKey(key string) (string, int, error) {
	if !strings.HasPrefix(key, storage.HelmStorageType+".") {
		return "", 0, fmt.Errorf("key missing storage prefix %s", storage.HelmStorageType)
	}
	key = strings.TrimPrefix(key, storage.HelmStorageType+".")

	matches := r.FindAllStringSubmatch(key, -1)
	if len(matches) == 0 {
		return "", 0, fmt.Errorf("failed to match regex")
	}

	rlsName := matches[0][0]
	version, err := strconv.Atoi(matches[0][1])
	if err != nil {
		return "", 0, err
	}
	return rlsName, version, nil
}
