package remind

import "os"

func writeRaw(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }
