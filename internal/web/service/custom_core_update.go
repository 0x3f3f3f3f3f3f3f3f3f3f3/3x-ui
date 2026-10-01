package service

import "errors"

// ErrCustomCoreBundledUpdate prevents replacing the required data plane with an
// official core which lacks the panel's native protocols and policy engine.
var ErrCustomCoreBundledUpdate = errors.New("Custom Xray-core must be upgraded with its matching panel/core package to retain Snell, mieru and SSH support")
