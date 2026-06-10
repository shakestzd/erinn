package pluginbuild

import "encoding/json"

// jsonMarshal is a thin wrapper so orderedHookMap can encode sub-values
// without pulling a second encoder into scope.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
