package ecosystem

import (
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
	"sync"
)

var schemas sync.Map

func ValidateArgs(declarations []tools.Declaration, name string, args jsjson.Value) error {
	var raw string
	for _, d := range declarations {
		if d.Name == name {
			raw = d.Parameters
			break
		}
	}
	if raw == "" {
		return fmt.Errorf("未知工具")
	}
	key := name + raw
	v, ok := schemas.Load(key)
	if !ok {
		var schema jsonschema.Schema
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			return err
		}
		compiled, err := schema.Resolve(nil)
		if err != nil {
			return err
		}
		v, _ = schemas.LoadOrStore(key, compiled)
	}
	var instance any
	if err := json.Unmarshal([]byte(jsjson.Stringify(args)), &instance); err != nil {
		return err
	}
	return v.(*jsonschema.Resolved).Validate(instance)
}
