package main

import (
	"fmt"

	"go.opentelemetry.io/collector/model/pdata"
)

func main() {
	// Remove all of the even keys
	keysToRemove := map[string]struct{}{}
	for j := 0; j < 50; j++ {
		keysToRemove[fmt.Sprintf("%d", j*2)] = struct{}{}
	}
	m := pdata.NewAttributeMap()
	for j := 0; j < 100; j++ {
		m.InsertString(fmt.Sprintf("%d", j), "string value")
	}
	for {
		m.RemoveIf(func(key string, _ pdata.AttributeValue) bool {
			_, remove := keysToRemove[key]
			return remove
		})
		// add the old keys back in
		for k := range keysToRemove {
			m.InsertString(k, "string value")
		}
	}
}
