//go:build !linux

package distribution

import "errors"

func lockPromotion(string) (func(), error) {
	return nil, errors.New("paired installation replacement currently requires Linux")
}
