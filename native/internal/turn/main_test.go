package turn

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	installMemTransport()
	code := m.Run()
	if factoryDir != "" {
		os.RemoveAll(factoryDir)
	}
	os.Exit(code)
}
