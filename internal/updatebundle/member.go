package updatebundle

import (
	"archive/tar"
	"fmt"
	"strings"
)

func memberName(h *tar.Header) (string, error) {
	name := h.Name
	if h.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	} else if h.Typeflag != tar.TypeReg {
		return "", fmt.Errorf("unsupported archive member type: %q", name)
	}
	if len(h.PAXRecords) != 0 || h.Format == tar.FormatPAX || h.Linkname != "" || h.Mode&^int64(0o777) != 0 {
		return "", fmt.Errorf("unsupported archive metadata: %q", name)
	}
	if len(name) > 240 || (name != "x-ui" && !strings.HasPrefix(name, "x-ui/")) || (name == "x-ui" && h.Typeflag != tar.TypeDir) {
		return "", fmt.Errorf("invalid archive path: %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("invalid archive path: %q", name)
		}
		for _, c := range component {
			allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
			if !allowed {
				return "", fmt.Errorf("invalid archive path: %q", name)
			}
		}
	}
	return name, nil
}
