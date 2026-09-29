package policyflow

import (
	"context"
	"errors"
	"io"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

const MaxDatagramSize = 65507

var ErrDatagramTooLarge = errors.New("UDP payload exceeds 65507 bytes")

func (f *Flow) DatagramWriter(direction Direction, destination io.Writer) io.Writer {
	return &datagramWriter{flow: f, direction: direction, destination: destination}
}

type datagramWriter struct {
	flow        *Flow
	direction   Direction
	destination io.Writer
}

func (w *datagramWriter) Write(p []byte) (int, error) {
	if w.direction != Upload && w.direction != Download {
		return 0, clientpolicy.ErrInvalidGrant
	}
	if len(p) > MaxDatagramSize {
		return 0, ErrDatagramTooLarge
	}
	if err := w.flow.ctx.Err(); err != nil {
		return 0, context.Cause(w.flow.ctx)
	}
	s := w.flow.state
	delivery := s.writeUp
	if w.direction == Download {
		delivery = s.writeDown
	}
	var err error
	if len(p) == 0 {
		err = s.check(w.flow.ctx)
	} else {
		limiter := s.upload
		if w.direction == Download {
			limiter = s.download
		}
		if _, err := limiter.AcquireDatagram(w.flow.ctx, len(p)); err != nil {
			return 0, err
		}
		_, err = s.admit(w.flow.ctx, w.direction, len(p), false)
	}
	if err != nil {
		var quota *database.UsageQuotaError
		if errors.As(err, &quota) && quota.RawAllowance > 0 {
			return 0, err
		}
		if w.flow.ctx.Err() == nil || s.fault.Load() != nil {
			s.closeFlows(err)
		}
		return 0, err
	}
	if err := w.flow.ctx.Err(); err != nil {
		return 0, context.Cause(w.flow.ctx)
	}
	if len(p) > 0 {
		if _, err := delivery.AcquireDatagram(w.flow.ctx, len(p)); err != nil {
			return 0, err
		}
	}
	n, err := w.destination.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
