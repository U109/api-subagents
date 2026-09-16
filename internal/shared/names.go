package shared

import (
	"regexp"
)

var MarketPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
