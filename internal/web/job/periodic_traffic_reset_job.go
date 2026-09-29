package job

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type Period string

type PeriodicTrafficResetJob struct {
	clientService service.ClientService
	period        Period
	location      *time.Location
}

func NewPeriodicTrafficResetJob(period Period, location *time.Location) *PeriodicTrafficResetJob {
	if location == nil {
		location = time.UTC
	}
	return &PeriodicTrafficResetJob{period: period, location: location}
}

func (j *PeriodicTrafficResetJob) Run() {
	j.runAt(time.Now().In(j.location))
}

func (j *PeriodicTrafficResetJob) runAt(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := j.clientService.RunScheduledTrafficReset(ctx, string(j.period), now); err != nil {
		logger.Warning("Scheduled traffic reset failed:", err)
	}
}
