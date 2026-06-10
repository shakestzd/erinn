package pluginbuild

import (
	"encoding/json"
	"io"
)

// writeJSONTo encodes v as indented JSON with a trailing newline.
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
