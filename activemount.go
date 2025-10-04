package main

import (
	"encoding/json"
)

// activeMount is used to count the number of active mounts of a volume by a container.
// Multiple mounts may be created, e.g., if `docker cp` is performed on a running container
// with the volume mounted. Such requests are supposed to have unique mount IDs, however, due to
// a bug in dockerd, they don't: https://github.com/moby/moby/issues/47964
type activeMount struct {
	UsageCount int
}

func (am *activeMount) mustMarshal() []byte {
	payload, err := json.Marshal(am)
	if err != nil {
		panic(err)
	}
	return payload
}
