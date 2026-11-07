package cmd

import (
	"strings"
)

func (i *Input) newPlatforms() map[string]string {
	platforms := map[string]string{
		"ubuntu-latest": "node:16-buster-slim",
		"ubuntu-22.04":  "node:16-bullseye-slim",
		"ubuntu-20.04":  "node:16-buster-slim",
		"ubuntu-18.04":  "node:16-buster-slim",
	}

	for _, p := range i.platforms {
		// Split on the first = only: images such as tart://img?cpu=4 contain more.
		if label, image, ok := strings.Cut(p, "="); ok && label != "" {
			platforms[strings.ToLower(label)] = image
		}
	}
	return platforms
}
