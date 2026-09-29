package features

import (
	"github.com/xtls/xray-core/common"
)

// Feature is the interface for Xray features. All features must implement this interface.
// All existing features have an implementation in app directory. These features can be replaced by third-party ones.
type Feature interface {
	common.HasType
	common.Runnable
}

// StartupBarrier commits durable configuration only after every other feature has started.
// An instance can have one such barrier; its Start must commit atomically or fail closed.
type StartupBarrier interface {
	Feature
	StartAfterFeatures() bool
}
