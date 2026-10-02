//go:build !linux

package distribution

import (
	"errors"
	"os"
)

func lockPromotionFile(string) (*os.File, error) {
	return nil, errors.New("paired installation replacement currently requires Linux")
}

func lockPromotion(string) (func(), error) {
	return nil, errors.New("paired installation replacement currently requires Linux")
}
