package value

import (
	"database/sql/driver"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/lib/pq"
)

// StringList stores an ordered list as PostgreSQL text[] and exposes a JSON array.
type StringList []string

func (list StringList) Value() (driver.Value, error) {
	return pq.StringArray(list).Value()
}

func (list *StringList) Scan(value any) error {
	return (*pq.StringArray)(list).Scan(value)
}

// MarshalBinary provides the Redis-compatible wire representation for hash/Lua
// arguments; SQL uses the separate driver.Valuer array representation above.
func (list StringList) MarshalBinary() ([]byte, error) {
	return common.Marshal([]string(list))
}

func (list StringList) Normalized() StringList {
	result := make(StringList, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, value := range list {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
